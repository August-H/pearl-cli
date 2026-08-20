package openrouter_request

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/joho/godotenv"
)

func TestConfigureOpenRouterAPIKey(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv(pearlConfigDirectoryEnvironment, configDirectory)

	environmentFile := filepath.Join(configDirectory, ".env")
	if err := os.WriteFile(environmentFile, []byte("EXISTING=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writtenPath, err := ConfigureOpenRouterAPIKey("  test-key  ")
	if err != nil {
		t.Fatal(err)
	}
	if writtenPath != environmentFile {
		t.Fatalf("written path = %q, want %q", writtenPath, environmentFile)
	}

	values, err := godotenv.Read(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	if values[openRouterAPIKeyEnvironment] != "test-key" {
		t.Fatalf("API key = %q, want %q", values[openRouterAPIKeyEnvironment], "test-key")
	}
	if values["EXISTING"] != "value" {
		t.Fatalf("existing value = %q, want %q", values["EXISTING"], "value")
	}

	if runtime.GOOS != "windows" {
		fileInfo, err := os.Stat(environmentFile)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := fileInfo.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("permissions = %o, want 600", permissions)
		}
	}
}

func TestConfigureOpenRouterAPIKeyRejectsEmptyKey(t *testing.T) {
	t.Setenv(pearlConfigDirectoryEnvironment, t.TempDir())

	if _, err := ConfigureOpenRouterAPIKey("  "); err == nil {
		t.Fatal("expected an empty API key to be rejected")
	}
}

func TestConfigureOpenRouterAPIKeyRemovesCopiedQuotes(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv(pearlConfigDirectoryEnvironment, configDirectory)

	if _, err := ConfigureOpenRouterAPIKey(`"sk-or-v1-example"`); err != nil {
		t.Fatal(err)
	}
	values, err := godotenv.Read(filepath.Join(configDirectory, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if values[openRouterAPIKeyEnvironment] != "sk-or-v1-example" {
		t.Fatalf("API key was not normalized")
	}
}

func TestLoadOpenRouterAPIKeyPrefersFreshPearlConfig(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv(pearlConfigDirectoryEnvironment, configDirectory)
	t.Setenv(openRouterAPIKeyEnvironment, "stale-key")

	if _, err := ConfigureOpenRouterAPIKey("fresh-key"); err != nil {
		t.Fatal(err)
	}
	apiKey, err := loadOpenRouterAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "fresh-key" {
		t.Fatalf("API key = %q, want the freshly configured key", apiKey)
	}
}

func TestEnsureAgentSettingsWritesDurableDefaults(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv(pearlConfigDirectoryEnvironment, configDirectory)

	settingsPath, err := EnsureAgentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settingsPath != filepath.Join(configDirectory, "settings.json") {
		t.Fatalf("settings path = %q", settingsPath)
	}
	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSettings(contents); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("settings permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestConfigureAgentModelPreservesOtherSettings(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv(pearlConfigDirectoryEnvironment, configDirectory)
	settingsPath := filepath.Join(configDirectory, "settings.json")
	original := []byte(`{
  "model": "old/model",
  "max_depth": 42,
  "custom_setting": "keep me"
}`)
	if err := os.WriteFile(settingsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	writtenPath, err := ConfigureAgentModel("  openrouter/free  ")
	if err != nil {
		t.Fatal(err)
	}
	if writtenPath != settingsPath {
		t.Fatalf("written path = %q, want %q", writtenPath, settingsPath)
	}
	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["model"] != "openrouter/free" || settings["custom_setting"] != "keep me" {
		t.Fatalf("updated settings = %#v", settings)
	}
	if settings["max_depth"] != float64(42) {
		t.Fatalf("max_depth changed to %#v", settings["max_depth"])
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("settings permissions = %o, want 600", info.Mode().Perm())
		}
	}
}
