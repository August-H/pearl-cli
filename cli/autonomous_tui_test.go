package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

var autonomousTestTime = time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)

func autonomousTestDetails() store.AutonomousDetails {
	return store.AutonomousDetails{
		Session: store.AutonomousSession{
			ID: "auto_1", Goal: "ship", Status: store.AutonomousRunning, WorkspaceRoot: "/work/pearl",
			Orchestrator: store.AutonomousModel{Provider: "claude", ID: "opus", Name: "Opus 5.5"},
			AllModels:    true, MaxSubagents: 4, CreatedAt: autonomousTestTime,
		},
	}
}

func TestActivityComesFromWhatsSavedNotWhenTheScreenOpened(t *testing.T) {
	details := autonomousTestDetails()
	finished := autonomousTestTime.Add(5 * time.Second)
	details.Turns = []store.AutonomousTurn{{Turn: 1, Outcome: "Started 1 job", StartedAt: autonomousTestTime, FinishedAt: &finished}}
	details.Jobs = []store.Job{{ID: "amber-otter", Status: store.JobCompleted, AssignedModel: "claude-opus-5-5"}}
	details.Events = []store.Event{
		{Sequence: 1, JobID: "amber-otter", Data: store.JobQueued, CreatedAt: autonomousTestTime.Add(4 * time.Second)},
		{Sequence: 2, JobID: "amber-otter", Data: store.JobRunning, CreatedAt: autonomousTestTime.Add(6 * time.Second)},
		{Sequence: 3, JobID: "amber-otter", Data: store.JobCompleted, CreatedAt: autonomousTestTime.Add(9 * time.Second)},
	}
	var text []string
	activity := buildAutonomousActivity(details)
	for _, entry := range activity {
		text = append(text, entry.Text)
	}
	want := []string{
		"session started", "created amber-otter · queued · Opus 5.5", "orchestrator · Started 1 job",
		"amber-otter · queued → running", "amber-otter · running → completed",
	}
	if strings.Join(text, "|") != strings.Join(want, "|") {
		t.Fatalf("activity = %q", text)
	}
	if !activity[0].At.Equal(autonomousTestTime) {
		t.Errorf("session started at %v", activity[0].At)
	}
	end := autonomousTestTime.Add(time.Minute)
	details.Session.FinishedAt = &end
	details.Session.Status = store.AutonomousCancelled
	if last := buildAutonomousActivity(details); last[len(last)-1].Text != "session cancelled" {
		t.Errorf("last = %+v", last[len(last)-1])
	}
}

func TestStatsCountJobsAndTime(t *testing.T) {
	details := autonomousTestDetails()
	details.Jobs = []store.Job{
		{Status: store.JobCompleted, Verification: "passed"},
		{Status: store.JobCompleted, Verification: "failed"},
		{Status: store.JobRunning}, {Status: store.JobQueued}, {Status: store.JobFailed},
		{Status: store.JobInterrupted}, {Status: store.JobWaitingInput}, {Status: store.JobCancelled},
	}
	details.Turns = []store.AutonomousTurn{{Turn: 3}}
	got := autonomousStats(details, autonomousTestTime.Add(64*time.Second))
	for _, want := range []string{
		"Elapsed 1m 04s", "8 created", "2 completed", "1 running", "1 queued", "1 need your answer",
		"2 failed", "1 cancelled", "1 tests passed", "1 tests failed", "Turn 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	finished := autonomousTestTime.Add(5 * time.Second)
	details.Session.FinishedAt = &finished
	if got := autonomousStats(details, autonomousTestTime.Add(time.Hour)); !strings.Contains(got, "Elapsed 5s") {
		t.Errorf("a finished session's clock kept going: %q", got)
	}
	for _, test := range []struct {
		elapsed time.Duration
		want    string
	}{{-time.Second, "0s"}, {42 * time.Second, "42s"}, {64 * time.Second, "1m 04s"}, {125 * time.Minute, "2h 05m"}} {
		if got := autonomousDuration(test.elapsed); got != test.want {
			t.Errorf("%v = %q", test.elapsed, got)
		}
	}
}

func TestScreenShowsReasoningWhileTheOrchestratorThinks(t *testing.T) {
	details := autonomousTestDetails()
	now := autonomousTestTime.Add(42 * time.Second)
	details.Turns = []store.AutonomousTurn{{Turn: 1, StartedAt: autonomousTestTime}}
	output := renderAutonomousScreen(details, nil, 80, 24, false, now)
	for _, want := range []string{"Opus 5.5 is thinking · turn 1 · 42s", "Waiting for the model's first words", "Elapsed 42s", "hasn't started any yet"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q:\n%s", want, output)
		}
	}
	details.Turns[0].Reasoning = "The game needs sprites first, so I'll start there."
	output = renderAutonomousScreen(details, nil, 80, 24, false, now)
	if !strings.Contains(output, "The game needs sprites first") {
		t.Errorf("the reasoning isn't shown:\n%s", output)
	}
	details.Turns[0].DecisionBytes = 4300
	output = renderAutonomousScreen(details, nil, 80, 24, false, now)
	if !strings.Contains(output, "writing jobs · 4.2 KB") {
		t.Errorf("the decision's progress isn't shown:\n%s", output)
	}
	finished := autonomousTestTime.Add(50 * time.Second)
	details.Turns[0].FinishedAt = &finished
	details.Jobs = []store.Job{{ID: "a", Status: store.JobRunning}}
	output = renderAutonomousScreen(details, nil, 80, 24, false, now)
	if !strings.Contains(output, "Waiting for a job to report back · 1 still going") || !strings.Contains(output, "The game needs sprites first") {
		t.Errorf("between turns:\n%s", output)
	}
}

