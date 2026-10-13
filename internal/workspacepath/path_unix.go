//go:build !windows

package workspacepath

import (
	"os"
	"path/filepath"
	"syscall"
)

func Canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func IsLink(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }

// LinkCount returns how many hard links point at a file.
func LinkCount(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 1
}
