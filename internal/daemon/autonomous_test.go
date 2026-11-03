package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

var testModels = []store.AutonomousModel{
	{Provider: "claude", ID: "opus", Name: "Opus 5.5"},
	{Provider: "chatgpt", ID: "gpt-6", Name: "GPT-6"},
}

// scriptedOrchestrator answers each turn with the next reply, and records prompts.
type scriptedOrchestrator struct {
	lock    sync.Mutex
	replies []func(prompt string) string
	prompts []string
}

func (s *scriptedOrchestrator) ask(_ context.Context, _ store.AutonomousSession, prompt string, progress orchestratorProgress) (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	turn := len(s.prompts)
	s.prompts = append(s.prompts, prompt)
	if turn >= len(s.replies) {
		return `{"finish":"out of script"}`, nil
	}
	answer := s.replies[turn](prompt)
	progress("text", answer)
	return answer, nil
}

func (s *scriptedOrchestrator) turns() []string {
	s.lock.Lock()
	defer s.lock.Unlock()
	return append([]string(nil), s.prompts...)
}

func reply(text string) func(string) string { return func(string) string { return text } }

// childRunner runs jobs at once when told to wait, so tests can see them overlap.
type childRunner struct {
	lock    sync.Mutex
	running int
	peak    int
	seen    []store.Job
	hold    time.Duration
	answer  func(store.Job) (string, error)
}

func (r *childRunner) Run(
	ctx context.Context,
	job store.Job,
	_ openrouter_request.EventSink,
	_ openrouter_request.CheckpointStore,
) (string, error) {
	r.lock.Lock()
	r.running++
	r.peak = max(r.peak, r.running)
	r.seen = append(r.seen, job)
	r.lock.Unlock()
	defer func() {
		r.lock.Lock()
		r.running--
		r.lock.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(r.hold):
	}
	if r.answer != nil {
		return r.answer(job)
	}
	return "report on " + job.Prompt, nil
}

type autonomousFixture struct {
	state      *store.Store
	session    store.AutonomousSession
	child      *childRunner
	autonomous *AutonomousManager
}

