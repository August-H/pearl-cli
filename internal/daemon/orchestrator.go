package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	// A reply that breaks the rules gets this many chances to be corrected.
	orchestratorMaxCorrections = 3
	orchestratorAskAttempts    = 3
	orchestratorAskTimeout     = 10 * time.Minute

	// Results the orchestrator already acted on shrink, so long sessions stay cheap.
	orchestratorNewResultBytes = 16 << 10
	orchestratorOldResultBytes = 1 << 10
	orchestratorOldPromptBytes = 1 << 10
	orchestratorFullReports    = 8
	orchestratorSaveInterval   = 300 * time.Millisecond
	// A model that says nothing for this long is treated as a dropped connection,
	// as happens when a laptop sleeps. Thinking can be silent, so this is generous.
	orchestratorSilenceLimit = 5 * time.Minute
)

// OrchestratorRunner drives a session in turns. It asks the orchestrator what to do
// next, starts the jobs it names, and asks again whenever a job reports back. Turns
// are plain text, so any model can orchestrate whichever harness it runs on, and the
// orchestrator never has tools that could touch the workspace.
type OrchestratorRunner struct {
	Ask func(ctx context.Context, session store.AutonomousSession, prompt string, progress orchestratorProgress) (string, error)
	// Poll is how often it checks on jobs, and RetryDelay how long it waits after a
	// failed turn. Tests shorten them.
	Poll         time.Duration
	RetryDelay   time.Duration
	SilenceLimit time.Duration
}

type orchestratorJobRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// orchestratorProgress receives the model's words as they arrive: "thought" for
// reasoning the provider shares, and "text" for what the model writes.
type orchestratorProgress func(kind, text string)

// orchestratorDecisionMarker separates what the orchestrator says from its decision.
const orchestratorDecisionMarker = "DECISION"

var (
	orchestratorMarkerLine = regexp.MustCompile(`(?m)^[ \t]*` + orchestratorDecisionMarker + `[ \t:]*\r?$`)
	// orchestratorDecisionStart is where the words stop being for reading, including
	// when a model leaves out the marker.
	orchestratorDecisionStart = regexp.MustCompile("(?m)^[ \t]*(?:" + orchestratorDecisionMarker + "\\b|\\{|```)")
)

// visibleOrchestratorText splits what's been written so far into the part to show
// and the size of the decision after it.
func visibleOrchestratorText(text string) (string, int) {
	if location := orchestratorDecisionStart.FindStringIndex(text); location != nil {
		return strings.TrimSpace(text[:location[0]]), len(text) - location[0]
	}
	// The last line may be the start of the marker, still arriving.
	lastLine := text[strings.LastIndex(text, "\n")+1:]
	if trimmed := strings.TrimSpace(lastLine); trimmed != "" && strings.HasPrefix(orchestratorDecisionMarker, trimmed) {
		text = text[:len(text)-len(lastLine)]
	}
	return strings.TrimSpace(text), 0
}

// turnRecorder collects a turn's words and saves them a few times a second.
type turnRecorder struct {
	lock    sync.Mutex
	notes   []string
	thought strings.Builder
	text    strings.Builder
	saved   time.Time
	heard   time.Time
	save    func(reasoning string, decisionBytes int)
}

// silentFor is how long it's been since the model said anything.
func (r *turnRecorder) silentFor(now time.Time) time.Duration {
	r.lock.Lock()
	defer r.lock.Unlock()
	return now.Sub(r.heard)
}

func (r *turnRecorder) listening() {
	r.lock.Lock()
	r.heard = time.Now()
	r.lock.Unlock()
}

func (r *turnRecorder) add(kind, text string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.heard = time.Now()
	if kind == "thought" {
		r.thought.WriteString(text)
	} else {
		r.text.WriteString(text)
	}
	if time.Since(r.saved) >= orchestratorSaveInterval {
		r.saveNow()
	}
}

// note adds a line of Pearl's own, and starts the attempt's words over.
func (r *turnRecorder) note(text string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.notes = append(r.notes, text)
	r.thought.Reset()
	r.text.Reset()
	r.saveNow()
}

func (r *turnRecorder) flush() {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.saveNow()
}

