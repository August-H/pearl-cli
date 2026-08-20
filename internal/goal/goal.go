package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
)

type Goal struct {
	Text          string    `json:"text"`
	WorkspaceRoot string    `json:"workspace_root"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func Save(text, workspaceRoot string) (Goal, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Goal{}, errors.New("goal cannot be empty")
	}
	workspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return Goal{}, fmt.Errorf("resolve goal workspace: %w", err)
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		return Goal{}, fmt.Errorf("inspect goal workspace: %w", err)
	}
	if !info.IsDir() {
		return Goal{}, fmt.Errorf("goal workspace %q is not a directory", workspaceRoot)
	}
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return Goal{}, err
	}
	if err := pearlpaths.Ensure(paths); err != nil {
		return Goal{}, err
	}
	goal := Goal{Text: text, WorkspaceRoot: workspaceRoot, UpdatedAt: time.Now().UTC()}
	contents, err := json.MarshalIndent(goal, "", "  ")
	if err != nil {
		return Goal{}, err
	}
	temporary, err := os.CreateTemp(paths.Directory, ".goal-*")
	if err != nil {
		return Goal{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Goal{}, err
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		_ = temporary.Close()
		return Goal{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Goal{}, err
	}
	if err := temporary.Close(); err != nil {
		return Goal{}, err
	}
	if err := os.Rename(temporaryPath, paths.Goal); err != nil {
		return Goal{}, err
	}
	return goal, nil
}

func Load() (Goal, error) {
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return Goal{}, err
	}
	contents, err := os.ReadFile(paths.Goal)
	if errors.Is(err, os.ErrNotExist) {
		return Goal{}, errors.New("no goal is set; use: pearl goal \"what you want to accomplish\"")
	}
	if err != nil {
		return Goal{}, err
	}
	var goal Goal
	if err := json.Unmarshal(contents, &goal); err != nil {
		return Goal{}, fmt.Errorf("decode saved goal: %w", err)
	}
	if strings.TrimSpace(goal.Text) == "" || strings.TrimSpace(goal.WorkspaceRoot) == "" {
		return Goal{}, errors.New("saved goal is incomplete; set it again with: pearl goal \"...\"")
	}
	return goal, nil
}
