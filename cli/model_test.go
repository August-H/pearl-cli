package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/openrouter_request"
)

func readTestSettingsModel(t *testing.T, dir string) string {
	t.Helper()
	contents, err := os.ReadFile(dir + "/settings.json")
	if err != nil {
		// PEARL_CONFIG_DIR may resolve through the OS config dir on some
		// platforms; fall back to the durable settings loader.
		settings, loadErr := openrouter_request.LoadAgentSettings()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return settings.Model
	}
	var settings openrouter_request.Settings
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	return settings.Model
}

func TestModelFreeSetsFreeTier(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"model", "--free"})
	})
	if exitCode != 0 {
		t.Fatalf("model --free exit=%d output=%q", exitCode, output)
	}
	if !strings.Contains(output, "openrouter/free") {
		t.Fatalf("model --free output=%q", output)
	}
	if got := readTestSettingsModel(t, configDirectory); got != "openrouter/free" {
		t.Fatalf("settings model = %q", got)
	}
}

func TestModelSetSavesCustomID(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"model", "--set", "anthropic/claude-fable-5.1"})
	})
	if exitCode != 0 {
		t.Fatalf("model --set exit=%d output=%q", exitCode, output)
	}
	if !strings.Contains(output, "anthropic/claude-fable-5.1") {
		t.Fatalf("model --set output=%q", output)
	}
	if got := readTestSettingsModel(t, configDirectory); got != "anthropic/claude-fable-5.1" {
		t.Fatalf("settings model = %q", got)
	}
}

func TestModelRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{"model", "--bogus"},
		{"model", "extra-arg"},
		{"model", "--free", "--set", "x"},
		{"model", "--set"},
	} {
		if exitCode := Run(args); exitCode != 2 {
			t.Fatalf("Run(%v) exit=%d, want 2", args, exitCode)
		}
	}
}