func (r *turnRecorder) saveNow() {
	r.saved = time.Now()
	visible, decisionBytes := visibleOrchestratorText(r.text.String())
	parts := append([]string(nil), r.notes...)
	if thought := strings.TrimSpace(r.thought.String()); thought != "" {
		parts = append(parts, thought)
	}
	if visible != "" {
		parts = append(parts, visible)
	}
	r.save(strings.Join(parts, "\n\n"), decisionBytes)
}

type orchestratorJobResponse struct {
	JobID    string `json:"job_id"`
	Response string `json:"response"`
}

// orchestratorDecision is what the orchestrator answers each turn.
type orchestratorDecision struct {
	Plan      string                    `json:"plan"`
	Jobs      []orchestratorJobRequest  `json:"jobs"`
	Responses []orchestratorJobResponse `json:"responses"`
	Finish    string                    `json:"finish"`
}

// parseOrchestratorDecision accepts the JSON with or without a Markdown fence or a
// sentence around it, since models add those however they're told.
func parseOrchestratorDecision(reply string) (orchestratorDecision, error) {
	// Anything before the marker is reasoning, which may have braces of its own.
	if markers := orchestratorMarkerLine.FindAllStringIndex(reply, -1); len(markers) > 0 {
		reply = reply[markers[len(markers)-1][1]:]
	}
	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return orchestratorDecision{}, errors.New("your reply had no JSON object")
	}
	var decision orchestratorDecision
	if err := json.Unmarshal([]byte(reply[start:end+1]), &decision); err != nil {
		return orchestratorDecision{}, fmt.Errorf("your reply wasn't valid JSON: %w", err)
	}
	decision.Finish = strings.TrimSpace(decision.Finish)
	return decision, nil
}

func orchestratorModelKey(model store.AutonomousModel) string {
	return model.Provider + "/" + model.ID
}

