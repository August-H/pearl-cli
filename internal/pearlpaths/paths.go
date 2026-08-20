package pearlpaths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	configDirectoryEnvironment = "PEARL_CONFIG_DIR"
	socketPathEnvironment      = "PEARL_SOCKET"
)

type Paths struct {
	Directory string
	Database  string
	Socket    string
	Log       string
	Goal      string
}

func Resolve() (Paths, error) {
	directory := strings.TrimSpace(os.Getenv(configDirectoryEnvironment))
	if directory == "" {
		userConfigDirectory, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, errors.New("could not determine the user config directory; set PEARL_CONFIG_DIR")
		}
		directory = filepath.Join(userConfigDirectory, "pearl")
	}

	directory, err := filepath.Abs(directory)
	if err != nil {
		return Paths{}, err
	}
	socket := strings.TrimSpace(os.Getenv(socketPathEnvironment))
	if socket == "" {
		socket = filepath.Join(directory, "pearl.sock")
	}

	return Paths{
		Directory: directory,
		Database:  filepath.Join(directory, "pearl.db"),
		Socket:    socket,
		Log:       filepath.Join(directory, "pearl.log"),
		Goal:      filepath.Join(directory, "goal.json"),
	}, nil
}

func Ensure(paths Paths) error {
	if err := os.MkdirAll(paths.Directory, 0o700); err != nil {
		return err
	}
	return os.Chmod(paths.Directory, 0o700)
}
