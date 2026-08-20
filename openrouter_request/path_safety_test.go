package openrouter_request

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafeAgentPathRejectsEscapesAndSecrets(t *testing.T) {
	workspace := t.TempDir()
	if _, err := safeAgentPath(workspace, "../outside"); err == nil {
		t.Fatal("expected parent traversal to be rejected")
	}
	if _, err := safeAgentPath(workspace, ".env.local"); err == nil {
		t.Fatal("expected environment file to be rejected")
	}
	if _, err := safeAgentPath(workspace, ".git/config"); err == nil {
		t.Fatal("expected .git path to be rejected")
	}
}

func TestSafeAgentPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := safeAgentPath(workspace, "outside/file.txt"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	inside := filepath.Join(workspace, "inside.txt")
	if err := os.WriteFile(inside, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := safeAgentPath(workspace, "inside.txt")
	if err != nil {
		t.Fatal(err)
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	insideInfo, err := os.Stat(inside)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(resolvedInfo, insideInfo) {
		t.Fatalf("resolved path = %q, want same file as %q", resolved, inside)
	}
}