// findAllowedModel matches the name from the list. A model ID on its own is fine
// too, as long as only one provider has it.
func findAllowedModel(allowed []store.AutonomousModel, name string) (store.AutonomousModel, bool) {
	name = strings.TrimSpace(name)
	var found []store.AutonomousModel
	for _, model := range allowed {
		if orchestratorModelKey(model) == name {
			return model, true
		}
		if model.ID == name {
			found = append(found, model)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return store.AutonomousModel{}, false
}

func jobIsActive(job store.Job) bool {
	return job.Status == store.JobQueued || job.Status == store.JobRunning
}

func (r OrchestratorRunner) Run(
	ctx context.Context,
	session store.AutonomousSession,
	tools AutonomousTools,
) (AutonomousOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, autonomousSessionLimit())
	defer cancel()
	poll := r.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	session = withSelectedModelDefaults(session)
	allowed := session.AllowedModels
	plan := session.Plan
	// reviewed is each job's status when the orchestrator last saw it.
	reviewed := map[string]string{}
	var corrections []string
	correctionCount := 0
	firstTurn := true
	for {
		jobs, err := tools.ListJobs(ctx)
		if err != nil {
			return AutonomousOutcome{}, err
		}
		active, waiting := 0, 0
		reports := false
		for _, job := range jobs {
			switch {
			case jobIsActive(job):
				active++
			case job.Status == store.JobWaitingInput:
				waiting++
			}
			if !jobIsActive(job) && reviewed[job.ID] != job.Status {
				reports = true
			}
		}
		// Only a report from a job gives the orchestrator something new to decide on.
		// Corrections with work still running wait for that too, so a mistake can't
		// turn into a loop of paid turns.
		if !firstTurn && !reports {
			if active == 0 && waiting > 0 && len(corrections) == 0 {
				return AutonomousOutcome{Waiting: true, Summary: plan}, nil
			}
			if active > 0 || len(corrections) == 0 {
				select {
				case <-ctx.Done():
					return AutonomousOutcome{}, orchestratorStopped(ctx)
				case <-time.After(poll):
				}
				continue
			}
		}
		firstTurn = false

		if err := tools.Reviewing(ctx); err != nil {
			return AutonomousOutcome{}, err
		}
		prompt, err := orchestratorPrompt(session, plan, jobs, reviewed, corrections)
		if err != nil {
			return AutonomousOutcome{}, err
		}
		turn, err := tools.TurnStarted(ctx, orchestratorTrigger(jobs, reviewed, corrections))
		if err != nil {
			return AutonomousOutcome{}, err
		}
		recorder := &turnRecorder{save: func(reasoning string, decisionBytes int) {
			tools.TurnProgress(turn, reasoning, decisionBytes)
		}}
		for _, job := range jobs {
			reviewed[job.ID] = job.Status
		}
		reply, err := r.ask(ctx, session, prompt, recorder)
		recorder.flush()
		if err != nil {
			if ctx.Err() != nil {
				err = orchestratorStopped(ctx)
				if errors.Is(err, context.Canceled) {
					tools.TurnFinished(turn, "Stopped")
				} else {
					tools.TurnFinished(turn, "Failed: "+err.Error())
				}
				return AutonomousOutcome{}, err
			}
			tools.TurnFinished(turn, "Failed: "+err.Error())
			return AutonomousOutcome{}, fmt.Errorf("the orchestrator failed: %w", err)
		}
		corrections = nil
		decision, parseErr := parseOrchestratorDecision(reply)
		if parseErr != nil {
			corrections = append(corrections, parseErr.Error()+". Answer with only the JSON object.")
		}
		if decision.Plan = strings.TrimSpace(decision.Plan); decision.Plan != "" {
			plan = decision.Plan
			if err := tools.SavePlan(ctx, plan); err != nil {
				tools.TurnFinished(turn, "Failed: "+err.Error())
				return AutonomousOutcome{}, err
			}
		}

		answered := 0
		responseJobs := make(map[string]bool)
		for _, response := range decision.Responses {
			id := strings.TrimSpace(response.JobID)
			answer := strings.TrimSpace(response.Response)
			var target store.Job
			for _, job := range jobs {
				if job.ID == id {
					target = job
					break
				}
			}
			var responseErr error
			switch {
			case target.ID == "" || target.Status != store.JobWaitingInput:
				responseErr = fmt.Errorf("job %q was not waiting for input in your report", id)
			case responseJobs[id]:
				responseErr = fmt.Errorf("job %q already received a response this turn", id)
			case answer == "":
				responseErr = errors.New("the response was empty")
			case len(answer) > autonomousPromptLimit:
				responseErr = fmt.Errorf("the response exceeds %d bytes", autonomousPromptLimit)
			default:
				responseErr = tools.RespondToJob(ctx, target, answer)
			}
			if responseErr != nil {
				corrections = append(corrections, "A response was not sent: "+responseErr.Error()+".")
				continue
			}
			responseJobs[id] = true
			answered++
			// A fast job may ask another question before the next poll. Its new
			// waiting_input state must count as a fresh report even then.
			reviewed[id] = store.JobQueued
		}

		created := 0
		for _, request := range decision.Jobs {
			assignment := strings.TrimSpace(request.Prompt)
			model, known := findAllowedModel(allowed, request.Model)
			switch {
			case !known:
				corrections = append(corrections, fmt.Sprintf("A job was not started: model %q isn't on the allowed list.", request.Model))
			case assignment == "":
				corrections = append(corrections, "A job was not started: its prompt was empty.")
			case len(assignment) > autonomousPromptLimit:
				corrections = append(corrections, fmt.Sprintf("A job was not started: its prompt exceeds %d bytes.", autonomousPromptLimit))
			case len(jobs)+created >= autonomousJobLimit:
				corrections = append(corrections, fmt.Sprintf("A job was not started: this session reached its limit of %d jobs. Finish with what's done.", autonomousJobLimit))
			default:
				if _, err := tools.CreateJob(ctx, assignment, model); err != nil {
					tools.TurnFinished(turn, "Failed: "+err.Error())
					return AutonomousOutcome{}, err
				}
				created++
			}
		}

		// Look again: jobs may have reported while the orchestrator was thinking.
		jobs, err = tools.ListJobs(ctx)
		if err != nil {
			tools.TurnFinished(turn, "Failed: "+err.Error())
			return AutonomousOutcome{}, err
		}
		active, waiting = 0, 0
		unread := false
		for _, job := range jobs {
			switch {
			case jobIsActive(job):
				active++
			case job.Status == store.JobWaitingInput:
				waiting++
			}
			if !jobIsActive(job) && reviewed[job.ID] != job.Status {
				unread = true
			}
		}

		if decision.Finish != "" {
			switch {
			case len(decision.Finish) > autonomousPromptLimit:
				corrections = append(corrections, fmt.Sprintf("You can't finish yet: the summary exceeds %d bytes.", autonomousPromptLimit))
			case len(jobs) == 0:
				corrections = append(corrections, "You can't finish yet: no job has done any work.")
			case active > 0 || created > 0 || answered > 0:
				corrections = append(corrections, "You can't finish yet: jobs are still running.")
			case waiting > 0:
				corrections = append(corrections, "You can't finish yet: a job is waiting for the user's answer.")
			case unread:
				corrections = append(corrections, "You can't finish yet: a job just reported back.")
			default:
				tools.TurnFinished(turn, "Finished the session")
				return AutonomousOutcome{Finished: true, Summary: decision.Finish}, nil
			}
		}
		tools.TurnFinished(turn, orchestratorTurnOutcome(parseErr, decision, created, answered, active, waiting))
		if active == 0 && waiting == 0 && created == 0 && !unread && len(corrections) == 0 {
			corrections = append(corrections, "No jobs are running and you started none. Start the next jobs, or finish if the goal is met.")
		}
		switch {
		case created > 0 || answered > 0 || active > 0 && parseErr == nil:
			correctionCount = 0
		case len(corrections) > 0:
			correctionCount++
			if correctionCount >= orchestratorMaxCorrections {
				return AutonomousOutcome{}, fmt.Errorf("the orchestrator stopped making progress: %s", corrections[0])
			}
		}
		if active > 0 {
			if err := tools.Working(ctx); err != nil {
				return AutonomousOutcome{}, err
			}
		}
	}
}

// withSelectedModelDefaults covers sessions started before these choices existed,
// which pick up again using the selected model.
func withSelectedModelDefaults(session store.AutonomousSession) store.AutonomousSession {
	if session.Orchestrator.ID != "" && len(session.AllowedModels) > 0 {
		return session
	}
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		return session
	}
	selected := store.AutonomousModel{
		Provider: openrouter_request.AgentProvider(settings),
		ID:       openrouter_request.AgentModel(settings),
	}
	if session.Orchestrator.ID == "" {
		session.Orchestrator = selected
	}
	if len(session.AllowedModels) == 0 {
		session.AllowedModels = []store.AutonomousModel{selected}
	}
	return session
}

