package pearlpaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveUsesConfigDirectoryOverride(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnvironment, directory)
	t.Setenv(socketPathEnvironment, "")
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		Directory: directory,
		Database:  filepath.Join(directory, "pearl.db"),
		Socket:    filepath.Join(directory, "pearl.sock"),
		Log:       filepath.Join(directory, "pearl.log"),
		Goal:      filepath.Join(directory, "goal.json"),
	}
	if paths != want {
		t.Fatalf("paths = %+v, want %+v", paths, want)
	}
}

func TestResolveMakesARelativeOverrideAbsolute(t *testing.T) {
	t.Setenv(configDirectoryEnvironment, "  relative/pearl  ")
	t.Setenv(socketPathEnvironment, "")
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(paths.Directory) || filepath.Base(paths.Directory) != "pearl" {
		t.Fatalf("directory = %q", paths.Directory)
	}
}

func TestResolveDefaultsToUserConfigDirectory(t *testing.T) {
	t.Setenv(configDirectoryEnvironment, "")
	t.Setenv(socketPathEnvironment, "")
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config directory: %v", err)
	}
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Directory != filepath.Join(userConfig, "pearl") {
		t.Fatalf("directory = %q", paths.Directory)
	}
}

func TestResolveSocketOverrideChangesOnlyTheSocket(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(t.TempDir(), "custom.sock")
	t.Setenv(configDirectoryEnvironment, directory)
	t.Setenv(socketPathEnvironment, socket)
	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Socket != socket || paths.Database != filepath.Join(directory, "pearl.db") {
		t.Fatalf("paths = %+v", paths)
	}
}

func TestEnsureCreatesAndTightensThePrivateDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "pearl")
	if err := Ensure(Paths{Directory: directory}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(Paths{Directory: directory}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("permissions = %o, want 700", info.Mode().Perm())
	}
}
