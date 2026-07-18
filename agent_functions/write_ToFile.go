package agent_functions

import (
	"errors"
	"os"
	"path/filepath"
)

func Write_ToFile(relative_path string, content string) error {

	absolute_filepath, err := filepath.Abs(relative_path)
	if err != nil {
		return err
	}
	if len(absolute_filepath) < 41 {
		return errors.New("Can't access this")
	}

	if _, err := os.ReadFile(relative_path); err != nil {
		return err
	}

	return os.WriteFile(relative_path, []byte(content), 0644)
}