func TestScreenSaysWhenTheModelHasGoneQuiet(t *testing.T) {
	details := autonomousTestDetails()
	spoke := autonomousTestTime.Add(10 * time.Second)
	details.Turns = []store.AutonomousTurn{{Turn: 2, StartedAt: autonomousTestTime, UpdatedAt: &spoke, DecisionBytes: 300}}
	output := renderAutonomousScreen(details, nil, 80, 24, false, spoke.Add(30*time.Second))
	if strings.Contains(output, "quiet") {
		t.Errorf("30 seconds isn't worth mentioning:\n%s", output)
	}
	output = renderAutonomousScreen(details, nil, 80, 24, false, spoke.Add(90*time.Second))
	if !strings.Contains(output, "Opus 5.5 has been quiet for 1m 30s · turn 2") {
		t.Errorf("the silence isn't shown:\n%s", output)
	}
}

func TestReasoningPanelShowsTheEndOfLongText(t *testing.T) {
	details := autonomousTestDetails()
	details.Turns = []store.AutonomousTurn{{Turn: 1, StartedAt: autonomousTestTime, Reasoning: "FIRST " + strings.Repeat("word ", 800) + "LAST"}}
	output := renderAutonomousScreen(details, nil, 80, 24, false, autonomousTestTime)
	if !strings.Contains(output, "LAST") || strings.Contains(output, "FIRST") {
		t.Errorf("the panel should follow the newest words:\n%s", output)
	}
}

func TestScreenStaysOnOneScreenAndKeepsItsFooter(t *testing.T) {
	details := autonomousTestDetails()
	details.Session.Plan = strings.Repeat("plan ", 100)
	for range 40 {
		details.Jobs = append(details.Jobs, store.Job{ID: "job", Status: store.JobRunning, Prompt: "p"})
	}
	details.Turns = []store.AutonomousTurn{{Turn: 1, StartedAt: autonomousTestTime, Reasoning: strings.Repeat("word ", 500)}}
	for _, size := range [][2]int{{80, 24}, {80, 12}, {32, 16}, {120, 50}} {
		output := renderAutonomousScreenAt(details, nil, size[0], size[1], false, "THE-HINT", autonomousTestTime)
		lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		if !strings.Contains(lines[len(lines)-1], "THE-HINT") {
			t.Errorf("%v lost the footer:\n%s", size, output)
		}
		if !strings.Contains(lines[0], "Pearl autonomous") {
			t.Errorf("%v lost the title:\n%s", size, output)
		}
		for _, line := range lines {
			if width := len([]rune(line)); width > max(32, size[0]) {
				t.Errorf("%v: line is %d wide: %q", size, width, line)
			}
		}
	}
}

// The frame ends without a new line, so the screen can't scroll its title away.
func TestScreenFrameDoesntScrollTheTerminal(t *testing.T) {
	details := autonomousTestDetails()
	frame := listTerminalFrame(renderAutonomousScreen(details, nil, 80, 24, false, autonomousTestTime))
	if strings.HasSuffix(frame, "\n") || strings.Count(frame, "\r\n") != 23 {
		t.Fatalf("%d line breaks, ends with a break: %v", strings.Count(frame, "\r\n"), strings.HasSuffix(frame, "\n"))
	}
}

func TestVisibleJobsPutWorkInProgressFirst(t *testing.T) {
	var jobs []store.Job
	for _, entry := range [][2]string{{"a", store.JobCompleted}, {"b", store.JobRunning}, {"c", store.JobCompleted}, {"d", store.JobQueued}, {"e", store.JobCompleted}} {
		jobs = append(jobs, store.Job{ID: entry[0], Status: entry[1]})
	}
	var ids []string
	for _, job := range autonomousVisibleJobs(jobs, 3) {
		ids = append(ids, job.ID)
	}
	if strings.Join(ids, "") != "bde" {
		t.Fatalf("ids = %v", ids)
	}
	if len(autonomousVisibleJobs(jobs, 9)) != 5 {
		t.Fatal("jobs that fit were dropped")
	}
}

