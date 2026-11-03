package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	state, err := Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state
}

func TestCreateJobUsesUniqueTwoWordIDs(t *testing.T) {
	state := openTestStore(t)
	adjectives := make(map[string]bool, len(jobIDAdjectives))
	for _, word := range jobIDAdjectives {
		adjectives[word] = true
	}
	nouns := make(map[string]bool, len(jobIDNouns))
	for _, word := range jobIDNouns {
		nouns[word] = true
	}
	seen := make(map[string]bool)
	workspace := t.TempDir()
	for index := 0; index < 64; index++ {
		job, err := state.CreateJob(context.Background(), "test", workspace)
		if err != nil {
			t.Fatal(err)
		}
		words := strings.Split(job.ID, "-")
		if len(words) != 2 || !adjectives[words[0]] || !nouns[words[1]] {
			t.Fatalf("job ID %q is not an adjective-noun pair", job.ID)
		}
		if seen[job.ID] {
			t.Fatalf("duplicate job ID %q", job.ID)
		}
		seen[job.ID] = true
	}
}

func TestJobsRememberTheirHarness(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	job, err := state.CreatePendingJob(ctx, "", "use codex", t.TempDir(), " Codex ")
	if err != nil {
		t.Fatal(err)
	}
	if stored, err := state.GetJob(ctx, job.ID); err != nil || stored.Harness != HarnessCodex {
		t.Fatalf("stored harness = %q, %v", stored.Harness, err)
	}
	if err := state.SetJobHarness(ctx, job.ID, HarnessPearl); err != nil {
		t.Fatal(err)
	}
	if stored, err := state.GetJob(ctx, job.ID); err != nil || stored.Harness != HarnessPearl {
		t.Fatalf("updated harness = %q, %v", stored.Harness, err)
	}
	if _, err := state.CreatePendingJob(ctx, "", "bad", t.TempDir(), "not-a-harness"); err == nil {
		t.Fatal("an unknown harness was accepted")
	}
	if defaultJob, err := state.CreateJob(ctx, "default", t.TempDir()); err != nil || defaultJob.Harness != "" {
		t.Fatalf("default job harness = %q, %v", defaultJob.Harness, err)
	}
}

func TestJobIDWordListsAreLargeUniqueAndShort(t *testing.T) {
	for name, words := range map[string][]string{
		"adjectives": jobIDAdjectives,
		"nouns":      jobIDNouns,
	} {
		if len(words) < 500 {
			t.Errorf("%s has %d words, want at least 500", name, len(words))
		}
		seen := make(map[string]bool, len(words))
		for _, word := range words {
			if seen[word] {
				t.Errorf("%s repeats %q", name, word)
			}
			seen[word] = true
			if word == "" || len(word) > jobIDMaxWordLength {
				t.Errorf("%s word %q must be 1 to %d letters", name, word, jobIDMaxWordLength)
			}
			for _, character := range word {
				if character < 'a' || character > 'z' {
					t.Errorf("%s word %q must be lowercase letters only", name, word)
					break
				}
			}
		}
	}
}

func TestCreateNamedJobUsesNameAsID(t *testing.T) {
	state := openTestStore(t)
	job, err := state.CreateNamedJob(
		context.Background(), "  release prep  ", "ship it", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "release prep" {
		t.Fatalf("created job ID = %q", job.ID)
	}
	loaded, err := state.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "release prep" || loaded.Prompt != "ship it" {
		t.Fatalf("loaded named job = %#v", loaded)
	}
	if _, err := state.CreateNamedJob(
		context.Background(), "release prep", "duplicate", t.TempDir(),
	); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate custom job ID error = %v", err)
	}
}

