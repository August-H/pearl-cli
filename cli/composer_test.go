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
	for _, want := range []string{"Harness: Pearl", "Harness: Codex", "Tests: passed", "Billing: ChatGPT subscription"} {
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

func TestComposerWaitsForWholeKeyReports(t *testing.T) {
	for _, partial := range []string{"\x1b[1", "\x1b[13", "\x1b[13;", "\x1b[13;2", "\x1b[27;2;13", "\x1b[13;2:1"} {
		if !isComposerEscapePrefix(partial) {
			t.Errorf("%q would be cut off", partial)
		}
	}
	for _, complete := range []string{"\x1b[13;2u", "\x1b[27;2;13~", "\x1b[5~", "\x1b[1;2A"} {
		if isComposerEscapePrefix(complete) {
			t.Errorf("%q would wait for another key", complete)
		}
	}
	if isComposerEscapePrefix("\x1b[" + strings.Repeat("1", composerSequenceLimit)) {
		t.Error("a sequence that never ends would swallow typing forever")
	}
}

func TestComposerKeyFromSequence(t *testing.T) {
	for sequence, want := range map[string]rune{
		"\x1b[13;2u":    composerNewlineKey, // Shift+Enter, Kitty's standard
		"\x1b[27;2;13~": composerNewlineKey, // Shift+Enter, the older standard
		"\x1b[13;2:1u":  composerNewlineKey,
		"\x1b[13;3u":    composerNewlineKey, // Alt+Enter
		"\x1b[13;5u":    composerNewlineKey, // Ctrl+Enter
		"\x1b\r":        composerNewlineKey, // Alt+Enter, the usual way
		"\x1b\n":        composerNewlineKey,
		"\x1b[13u":      '\r',
		"\x1b[27u":      escapeKey,
		"\x1b[27;1u":    escapeKey,
		"\x1b[99;5u":    3,  // Ctrl+C
		"\x1b[100;5u":   4,  // Ctrl+D
		"\x1b[101;5u":   5,  // Ctrl+E
		"\x1b[117;5u":   21, // Ctrl+U
		"\x1b[9u":       '\t',
		"\x1b[127u":     127,
		"\x1b[65;2u":    'A',
	} {
		if got, ok := composerKeyFromSequence(sequence); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", sequence, got, ok, want)
		}
	}
	for _, sequence := range []string{
		"\x1b[A", "\x1b[5~", "\x1b[200~", "\x1b[<64;1;1M", "\x1b[1;2A",
		"\x1b[57441;2;5u", "\x1b[120;3u", "\x1b[u", "\x1b[x;2u", "\x1b[13;xu", "\x1b[0u",
	} {
		if got, ok := composerKeyFromSequence(sequence); ok {
			t.Errorf("%q decoded as %d", sequence, got)
		}
	}
}
