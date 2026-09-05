package openrouter_request

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryCannotEnableCommandsDuringSetup(t *testing.T) {
	root, config := t.TempDir(), t.TempDir()
	t.Chdir(root)
	t.Setenv("PEARL_CONFIG_DIR", config)
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"model":"dummy","allowed_commands":["go"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAgentSettings(); err != nil {
		t.Fatal(err)
	}
	allowed, err := allowedCommands()
	if err != nil || len(allowed) != 0 {
		t.Fatalf("imported command policy=%v error=%v", allowed, err)
	}
}
