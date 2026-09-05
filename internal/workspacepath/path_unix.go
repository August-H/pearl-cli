//go:build !windows

package workspacepath

import (
	"os"
	"path/filepath"
)

func Canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func IsLink(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }
