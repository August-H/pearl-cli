package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/August-H/pearl-cli/internal/store"
)

func currentWorkspace() string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	return resolveWorkspacePath(directory)
}

func resolveWorkspacePath(path string) string {
	cleaned := filepath.Clean(path)
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return cleaned
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return absolute
	}
	return resolved
}

func filterJobsForWorkspace(jobs []store.Job, workspace string) []store.Job {
	if workspace == "" {
		return jobs
	}
	scoped := make([]store.Job, 0, len(jobs))
	for _, job := range jobs {
		if jobBelongsToWorkspace(job.WorkspaceRoot, workspace) {
			scoped = append(scoped, job)
		}
	}
	return scoped
}

func jobBelongsToWorkspace(workspaceRoot, root string) bool {
	return pathContains(resolveWorkspacePath(root), resolveWorkspacePath(workspaceRoot))
}

func pathContains(root, target string) bool {
	if strings.EqualFold(root, target) {
		return true
	}
	attempts := [2][2]string{
		{root, target},
		{strings.ToLower(root), strings.ToLower(target)},
	}
	for _, attempt := range attempts {
		relative, err := filepath.Rel(attempt[0], attempt[1])
		if err != nil || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

const workspaceBoardLabelLimit = 32

func workspaceBoardLabel(directory string) string {
	label := strings.ReplaceAll(displayJobDirectory(directory), "\n", " ")
	runes := []rune(label)
	if len(runes) > workspaceBoardLabelLimit {
		return "…" + string(runes[len(runes)-workspaceBoardLabelLimit+1:])
	}
	return label
}