func jobCountText(count int) string {
	if count == 1 {
		return "1 job"
	}
	return fmt.Sprintf("%d jobs", count)
}

// orchestratorTrigger says why the orchestrator is being asked now.
func orchestratorTrigger(jobs []store.Job, reviewed map[string]string, corrections []string) string {
	var reports []string
	for _, job := range jobs {
		if !jobIsActive(job) && reviewed[job.ID] != job.Status {
			reports = append(reports, job.ID+" "+job.Status)
		}
	}
	switch {
	case len(reports) > 3:
		return fmt.Sprintf("%d jobs reported back", len(reports))
	case len(reports) > 0:
		return strings.Join(reports, ", ")
	case len(corrections) > 0:
		return "Fixing its last answer"
	case len(jobs) > 0:
		return "Picking the session back up"
	}
	return "Session started"
}

// orchestratorTurnOutcome sums up what a turn came to.
func orchestratorTurnOutcome(parseErr error, decision orchestratorDecision, created, answered, active, waiting int) string {
	if parseErr == nil && answered > 0 {
		outcome := fmt.Sprintf("Answered %d questions", answered)
		if answered == 1 {
			outcome = "Answered 1 question"
		}
		if created > 0 {
			outcome += "; started " + jobCountText(created)
		}
		return outcome
	}
	switch {
	case parseErr != nil:
		return "Pearl couldn't read its answer"
	case created > 0:
		return "Started " + jobCountText(created)
	case decision.Finish != "":
		return "Tried to finish too early"
	case len(decision.Jobs) > 0:
		return "Its jobs were rejected"
	case len(decision.Responses) > 0:
		return "Its responses were rejected"
	case active > 0:
		return "Waiting on " + jobCountText(active)
	case waiting > 0:
		return "Waiting for your answer"
	}
	return "Didn't choose a next step"
}

