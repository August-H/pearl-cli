package agent_functions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func openTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, directory
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileInReadsAndEnforcesItsLimit(t *testing.T) {
	root, directory := openTestRoot(t)
	writeTestFile(t, filepath.Join(directory, "notes.txt"), "hello")
	content, err := ReadFileIn(root, "notes.txt", 5)
	if err != nil || content != "hello" {
		t.Fatalf("read = %q, %v", content, err)
	}
	if _, err := ReadFileIn(root, "notes.txt", 4); err == nil || !strings.Contains(err.Error(), "maximum readable size") {
		t.Fatalf("oversized read error = %v", err)
	}
}

func TestReadFileInRefusesDirectoriesAndEscapes(t *testing.T) {
	root, directory := openTestRoot(t)
	if err := os.Mkdir(filepath.Join(directory, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileIn(root, "folder", 1024); err == nil {
		t.Fatal("read a directory")
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeTestFile(t, outside, "secret")
	if _, err := ReadFileIn(root, filepath.Join("..", filepath.Base(filepath.Dir(outside)), "secret.txt"), 1024); err == nil {
		t.Fatal("read through a parent-directory path")
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link.txt")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need extra privileges on Windows")
		}
		t.Fatal(err)
	}
	if content, err := ReadFileIn(root, "link.txt", 1024); err == nil {
		t.Fatalf("followed a symlink out of the root: %q", content)
	}
}

func TestReadFileInRefusesHardLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link counts aren't read on Windows")
	}
	root, directory := openTestRoot(t)
	original := filepath.Join(directory, "original.txt")
	writeTestFile(t, original, "content")
	if err := os.Link(original, filepath.Join(directory, "alias.txt")); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	if _, err := ReadFileIn(root, "alias.txt", 1024); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard link read error = %v", err)
	}
}

func TestCreateFileInReportsExistingFiles(t *testing.T) {
	root, directory := openTestRoot(t)
	existed, err := CreateFileIn(root, "new.txt")
	if err != nil || existed {
		t.Fatalf("create = %v, %v", existed, err)
	}
	writeTestFile(t, filepath.Join(directory, "new.txt"), "keep")
	existed, err = CreateFileIn(root, "new.txt")
	if err != nil || !existed {
		t.Fatalf("second create = %v, %v", existed, err)
	}
	if content, err := os.ReadFile(filepath.Join(directory, "new.txt")); err != nil || string(content) != "keep" {
		t.Fatalf("existing file changed: %q, %v", content, err)
	}
}

func TestWriteFileInReplacesAtomicallyAndKeepsPermissions(t *testing.T) {
	root, directory := openTestRoot(t)
	path := filepath.Join(directory, "file.txt")
	writeTestFile(t, path, "old")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileIn(root, "file.txt", "new"); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "new" {
		t.Fatalf("content = %q, %v", content, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("permissions = %o, want 640", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

func TestWriteFileInRefusesMissingFilesAndLinks(t *testing.T) {
	root, directory := openTestRoot(t)
	if err := WriteFileIn(root, "missing.txt", "x"); err == nil {
		t.Fatal("wrote a file that does not exist")
	}
	target := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, target, "untouched")
	if err := os.Symlink(target, filepath.Join(directory, "link.txt")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need extra privileges on Windows")
		}
		t.Fatal(err)
	}
	if err := WriteFileIn(root, "link.txt", "changed"); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "untouched" {
		t.Fatalf("outside file changed: %q, %v", content, err)
	}
}
