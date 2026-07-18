package agent_functions

import (
	"os"

	"errors"
	"path/filepath"
)

func Read_fileContents(relative_path string) (string, error) {

	if relative_path == ".env" {
		return "", errors.New("Unable to read " + relative_path + ". No permission!")

	}

	absolute_filepath, err := filepath.Abs(relative_path)
	if err != nil {
		return "", err
	}

	if len(absolute_filepath) < 41 {
		return "", errors.New("Can't access this")
	}
	bytes, err := os.ReadFile(absolute_filepath)

	if err != nil {
		return "", err
	}

	return string(bytes), nil

}
