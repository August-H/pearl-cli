package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/filelock"
	"github.com/August-H/pearl-cli/internal/pearlpaths"
)

const (
	modelCatalogCacheFile = "model_catalogs.json"
	// modelCatalogMaxAge is how long a saved list goes without a check. Each check
	// starts Claude Code and Antigravity, so opening the picker repeatedly
	// shouldn't start them every time.
	modelCatalogMaxAge = 5 * time.Minute
	// The background check gives up after this long, and the saved list stays.
	modelCatalogRefreshLimit = time.Minute
)

type savedModelCatalog struct {
	Lab       string              `json:"lab"`
	Provider  string              `json:"provider"`
	Login     string              `json:"login"`
	Connected bool                `json:"connected"`
	Models    []daemon.AgentModel `json:"models,omitempty"`
	Error     string              `json:"error,omitempty"`
}

type savedModelCatalogs struct {
	FetchedAt time.Time           `json:"fetched_at"`
	Catalogs  []savedModelCatalog `json:"catalogs"`
}

func modelCatalogCachePath() (string, error) {
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.Directory, modelCatalogCacheFile), nil
}

// readSavedModelCatalogs returns the saved lists. A file with no models doesn't
// count, so someone who just signed in never waits on an empty list.
func readSavedModelCatalogs() ([]modelCatalog, time.Time, bool) {
	path, err := modelCatalogCachePath()
	if err != nil {
		return nil, time.Time{}, false
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var saved savedModelCatalogs
	if json.Unmarshal(contents, &saved) != nil {
		return nil, time.Time{}, false
	}
	hasModels := false
	catalogs := make([]modelCatalog, 0, len(saved.Catalogs))
	for _, entry := range saved.Catalogs {
		catalog := modelCatalog{
			lab: entry.Lab, provider: entry.Provider, login: entry.Login,
			connected: entry.Connected, models: entry.Models,
		}
		if entry.Error != "" {
			catalog.err = errors.New(entry.Error)
		}
		hasModels = hasModels || len(entry.Models) > 0
		catalogs = append(catalogs, catalog)
	}
	return catalogs, saved.FetchedAt, hasModels
}

func saveModelCatalogs(catalogs []modelCatalog) error {
	path, err := modelCatalogCachePath()
	if err != nil {
		return err
	}
	saved := savedModelCatalogs{FetchedAt: time.Now()}
	for _, catalog := range catalogs {
		entry := savedModelCatalog{
			Lab: catalog.lab, Provider: catalog.provider, Login: catalog.login,
			Connected: catalog.connected, Models: catalog.models,
		}
		if catalog.err != nil && len(catalog.models) == 0 {
			entry.Error = catalog.err.Error()
		}
		saved.Catalogs = append(saved.Catalogs, entry)
	}
	encoded, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Replace the file in one step: another process may be reading it.
	temporary, err := os.CreateTemp(filepath.Dir(path), modelCatalogCacheFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(append(encoded, '\n'))
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// forgetModelCatalogs is for sign-ins and sign-outs, which change what's offered.
// The next `pearl model` then waits for the new list instead of showing the old one.
func forgetModelCatalogs() {
	if path, err := modelCatalogCachePath(); err == nil {
		_ = os.Remove(path)
	}
}

// refreshModelCatalogs asks every connection for its models and saves the result.
// A connection that fails keeps its last list, so one bad check doesn't empty the
// menu. A signed-out connection loses its models.
func refreshModelCatalogs(ctx context.Context) []modelCatalog {
	catalogs := loadModelCatalogs(ctx)
	previous, _, _ := readSavedModelCatalogs()
	for index := range catalogs {
		if catalogs[index].err == nil {
			continue
		}
		for _, saved := range previous {
			if saved.provider == catalogs[index].provider {
				catalogs[index].models = saved.models
			}
		}
	}
	if ctx.Err() == nil {
		_ = saveModelCatalogs(catalogs)
	}
	return catalogs
}

// currentModelCatalogs returns the saved lists right away when it can, and checks
// for changes in the background when they're old. It waits only when nothing is
// saved or the caller asked for a fresh list.
func currentModelCatalogs(ctx context.Context, refresh bool) []modelCatalog {
	if !refresh {
		if catalogs, fetchedAt, ok := readSavedModelCatalogs(); ok {
			if time.Since(fetchedAt) >= modelCatalogMaxAge {
				startModelCatalogRefresh()
			}
			return catalogs
		}
	}
	return refreshModelCatalogs(ctx)
}

// checkModelCatalogsIfOld starts a background check when the saved list is missing
// or old, so anything that shows model names has them soon without waiting.
func checkModelCatalogsIfOld() {
	if _, fetchedAt, ok := readSavedModelCatalogs(); !ok || time.Since(fetchedAt) >= modelCatalogMaxAge {
		startModelCatalogRefresh()
	}
}

// startModelCatalogRefresh runs the check as its own process, so it finishes even
// if `pearl model` exits first. Changes appear the next time the picker opens.
var startModelCatalogRefresh = func() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	command := exec.Command(executable, "model", "--update-cache")
	configureDetachedProcess(command)
	if command.Start() == nil {
		go func() { _ = command.Wait() }()
	}
}

// updateModelCatalogCache is the background check. If one is already running,
// this one has nothing to add.
func updateModelCatalogCache() int {
	path, err := modelCatalogCachePath()
	if err != nil {
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 1
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 1
	}
	defer lock.Close()
	locked, err := filelock.TryLock(lock)
	if err != nil {
		return 1
	}
	if !locked {
		return 0
	}
	defer filelock.Unlock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), modelCatalogRefreshLimit)
	defer cancel()
	refreshModelCatalogs(ctx)
	return 0
}
