// Package workspacepath validates paths used by agent filesystem tools.
package workspacepath

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func Protected(name string) bool {
	return strings.EqualFold(name, ".git") || strings.HasPrefix(strings.ToLower(name), ".env")
}

// Resolve rejects links below the workspace, including Windows junctions. Missing
// suffixes are allowed for directory creation; every existing ancestor is checked.
func Resolve(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("path must be relative to the workspace")
	}
	root, err := Canonical(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(relative)
	path := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		if Protected(part) || (runtime.GOOS == "windows" && (strings.Contains(part, ":") || strings.TrimRight(part, ". ") != part)) {
			return "", fmt.Errorf("access to %q is not allowed", relative)
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if IsLink(info) {
			return "", fmt.Errorf("links and junctions are not allowed: %s", relative)
		}
	}
	return path, nil
}

func Contains(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
