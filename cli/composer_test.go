package cli

import (
	"bufio"
	"context"
	"github.com/August-H/pearl-cli/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestComposerPasteKeepsNewlinesInDraft(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("first\r\nsecond\nthird\x1b[201~after"))
	text, err := readComposerPaste(reader)
	if err != nil || text != "first\nsecond\nthird" {
		t.Fatalf("paste=%q,%v", text, err)
	}
	remaining, _ := reader.ReadString('\n')
	if remaining != "after" {
		t.Fatalf("consumed input after paste: %q", remaining)
	}
	output := renderDashboardComposer(nil, `respond job "first`+"\nsecond\nthird\"", "", 60, false, 0, false, dashboardConfigureInactive, false, "", "", 0, 0, 0, true)
	for _, want := range []string{"first", "second", "third", "Ctrl+E editor"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in multiline composer: %s", want, output)
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if len([]rune(line)) > 60 {
			t.Fatalf("composer line exceeds terminal width: %q", line)
		}
	}
}

func TestComposerRecognizesPastePrefixBeforeNavigation(t *testing.T) {
	for _, prefix := range []string{"\x1b[", "\x1b[2", "\x1b[20", "\x1b[200"} {
		if !isComposerEscapePrefix(prefix) {
			t.Fatalf("paste prefix %q would be discarded by navigation", prefix)
		}
	}
	for _, complete := range []string{"\x1b[200~", "\x1b\r", "\x1b[A"} {
		if isComposerEscapePrefix(complete) {
			t.Fatalf("complete sequence %q would wait for another key", complete)
		}
	}
}

func TestComposerOversizedPasteStopsAtTerminator(t *testing.T) {
	for _, size := range []int{dashboardDraftLimit, dashboardDraftLimit + 1, dashboardDraftLimit + 50} {
		reader := bufio.NewReader(strings.NewReader(strings.Repeat("a", size) + "\x1b[201~after"))
		text, err := readComposerPaste(reader)
		if size == dashboardDraftLimit && (err != nil || len(text) != size) || size > dashboardDraftLimit && err == nil {
			t.Fatalf("size=%d returned %d bytes, err=%v", size, len(text), err)
		}
		remaining, _ := reader.ReadString('\n')
		if remaining != "after" {
			t.Fatalf("consumed input after rejected paste: %q", remaining)
		}
	}
}

func TestDashboardScopesSuggestionsAndLabelsActiveRun(t *testing.T) {
	root := t.TempDir()
	jobs := []store.Job{{ID: "local-job", WorkspaceRoot: root, Status: store.JobRunning, Harness: "pearl", Provider: "chatgpt", Model: "gpt-6.1-sol", Verification: "passed"}, {ID: "foreign-job", WorkspaceRoot: t.TempDir(), Status: store.JobCompleted}}
	output := renderDashboardState(jobs, time.Now(), 100, false, nil, "respond ", "", 0, dashboardCommandView{workspace: root, harness: "codex", provider: "chatgpt", model: "gpt-6.1-sol"})
	if strings.Contains(output, "foreign-job") {
		t.Fatal("foreign workspace job leaked into dashboard or suggestions")
	}
	for _, want := range []string{"Harness: pearl", "Default harness: codex", "Tests: passed", "Billing: chatgpt"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q: %s", want, output)
		}
	}
}

func TestEditorUpdatesDraftAndPreservesItOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test editor uses POSIX shell")
	}
	directory := t.TempDir()
	editor := filepath.Join(directory, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'respond job \"first\\nsecond\"' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)
	text, err := editDashboardDraft(context.Background(), "original", nil, os.Stdout)
	if err != nil || text != "respond job \"first\nsecond\"" {
		t.Fatalf("draft=%q, %v", text, err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	text, err = editDashboardDraft(context.Background(), "original", nil, os.Stdout)
	if err == nil || text != "original" {
		t.Fatal("failed editor lost original draft")
	}
}
