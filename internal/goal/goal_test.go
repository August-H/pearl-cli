package goal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveAndLoadGoal(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	workspace := t.TempDir()

	saved, err := Save("  finish the project  ", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Text != "finish the project" || saved.WorkspaceRoot != workspace {
		t.Fatalf("saved goal = %#v", saved)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != saved {
		t.Fatalf("loaded goal = %#v, want %#v", loaded, saved)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(configDirectory, "goal.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("goal permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestLoadGoalExplainsHowToSetOne(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	if _, err := Load(); err == nil {
		t.Fatal("expected a missing goal error")
	}
}
