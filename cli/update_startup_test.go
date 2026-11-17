package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func startupTestRelease(tag string) updateRelease {
	name := "pearl-" + runtime.GOOS + "-" + runtime.GOARCH + exeSuffix()
	return updateRelease{
		TagName: tag,
		Assets: []updateAsset{
			{Name: name, URL: "https://example.com/" + name},
			{Name: name + ".sha256", URL: "https://example.com/" + name + ".sha256"},
		},
	}
}

func TestStartupUpdateFetchesAndCachesRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "updates.json")
	now := time.Now().UTC()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("release request is missing GitHub's Accept header")
		}
		_ = json.NewEncoder(output).Encode(startupTestRelease("v0.2.0"))
	}))
	defer server.Close()
	restoreUpdateEndpoints(t, server.URL, server.Client())

	for _, checkedAt := range []time.Time{now, now.Add(startupUpdateMaxAge - time.Second)} {
		release := startupUpdateRelease(context.Background(), path, "0.1.0", checkedAt)
		if release == nil || release.TagName != "v0.2.0" {
			t.Fatalf("release = %+v, want v0.2.0", release)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one request within the cache interval", requests)
	}
	saved := readUpdateCheck(path)
	if saved.Release.TagName != "v0.2.0" || !saved.LastCheckedAt.Equal(now) {
		t.Fatalf("cached check = %+v", saved)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("cache permissions: info=%v err=%v", info, err)
		}
		if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("cache directory permissions: info=%v err=%v", info, err)
		}
	}
}

func TestStartupUpdateRefreshesExpiredAndInvalidCache(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "missing"},
		{name: "invalid JSON", content: "{broken"},
		{name: "partially decoded JSON", content: `{"last_checked_at":"2099-01-01T00:00:00Z","release":false}`},
		{name: "expired", content: updateCheckJSON(t, savedUpdateCheck{Release: startupTestRelease("v0.1.0"), LastCheckedAt: now.Add(-startupUpdateMaxAge)})},
		{name: "clock moved backwards", content: updateCheckJSON(t, savedUpdateCheck{LastCheckedAt: now.Add(time.Hour)})},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "updates.json")
			if test.content != "" {
				if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
				requests++
				_ = json.NewEncoder(output).Encode(startupTestRelease("v0.3.0"))
			}))
			defer server.Close()
			restoreUpdateEndpoints(t, server.URL, server.Client())
			release := startupUpdateRelease(context.Background(), path, "0.1.0", now)
			if release == nil || release.TagName != "v0.3.0" || requests != 1 {
				t.Fatalf("release=%+v requests=%d", release, requests)
			}
		})
	}
}

func TestStartupUpdateOnlyOffersNewerInstallableReleases(t *testing.T) {
	for _, test := range []struct {
		name    string
		current string
		modify  func(*updateRelease)
	}{
		{name: "same version", current: "0.2.0"},
		{name: "newer local build", current: "0.3.0"},
		{name: "invalid release", modify: func(release *updateRelease) { release.TagName = "not-a-version" }},
		{name: "draft", modify: func(release *updateRelease) { release.Draft = true }},
		{name: "prerelease", modify: func(release *updateRelease) { release.Prerelease = true }},
		{name: "missing platform binary", modify: func(release *updateRelease) { release.Assets = release.Assets[1:] }},
		{name: "missing checksum", modify: func(release *updateRelease) { release.Assets = release.Assets[:1] }},
		{name: "missing binary URL", modify: func(release *updateRelease) { release.Assets[0].URL = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			release := startupTestRelease("v0.2.0")
			if test.modify != nil {
				test.modify(&release)
			}
			server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
				_ = json.NewEncoder(output).Encode(release)
			}))
			defer server.Close()
			restoreUpdateEndpoints(t, server.URL, server.Client())
			current := test.current
			if current == "" {
				current = "0.1.0"
			}
			path := filepath.Join(t.TempDir(), "updates.json")
			if got := startupUpdateRelease(context.Background(), path, current, time.Now()); got != nil {
				t.Fatalf("unexpected update: %+v", got)
			}
			if test.modify != nil && !readUpdateCheck(path).LastCheckedAt.IsZero() {
				t.Fatal("incomplete or invalid releases must be retried on the next launch")
			}
		})
	}
}

func TestStartupUpdateDismissesOnlySelectedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates.json")
	now := time.Now().UTC()
	if err := saveUpdateCheck(path, savedUpdateCheck{Release: startupTestRelease("v0.2.0"), LastCheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := dismissStartupUpdate(path, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if got := startupUpdateRelease(context.Background(), path, "0.1.0", now); got != nil {
		t.Fatalf("dismissed update was offered: %+v", got)
	}
	saved := readUpdateCheck(path)
	if saved.DismissedVersion != "0.2.0" || saved.Release.TagName != "v0.2.0" || !saved.LastCheckedAt.Equal(now) {
		t.Fatalf("dismissal lost cached check: %+v", saved)
	}
	latest := "v0.2.0"
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(output).Encode(startupTestRelease(latest))
	}))
	defer server.Close()
	restoreUpdateEndpoints(t, server.URL, server.Client())
	if got := startupUpdateRelease(context.Background(), path, "0.1.0", now.Add(startupUpdateMaxAge)); got != nil {
		t.Fatalf("refresh forgot dismissal: %+v", got)
	}
	latest = "v0.3.0"
	if got := startupUpdateRelease(context.Background(), path, "0.1.0", now.Add(2*startupUpdateMaxAge)); got == nil || got.TagName != latest {
		t.Fatalf("new release was not offered after dismissal: %+v", got)
	}
}

