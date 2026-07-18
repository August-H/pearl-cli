package openrouter_request

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

const pearlConfigDirectoryEnvironment = "PEARL_CONFIG_DIR"

func pearlConfigFiles(filename string, includeWorkingDirectory bool) []string {
	var paths []string

	if configDirectory := strings.TrimSpace(
		os.Getenv(pearlConfigDirectoryEnvironment),
	); configDirectory != "" {
		paths = append(paths, filepath.Join(configDirectory, filename))
	}

	if userConfigDirectory, err := os.UserConfigDir(); err == nil {
		userPath := filepath.Join(userConfigDirectory, "pearl", filename)
		if len(paths) == 0 || !strings.EqualFold(paths[0], userPath) {
			paths = append(paths, userPath)
		}
	}

	if includeWorkingDirectory {
		paths = append(paths, filename)
	}

	return paths
}

func loadPearlEnvironment() {
	for _, environmentFile := range pearlConfigFiles(".env", false) {
		if fileInfo, err := os.Stat(environmentFile); err != nil || fileInfo.IsDir() {
			continue
		}
		_ = godotenv.Load(environmentFile)
		return
	}
}
