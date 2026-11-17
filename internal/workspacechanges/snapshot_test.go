package workspacechanges

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, directory, name, text string) {
	t.Helper()
	path := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, directory string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s, %v", args, output, err)
	}
}

func capture(t *testing.T, directory string, previous *Snapshot) Snapshot {
	t.Helper()
	snapshot, err := Capture(context.Background(), directory, previous)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCaptureComparesDirtyFilesAndShellChanges(t *testing.T) {
	directory := t.TempDir()
	git(t, directory, "init", "-q")
	writeFile(t, directory, ".gitignore", "ignored/\n*.log\ndist/\n")
	writeFile(t, directory, "tracked.txt", "committed\n")
	writeFile(t, directory, "dist/tracked.txt", "tracked despite ignore\n")
	git(t, directory, "add", "tracked.txt", ".gitignore")
	git(t, directory, "add", "-f", "dist/tracked.txt")
	writeFile(t, directory, "tracked.txt", "already dirty\n")
	writeFile(t, directory, "unchanged.txt", "pre-existing untracked\n")
	writeFile(t, directory, "deleted.txt", "remove me\n")
	writeFile(t, directory, "ignored/private.txt", "excluded\n")
	writeFile(t, directory, ".env.local", "secret\n")
	before := capture(t, directory, nil)
	if _, ok := before.Files["dist/tracked.txt"]; !ok {
		t.Fatal("tracked ignored file missing")
	}
	for _, path := range []string{".env.local", "ignored/private.txt", ".git/config"} {
		if _, ok := before.Files[path]; ok {
			t.Fatalf("captured excluded file %s", path)
		}
	}
	writeFile(t, directory, "tracked.txt", "changed by shell\n")
	writeFile(t, directory, "new.txt", "created by shell\n")
	if err := os.Remove(filepath.Join(directory, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	after := capture(t, directory, &before)
	report := Compare(before, after)
	if len(report.Changes) != 3 {
		t.Fatalf("changes = %+v", report)
	}
	changes := map[string]Change{}
	for _, change := range report.Changes {
		changes[change.Path] = change
	}
	if changes["deleted.txt"].Action != "deleted" || changes["new.txt"].Action != "created" || changes["tracked.txt"].Action != "modified" {
		t.Fatalf("actions = %+v", changes)
	}
	diff := changes["tracked.txt"].Diff
	if !strings.Contains(diff, "-already dirty\n+changed by shell\n") || strings.Contains(diff, "committed") {
		t.Fatalf("wrong baseline: %s", diff)
	}
	// An ignore edit must not make a still-present file look deleted.
	writeFile(t, directory, ".gitignore", "ignored/\n*.log\ndist/\nnew.txt\n")
	writeFile(t, directory, "new.txt", "still present\n")
	next := capture(t, directory, &after)
	if next.Files["new.txt"].Text != "still present\n" {
		t.Fatal("previously captured file lost after ignore edit")
	}
}

func TestCaptureWithoutGitAndProtectedLinks(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	directory := t.TempDir()
	writeFile(t, directory, ".gitignore", "*.log\n")
	writeFile(t, directory, ".pearlignore", "private/\n")
	writeFile(t, directory, "src/main.go", "package main\n")
	for _, path := range []string{"debug.log", "private/key", "node_modules/dependency.js", ".env", ".git/config"} {
		writeFile(t, directory, path, "must not be captured")
	}
	outside := t.TempDir()
	writeFile(t, outside, "secret", "outside contents")
	// Link creation can require privileges on Windows.
	_ = os.Symlink(outside, filepath.Join(directory, "linked"))
	_ = os.Link(filepath.Join(outside, "secret"), filepath.Join(directory, "hardlink"))
	snapshot := capture(t, directory, nil)
	if snapshot.Files["src/main.go"].Text != "package main\n" {
		t.Fatal("source missing")
	}
	for path, file := range snapshot.Files {
		if file.Text == "must not be captured" || file.Text == "outside contents" {
			t.Fatalf("captured excluded contents: %s", path)
		}
	}
}

func TestBinaryAndLargeFilesHaveExplicitNotes(t *testing.T) {
	directory := t.TempDir()
	writeFile(t, directory, "image.bin", "\x00before")
	writeFile(t, directory, "large.txt", strings.Repeat("x", maxFileBytes+1))
	before := capture(t, directory, nil)
	writeFile(t, directory, "image.bin", "\x00after!")
	report := Compare(before, capture(t, directory, &before))
	if len(report.Changes) != 1 || report.Changes[0].Path != "image.bin" || !strings.Contains(report.Changes[0].Note, "Binary") || report.Changes[0].Diff != "" {
		t.Fatalf("binary report = %+v", report)
	}
	if len(report.Notes) != 1 || !strings.Contains(report.Notes[0], "large.txt") {
		t.Fatalf("omissions = %+v", report.Notes)
	}
}

func TestCaptureCancellationAndDiffBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, t.TempDir(), nil); err == nil {
		t.Fatal("cancelled capture succeeded")
	}
	before := Snapshot{Files: map[string]File{"large.txt": {Text: strings.Repeat("old\n", 20000), Hash: "before"}}}
	after := Snapshot{Files: map[string]File{"large.txt": {Text: strings.Repeat("new\n", 20000), Hash: "after"}}}
	report := Compare(before, after)
	if len(report.Changes) != 1 || len(report.Changes[0].Diff) > 64<<10 || !strings.Contains(report.Changes[0].Note, "truncated") {
		t.Fatalf("diff bounds: %+v", report.Changes)
	}
}

func TestGitOutputBoundCannotBeBypassedByReaderFrom(t *testing.T) {
	var output boundedBuffer
	reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", maxDiscoveryBytes+1))}
	if _, err := io.Copy(&output, reader); err == nil || !output.exceeded || output.buffer.Len() > maxDiscoveryBytes {
		t.Fatalf("Git output exceeded its limit: bytes=%d, err=%v", output.buffer.Len(), err)
	}
}
