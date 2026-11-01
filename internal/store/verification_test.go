package store

import (
	"context"
	"github.com/August-H/pearl-cli/internal/verification"
	"testing"
)

func TestVerificationPersistsAndResetsForNextRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	job, err := s.CreateJob(ctx, "Implement a feature", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal(err)
	}
	for _, event := range []struct{ kind, data string }{
		{"run_config", `{"provider":"chatgpt","model":"requested-model"}`},
		{"model", "actual-model"},
		{"verification", verification.Event("running", "go test ./...")},
		{"verification", verification.Event("failed", "go test ./...")},
		{"verification", verification.Event("not_run", "")},
		{"verification", verification.Event("passed", "go test ./...")},
	} {
		if _, err := s.AppendEvent(ctx, job.ID, event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "actual-model" || stored.Provider != "chatgpt" || stored.Verification != "passed" || stored.VerificationCommand != "go test ./..." {
		t.Fatalf("run metadata=%+v", stored)
	}
	if err := s.FinishJob(ctx, job.ID, JobCompleted, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	job, found, err = s.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal(err)
	}
	if job.Verification != "" || job.Model != "" {
		t.Fatal("previous run's verification leaked into retry")
	}
	stored, err = s.GetJob(ctx, job.ID)
	if err != nil || stored.Verification != "" {
		t.Fatal("verification reset was not persisted")
	}
}
