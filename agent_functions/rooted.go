package agent_functions

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/August-H/pearl-cli/internal/workspacepath"
)

// The *In functions do all file access through an os.Root, which refuses at
// open time to follow any link out of the root. Names are relative to it.

// ReadFileIn reads a regular file. Files with more than one hard link are
// refused, because the other link may be a file outside the root.
func ReadFileIn(root *os.Root, name string, maxBytes int64) (string, error) {
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return readOpenFile(file, maxBytes)
}

func readOpenFile(file fs.File, maxBytes int64) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	if links := workspacepath.LinkCount(info); links > 1 {
		return "", fmt.Errorf("file has %d hard links, so it may be a file from outside the workspace; Pearl won't read it", links)
	}
	if info.Size() > maxBytes {
		return "", fmt.Errorf("file is %d bytes; maximum readable size is %d", info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", errors.New("file exceeds the readable size limit")
	}
	return string(data), nil
}

// CreateFileIn creates an empty file and reports whether it already existed.
func CreateFileIn(root *os.Root, name string) (bool, error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, file.Close()
}

// WriteFileIn replaces an existing regular file. It writes a temporary file
// and renames it into place, so it never writes through a link.
func WriteFileIn(root *os.Root, name, content string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(name), ".pearl-write-"+hex.EncodeToString(suffix))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}
