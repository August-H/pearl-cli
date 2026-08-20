package agent_functions

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

func View_fileTree(relative_path string) ([]string, error) {
	var filetree []string

	root, err := filepath.Abs(relative_path)
	if err != nil {
		return nil, err
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() && strings.EqualFold(d.Name(), ".git") {
			return filepath.SkipDir
		}
		if strings.HasPrefix(strings.ToLower(d.Name()), ".env") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		pathFromRoot, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		filetree = append(filetree, filepath.ToSlash(pathFromRoot))
		if len(filetree) > 10000 {
			return fmt.Errorf("file tree exceeds the 10000-entry limit")
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return filetree, nil
}