func TestAutonomousSessionPersistsLinkedQueuedJobs(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "prepare the release", WorkspaceRoot: t.TempDir(), MaxSubagents: 1})
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != AutonomousRunning || session.Terminal() {
		t.Fatalf("new autonomous session = %#v", session)
	}
	first, err := state.CreateAutonomousJob(ctx, session.ID, "run the tests", "claude", "opus")
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.CreateAutonomousJob(ctx, session.ID, "review the release notes", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != JobQueued || second.Status != JobQueued || first.ID == second.ID {
		t.Fatalf("autonomous jobs = %#v %#v", first, second)
	}
	details, err := state.AutonomousDetails(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Session.ID != session.ID || len(details.Jobs) != 2 || len(details.Events) != 2 ||
		details.Jobs[0].ID != first.ID || details.Jobs[1].ID != second.ID {
		t.Fatalf("autonomous details = %#v", details)
	}
	if err := state.UpdateAutonomousSession(
		ctx, session.ID, AutonomousCompleted, "release ready", "",
	); err != nil {
		t.Fatal(err)
	}
	finished, err := state.GetAutonomousSession(ctx, session.ID)
	if err != nil || !finished.Terminal() || finished.Summary != "release ready" ||
		finished.FinishedAt == nil {
		t.Fatalf("finished autonomous session = %#v, err=%v", finished, err)
	}
	if _, err := state.CreateAutonomousJob(ctx, session.ID, "too late", "", ""); err == nil ||
		!strings.Contains(err.Error(), "already completed") {
		t.Fatalf("create job in completed session error = %v", err)
	}
}

func TestPendingJobMustBeQueuedBeforeItCanRun(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	job, err := state.CreatePendingNamedJob(
		ctx, "release prep", "ship it", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobPending || !job.Terminal() {
		t.Fatalf("pending job = %#v", job)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || found {
		t.Fatalf("claimed pending job: found=%v err=%v", found, err)
	}
	queued, sequence, err := state.QueueJob(ctx, job.ID)
	if err != nil || queued.Status != JobQueued || sequence == 0 {
		t.Fatalf("queued job = %#v, sequence=%d, err=%v", queued, sequence, err)
	}
	if _, _, err := state.QueueJob(ctx, job.ID); err == nil {
		t.Fatal("queued the same pending job twice")
	}
}

func TestCancelPendingJobMarksItCancelledAndUnrunnable(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	job, err := state.CreatePendingNamedJob(
		ctx, "cancel me", "temporary job", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := state.RequestCancel(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != JobCancelled || !cancelled.Terminal() ||
		cancelled.FinishedAt == nil || cancelled.CancelRequested {
		t.Fatalf("cancelled pending job = %#v", cancelled)
	}
	loaded, err := state.GetJob(ctx, job.ID)
	if err != nil || loaded.Status != JobCancelled || loaded.FinishedAt == nil {
		t.Fatalf("loaded cancelled pending job = %#v, err=%v", loaded, err)
	}
	if _, _, err := state.QueueJob(ctx, job.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be run from status") {
		t.Fatalf("queue cancelled job error = %v", err)
	}
	if again, err := state.RequestCancel(ctx, job.ID); err != nil ||
		again.Status != JobCancelled {
		t.Fatalf("second cancel = %#v, err=%v", again, err)
	}
}

func TestArchiveJobPreservesHistoryAndRejectsActiveJobs(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	archivable, err := state.CreatePendingNamedJob(
		ctx, "archive me", "temporary job", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	transcript := []byte(`[{"role":"user","content":"temporary job"}]`)
	if err := state.SaveTranscript(ctx, archivable.ID, transcript); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveToolResult(
		ctx, archivable.ID, "tool-1", "read_file", `{}`, []byte(`{"ok":true}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := state.ArchiveJob(ctx, archivable.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.GetJob(ctx, archivable.ID)
	if err != nil || loaded.ArchivedAt == nil {
		t.Fatalf("archived job = %#v, err=%v", loaded, err)
	}
	activeJobs, err := state.ListJobs(ctx, 10)
	if err != nil || len(activeJobs) != 0 {
		t.Fatalf("regular jobs include archive = %#v, err=%v", activeJobs, err)
	}
	archivedJobs, err := state.ListArchivedJobs(ctx, 10)
	if err != nil || len(archivedJobs) != 1 || archivedJobs[0].ID != archivable.ID {
		t.Fatalf("archived jobs = %#v, err=%v", archivedJobs, err)
	}
	events, err := state.EventsAfter(ctx, archivable.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].Type != "archived" {
		t.Fatalf("archived job events = %#v, err=%v", events, err)
	}
	storedTranscript, err := state.LoadTranscript(ctx, archivable.ID)
	if err != nil || string(storedTranscript) != string(transcript) {
		t.Fatalf("archived transcript = %q, err=%v", storedTranscript, err)
	}
	tools, err := state.ListToolExecutions(ctx, archivable.ID)
	if err != nil || len(tools) != 1 || tools[0].ToolCallID != "tool-1" {
		t.Fatalf("archived tools = %#v, err=%v", tools, err)
	}
	if _, _, err := state.RunJob(ctx, archivable.ID); err == nil ||
		!strings.Contains(err.Error(), "is archived") {
		t.Fatalf("run archived job error = %v", err)
	}

	active, err := state.CreatePendingNamedJob(
		ctx, "keep active", "active job", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.QueueJob(ctx, active.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.ArchiveJob(ctx, active.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be archived") {
		t.Fatalf("archive active job error = %v", err)
	}
	if _, err := state.GetJob(ctx, active.ID); err != nil {
		t.Fatalf("active job was archived: %v", err)
	}
}

func TestValidateJobNameLimitsCustomIDs(t *testing.T) {
	if err := ValidateJobName(strings.Repeat("a", MaxJobNameLength)); err != nil {
		t.Fatalf("20-character name was rejected: %v", err)
	}
	for _, name := range []string{
		strings.Repeat("a", MaxJobNameLength+1), "nested/name", "line\nbreak",
		"-a", "--all", ".", "..",
	} {
		if err := ValidateJobName(name); err == nil {
			t.Fatalf("invalid job name %q was accepted", name)
		}
	}
	if isLegacyJobID("job_custom") {
		t.Fatal("custom job ID was mistaken for a legacy generated ID")
	}
}

func TestValidateScheduleNameRejectsControlCharacters(t *testing.T) {
	for _, name := range []string{"line\nbreak", "tab\tname", "escape\x1bname"} {
		if err := ValidateScheduleName(name); err == nil {
			t.Fatalf("invalid schedule name %q was accepted", name)
		}
	}
	if err := ValidateScheduleName("repository check"); err != nil {
		t.Fatalf("valid schedule name was rejected: %v", err)
	}
}

func TestListJobsPageUsesOffset(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	workspace := t.TempDir()
	for _, name := range []string{"first", "second", "third"} {
		if _, err := state.CreatePendingNamedJob(ctx, name, name, workspace); err != nil {
			t.Fatal(err)
		}
	}

	firstPage, err := state.ListJobsPage(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := state.ListJobsPage(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || len(secondPage) != 1 {
		t.Fatalf("page lengths = %d and %d, want 2 and 1", len(firstPage), len(secondPage))
	}
	if firstPage[0].ID == secondPage[0].ID || firstPage[1].ID == secondPage[0].ID {
		t.Fatalf("job pages overlap: first=%#v second=%#v", firstPage, secondPage)
	}
}

func TestWaitingInputSurvivesRestartAndResponseResumesJob(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "pearl.db")
	state, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreateJob(ctx, "choose a color", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim job: found=%v err=%v", found, err)
	}
	transcript := []byte(`[{"role":"user","content":"choose a color"},{"role":"assistant","tool_calls":[{"id":"call-input","type":"function","function":{"name":"request_user_input","arguments":"{\"question\":\"Which color?\"}"}}]}]`)
	if err := state.SaveTranscript(ctx, job.ID, transcript); err != nil {
		t.Fatal(err)
	}
	if err := state.PauseForInput(ctx, job.ID, "call-input", "Which color?"); err != nil {
		t.Fatal(err)
	}
	waiting, err := state.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != JobWaitingInput || waiting.Question != "Which color?" {
		t.Fatalf("waiting job = %#v", waiting)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || found {
		t.Fatalf("paused job was claimable: found=%v err=%v", found, err)
	}
	other, err := state.CreateJob(ctx, "independent work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claimedOther, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || claimedOther.ID != other.ID {
		t.Fatalf("paused job blocked another job: claimed=%#v found=%v err=%v", claimedOther, found, err)
	}
	if err := state.FinishJob(ctx, other.ID, JobCompleted, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	waiting, err = state.GetJob(ctx, job.ID)
	if err != nil || waiting.Status != JobWaitingInput || waiting.Question != "Which color?" {
		t.Fatalf("waiting job after restart = %#v err=%v", waiting, err)
	}
	loadedTranscript, err := state.LoadTranscript(ctx, job.ID)
	if err != nil || string(loadedTranscript) != string(transcript) {
		t.Fatalf("transcript after restart = %s err=%v", loadedTranscript, err)
	}

	resumed, sequence, err := state.RespondToJob(ctx, job.ID, "Blue")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != JobQueued || resumed.Question != "" || sequence == 0 {
		t.Fatalf("resumed job = %#v sequence=%d", resumed, sequence)
	}
	response, found, err := state.LoadUserInputResponse(ctx, job.ID, "call-input")
	if err != nil || !found || response != "Blue" {
		t.Fatalf("response = %q found=%v err=%v", response, found, err)
	}
	claimed, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatalf("resumed claim = %#v found=%v err=%v", claimed, found, err)
	}
	if _, err := state.RecoverRunningJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.RetryJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	response, found, err = state.LoadUserInputResponse(ctx, job.ID, "call-input")
	if err != nil || !found || response != "Blue" {
		t.Fatalf("response after interrupted retry = %q found=%v err=%v", response, found, err)
	}
}

func TestMigrationAddsWaitingInputColumns(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    prompt TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    status TEXT NOT NULL,
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    transcript BLOB,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	state, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	job, err := state.CreateJob(context.Background(), "migrated", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := state.GetJob(context.Background(), job.ID)
	if err != nil || loaded.Question != "" || loaded.Status != JobQueued {
		t.Fatalf("migrated job = %#v err=%v", loaded, err)
	}
	if err := state.SaveAgentCheckpoint(
		context.Background(), job.ID, []byte(`[]`), []byte(`[{"role":"user"}]`),
	); err != nil {
		t.Fatalf("save migrated agent context: %v", err)
	}
	if loadedContext, err := state.LoadAgentContext(context.Background(), job.ID); err != nil ||
		string(loadedContext) != `[{"role":"user"}]` {
		t.Fatalf("migrated agent context = %s, err=%v", loadedContext, err)
	}
}

func TestMigrationRenamesLegacyJobIDsAndPreservesReferences(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-job-ids.db")
	state, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "job_0123456789abcdef"
	createdAt := nowText()
	agentContext := `[{"role":"user","content":"compact"}]`
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO jobs (id, prompt, workspace_root, status, agent_context, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, legacyID, "legacy job", t.TempDir(), JobCompleted, agentContext, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO events (job_id, type, data, created_at)
VALUES (?, ?, ?, ?)`, legacyID, "status", JobCompleted, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO tool_executions (
    job_id, tool_call_id, tool_name, arguments, result, created_at
)
VALUES (?, ?, ?, ?, ?, ?)`, legacyID, "call-1", "test_tool", `{}`, `{"ok":true}`, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	jobs, err := state.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs after migration = %#v", jobs)
	}
	migratedID := jobs[0].ID
	if migratedID == legacyID || len(strings.Split(migratedID, "-")) != 2 {
		t.Fatalf("migrated job ID = %q", migratedID)
	}
	if _, err := state.GetJob(ctx, legacyID); err == nil {
		t.Fatalf("legacy job ID %q still exists", legacyID)
	}
	loadedContext, err := state.LoadAgentContext(ctx, migratedID)
	if err != nil || string(loadedContext) != agentContext {
		t.Fatalf("migrated agent context = %s, err=%v", loadedContext, err)
	}
	events, err := state.EventsAfter(ctx, migratedID, 0, 10)
	if err != nil || len(events) != 1 || events[0].JobID != migratedID {
		t.Fatalf("migrated events = %#v, err=%v", events, err)
	}
	result, found, err := state.LoadToolResult(ctx, migratedID, "call-1")
	if err != nil || !found || string(result) != `{"ok":true}` {
		t.Fatalf("migrated tool result = %s, found=%v, err=%v", result, found, err)
	}
}

func TestMigrationReplacesStoredNameWithJobID(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "stored-job-name.db")
	state, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	oldID := "amber-otter"
	createdAt := nowText()
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO jobs (id, name, prompt, workspace_root, status, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, oldID, "release prep", "ship it", t.TempDir(),
		JobCompleted, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO events (job_id, type, data, created_at)
VALUES (?, ?, ?, ?)`, oldID, "status", JobCompleted, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	job, err := state.GetJob(ctx, "release prep")
	if err != nil || job.ID != "release prep" {
		t.Fatalf("migrated named job = %#v, err=%v", job, err)
	}
	if _, err := state.GetJob(ctx, oldID); err == nil {
		t.Fatalf("old job ID %q still exists", oldID)
	}
	events, err := state.EventsAfter(ctx, "release prep", 0, 10)
	if err != nil || len(events) != 1 || events[0].JobID != "release prep" {
		t.Fatalf("migrated named job events = %#v, err=%v", events, err)
	}
	var storedName string
	if err := state.db.QueryRowContext(
		ctx, "SELECT name FROM jobs WHERE id = ?", "release prep",
	).Scan(&storedName); err != nil || storedName != "" {
		t.Fatalf("stored name = %q, err=%v", storedName, err)
	}
}

func TestListActiveJobsExcludesFinishedJobs(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)

	completed, err := state.CreateJob(ctx, "completed", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim completed job: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, completed.ID, JobCompleted, "done", ""); err != nil {
		t.Fatal(err)
	}

	waiting, err := state.CreateJob(ctx, "waiting", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim waiting job: found=%v err=%v", found, err)
	}
	if err := state.PauseForInput(ctx, waiting.ID, "call-input", "Choose one"); err != nil {
		t.Fatal(err)
	}

	running, err := state.CreateJob(ctx, "running", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim running job: found=%v err=%v", found, err)
	}
	queued, err := state.CreateJob(ctx, "queued", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	active, err := state.ListActiveJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 {
		t.Fatalf("active jobs = %#v", active)
	}
	want := []struct {
		id     string
		status string
	}{
		{id: running.ID, status: JobRunning},
		{id: waiting.ID, status: JobWaitingInput},
		{id: queued.ID, status: JobQueued},
	}
	for index, expected := range want {
		if active[index].ID != expected.id || active[index].Status != expected.status {
			t.Fatalf("active[%d] = %#v, want id=%s status=%s", index, active[index], expected.id, expected.status)
		}
	}
}

func TestJobLifecycleAndCheckpoints(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	job, err := state.CreateJob(ctx, "test prompt", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim job: found=%v err=%v", found, err)
	}
	if claimed.ID != job.ID || claimed.Status != JobRunning {
		t.Fatalf("claimed job = %#v", claimed)
	}

	transcript := []byte(`[{"role":"user","content":"test prompt"}]`)
	if err := state.SaveTranscript(ctx, job.ID, transcript); err != nil {
		t.Fatal(err)
	}
	loadedTranscript, err := state.LoadTranscript(ctx, job.ID)
	if err != nil || string(loadedTranscript) != string(transcript) {
		t.Fatalf("transcript = %s, err=%v", loadedTranscript, err)
	}
	agentContext := []byte(`[{"role":"user","content":"compacted context"}]`)
	updatedTranscript := []byte(`[{"role":"user","content":"complete transcript"}]`)
	if err := state.SaveAgentCheckpoint(ctx, job.ID, updatedTranscript, agentContext); err != nil {
		t.Fatal(err)
	}
	loadedTranscript, err = state.LoadTranscript(ctx, job.ID)
	if err != nil || string(loadedTranscript) != string(updatedTranscript) {
		t.Fatalf("checkpoint transcript = %s, err=%v", loadedTranscript, err)
	}
	loadedContext, err := state.LoadAgentContext(ctx, job.ID)
	if err != nil || string(loadedContext) != string(agentContext) {
		t.Fatalf("agent context = %s, err=%v", loadedContext, err)
	}
	toolResult := []byte(`{"success":true}`)
	if err := state.SaveToolResult(ctx, job.ID, "call-1", "read_file_contents", `{}`, toolResult); err != nil {
		t.Fatal(err)
	}
	loadedResult, found, err := state.LoadToolResult(ctx, job.ID, "call-1")
	if err != nil || !found || string(loadedResult) != string(toolResult) {
		t.Fatalf("tool result = %s, found=%v, err=%v", loadedResult, found, err)
	}
	executions, err := state.ListToolExecutions(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 || executions[0].ToolCallID != "call-1" ||
		executions[0].ToolName != "read_file_contents" ||
		executions[0].Result != string(toolResult) {
		t.Fatalf("tool executions = %#v", executions)
	}

	if err := state.FinishJob(ctx, job.ID, JobCompleted, "done", ""); err != nil {
		t.Fatal(err)
	}
	finished, err := state.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != JobCompleted || finished.Result != "done" || !finished.Terminal() {
		t.Fatalf("finished job = %#v", finished)
	}
	events, err := state.EventsAfter(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Data != JobQueued || events[2].Data != JobCompleted {
		t.Fatalf("events = %#v", events)
	}
}

func TestMigrationMarksErroredPendingJobsFailed(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "pearl.db")
	state, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	untouched, err := state.CreatePendingNamedJob(
		ctx, "untouched", "not started", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}

	misclassified, err := state.CreatePendingNamedJob(
		ctx, "misclassified", "failed before output", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `
UPDATE jobs
SET error = ?, started_at = ?, finished_at = ?
WHERE id = ?`, "could not start", nowText(), nowText(), misclassified.ID); err != nil {
		t.Fatal(err)
	}

	alreadyFailed, err := state.CreateJob(ctx, "started", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim started job: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, alreadyFailed.ID, JobFailed, "", "stopped later"); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	loadedUntouched, err := state.GetJob(ctx, untouched.ID)
	if err != nil || loadedUntouched.Status != JobPending {
		t.Fatalf("untouched job = %#v, err=%v", loadedUntouched, err)
	}
	loadedMisclassified, err := state.GetJob(ctx, misclassified.ID)
	if err != nil || loadedMisclassified.Status != JobFailed {
		t.Fatalf("misclassified job = %#v, err=%v", loadedMisclassified, err)
	}
	loadedFailed, err := state.GetJob(ctx, alreadyFailed.ID)
	if err != nil || loadedFailed.Status != JobFailed {
		t.Fatalf("failed job = %#v, err=%v", loadedFailed, err)
	}
	if queued, _, err := state.QueueJob(ctx, untouched.ID); err != nil || queued.Status != JobQueued {
		t.Fatalf("queue untouched job = %#v, err=%v", queued, err)
	}
}

func TestRecoveryMarksRunningJobInterruptedAndRetryQueuesIt(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	created, err := state.CreateJob(ctx, "recover me", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim job: found=%v err=%v", found, err)
	}
	count, err := state.RecoverRunningJobs(ctx)
	if err != nil || count != 1 {
		t.Fatalf("recover: count=%d err=%v", count, err)
	}
	interrupted, err := state.GetJob(ctx, created.ID)
	if err != nil || interrupted.Status != JobInterrupted {
		t.Fatalf("interrupted job = %#v err=%v", interrupted, err)
	}
	retried, retrySequence, err := state.RetryJob(ctx, created.ID)
	if err != nil || retried.Status != JobQueued {
		t.Fatalf("retried job = %#v err=%v", retried, err)
	}
	if retrySequence == 0 {
		t.Fatal("retry event sequence was not returned")
	}
}

func TestScheduleFiresOnceAndAdvances(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	schedule, err := state.CreateSchedule(ctx, "test", "scheduled prompt", t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	job, fired, err := state.FireSchedule(ctx, schedule, schedule.NextRunAt)
	if err != nil || !fired {
		t.Fatalf("fire schedule: job=%#v fired=%v err=%v", job, fired, err)
	}
	if job.Prompt != schedule.Prompt || job.Status != JobQueued {
		t.Fatalf("scheduled job = %#v", job)
	}
	if words := strings.Split(job.ID, "-"); len(words) != 2 {
		t.Fatalf("scheduled job ID %q is not two words", job.ID)
	}
	if _, fired, err := state.FireSchedule(ctx, schedule, schedule.NextRunAt); err != nil || fired {
		t.Fatalf("duplicate fire: fired=%v err=%v", fired, err)
	}
	listed, err := state.ListSchedules(ctx)
	if err != nil || len(listed) != 1 || !listed[0].NextRunAt.After(schedule.NextRunAt) {
		t.Fatalf("schedules = %#v err=%v", listed, err)
	}
}

func TestAutonomousSessionKeepsTheUsersChoices(t *testing.T) {
	state := openClaimTestStore(t)
	ctx := context.Background()
	config := AutonomousConfig{
		Goal: "ship it", WorkspaceRoot: t.TempDir(),
		Orchestrator:  AutonomousModel{Provider: "claude", ID: "opus", Name: "Opus 5.5"},
		AllowedModels: []AutonomousModel{{Provider: "claude", ID: "haiku"}, {Provider: "chatgpt", ID: "gpt-6"}},
		AllModels:     true,
		MaxSubagents:  4,
	}
	created, err := state.CreateAutonomousSession(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetAutonomousPlan(ctx, created.ID, "step one"); err != nil {
		t.Fatal(err)
	}
	session, err := state.GetAutonomousSession(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Orchestrator != config.Orchestrator || len(session.AllowedModels) != 2 ||
		session.AllowedModels[1].ID != "gpt-6" || !session.AllModels || session.MaxSubagents != 4 || session.Plan != "step one" {
		t.Fatalf("session = %+v", session)
	}
	job, err := state.CreateAutonomousJob(ctx, created.ID, "do it", "claude", "haiku")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := state.GetJob(ctx, job.ID)
	if err != nil || loaded.AssignedProvider != "claude" || loaded.AssignedModel != "haiku" {
		t.Fatalf("job = %+v, err = %v", loaded, err)
	}
}

func openClaimTestStore(t *testing.T) *Store {
	t.Helper()
	state, err := Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	return state
}

func claimAll(t *testing.T, state *Store) []string {
	t.Helper()
	var claimed []string
	for {
		job, found, err := state.ClaimNextJob(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return claimed
		}
		claimed = append(claimed, job.ID)
	}
}

func TestClaimRunsSubagentsSideBySideUpToTheirLimit(t *testing.T) {
	state := openClaimTestStore(t)
	ctx := context.Background()
	session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: t.TempDir(), MaxSubagents: 2})
	if err != nil {
		t.Fatal(err)
	}
	var jobs []Job
	for range 3 {
		job, err := state.CreateAutonomousJob(ctx, session.ID, "work", "", "")
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	claimed := claimAll(t, state)
	if len(claimed) != 2 {
		t.Fatalf("claimed %v, want 2 of 3", claimed)
	}
	if err := state.FinishJob(ctx, claimed[0], JobCompleted, "ok", ""); err != nil {
		t.Fatal(err)
	}
	next := claimAll(t, state)
	if len(next) != 1 || next[0] == claimed[0] || next[0] == claimed[1] {
		t.Fatalf("after one of %v finished, claimed %v, want the third job", claimed, next)
	}
}

func TestClaimKeepsOrdinaryJobsOneAtATimeBesideSubagents(t *testing.T) {
	state := openClaimTestStore(t)
	ctx := context.Background()
	workspace := t.TempDir()
	first, err := state.CreateJob(ctx, "first", workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.CreateJob(ctx, "second", workspace)
	if err != nil {
		t.Fatal(err)
	}
	session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: workspace, MaxSubagents: 1})
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.CreateAutonomousJob(ctx, session.ID, "child", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Jobs made within the same moment can claim in either order, so go by who's in.
	claimed := claimAll(t, state)
	if len(claimed) != 2 || !slices.Contains(claimed, child.ID) ||
		slices.Contains(claimed, first.ID) == slices.Contains(claimed, second.ID) {
		t.Fatalf("claimed %v, want one ordinary job and the subagent", claimed)
	}
	running, waiting := first.ID, second.ID
	if !slices.Contains(claimed, running) {
		running, waiting = waiting, running
	}
	if err := state.FinishJob(ctx, running, JobCompleted, "ok", ""); err != nil {
		t.Fatal(err)
	}
	if next := claimAll(t, state); len(next) != 1 || next[0] != waiting {
		t.Fatalf("claimed %v, want the other ordinary job", next)
	}
}

func TestStoppedSessionStaysStopped(t *testing.T) {
	state := openClaimTestStore(t)
	ctx := context.Background()
	session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: t.TempDir(), MaxSubagents: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateAutonomousSession(ctx, session.ID, AutonomousCancelled, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateAutonomousSession(ctx, session.ID, AutonomousRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.GetAutonomousSession(ctx, session.ID)
	if err != nil || loaded.Status != AutonomousCancelled {
		t.Fatalf("status = %q, err = %v", loaded.Status, err)
	}
	if err := state.UpdateAutonomousSession(ctx, "missing", AutonomousRunning, "", ""); err == nil {
		t.Fatal("updating a missing session succeeded")
	}
}

func TestAutonomousTurnsAndJobActivity(t *testing.T) {
	state := openClaimTestStore(t)
	ctx := context.Background()
	session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: t.TempDir(), MaxSubagents: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.StartAutonomousTurn(ctx, session.ID, "Session started")
	if err != nil || first != 1 {
		t.Fatalf("turn = %d, err = %v", first, err)
	}
	if err := state.UpdateAutonomousTurn(ctx, session.ID, first, "thinking", 12); err != nil {
		t.Fatal(err)
	}
	// A turn left open is closed when the next one starts, as after a restart.
	second, err := state.StartAutonomousTurn(ctx, session.ID, "Picking up")
	if err != nil || second != 2 {
		t.Fatalf("turn = %d, err = %v", second, err)
	}
	if err := state.FinishAutonomousTurn(ctx, session.ID, second, "Started 1 job"); err != nil {
		t.Fatal(err)
	}
	job, err := state.CreateAutonomousJob(ctx, session.ID, "work", "claude", "opus")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range [][2]string{{"tool", "shell: go test"}, {"answer", "frag"}} {
		if _, err := state.AppendEvent(ctx, job.ID, event[0], event[1]); err != nil {
			t.Fatal(err)
		}
	}
	details, err := state.AutonomousDetails(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Turns) != 2 || details.Turns[0].Reasoning != "thinking" || details.Turns[0].DecisionBytes != 12 ||
		details.Turns[0].FinishedAt == nil || details.Turns[0].Outcome == "" || details.Turns[1].Outcome != "Started 1 job" {
		t.Fatalf("turns = %+v", details.Turns)
	}
	if details.JobActivity[job.ID] != "shell: go test" {
		t.Fatalf("activity = %v", details.JobActivity)
	}
	if _, err := state.AppendEvent(ctx, job.ID, "reasoning", "Looking at the failing parser test now"); err != nil {
		t.Fatal(err)
	}
	details, err = state.AutonomousDetails(ctx, session.ID)
	if err != nil || details.JobActivity[job.ID] != "thinking: Looking at the failing parser test now" {
		t.Fatalf("activity = %v, err = %v", details.JobActivity, err)
	}
}
