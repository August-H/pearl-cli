package workspacepath

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestJobDirectoriesMustBeNarrowAndOutsideProtectedFolders(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)

	project := t.TempDir()
	if err := CheckJobDirectory(project); err != nil {
		t.Fatalf("ordinary project rejected: %v", err)
	}
	for _, rejected := range []string{
		string(filepath.Separator),
		home,
		filepath.Dir(home),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws", "nested"),
		config,
		filepath.Join(config, "jobs"),
		filepath.Dir(config),
	} {
		if err := CheckJobDirectory(rejected); err == nil {
			t.Errorf("CheckJobDirectory(%q) accepted", rejected)
		}
	}
}

func TestDeniedCoversCredentialsAndPearlConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)
	for _, path := range []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".config", "gh", "hosts.yml"),
		filepath.Join(config, "credentials.json"),
	} {
		if !Denied(path) {
			t.Errorf("Denied(%q) = false", path)
		}
	}
	for _, path := range []string{t.TempDir(), filepath.Join(home, ".sshfoo")} {
		if Denied(path) {
			t.Errorf("Denied(%q) = true", path)
		}
	}
}

func TestLinkCountSeesHardLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link counts aren't read on Windows")
	}
	directory := t.TempDir()
	original := filepath.Join(directory, "original")
	if err := os.WriteFile(original, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(original)
	if LinkCount(info) != 1 {
		t.Fatalf("LinkCount = %d", LinkCount(info))
	}
	if err := os.Link(original, filepath.Join(directory, "copy")); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(original)
	if LinkCount(info) != 2 {
		t.Fatalf("LinkCount after link = %d", LinkCount(info))
	}
}