func startAutonomous(t *testing.T, maxSubagents int, script *scriptedOrchestrator, child *childRunner) autonomousFixture {
	t.Helper()
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	session, err := state.CreateAutonomousSession(context.Background(), store.AutonomousConfig{
		Goal: "audit the project", WorkspaceRoot: t.TempDir(),
		Orchestrator: testModels[0], AllowedModels: testModels, MaxSubagents: maxSubagents,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := NewManager(state, child)
	jobs.Start(ctx)
	autonomous := NewAutonomousManager(state, jobs, OrchestratorRunner{
		Ask: script.ask, Poll: 5 * time.Millisecond, RetryDelay: time.Millisecond,
	})
	autonomous.Start(ctx)
	t.Cleanup(func() {
		cancel()
		jobs.Wait()
		autonomous.Wait()
		state.Close()
	})
	return autonomousFixture{state, session, child, autonomous}
}

func (f autonomousFixture) waitForStatus(t *testing.T, want string) store.AutonomousDetails {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var details store.AutonomousDetails
	for time.Now().Before(deadline) {
		var err error
		details, err = f.state.AutonomousDetails(context.Background(), f.session.ID)
		if err == nil && details.Session.Status == want {
			return details
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session stayed %q, want %q: %+v", details.Session.Status, want, details.Session)
	return details
}

func decisionWith(jobs ...orchestratorJobRequest) string {
	data, _ := json.Marshal(orchestratorDecision{Plan: "the plan", Jobs: jobs})
	return string(data)
}

func TestOrchestratorAssignsModelsAndMakesNewTasksFromReports(t *testing.T) {
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply(decisionWith(
			orchestratorJobRequest{Model: "claude/opus", Prompt: "fix the parser"},
			orchestratorJobRequest{Model: "chatgpt/gpt-6", Prompt: "write the docs"},
		)),
		// One report at a time may come back, so keep answering until both are in.
		func(prompt string) string {
			if strings.Contains(prompt, "report on fix the parser") {
				return decisionWith(orchestratorJobRequest{Model: "gpt-6", Prompt: "fix what the report found"})
			}
			return `{"plan":"waiting"}`
		},
	}}
	script.replies = append(script.replies, func(prompt string) string {
		if strings.Contains(prompt, "fix what the report found") {
			return `{"plan":"all done"}`
		}
		return decisionWith(orchestratorJobRequest{Model: "gpt-6", Prompt: "fix what the report found"})
	})
	for range 8 {
		script.replies = append(script.replies, func(prompt string) string {
			if strings.Count(prompt, `"status": "completed"`) == 3 {
				return `{"finish":"everything is fixed"}`
			}
			return `{"plan":"waiting"}`
		})
	}
	child := &childRunner{hold: 20 * time.Millisecond}
	fixture := startAutonomous(t, 2, script, child)
	details := fixture.waitForStatus(t, store.AutonomousCompleted)
	if details.Session.Summary != "everything is fixed" {
		t.Fatalf("summary = %q", details.Session.Summary)
	}
	if len(details.Jobs) != 3 {
		t.Fatalf("jobs = %+v", details.Jobs)
	}
	models := map[string]string{}
	for _, job := range details.Jobs {
		if job.Status != store.JobCompleted {
			t.Errorf("job %+v didn't finish", job)
		}
		models[job.Prompt] = job.AssignedProvider + "/" + job.AssignedModel
	}
	if models["fix the parser"] != "claude/opus" || models["write the docs"] != "chatgpt/gpt-6" ||
		models["fix what the report found"] != "chatgpt/gpt-6" {
		t.Errorf("assigned models = %v", models)
	}
	if child.peak != 2 {
		t.Errorf("peak running = %d, want 2", child.peak)
	}
	if details.Session.Plan == "" {
		t.Error("the plan wasn't saved")
	}
}

func TestOrchestratorSeesTestResultsAndLimits(t *testing.T) {
	session := store.AutonomousSession{
		Goal: "g", WorkspaceRoot: "/w", AllowedModels: testModels, MaxSubagents: 3,
	}
	jobs := []store.Job{
		{ID: "a", Status: store.JobRunning, Prompt: "still going"},
		{
			ID: "b", Status: store.JobCompleted, Prompt: "fixed it", Result: "done",
			Verification: "failed", VerificationCommand: "go test ./...", AssignedProvider: "claude", AssignedModel: "opus",
		},
	}
	prompt, err := orchestratorPrompt(session, "my notes", jobs, map[string]string{}, []string{"bad model"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"tests": "failed"`, `"test_command": "go test ./..."`, `"new_report": true`, `"model": "claude/opus"`,
		"claude/opus (Opus 5.5)", "chatgpt/gpt-6 (GPT-6)", "At most 3 jobs run at once", "1 are running or queued now, so 2 more",
		"my notes", "bad model",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

func TestOrchestratorPromptShortensWhatItAlreadyRead(t *testing.T) {
	session := store.AutonomousSession{Goal: "g", WorkspaceRoot: "/w", AllowedModels: testModels, MaxSubagents: 1}
	long := strings.Repeat("x", 3*orchestratorOldResultBytes)
	job := store.Job{ID: "a", Status: store.JobCompleted, Prompt: "p", Result: long}
	fresh, err := orchestratorPrompt(session, "", []store.Job{job}, map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, err := orchestratorPrompt(session, "", []store.Job{job}, map[string]string{"a": store.JobCompleted}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fresh, long) || strings.Contains(old, long) || !strings.Contains(old, "[... cut ...]") {
		t.Fatalf("fresh has it all: %v, old has it all: %v", strings.Contains(fresh, long), strings.Contains(old, long))
	}
	var many []store.Job
	for index := range orchestratorFullReports + 5 {
		many = append(many, store.Job{ID: fmt.Sprint(index), Status: store.JobCompleted, Prompt: "p", Result: long})
	}
	bounded, err := orchestratorPrompt(session, "", many, map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(bounded, long); count != orchestratorFullReports {
		t.Fatalf("%d full reports, want %d", count, orchestratorFullReports)
	}
}

func TestOrchestratorRejectsModelsOffTheList(t *testing.T) {
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply(decisionWith(orchestratorJobRequest{Model: "claude/secret-model", Prompt: "x"})),
		reply(decisionWith(orchestratorJobRequest{Model: "claude/opus", Prompt: "y"})),
	}}
	for range 4 {
		script.replies = append(script.replies, reply(`{"finish":"done"}`))
	}
	fixture := startAutonomous(t, 1, script, &childRunner{})
	details := fixture.waitForStatus(t, store.AutonomousCompleted)
	if len(details.Jobs) != 1 || details.Jobs[0].AssignedModel != "opus" {
		t.Fatalf("jobs = %+v", details.Jobs)
	}
	if turns := script.turns(); len(turns) < 2 || !strings.Contains(turns[1], `"claude/secret-model" isn't on the allowed list`) {
		t.Fatalf("the orchestrator wasn't told: %v", turns)
	}
}

func TestOrchestratorCantFinishWhileJobsRun(t *testing.T) {
	release := make(chan struct{})
	child := &childRunner{answer: func(store.Job) (string, error) {
		<-release
		return "report", nil
	}}
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply(`{"jobs":[{"model":"claude/opus","prompt":"slow"}],"finish":"too early"}`),
		func(prompt string) string {
			if !strings.Contains(prompt, "You can't finish yet") {
				return `{"plan":"the correction went missing"}`
			}
			return `{"finish":"finished properly"}`
		},
	}}
	fixture := startAutonomous(t, 1, script, child)
	deadline := time.Now().Add(5 * time.Second)
	for len(script.turns()) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// It must wait for the job instead of spending another turn.
	time.Sleep(50 * time.Millisecond)
	if turns := len(script.turns()); turns != 1 {
		t.Fatalf("%d turns while the job was running, want 1", turns)
	}
	close(release)
	details := fixture.waitForStatus(t, store.AutonomousCompleted)
	if details.Session.Summary != "finished properly" {
		t.Fatalf("summary = %q", details.Session.Summary)
	}
}

func TestOrchestratorGivesUpAfterRepeatedBadReplies(t *testing.T) {
	script := &scriptedOrchestrator{}
	for range 10 {
		script.replies = append(script.replies, reply("I would rather chat"))
	}
	fixture := startAutonomous(t, 1, script, &childRunner{})
	details := fixture.waitForStatus(t, store.AutonomousFailed)
	if !strings.Contains(details.Session.Error, "stopped making progress") {
		t.Fatalf("error = %q", details.Session.Error)
	}
	if turns := len(script.turns()); turns != orchestratorMaxCorrections {
		t.Fatalf("%d turns, want %d", turns, orchestratorMaxCorrections)
	}
}

func TestOrchestratorRetriesAFailedTurnThenFails(t *testing.T) {
	calls := 0
	runner := OrchestratorRunner{
		Ask: func(context.Context, store.AutonomousSession, string, orchestratorProgress) (string, error) {
			calls++
			return "", fmt.Errorf("network down")
		},
		Poll: time.Millisecond, RetryDelay: time.Millisecond,
	}
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
	var saved, outcome string
	_, err := runner.Run(context.Background(), store.AutonomousSession{AllowedModels: testModels, Orchestrator: testModels[0]}, AutonomousTools{
		ListJobs:     func(context.Context) ([]store.Job, error) { return nil, nil },
		Reviewing:    func(context.Context) error { return nil },
		TurnStarted:  func(context.Context, string) (int, error) { return 1, nil },
		TurnProgress: func(_ int, reasoning string, _ int) { saved = reasoning },
		TurnFinished: func(_ int, text string) { outcome = text },
	})
	if err == nil || !strings.Contains(err.Error(), "network down") || calls != orchestratorAskAttempts {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
	// The retries show on the screen instead of looking like silence.
	if !strings.Contains(saved, "Attempt 1 failed: network down") || !strings.Contains(saved, "Attempt 2 failed") {
		t.Errorf("saved = %q", saved)
	}
	if !strings.Contains(outcome, "Failed: network down") {
		t.Errorf("outcome = %q", outcome)
	}
}

func TestOrchestratorPausesForTheUsersAnswer(t *testing.T) {
	child := &childRunner{answer: func(store.Job) (string, error) {
		return "", &openrouter_request.UserInputRequiredError{ToolCallID: "q", Question: "which one?"}
	}}
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply(decisionWith(orchestratorJobRequest{Model: "claude/opus", Prompt: "ask"})),
		reply(`{"plan":"waiting for the user"}`),
	}}
	fixture := startAutonomous(t, 1, script, child)
	details := fixture.waitForStatus(t, store.AutonomousWaitingInput)
	if !strings.Contains(script.turns()[1], `"waiting_for_user": "which one?"`) {
		t.Fatalf("the orchestrator didn't see the question: %s", script.turns()[1])
	}
	if len(details.Jobs) != 1 {
		t.Fatalf("jobs = %+v", details.Jobs)
	}
}