func TestStartupUpdateFailureDoesNotBlockLaunch(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
				output.WriteHeader(status)
				fmt.Fprint(output, "not JSON")
			}))
			defer server.Close()
			restoreUpdateEndpoints(t, server.URL, server.Client())
			path := filepath.Join(t.TempDir(), "updates.json")
			if got := startupUpdateRelease(context.Background(), path, "0.1.0", time.Now()); got != nil {
				t.Fatalf("failed check returned an update: %+v", got)
			}
		})
	}
}

func TestStartupUpdateDeadlineAndOfflineCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	restoreUpdateEndpoints(t, server.URL, server.Client())
	now := time.Now().UTC()
	for _, cached := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "updates.json")
		if cached {
			if err := saveUpdateCheck(path, savedUpdateCheck{Release: startupTestRelease("v0.2.0"), LastCheckedAt: now.Add(-startupUpdateMaxAge)}); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		started := time.Now()
		release := startupUpdateRelease(ctx, path, "0.1.0", now)
		cancel()
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("check ignored deadline: %s", elapsed)
		}
		if cached != (release != nil) {
			t.Fatalf("cached=%v release=%+v", cached, release)
		}
	}
}

func TestStartupUpdateWorksWithoutWritableCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(output).Encode(startupTestRelease("v0.2.0"))
	}))
	defer server.Close()
	restoreUpdateEndpoints(t, server.URL, server.Client())
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := startupUpdateRelease(context.Background(), filepath.Join(parent, "updates.json"), "0.1.0", time.Now()); got == nil {
		t.Fatal("cache write failure suppressed an available update")
	}
}

func TestStartupUpdateSkipsDevelopmentBuildsAndNonInteractiveRuns(t *testing.T) {
	previousVersion := version
	t.Cleanup(func() { version = previousVersion })
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Error("unexpected release check")
		return nil, errors.New("unexpected release check")
	})}
	restoreUpdateEndpoints(t, "https://example.com/latest", client)
	path := filepath.Join(t.TempDir(), "updates.json")
	if got := startupUpdateRelease(context.Background(), path, "dev", time.Now()); got != nil {
		t.Fatalf("development build offered an update: %+v", got)
	}
	for _, test := range []struct {
		version string
		disable string
	}{
		{version: "dev"},
		{version: "0.1.0"},
		{version: "0.1.0", disable: "1"},
	} {
		version = test.version
		t.Setenv("PEARL_NO_UPDATE_CHECK", test.disable)
		stdout, stderr, code := captureUpdateOutput(t, func() int {
			handled, code := runStartupUpdate()
			if handled {
				t.Error("startup update handled a skipped launch")
			}
			return code
		})
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("skipped launch: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	}
}

func TestUpdatePromptKeyboard(t *testing.T) {
	for _, test := range []struct {
		name     string
		keys     string
		expected updateSelection
	}{
		{name: "default", keys: "\r", expected: updateNow},
		{name: "update shortcut", keys: "1", expected: updateNow},
		{name: "later shortcut", keys: "2", expected: updateLater},
		{name: "dismiss shortcut", keys: "3", expected: updateSkipVersion},
		{name: "arrow down", keys: "\x1b[B\r", expected: updateLater},
		{name: "arrow up wraps", keys: "\x1b[A\r", expected: updateSkipVersion},
		{name: "application arrows", keys: "\x1bOB\x1bOB\r", expected: updateSkipVersion},
		{name: "vim down", keys: "j\r", expected: updateLater},
		{name: "vim up wraps", keys: "k\r", expected: updateSkipVersion},
		{name: "wrap down", keys: "jjj\r", expected: updateNow},
		{name: "tab", keys: "\t\r", expected: updateLater},
		{name: "escape", keys: "\x1b", expected: updateLater},
		{name: "ctrl c", keys: "\x03", expected: updateLater},
		{name: "ctrl d", keys: "\x04", expected: updateLater},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			selected, err := updatePromptLoop(strings.NewReader(test.keys), &output, "0.1.0", "v0.2.0", false)
			if err != nil || selected != test.expected {
				t.Fatalf("selection=%d error=%v, want %d", selected, err, test.expected)
			}
			if !strings.Contains(output.String(), "0.1.0 → 0.2.0") {
				t.Fatal("prompt is missing current and latest versions")
			}
		})
	}
}

func TestUpdatePromptEOFDoesNotInstall(t *testing.T) {
	selected, err := updatePromptLoop(strings.NewReader(""), io.Discard, "0.1.0", "0.2.0", false)
	if selected != updateLater || !errors.Is(err, io.EOF) {
		t.Fatalf("selection=%d error=%v", selected, err)
	}
}

func TestRenderUpdatePrompt(t *testing.T) {
	plain := renderUpdatePrompt("v0.1.0", "v0.2.0", updateLater, false)
	for _, text := range []string{
		"Update available · 0.1.0 → 0.2.0", "https://github.com/August-H/pearl-cli/releases/latest",
		"Update now (runs `pearl update`)", "> 2. Install later", "3. Skip this version", "Enter continue", "Esc install later",
	} {
		if !strings.Contains(plain, text) {
			t.Fatalf("prompt is missing %q: %q", text, plain)
		}
	}
	if strings.Contains(plain, "\x1b") {
		t.Fatal("uncolored prompt contains ANSI styling")
	}
	colored := renderUpdatePrompt("0.1.0", "0.2.0", updateNow, true)
	if !strings.Contains(colored, ansiBold+ansiCyan+"  > 1. Update now") || !strings.Contains(colored, ansiReset) {
		t.Fatalf("selected option has no highlight: %q", colored)
	}
}

func updateCheckJSON(t *testing.T, saved savedUpdateCheck) string {
	t.Helper()
	content, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
