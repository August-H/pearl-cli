package agent_functions

import (
	"io/fs"

	"path/filepath"
)

func View_fileTree(relative_path string) ([]string, error) {
	var filetree []string
	err := filepath.WalkDir(relative_path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		filetree = append(filetree, path)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return filetree, nil
}