func orchestratorStopped(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("the session reached its time limit")
	}
	return ctx.Err()
}

// ask retries, because one dropped connection shouldn't end a session of finished
// work.
func (r OrchestratorRunner) ask(ctx context.Context, session store.AutonomousSession, prompt string, recorder *turnRecorder) (string, error) {
	delay := r.RetryDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt < orchestratorAskAttempts; attempt++ {
		if attempt > 0 {
			recorder.note(fmt.Sprintf("Attempt %d failed: %v. Trying again in %s.", attempt, lastErr, delay.Round(time.Second)))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
			delay *= 3
		}
		reply, err := r.askOnce(ctx, session, prompt, recorder)
		if err == nil {
			return reply, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lastErr = err
	}
	return "", lastErr
}

// askOnce makes one attempt, and gives up if the model goes silent.
func (r OrchestratorRunner) askOnce(ctx context.Context, session store.AutonomousSession, prompt string, recorder *turnRecorder) (string, error) {
	limit := r.SilenceLimit
	if limit <= 0 {
		limit = orchestratorSilenceLimit
	}
	turn, cancel := context.WithTimeout(ctx, orchestratorAskTimeout)
	defer cancel()
	recorder.listening()
	silent := make(chan struct{})
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		ticker := time.NewTicker(min(limit, time.Second))
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-turn.Done():
				return
			case now := <-ticker.C:
				if recorder.silentFor(now) >= limit {
					close(silent)
					cancel()
					return
				}
			}
		}
	}()
	reply, err := r.Ask(turn, session, prompt, recorder.add)
	if err != nil {
		select {
		case <-silent:
			return "", fmt.Errorf("the model said nothing for %s", limit.Round(time.Second))
		default:
		}
	}
	return reply, err
}

// orchestratorJobReport is one job as the orchestrator sees it.
type orchestratorJobReport struct {
	ID       string               `json:"id"`
	Model    string               `json:"model,omitempty"`
	Status   string               `json:"status"`
	New      bool                 `json:"new_report,omitempty"`
	Tests    string               `json:"tests,omitempty"`
	Command  string               `json:"test_command,omitempty"`
	Prompt   string               `json:"prompt"`
	Result   string               `json:"result,omitempty"`
	Error    string               `json:"error,omitempty"`
	Question string               `json:"waiting_for_user,omitempty"`
	Requests []subagentJobRequest `json:"job_requests,omitempty"`
}

// shortenMiddle keeps both ends, since a report's conclusion is usually at the end.
func shortenMiddle(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	keep := limit / 2
	return text[:keep] + "\n[... cut ...]\n" + text[len(text)-keep:]
}

