package workspacepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
)

// deniedHomePaths hold credentials, other apps' private data, or files that
// run code when you log in or open a shell. No agent may read or write them,
// even with an approved outside-access request.
var deniedHomePaths = []string{
	".ssh", ".aws", ".azure", ".gnupg", ".kube", ".docker", ".netrc",
	".git-credentials", ".npmrc", ".pypirc", ".codex", ".cursor",
	".config/gcloud", ".config/gh", ".config/op", ".password-store",
	".zshrc", ".zprofile", ".zshenv", ".bashrc", ".bash_profile", ".profile",
	".config/fish",
	"Library/Keychains", "Library/Cookies", "Library/LaunchAgents", "Library/Mail",
	"Library/Messages", "Library/Application Support/Google/Chrome",
	"Library/Application Support/Firefox", "Library/Application Support/com.apple.TCC",
	"AppData/Roaming/Microsoft/Credentials", "AppData/Local/Google/Chrome",
}

// DeniedRoots returns every path no agent may touch: the list above plus
// Pearl's own config folder, which holds its API key, settings, and database.
func DeniedRoots() []string {
	var roots []string
	if paths, err := pearlpaths.Resolve(); err == nil {
		roots = append(roots, canonicalOrClean(paths.Directory))
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, relative := range deniedHomePaths {
			if strings.HasPrefix(relative, "Library/") && runtime.GOOS != "darwin" ||
				strings.HasPrefix(relative, "AppData/") && runtime.GOOS != "windows" {
				continue
			}
			roots = append(roots, canonicalOrClean(filepath.Join(home, filepath.FromSlash(relative))))
		}
	}
	return roots
}

// Denied reports whether path is, or is inside, a denied root.
func Denied(path string) bool {
	path = canonicalOrClean(path)
	for _, root := range DeniedRoots() {
		if containsFold(root, path) {
			return true
		}
	}
	return false
}

// CheckJobDirectory rejects job directories that are too broad to contain an
// agent: the filesystem root, the home folder, or any folder holding a denied
// path such as Pearl's own settings.
func CheckJobDirectory(path string) error {
	path = canonicalOrClean(path)
	if filepath.Dir(path) == path {
		return errors.New("Pearl won't run jobs in the filesystem root; choose a project folder")
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = canonicalOrClean(home)
		if containsFold(path, home) {
			return errors.New("Pearl won't run jobs in your home folder or above it; choose a project folder")
		}
	}
	for _, root := range DeniedRoots() {
		if containsFold(path, root) || containsFold(root, path) {
			return fmt.Errorf("Pearl won't run jobs in %s because it holds protected files such as %s; choose a project folder", path, root)
		}
	}
	return nil
}

// canonicalOrClean resolves links in the longest existing part of path, so
// paths that do not exist yet still compare correctly.
func canonicalOrClean(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	existing, rest := absolute, ""
	for {
		if resolved, err := Canonical(existing); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return absolute
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}

// containsFold is Contains, ignoring case where the filesystem usually does.
func containsFold(root, target string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		root, target = strings.ToLower(root), strings.ToLower(target)
	}
	return Contains(root, target)
}

// CanonicalPath resolves links in the existing part of path.
func CanonicalPath(path string) string {
	return canonicalOrClean(path)
}

// ContainsPath reports whether target is root or inside it.
func ContainsPath(root, target string) bool {
	return containsFold(canonicalOrClean(root), canonicalOrClean(target))
}
