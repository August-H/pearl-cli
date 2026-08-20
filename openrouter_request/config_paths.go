package openrouter_request

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

const pearlConfigDirectoryEnvironment = "PEARL_CONFIG_DIR"

const openRouterAPIKeyEnvironment = "OPENROUTER_API_KEY"

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

// loadOpenRouterAPIKey reads Pearl's config on every request so a daemon that
// is already running immediately sees keys saved by `pearl configure`.
func loadOpenRouterAPIKey() (string, error) {
	for _, environmentFile := range pearlConfigFiles(".env", false) {
		values, err := godotenv.Read(environmentFile)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", environmentFile, err)
		}

		apiKey := strings.TrimSpace(values[openRouterAPIKeyEnvironment])
		if apiKey != "" {
			return apiKey, nil
		}
	}

	apiKey := strings.TrimSpace(os.Getenv(openRouterAPIKeyEnvironment))
	if apiKey == "" {
		return "", errors.New(
			"OpenRouter API key is not configured; run `pearl configure`",
		)
	}
	return apiKey, nil
}

// ConfigureOpenRouterAPIKey stores the OpenRouter API key in Pearl's runtime
// environment file and returns the path that was written.
func ConfigureOpenRouterAPIKey(apiKey string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if len(apiKey) >= 2 {
		first, last := apiKey[0], apiKey[len(apiKey)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			apiKey = strings.TrimSpace(apiKey[1 : len(apiKey)-1])
		}
	}
	if apiKey == "" {
		return "", errors.New("OpenRouter API key cannot be empty")
	}

	environmentFiles := pearlConfigFiles(".env", false)
	if len(environmentFiles) == 0 {
		return "", errors.New(
			"could not determine a config directory; set PEARL_CONFIG_DIR",
		)
	}
	environmentFile := environmentFiles[0]

	values := make(map[string]string)
	if _, err := os.Stat(environmentFile); err == nil {
		values, err = godotenv.Read(environmentFile)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", environmentFile, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s: %w", environmentFile, err)
	}

	values[openRouterAPIKeyEnvironment] = apiKey
	contents, err := godotenv.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode Pearl environment: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(environmentFile), 0o700); err != nil {
		return "", fmt.Errorf("create Pearl config directory: %w", err)
	}
	if err := os.WriteFile(environmentFile, []byte(contents+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", environmentFile, err)
	}
	if err := os.Chmod(environmentFile, 0o600); err != nil {
		return "", fmt.Errorf("secure %s: %w", environmentFile, err)
	}

	return environmentFile, nil
}

// EnsureAgentSettings copies a valid working-directory settings file into the
// durable Pearl config directory. If Pearl is installed without one, it writes
// conservative single-agent defaults.
func EnsureAgentSettings() (string, error) {
	targets := pearlConfigFiles("settings.json", false)
	if len(targets) == 0 {
		return "", errors.New("could not determine a config directory; set PEARL_CONFIG_DIR")
	}
	target := targets[0]
	if contents, err := os.ReadFile(target); err == nil {
		if err := validateSettings(contents); err != nil {
			return "", fmt.Errorf("validate %s: %w", target, err)
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var contents []byte
	workingDirectorySettings, err := os.ReadFile("settings.json")
	if err == nil {
		contents = workingDirectorySettings
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		contents = []byte(`{
  "username": "",
  "model": "poolside/laguna-s-2.1:free",
  "max_concurrency": 1,
  "max_depth": 30,
  "max_job_seconds": 1800,
  "max_file_bytes": 4194304,
  "approved_workspace_roots": [],
  "mode": "default"
}
`)
	}
	if err := validateSettings(contents); err != nil {
		return "", fmt.Errorf("validate settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, contents, 0o600); err != nil {
		return "", err
	}
	return target, nil
}

// ConfigureAgentModel updates the model in Pearl's durable settings file and
// preserves every other setting.
func ConfigureAgentModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("model cannot be empty")
	}

	settingsPath, err := EnsureAgentSettings()
	if err != nil {
		return "", err
	}
	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", settingsPath, err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(contents, &settings); err != nil {
		return "", fmt.Errorf("decode %s: %w", settingsPath, err)
	}
	encodedModel, err := json.Marshal(model)
	if err != nil {
		return "", fmt.Errorf("encode model: %w", err)
	}
	settings["model"] = encodedModel
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", settingsPath, err)
	}
	updated = append(updated, '\n')
	if err := validateSettings(updated); err != nil {
		return "", fmt.Errorf("validate %s: %w", settingsPath, err)
	}
	if err := os.WriteFile(settingsPath, updated, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", settingsPath, err)
	}
	if err := os.Chmod(settingsPath, 0o600); err != nil {
		return "", fmt.Errorf("secure %s: %w", settingsPath, err)
	}
	return settingsPath, nil
}

func validateSettings(contents []byte) error {
	var settings Settings
	if err := json.Unmarshal(contents, &settings); err != nil {
		return err
	}
	if strings.TrimSpace(settings.Model) == "" {
		return errors.New("model cannot be empty")
	}
	return nil
}