func TestStopEndsTheSessionAndItsJobs(t *testing.T) {
	child := &childRunner{hold: time.Minute}
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply(decisionWith(
			orchestratorJobRequest{Model: "claude/opus", Prompt: "one"},
			orchestratorJobRequest{Model: "claude/opus", Prompt: "two"},
		)),
	}}
	fixture := startAutonomous(t, 1, script, child)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := fixture.state.ListAutonomousJobs(context.Background(), fixture.session.ID)
		if len(jobs) == 2 && (jobs[0].Status == store.JobRunning || jobs[1].Status == store.JobRunning) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := fixture.autonomous.Stop(context.Background(), fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	details := fixture.waitForStatus(t, store.AutonomousCancelled)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		settled := true
		jobs, _ := fixture.state.ListAutonomousJobs(context.Background(), fixture.session.ID)
		for _, job := range jobs {
			settled = settled && job.Status == store.JobCancelled
		}
		if settled {
			details.Jobs = jobs
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, job := range details.Jobs {
		if job.Status != store.JobCancelled {
			t.Errorf("job %s is %s", job.ID, job.Status)
		}
	}
	// Stopping twice is harmless, and it stays stopped.
	if err := fixture.autonomous.Stop(context.Background(), fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if session, _ := fixture.state.GetAutonomousSession(context.Background(), fixture.session.ID); session.Status != store.AutonomousCancelled {
		t.Fatalf("status = %q", session.Status)
	}
}

func TestParseOrchestratorDecisionToleratesWrapping(t *testing.T) {
	for _, text := range []string{
		`{"finish":"done"}`,
		"Here you go:\n```json\n{\"finish\":\"done\"}\n```\n",
	} {
		decision, err := parseOrchestratorDecision(text)
		if err != nil || decision.Finish != "done" {
			t.Errorf("%q: decision = %+v, err = %v", text, decision, err)
		}
	}
	for _, text := range []string{"no json here", "{broken"} {
		if _, err := parseOrchestratorDecision(text); err == nil {
			t.Errorf("%q was accepted", text)
		}
	}
}

func TestFindAllowedModelNeedsAnExactOrUniqueName(t *testing.T) {
	models := []store.AutonomousModel{{Provider: "a", ID: "same"}, {Provider: "b", ID: "same"}, {Provider: "b", ID: "only"}}
	if model, ok := findAllowedModel(models, "b/same"); !ok || model.Provider != "b" {
		t.Errorf("exact: %+v, %v", model, ok)
	}
	if model, ok := findAllowedModel(models, "only"); !ok || model.Provider != "b" {
		t.Errorf("unique: %+v, %v", model, ok)
	}
	if _, ok := findAllowedModel(models, "same"); ok {
		t.Error("an ambiguous ID was accepted")
	}
	if _, ok := findAllowedModel(models, "other"); ok {
		t.Error("an unknown model was accepted")
	}
}

func TestValidateAutonomousConfig(t *testing.T) {
	valid := store.AutonomousConfig{
		Goal: "g", Orchestrator: testModels[0], AllowedModels: testModels, MaxSubagents: 2,
	}
	if err := validateAutonomousConfig(&valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*store.AutonomousConfig){
		"no goal":     func(c *store.AutonomousConfig) { c.Goal = " " },
		"no models":   func(c *store.AutonomousConfig) { c.AllowedModels = nil },
		"zero":        func(c *store.AutonomousConfig) { c.MaxSubagents = 0 },
		"too many":    func(c *store.AutonomousConfig) { c.MaxSubagents = AutonomousMaxSubagents + 1 },
		"no provider": func(c *store.AutonomousConfig) { c.Orchestrator = store.AutonomousModel{ID: "x", Provider: "nope"} },
		"no model ID": func(c *store.AutonomousConfig) { c.Orchestrator = store.AutonomousModel{Provider: "claude"} },
	} {
		config := valid
		change(&config)
		if err := validateAutonomousConfig(&config); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestAssignedModelDecidesTheHarnessAndSettings(t *testing.T) {
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m","claude_model":"selected"}`)
	if got := resolveJobHarness(store.Job{AssignedProvider: "claude", AssignedModel: "opus"}); got != store.HarnessClaude {
		t.Errorf("harness = %q", got)
	}
	if got := resolveJobHarness(store.Job{AssignedProvider: "chatgpt"}); got != store.HarnessCodex {
		t.Errorf("harness = %q", got)
	}
	if got := resolveJobHarness(store.Job{AssignedProvider: "antigravity", Harness: store.HarnessClaude, InputToolCallID: "x"}); got != store.HarnessAntigravity {
		t.Errorf("harness = %q", got)
	}
	if got := resolveJobHarness(store.Job{}); got != store.HarnessPearl {
		t.Errorf("unassigned harness = %q", got)
	}
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		t.Fatal(err)
	}
	provider, model := harnessBilling(store.HarnessClaude, settings.WithModel("claude", "opus"))
	if provider != "claude" || model != "opus" {
		t.Errorf("billing = %s %s", provider, model)
	}
	if settings.ClaudeModel != "selected" {
		t.Error("assigning a model changed the saved settings")
	}
}

func TestParseClaudeAnswer(t *testing.T) {
	if answer, err := parseClaudeAnswer([]byte(`{"type":"result","subtype":"success","result":"{}"}`), "", nil); err != nil || answer != "{}" {
		t.Errorf("answer = %q, err = %v", answer, err)
	}
	if _, err := parseClaudeAnswer([]byte(`{"is_error":true,"result":"usage limit"}`), "", nil); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Errorf("err = %v", err)
	}
	if _, err := parseClaudeAnswer(nil, "not signed in", nil); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("err = %v", err)
	}
}

func TestVisibleOrchestratorTextHidesTheDecision(t *testing.T) {
	for _, test := range []struct {
		text, want string
		hidden     bool
	}{
		{"Thinking it over.", "Thinking it over.", false},
		{"Thinking.\nDECI", "Thinking.", false},
		{"Thinking.\nDECISION\n{\"plan\":\"x\"}", "Thinking.", true},
		{"No marker.\n{\"plan\":\"x\"}", "No marker.", true},
		{"Fenced.\n```json\n{}\n```", "Fenced.", true},
		{"Use the {curly} config and DECISION logic.", "Use the {curly} config and DECISION logic.", false},
		{"{\"plan\":\"x\"}", "", true},
	} {
		got, bytes := visibleOrchestratorText(test.text)
		if got != test.want || (bytes > 0) != test.hidden {
			t.Errorf("%q: got %q with %d hidden", test.text, got, bytes)
		}
	}
}

func TestParseOrchestratorDecisionIgnoresBracesInReasoning(t *testing.T) {
	decision, err := parseOrchestratorDecision("The {config} file matters.\nDECISION\n{\"finish\":\"done\"}\n")
	if err != nil || decision.Finish != "done" {
		t.Fatalf("decision = %+v, err = %v", decision, err)
	}
}

func TestReadClaudeStreamPassesOnWordsAndReturnsTheResult(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}`,
		`not json`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"there"}}}`,
		`{"type":"result","subtype":"success","result":"Hello there"}`,
	}, "\n") + "\n"
	var words []string
	result := readClaudeStream(strings.NewReader(stream), func(kind, text string) { words = append(words, kind+":"+text) })
	if strings.Join(words, "|") != "thought:hmm|text:Hello |text:there" {
		t.Errorf("words = %v", words)
	}
	if answer, err := parseClaudeAnswer(result, "", nil); err != nil || answer != "Hello there" {
		t.Errorf("answer = %q, err = %v", answer, err)
	}
}

