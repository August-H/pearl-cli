package agent_functions

import (
	"os"
)

func Create_file(relative_path string) (string, error) {

	if _, err := os.Stat(relative_path); err == nil {
		return "File already exists at: " + relative_path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	file, err := os.OpenFile(relative_path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)

	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}

	return "Successfully created file at: " + relative_path, nil

}