func orchestratorPrompt(
	session store.AutonomousSession,
	plan string,
	jobs []store.Job,
	reviewed map[string]string,
	corrections []string,
) (string, error) {
	reports := make([]orchestratorJobReport, 0, len(jobs))
	running := 0
	// Newest first, so that if many are new at once, as after a restart, the size
	// limit shortens the oldest.
	fullReports := orchestratorFullReports
	limits := make(map[string]int, len(jobs))
	for _, job := range slices.Backward(jobs) {
		if !jobIsActive(job) && reviewed[job.ID] != job.Status && fullReports > 0 {
			limits[job.ID] = orchestratorNewResultBytes
			fullReports--
		}
	}
	for _, job := range jobs {
		if jobIsActive(job) {
			running++
		}
		isNew := !jobIsActive(job) && reviewed[job.ID] != job.Status
		resultLimit, promptLimit := orchestratorOldResultBytes, orchestratorOldPromptBytes
		if limit := limits[job.ID]; limit > 0 {
			resultLimit, promptLimit = limit, limit
		}
		report := orchestratorJobReport{
			ID: job.ID, Status: job.Status, New: isNew,
			Prompt:   shortenMiddle(job.Prompt, promptLimit),
			Result:   shortenMiddle(job.Result, resultLimit),
			Error:    shortenMiddle(job.Error, resultLimit),
			Question: job.Question, Command: job.VerificationCommand,
			Requests: readSubagentJobRequests(job.Result, resultLimit),
		}
		if job.AssignedProvider != "" {
			report.Model = job.AssignedProvider + "/" + job.AssignedModel
		}
		switch job.Verification {
		case "passed", "failed", "running":
			report.Tests = job.Verification
		default:
			if job.Terminal() {
				report.Tests = "not run"
			}
		}
		reports = append(reports, report)
	}
	jobsJSON, err := json.MarshalIndent(reports, "", " ")
	if err != nil {
		return "", err
	}
	var prompt strings.Builder
	prompt.WriteString(orchestratorInstructions)
	fmt.Fprintf(&prompt, "\n\nGoal:\n%s\n\nFolder the jobs work in:\n%s\n", session.Goal, session.WorkspaceRoot)
	prompt.WriteString("\nModels you may assign:\n")
	for _, model := range session.AllowedModels {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		fmt.Fprintf(&prompt, "- %s (%s)\n", orchestratorModelKey(model), name)
	}
	fmt.Fprintf(&prompt, "\nAt most %d jobs run at once, and more wait in a queue. %d are running or queued now, so %d more would start right away.\n",
		session.MaxSubagents, running, max(0, session.MaxSubagents-running))
	if plan != "" {
		fmt.Fprintf(&prompt, "\nYour plan and notes so far:\n%s\n", plan)
	}
	fmt.Fprintf(&prompt, "\nJobs so far (new_report marks what you haven't seen):\n%s\n", jobsJSON)
	if len(corrections) > 0 {
		prompt.WriteString("\nProblems with your last answer:\n")
		for _, correction := range corrections {
			prompt.WriteString("- " + correction + "\n")
		}
	}
	return prompt.String(), nil
}

const orchestratorInstructions = `You are Pearl's orchestrator. You run a team of subagent jobs toward the user's goal. You have no tools and never touch files yourself. Pearl calls you when the session starts and again whenever a job reports back.

The user is watching. First think out loud for them in a few short sentences: what you know now, and why you're choosing this work and these models. Then write a line with only the word DECISION, and after it one JSON object:
{"plan": "your current plan and progress notes, which you get back next time", "jobs": [{"model": "<a model from the list>", "prompt": "the job's full assignment"}], "responses": [{"job_id": "<a waiting job's ID>", "response": "your answer to its question"}], "finish": "a summary of the result, only when the goal is met"}
Leave out "jobs", "responses" or "finish" when you have none. To keep waiting for running jobs or leave a question for the user, answer with just "plan".

How to run the team:
- Start work right away, and use every free slot on work that can safely run at the same time.
- Choose the model for each job yourself. Give the strongest models hard design and debugging, and use faster or cheaper ones for simple or mechanical work. If a model fails because of its sign-in or usage limits, use another one.
- Within the chosen model family and class, always prioritize the newest available version on the allowed list. For example, choose GPT 6.1 Sol over GPT 5.6 Sol when both are listed. Use an older version only if the user explicitly requests it or newer versions are unavailable. The allowed list is the source of truth for model IDs; never invent an ID or choose an older version from habit.
- Jobs can't see this conversation or each other. Each prompt has to stand on its own: the relevant goal, the files involved, what earlier jobs found, and the expected deliverable.
- Jobs share one folder, so never give two running jobs the same files.
- Tell jobs that change code to run the relevant tests and report the results. Each job's "tests" field shows what Pearl saw happen.
- When a job reports, read its result, error and tests. Never trust its status alone. If something failed or is missing, start a new job with the details needed to fix it. If its work reveals more to do, start jobs for that too.
- A report's "job_requests" contains requests from that subagent, each with an "issue" and a "task". Review requests on new reports against the user's goal and existing jobs. You may accept, revise, or decline each request; explain your decision in your visible response and retain it in your plan. For accepted work, choose the model yourself from the allowed list and put a self-contained assignment in your "jobs" array. Requests never create jobs automatically or override your model choice. Do not duplicate work already assigned or reconsider a handled request just because an old report is still listed.
- A job with "waiting_for_user" has paused with a question. You may answer it through "responses" when the user's goal, your plan, or the job reports give you enough information to answer confidently. This resumes that same job; don't start a replacement. Explain your answer in your visible response and retain the relevant decision in your plan. Answering is optional. Leave questions you can't resolve for the user, including missing preferences, credentials, or approvals the user hasn't granted. Keep other work going, and Pearl pauses the session when nothing else can move.
- Use "finish" only when no job is running or waiting and the combined work meets the goal.`