func TestSessionRecordsEachTurnsReasoningAndOutcome(t *testing.T) {
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply("Two files to write, so two jobs.\nDECISION\n" + decisionWith(
			orchestratorJobRequest{Model: "claude/opus", Prompt: "one"},
			orchestratorJobRequest{Model: "chatgpt/gpt-6", Prompt: "two"},
		)),
	}}
	for range 6 {
		script.replies = append(script.replies, func(prompt string) string {
			if strings.Count(prompt, `"status": "completed"`) == 2 {
				return "Both are in.\nDECISION\n{\"finish\":\"done\"}"
			}
			return "One is still going.\nDECISION\n{\"plan\":\"waiting\"}"
		})
	}
	fixture := startAutonomous(t, 2, script, &childRunner{hold: 10 * time.Millisecond})
	details := fixture.waitForStatus(t, store.AutonomousCompleted)
	if len(details.Turns) < 2 {
		t.Fatalf("turns = %+v", details.Turns)
	}
	first, last := details.Turns[0], details.Turns[len(details.Turns)-1]
	if first.Trigger != "Session started" || first.Reasoning != "Two files to write, so two jobs." ||
		first.Outcome != "Started 2 jobs" || first.DecisionBytes == 0 || first.FinishedAt == nil {
		t.Errorf("first turn = %+v", first)
	}
	if strings.Contains(first.Reasoning, "DECISION") || strings.Contains(first.Reasoning, "{") {
		t.Errorf("the decision leaked into the reasoning: %q", first.Reasoning)
	}
	if last.Outcome != "Finished the session" || !strings.Contains(last.Trigger, "completed") {
		t.Errorf("last turn = %+v", last)
	}
}

