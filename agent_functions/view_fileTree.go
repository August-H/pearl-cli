package agent_functions

import (
	"io/fs"
	"path/filepath"
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
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Name() == ".env" {
			return nil
		}

		pathFromRoot, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		filetree = append(filetree, filepath.ToSlash(pathFromRoot))

		return nil
	})

	if err != nil {
		return nil, err
	}

	return filetree, nil
}
