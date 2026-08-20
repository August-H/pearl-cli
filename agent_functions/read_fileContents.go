package agent_functions

import (
	"os"
)

func Read_fileContents(relative_path string) (string, error) {

	bytes, err := os.ReadFile(relative_path)

	if err != nil {
		return "", err
	}

	return string(bytes), nil

}
