package agent_functions

import (
	"os"
)

func Create_file(relative_path string) (string, error) {

	if _, err := os.ReadFile(relative_path); err == nil {
		return "File already exsists at: " + relative_path, nil
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