func TestJobRowShowsTimeModelAndCurrentStep(t *testing.T) {
	started := autonomousTestTime
	job := store.Job{ID: "amber", Status: store.JobRunning, AssignedModel: "claude-opus-5-5", Prompt: "fix the parser\nmore", StartedAt: &started}
	row := renderAutonomousJob(job, "shell: go test", 100, false, autonomousTestTime.Add(62*time.Second))
	for _, want := range []string{"amber", "running", "1m 02s", "Opus 5.5 · shell: go test"} {
		if !strings.Contains(row, want) {
			t.Errorf("missing %q in %q", want, row)
		}
	}
	if strings.Contains(row, "fix the parser") {
		t.Errorf("a running job should show its step: %q", row)
	}
	finished := autonomousTestTime.Add(10 * time.Second)
	job.Status, job.FinishedAt, job.Verification = store.JobCompleted, &finished, "failed"
	row = renderAutonomousJob(job, "shell: go test", 100, false, autonomousTestTime.Add(time.Hour))
	for _, want := range []string{"10s", "tests failed", "fix the parser"} {
		if !strings.Contains(row, want) {
			t.Errorf("missing %q in %q", want, row)
		}
	}
	if strings.Contains(row, "more") {
		t.Errorf("only the prompt's first line: %q", row)
	}
	if width := len([]rune(renderAutonomousJob(job, "x", 40, false, autonomousTestTime))); width > 40 {
		t.Errorf("row is %d wide", width)
	}
}

func TestFullReasoningViewScrollsThroughEveryTurn(t *testing.T) {
	finished := autonomousTestTime.Add(30 * time.Second)
	screen := autonomousScreen{details: autonomousTestDetails()}
	screen.details.Session.Plan = "my plan"
	screen.details.Turns = []store.AutonomousTurn{
		{Turn: 1, Trigger: "Session started", Reasoning: "START " + strings.Repeat("word ", 400), Outcome: "Started 2 jobs", StartedAt: autonomousTestTime, FinishedAt: &finished},
		{Turn: 2, Trigger: "amber completed", Reasoning: "Now the docs. END", StartedAt: finished},
	}
	if result := screen.handleKey('t'); result != autonomousScreenRefresh || !screen.showReasoning {
		t.Fatalf("t = %v", result)
	}
	output := screen.frame("Esc back", 80, 24, false, finished)
	if !strings.Contains(output, "Now the docs. END") || strings.Contains(output, "START") {
		t.Errorf("it should open on the newest words:\n%s", output)
	}
	if lines := strings.Count(output, "\n"); lines != 24 {
		t.Errorf("%d lines", lines)
	}
	if !screen.applyAction("home") {
		t.Fatal("home wasn't handled")
	}
	output = screen.frame("Esc back", 80, 24, false, finished)
	for _, want := range []string{"Current plan", "my plan", "Turn 1", "Session started", "START"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q at the top:\n%s", want, output)
		}
	}
	screen.applyAction("next")
	screen.applyAction("end")
	output = screen.frame("Esc back", 80, 24, false, finished)
	if !strings.Contains(output, "END") || !strings.Contains(output, "t or Esc back") {
		t.Errorf("at the end:\n%s", output)
	}
	if result := screen.handleKey(escapeKey); result != autonomousScreenRefresh || screen.showReasoning {
		t.Fatalf("Esc = %v, still showing %v", result, screen.showReasoning)
	}
	if result := screen.handleKey(escapeKey); result != autonomousScreenLeave {
		t.Fatalf("second Esc = %v", result)
	}
}

func TestStopNeedsTwoPresses(t *testing.T) {
	screen := autonomousScreen{details: autonomousTestDetails()}
	if result := screen.handleKey('x'); result != autonomousScreenRefresh || !screen.confirmStop {
		t.Fatalf("first x = %v", result)
	}
	if !strings.Contains(screen.frame("Esc back", 100, 24, false, autonomousTestTime), "Press x again") {
		t.Error("the confirmation isn't shown")
	}
	screen.handleKey('z')
	if screen.confirmStop {
		t.Fatal("another key should cancel it")
	}
	screen.handleKey('x')
	if result := screen.handleKey('x'); result != autonomousScreenStop {
		t.Fatalf("second x = %v", result)
	}
	screen.details.Session.Status = store.AutonomousCompleted
	if result := screen.handleKey('x'); result != autonomousScreenStay {
		t.Fatalf("x on a finished session = %v", result)
	}
}

func TestScreenShowsGoalSettingsAndSummary(t *testing.T) {
	details := autonomousTestDetails()
	details.Session.Status = store.AutonomousCompleted
	details.Session.Summary = "Release checks passed."
	output := renderAutonomousScreen(details, nil, 100, 24, false, autonomousTestTime)
	for _, want := range []string{
		"Pearl autonomous", "COMPLETED", "Goal: ship", "pearl", "Orchestrator: Opus 5.5", "Subagents: all models", "Max 4 at once", "Release checks passed.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b[") {
		t.Errorf("no-color screen has ANSI escapes: %q", output)
	}
	if strings.Contains(output, "x stop") {
		t.Error("a finished session still offers to stop")
	}
}
