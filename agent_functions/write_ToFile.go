package agent_functions

import (
	"fmt"
	"os"
	"path/filepath"
)

func Write_ToFile(relative_path string, content string) error {

	fileInfo, err := os.Stat(relative_path)
	if err != nil {
		return err
	}
	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", relative_path)
	}

	temporaryFile, err := os.CreateTemp(filepath.Dir(relative_path), ".pearl-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporaryFile.Name()
	defer os.Remove(temporaryPath)
	if err := temporaryFile.Chmod(fileInfo.Mode().Perm()); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if _, err := temporaryFile.WriteString(content); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Sync(); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, relative_path)
}
