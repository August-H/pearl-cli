package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestJobsListScreenIsSelectableScrollableAndColored(t *testing.T) {
	now := time.Date(2026, time.August, 19, 17, 30, 0, 0, time.Local)
	statuses := []string{
		store.JobPending,
		store.JobRunning,
		store.JobCompleted,
		store.JobFailed,
		store.JobWaitingInput,
	}
	jobs := make([]store.Job, 0, len(statuses))
	for index, status := range statuses {
		jobs = append(jobs, store.Job{
			ID: fmt.Sprintf("job-%d", index), Status: status,
			Prompt: "prompt for job", CreatedAt: now,
		})
	}

	screen := renderJobsListScreen(jobs, 80, 8, 3, 2, true)
	for _, expected := range []string{
		"Pearl", "ID", "STATUS", "CREATED", "PROMPT",
		"job-2", "job-3", "job-4", "› ",
		ansiRed + jobsListCell(store.JobFailed, 13) + ansiReset,
		"Enter view", "Space retry", "d archive", "4/5",
	} {
		if !strings.Contains(screen.Frame, expected) {
			t.Fatalf("jobs list screen is missing %q:\n%s", expected, screen.Frame)
		}
	}
	if strings.Contains(screen.Frame, "job-0") || strings.Contains(screen.Frame, "job-1") {
		t.Fatalf("jobs list ignored its scroll offset:\n%s", screen.Frame)
	}
}

func TestJobsListFooterShowsTheSelectedSpaceAction(t *testing.T) {
	tests := []struct {
		status string
		label  string
	}{
		{status: store.JobWaitingInput, label: "Space answer"},
		{status: store.JobPending, label: "Space run"},
		{status: store.JobFailed, label: "Space retry"},
		{status: store.JobRunning, label: ""},
	}
	for _, test := range tests {
		footer := jobsListFooter([]store.Job{{Status: test.status}}, 0, "q back")
		if test.label != "" && !strings.Contains(footer, test.label) {
			t.Fatalf("%s footer = %q, want %q", test.status, footer, test.label)
		}
		if test.label == "" && strings.Contains(footer, "Space ") {
			t.Fatalf("%s footer has an invalid Space action: %q", test.status, footer)
		}
		if !strings.Contains(footer, "d archive") {
			t.Fatalf("%s footer is missing archive: %q", test.status, footer)
		}
	}

	confirmation := renderJobsListScreenWithFooter(
		[]store.Job{{ID: "polar-hare", Status: store.JobFailed}},
		80, 10, 0, 0, false, "q back", "Archive polar-hare? y confirm · n cancel",
	)
	if !strings.Contains(confirmation.Frame, "Archive polar-hare? y confirm · n cancel") {
		t.Fatalf("archive confirmation is missing:\n%s", confirmation.Frame)
	}
}

func TestArchivedJobsListHasNoRunOrArchiveActions(t *testing.T) {
	jobs := []store.Job{{ID: "polar-hare", Status: store.JobFailed}}
	footer := archivedJobsListFooter(jobs, 0, "q back")
	if footer != "q back · ↑/↓ navigate · Enter view" {
		t.Fatalf("archive footer = %q", footer)
	}
	screen := renderJobsListScreenWithTitleAndFooter(
		jobs, 80, 10, 0, 0, false, "q back", footer, "Pearl archive",
	)
	for _, expected := range []string{"Pearl archive", "polar-hare", "Enter view"} {
		if !strings.Contains(screen.Frame, expected) {
			t.Fatalf("archive list is missing %q:\n%s", expected, screen.Frame)
		}
	}
	if strings.Contains(screen.Frame, "Space ") || strings.Contains(screen.Frame, "d archive") {
		t.Fatalf("archive list exposes active-job actions:\n%s", screen.Frame)
	}
}

func TestJobsListUsesCompactColumnsInNarrowTerminal(t *testing.T) {
	layout := calculateJobsListLayout(30)
	if layout.CreatedWidth != 0 || layout.PromptWidth != 0 ||
		layout.IDWidth+layout.StatusWidth+4 != 30 {
		t.Fatalf("narrow jobs layout = %#v", layout)
	}
	screen := renderJobsListScreen([]store.Job{{
		ID: "small-job", Status: store.JobPending, Prompt: "hidden prompt",
		CreatedAt: time.Now(),
	}}, 30, 8, 0, 0, false)
	if !strings.Contains(screen.Frame, "small-job") || strings.Contains(screen.Frame, "PROMPT") {
		t.Fatalf("narrow jobs screen =\n%s", screen.Frame)
	}
}

func TestJobsListSpaceBuildsTerminalPromptCommands(t *testing.T) {
	tests := []struct {
		status  string
		command string
	}{
		{status: store.JobPending, command: "run release\\ prep"},
		{status: store.JobCompleted, command: "retry release\\ prep"},
		{status: store.JobFailed, command: "retry release\\ prep"},
		{status: store.JobCancelled, command: "retry release\\ prep"},
		{status: store.JobInterrupted, command: "retry release\\ prep"},
		{status: store.JobWaitingInput, command: "respond release\\ prep "},
		{status: store.JobRunning, command: ""},
	}
	for _, test := range tests {
		job := store.Job{ID: "release prep", Status: test.status}
		if got := jobsListPreloadCommand(job); got != test.command {
			t.Fatalf("%s preload = %q, want %q", test.status, got, test.command)
		}
	}
}
