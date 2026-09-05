package workspacepath

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRejectProtectedAliases(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, ".git")
	if err := os.Mkdir(protected, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protected, "config"), []byte("synthetic data"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if runtime.GOOS == "windows" {
		t.Setenv("PEARL_TEST_ALIAS", alias)
		t.Setenv("PEARL_TEST_TARGET", protected)
		command := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path $env:PEARL_TEST_ALIAS -Target $env:PEARL_TEST_TARGET | Out-Null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("junction: %v %s", err, output)
		}
	} else if err := os.Symlink(protected, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"alias/config", "alias/new.txt", ".git/config", ".env.local", "../outside"} {
		if resolved, err := Resolve(root, path); err == nil {
			t.Errorf("accepted %s: %s", path, resolved)
		}
	}
	if _, err := Resolve(root, "nested/new/file.txt"); err != nil {
		t.Fatal(err)
	}
	resolved, err := Canonical(alias)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Canonical(protected)
	if resolved != want {
		t.Fatalf("canonical alias=%s want=%s", resolved, want)
	}
}
