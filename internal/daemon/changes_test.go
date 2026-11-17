package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/internal/workspacechanges"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type changingRunner struct{ outcome error }

func (r changingRunner) Run(_ context.Context, job store.Job, _ openrouter_request.EventSink, _ openrouter_request.CheckpointStore) (string, error) {
	// These writes have no editing-tool records, just like shell commands.
	if err := os.WriteFile(filepath.Join(job.WorkspaceRoot, "existing.txt"), []byte("agent edit\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(job.WorkspaceRoot, "new.txt"), []byte("new file\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(job.WorkspaceRoot, "deleted.txt")); err != nil {
		return "", err
	}
	return "done", r.outcome
}

func TestWorkerCapturesActualChangesOnEveryOutcome(t *testing.T) {
	for _, test := range []struct {
		name, status string
		outcome      error
	}{
		{"completed", store.JobCompleted, nil},
		{"failed", store.JobFailed, errors.New("agent failed")},
		{"cancelled", store.JobCancelled, context.Canceled},
		{"paused", store.JobWaitingInput, &openrouter_request.UserInputRequiredError{ToolCallID: "question", Question: "continue?"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
			path := filepath.Join(t.TempDir(), "pearl.db")
			state, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { state.Close() }()
			workspace := t.TempDir()
			for _, name := range []string{"existing.txt", "deleted.txt"} {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte("already dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			job, err := state.CreateJob(context.Background(), "edit files", workspace)
			if err != nil {
				t.Fatal(err)
			}
			job, found, err := state.ClaimNextJob(context.Background())
			if err != nil || !found {
				t.Fatalf("claim = %v, %v", found, err)
			}
			NewManager(state, changingRunner{test.outcome}).runJob(context.Background(), job)
			stored, err := state.GetJob(context.Background(), job.ID)
			if err != nil || stored.Status != test.status {
				t.Fatalf("status = %+v, %v", stored, err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			state, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := state.ListChangeRuns(context.Background(), job.ID)
			if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil || len(runs[0].Report.Changes) != 3 {
				t.Fatalf("persisted report = %+v, %v", runs, err)
			}
			if diff := runs[0].Report.Changes[1].Diff; !strings.Contains(diff, "-already dirty\n+agent edit\n") {
				t.Fatalf("wrong diff: %s", diff)
			}
			tools, err := state.ListToolExecutions(context.Background(), job.ID)
			if err != nil || len(tools) != 0 {
				t.Fatalf("test requires no tool records: %+v, %v", tools, err)
			}
		})
	}
}

func TestRetryDiffExcludesInterveningEdits(t *testing.T) {
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	workspace := t.TempDir()
	job, err := state.CreateJob(ctx, "edit files", workspace)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(state, changingRunner{})
	for attempt := range 2 {
		if err := os.WriteFile(filepath.Join(workspace, "existing.txt"), []byte("intervening edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "deleted.txt"), []byte("delete me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if attempt > 0 {
			if _, _, err := state.RetryJob(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
		}
		claimed, found, err := state.ClaimNextJob(ctx)
		if err != nil || !found {
			t.Fatalf("claim = %v, %v", found, err)
		}
		manager.runJob(ctx, claimed)
	}
	runs, err := state.ListChangeRuns(ctx, job.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("reports = %+v, %v", runs, err)
	}
	if diff := runs[1].Report.Changes[1].Diff; !strings.Contains(diff, "-intervening edit\n+agent edit\n") {
		t.Fatalf("retry baseline: %s", diff)
	}
}

func TestRecoveryFinishesSavedSnapshotsAndReleasesBaseline(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	workspace := t.TempDir()
	job, err := state.CreateJob(ctx, "edit files", workspace)
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspacechanges.Capture(ctx, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.BeginChangeRun(ctx, job.ID, &before, workspacechanges.Report{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("survived crash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recoverChanges(ctx, state); err != nil {
		t.Fatal(err)
	}
	runs, err := state.ListChangeRuns(ctx, job.ID)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil || len(runs[0].Report.Changes) != 1 || len(runs[0].Report.Notes) == 0 {
		t.Fatalf("recovered report = %+v, %v", runs, err)
	}
	if open, found, err := state.NextOpenChangeRun(ctx); err != nil || found {
		t.Fatalf("open capture = %+v, %v, %v", open, found, err)
	}
}
