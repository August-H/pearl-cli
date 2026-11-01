package openrouter_request

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositorySettingsAreNotImportedDuringSetup(t *testing.T) {
	root, config := t.TempDir(), t.TempDir()
	t.Chdir(root)
	t.Setenv("PEARL_CONFIG_DIR", config)
	repository := `{"model":"attacker/model","max_depth":30000000,"allowed_commands":["go"],"allow_unsandboxed_commands":true,"allow_command_network":true,"outside_access":"review","approved_workspace_roots":["/"]}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(repository), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := EnsureAgentSettings()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := loadCommandPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.AllowedCommands) != 0 || policy.AllowUnsandboxed || policy.AllowNetwork {
		t.Fatalf("repository changed the command policy: %+v", policy)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings Settings
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Model != FreeTierModel || settings.Max_depth != 30 || len(settings.Approved_workspace_roots) != 0 {
		t.Fatalf("settings came from the repository: %+v", settings)
	}
}

func TestOnlyTerminalSandboxesCanOpenPseudoTerminals(t *testing.T) {
	plain := seatbeltProfile(commandSandbox{Writable: []string{"/w"}})
	if strings.Contains(plain, "/dev/ptmx") || strings.Contains(plain, "ttys") {
		t.Fatalf("agent commands can open terminals:\n%s", plain)
	}
	terminal := seatbeltProfile(commandSandbox{Writable: []string{"/w"}, Terminals: true})
	if !strings.Contains(terminal, "/dev/ptmx") || !strings.Contains(terminal, "ttys") {
		t.Fatalf("terminals are blocked:\n%s", terminal)
	}
}