// askOrchestrator runs one turn on the model the user chose, with no tools.
func askOrchestrator(ctx context.Context, session store.AutonomousSession, prompt string, progress orchestratorProgress) (string, error) {
	switch session.Orchestrator.Provider {
	case openrouter_request.ProviderClaude:
		return askClaude(ctx, session.Orchestrator.ID, session.WorkspaceRoot, prompt, progress)
	case openrouter_request.ProviderAntigravity:
		return askAntigravity(ctx, session.Orchestrator.ID, prompt, progress)
	}
	return openrouter_request.Run(ctx, prompt, openrouter_request.RunOptions{
		Provider:      session.Orchestrator.Provider,
		Model:         session.Orchestrator.ID,
		WorkspaceRoot: session.WorkspaceRoot,
		MaxDuration:   orchestratorAskTimeout,
		// No tools: the answer comes back as text.
		OnlyTools: true,
		Events: func(event openrouter_request.AgentEvent) error {
			switch event.Type {
			case "reasoning":
				progress("thought", event.Data)
			case "answer":
				progress("text", event.Data)
			}
			return nil
		},
	})
}

// askClaude asks the installed Claude Code one question with every tool turned off,
// passing on its words as they arrive.
func askClaude(ctx context.Context, model, workspace, prompt string, progress orchestratorProgress) (string, error) {
	path, err := FindClaude()
	if err != nil {
		return "", err
	}
	arguments := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", "", "--strict-mcp-config", "--no-session-persistence",
	}
	if model = strings.TrimSpace(model); model != "" {
		arguments = append(arguments, "--model", model)
	}
	command := exec.CommandContext(ctx, path, arguments...)
	command.Dir = workspace
	command.Env = ClaudeEnvironment()
	command.Stdin = strings.NewReader(prompt)
	command.WaitDelay = 5 * time.Second
	var stderr codexTailBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("connect to Claude Code: %w", err)
	}
	stop, err := openrouter_request.StartProcessGroup(command)
	if err != nil {
		return "", fmt.Errorf("start Claude Code: %w", err)
	}
	defer stop()
	result := readClaudeStream(stdout, progress)
	waitErr := command.Wait()
	return parseClaudeAnswer(result, stderr.String(), waitErr)
}

// readClaudeStream passes on the words and returns the final result message.
func readClaudeStream(output io.Reader, progress orchestratorProgress) []byte {
	reader := bufio.NewReaderSize(output, 64<<10)
	var result []byte
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var message struct {
				Type  string `json:"type"`
				Event struct {
					Delta struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						Thinking string `json:"thinking"`
					} `json:"delta"`
				} `json:"event"`
			}
			if json.Unmarshal(trimmed, &message) == nil {
				switch message.Type {
				case "result":
					result = trimmed
				case "stream_event":
					switch message.Event.Delta.Type {
					case "thinking_delta":
						progress("thought", message.Event.Delta.Thinking)
					case "text_delta":
						progress("text", message.Event.Delta.Text)
					}
				}
			}
		}
		if err != nil {
			return result
		}
	}
}

func parseClaudeAnswer(stdout []byte, stderr string, waitErr error) (string, error) {
	var answer struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &answer); err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" && waitErr != nil {
			detail = waitErr.Error()
		}
		if detail == "" {
			detail = "it didn't give an answer"
		}
		return "", errors.New("Claude Code failed: " + detail)
	}
	if answer.IsError || answer.Subtype != "" && answer.Subtype != "success" {
		detail := strings.TrimSpace(answer.Result)
		if detail == "" {
			detail = answer.Subtype
		}
		return "", errors.New("Claude Code failed: " + detail)
	}
	return answer.Result, nil
}

