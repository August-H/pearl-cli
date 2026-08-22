package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestUpdateAutonomousActivityRecordsCreationAndStatusChanges(t *testing.T) {
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	state := autonomousTUIState{}
	details := store.AutonomousDetails{
		Session: store.AutonomousSession{Status: store.AutonomousRunning},
		Jobs:    []store.Job{{ID: "amber-otter", Status: store.JobQueued}},
		Events: []store.Event{{
			Sequence: 1, JobID: "amber-otter", Type: "status",
			Data: store.JobQueued, CreatedAt: now,
		}},
	}
	updateAutonomousActivity(&state, details, now)
	if len(state.activity) != 2 ||
		state.activity[1].Text != "created amber-otter · queued" {
		t.Fatalf("initial autonomous activity = %#v", state.activity)
	}
	details.Session.Status = store.AutonomousReviewing
	details.Jobs[0].Status = store.JobCompleted
	details.Events = append(details.Events,
		store.Event{
			Sequence: 2, JobID: "amber-otter", Type: "status",
			Data: store.JobRunning, CreatedAt: now.Add(500 * time.Millisecond),
		},
		store.Event{
			Sequence: 3, JobID: "amber-otter", Type: "status",
			Data: store.JobCompleted, CreatedAt: now.Add(time.Second),
		},
	)
	updateAutonomousActivity(&state, details, now.Add(time.Second))
	if len(state.activity) != 5 ||
		state.activity[2].Text != "session · running → reviewing" ||
		state.activity[3].Text != "amber-otter · queued → running" ||
		state.activity[4].Text != "amber-otter · running → completed" {
		t.Fatalf("updated autonomous activity = %#v", state.activity)
	}
}

func TestRenderAutonomousScreenShowsJobsActivityAndSummary(t *testing.T) {
	details := store.AutonomousDetails{
		Session: store.AutonomousSession{
			ID: "auto_1", Goal: "prepare the release", Status: store.AutonomousCompleted,
			WorkspaceRoot: "/work/pearl", Summary: "Release checks passed.",
		},
		Jobs: []store.Job{{ID: "amber-otter", Status: store.JobCompleted}},
	}
	activity := []autonomousActivity{{
		At:   time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC),
		Text: "amber-otter · running → completed", Status: store.JobCompleted,
	}}
	output := renderAutonomousScreen(details, activity, nil, 80, 20, false)
	for _, expected := range []string{
		"Pearl autonomous", "COMPLETED", "Goal: prepare the release",
		"amber-otter", "running → completed", "Release checks passed.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("autonomous screen is missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("no-color autonomous screen contains ANSI escapes: %q", output)
	}
	if got := len(strings.Split(strings.TrimSuffix(output, "\n"), "\n")); got != 20 {
		t.Fatalf("autonomous screen has %d lines, want 20", got)
	}
}

func TestRenderAutonomousScreenUsesDashboardNavigationHint(t *testing.T) {
	output := renderAutonomousScreenWithHint(
		store.AutonomousDetails{Session: store.AutonomousSession{
			Goal: "inspect the project", Status: store.AutonomousRunning,
			WorkspaceRoot: "/work/pearl",
		}},
		nil, nil, 80, 16, false,
		"q back · r refresh · work continues in daemon",
	)
	if !strings.Contains(output, "q back · r refresh") ||
		strings.Contains(output, "q detach") {
		t.Fatalf("dashboard autonomous hint is wrong:\n%s", output)
	}
}
