package store

import (
	"context"
	"strings"
	"testing"
)

func TestFolderJobsUseAndFollowTheFolderDirectory(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	first, second := t.TempDir(), t.TempDir()
	if _, err := state.CreateFolder(ctx, " docs ", first); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateFolder(ctx, "docs", second); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate folder error = %v", err)
	}
	running, err := state.CreatePendingFolderJob(ctx, "", "first", "", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if running.Folder != "docs" || running.WorkspaceRoot != first {
		t.Fatalf("folder job = %+v", running)
	}
	if _, _, err := state.RunJob(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, ok, err := state.ClaimNextJob(ctx); err != nil || !ok || claimed.ID != running.ID {
		t.Fatalf("claim = %v, %v, %v", claimed.ID, ok, err)
	}
	pending, err := state.CreatePendingFolderJob(ctx, "", "second", "", "docs")
	if err != nil {
		t.Fatal(err)
	}

	folder, err := state.SetFolderWorkspace(ctx, "docs", second)
	if err != nil || folder.WorkspaceRoot != second {
		t.Fatalf("moved folder = %+v, %v", folder, err)
	}
	if job, _ := state.GetJob(ctx, pending.ID); job.WorkspaceRoot != second {
		t.Fatalf("pending job stayed in %q", job.WorkspaceRoot)
	}
	if job, _ := state.GetJob(ctx, running.ID); job.WorkspaceRoot != first {
		t.Fatalf("running job moved to %q", job.WorkspaceRoot)
	}

	if _, err := state.CreatePendingFolderJob(ctx, "", "lost", "", "missing"); err == nil {
		t.Fatal("a job was created in a missing folder")
	}
	if err := state.DeleteFolder(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if job, _ := state.GetJob(ctx, pending.ID); job.Folder != "" {
		t.Fatalf("job kept removed folder %q", job.Folder)
	}
	if folders, err := state.ListFolders(ctx); err != nil || len(folders) != 0 {
		t.Fatalf("folders after delete = %v, %v", folders, err)
	}
}

func TestRespondingToAFinishedJobQueuesAFollowUp(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	job, err := state.CreatePendingJob(ctx, "", "write docs", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.RespondToJob(ctx, job.ID, "more"); err == nil ||
		!strings.Contains(err.Error(), "has not run yet") {
		t.Fatalf("pending job respond error = %v", err)
	}
	if _, _, err := state.RunJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.ClaimNextJob(ctx); err != nil {
		t.Fatal(err)
	}
	if err := state.FinishJob(ctx, job.ID, JobCompleted, "done", ""); err != nil {
		t.Fatal(err)
	}
	queued, _, err := state.RespondToJob(ctx, job.ID, " add examples ")
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != JobQueued || !strings.HasPrefix(queued.InputToolCallID, FollowUpIDPrefix) {
		t.Fatalf("follow-up job = %+v", queued)
	}
	if message, found, err := state.LoadUserInputResponse(ctx, job.ID, queued.InputToolCallID); err != nil ||
		!found || message != "add examples" {
		t.Fatalf("follow-up message = %q, %v, %v", message, found, err)
	}

	if _, _, err := state.ClaimNextJob(ctx); err != nil {
		t.Fatal(err)
	}
	if err := state.FinishJob(ctx, job.ID, JobFailed, "", "boom"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.RetryJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if stored, _ := state.GetJob(ctx, job.ID); stored.InputToolCallID != "" || stored.InputResponse != "" {
		t.Fatalf("retry kept the follow-up: %+v", stored)
	}
}

func TestFolderNamesAreValidated(t *testing.T) {
	for _, name := range []string{"", "a/b", "-x", "..", strings.Repeat("x", 41)} {
		if err := ValidateFolderName(name); err == nil {
			t.Errorf("ValidateFolderName(%q) accepted", name)
		}
	}
	if err := ValidateFolderName("release notes"); err != nil {
		t.Fatal(err)
	}
}