// antigravityTurn is a conversation that refuses every tool and keeps only what
// the model says.
type antigravityTurn struct {
	lock     sync.Mutex
	message  strings.Builder
	progress orchestratorProgress
}

func (t *antigravityTurn) attach(*acpClient) {}

func (t *antigravityTurn) request(method string, _ json.RawMessage) (any, *acpError) {
	if method == "session/request_permission" {
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	}
	return nil, &acpError{Code: -32601, Message: "the orchestrator has no tools"}
}

func (t *antigravityTurn) notification(method string, params json.RawMessage) error {
	if method != "session/update" {
		return nil
	}
	var envelope struct {
		Update struct {
			Kind    string          `json:"sessionUpdate"`
			Content json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &envelope) != nil {
		return nil
	}
	var chunk struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(envelope.Update.Content, &chunk) != nil {
		return nil
	}
	switch envelope.Update.Kind {
	case "agent_thought_chunk":
		t.progress("thought", chunk.Text)
	case "agent_message_chunk":
		t.lock.Lock()
		t.message.WriteString(chunk.Text)
		t.lock.Unlock()
		t.progress("text", chunk.Text)
	}
	return nil
}

// askAntigravity asks the installed Antigravity server one question. The server
// runs in a sandbox around a scratch folder, so it can't reach the project even if
// it tries to use a tool.
func askAntigravity(ctx context.Context, model, prompt string, progress orchestratorProgress) (string, error) {
	install, err := FindAntigravity()
	if err != nil {
		return "", err
	}
	if !AntigravitySignedIn() {
		return "", ErrAntigravitySignIn
	}
	profile, err := prepareAntigravityProfile()
	if err != nil {
		return "", err
	}
	scratch := filepath.Join(profile, "tmp")
	permissions, err := openrouter_request.LoadCommandPermissions()
	if err != nil {
		return "", err
	}
	program, arguments, err := openrouter_request.SandboxedTerminalProcess(
		scratch, []string{profile}, true, install.Server, antigravityServerArguments(), scratch,
	)
	if err != nil {
		if !errors.Is(err, openrouter_request.ErrNoSandbox) || !permissions.AllowUnsandboxed {
			return "", err
		}
		program, arguments = install.Server, antigravityServerArguments()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, program, arguments...)
	command.Dir = scratch
	command.Env = antigravityEnvironment(install, profile, false)
	turn := &antigravityTurn{progress: progress}
	var signedOut sync.Once
	needsSignIn := false
	client, stop, err := startAntigravity(command, turn, func(line string) {
		if strings.HasPrefix(line, antigravityAuthLinkPrefix) {
			// The server is now waiting for a browser nobody is watching.
			signedOut.Do(func() {
				needsSignIn = true
				cancel()
			})
		}
	})
	if err != nil {
		return "", err
	}
	defer func() {
		stop()
		_ = client.wait()
	}()
	var setup acpSessionSetup
	err = client.initialize(ctx)
	if err == nil {
		err = client.call(ctx, "authenticate", map[string]any{"methodId": antigravityAuthMethod}, nil)
	}
	if err == nil {
		err = client.call(ctx, "session/new", map[string]any{"cwd": scratch, "mcpServers": []any{}}, &setup)
	}
	if err == nil {
		if model = strings.TrimSpace(model); model != "" {
			err = client.call(ctx, "session/set_config_option", map[string]any{"sessionId": setup.SessionID, "configId": "model", "value": model}, nil)
		}
	}
	if err == nil {
		err = client.call(ctx, "session/prompt", map[string]any{
			"sessionId": setup.SessionID,
			"prompt":    []map[string]string{{"type": "text", "text": prompt}},
		}, nil)
	}
	stop()
	_ = client.wait()
	if needsSignIn {
		return "", ErrAntigravitySignIn
	}
	if err != nil {
		return "", fmt.Errorf("Antigravity failed: %w", err)
	}
	turn.lock.Lock()
	defer turn.lock.Unlock()
	return turn.message.String(), nil
}
