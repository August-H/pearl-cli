package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestJobViewUsesExpandableSectionsWithoutDroppingTranscript(t *testing.T) {
	now := time.Date(2026, time.August, 19, 16, 30, 0, 0, time.Local)
	details := jobDetails{
		Job: store.Job{
			ID: "swift-delta", Status: store.JobCompleted,
			Prompt: "explain this folder", WorkspaceRoot: "/work/pearl-cli",
			CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		},
		Transcript: []byte(`[
  {"role":"user","content":"explain this folder"},
  {"role":"assistant","content":"Here is the complete answer."}
]`),
	}
	sections, err := buildJobViewSections(details)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 5 || !sections[0].Expanded {
		t.Fatalf("job view sections = %#v", sections)
	}
	for _, section := range sections[1:] {
		if section.Expanded {
			t.Fatalf("section %q starts expanded", section.Title)
		}
	}

	collapsed, _ := jobViewDisplayLines(sections, 80, 0, false)
	if strings.Contains(strings.Join(collapsed, "\n"), "complete answer") {
		t.Fatalf("collapsed transcript leaked into compact view: %q", collapsed)
	}
	colored, _ := jobViewDisplayLines(sections, 80, 0, true)
	if !strings.Contains(
		strings.Join(colored, "\n"),
		"Status: "+ansiGreen+store.JobCompleted+ansiReset,
	) {
		t.Fatalf("completed status is not green: %q", colored)
	}
	transcriptIndex := len(sections) - 1
	sections[transcriptIndex].Expanded = true
	expanded, headers := jobViewDisplayLines(sections, 80, transcriptIndex, false)
	joined := strings.Join(expanded, "\n")
	if !strings.Contains(joined, "complete answer") || headers[transcriptIndex] <= 0 {
		t.Fatalf("expanded transcript is incomplete: %q", expanded)
	}

	screen := renderJobViewScreen(
		details.Job, sections, 60, 12, transcriptIndex, headers[transcriptIndex], false,
	)
	for _, expected := range []string{"Pearl job: swift-delta", "Transcript", "q exit"} {
		if !strings.Contains(screen.Frame, expected) {
			t.Fatalf("job view screen is missing %q:\n%s", expected, screen.Frame)
		}
	}
}

func TestJobViewKeyboardSequences(t *testing.T) {
	tests := map[string]string{
		"\x1b[A":  "previous",
		"\x1b[B":  "next",
		"\x1b[C":  "expand",
		"\x1b[D":  "collapse",
		"\x1b[5~": "page_up",
		"\x1b[6~": "page_down",
	}
	for sequence, want := range tests {
		got, complete := jobViewEscapeAction(sequence)
		if !complete || got != want {
			t.Fatalf("jobViewEscapeAction(%q) = %q, %v; want %q, true",
				sequence, got, complete, want)
		}
	}
	if _, complete := jobViewEscapeAction("\x1b["); complete {
		t.Fatal("partial escape sequence was treated as complete")
	}
}