func TestStoppingMarksTheOpenTurn(t *testing.T) {
	entered := make(chan struct{})
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
	session, err := state.CreateAutonomousSession(context.Background(), store.AutonomousConfig{
		Goal: "g", WorkspaceRoot: t.TempDir(), Orchestrator: testModels[0], AllowedModels: testModels, MaxSubagents: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := NewManager(state, &childRunner{})
	jobs.Start(ctx)
	manager := NewAutonomousManager(state, jobs, OrchestratorRunner{
		Ask: func(ctx context.Context, _ store.AutonomousSession, _ string, progress orchestratorProgress) (string, error) {
			progress("text", "Still thinking")
			close(entered)
			<-ctx.Done()
			return "", ctx.Err()
		},
		RetryDelay: time.Millisecond,
	})
	manager.Start(ctx)
	defer func() {
		cancel()
		jobs.Wait()
		manager.Wait()
	}()
	<-entered
	if err := manager.Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		details, err := state.AutonomousDetails(context.Background(), session.ID)
		if err == nil && len(details.Turns) == 1 && details.Turns[0].FinishedAt != nil {
			if details.Turns[0].Outcome != "Stopped" || details.Turns[0].Reasoning != "Still thinking" {
				t.Fatalf("turn = %+v", details.Turns[0])
			}
			return
		}
	}
	t.Fatal("the turn never closed")
}

func TestOrchestratorRetriesWhenTheModelGoesSilent(t *testing.T) {
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
	calls := 0
	var saved, outcome string
	runner := OrchestratorRunner{
		Ask: func(ctx context.Context, _ store.AutonomousSession, _ string, progress orchestratorProgress) (string, error) {
			calls++
			if calls == 1 {
				progress("text", "Starting")
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "Back again.\nDECISION\n{\"finish\":\"done\"}", nil
		},
		Poll: time.Millisecond, RetryDelay: time.Millisecond, SilenceLimit: 20 * time.Millisecond,
	}
	jobs := []store.Job{{ID: "a", Status: store.JobCompleted}}
	outcomeValue, err := runner.Run(context.Background(), store.AutonomousSession{AllowedModels: testModels, Orchestrator: testModels[0]}, AutonomousTools{
		ListJobs:     func(context.Context) ([]store.Job, error) { return jobs, nil },
		Reviewing:    func(context.Context) error { return nil },
		SavePlan:     func(context.Context, string) error { return nil },
		TurnStarted:  func(context.Context, string) (int, error) { return 1, nil },
		TurnProgress: func(_ int, reasoning string, _ int) { saved += reasoning + "|" },
		TurnFinished: func(_ int, text string) { outcome = text },
	})
	if err != nil || !outcomeValue.Finished || calls != 2 {
		t.Fatalf("outcome = %+v, err = %v, calls = %d", outcomeValue, err, calls)
	}
	if !strings.Contains(saved, "the model said nothing for") {
		t.Errorf("the retry wasn't explained: %q", saved)
	}
	if outcome != "Finished the session" {
		t.Errorf("outcome = %q", outcome)
	}
}
