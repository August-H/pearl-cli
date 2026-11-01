package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/internal/workspacepath"
	"github.com/August-H/pearl-cli/openrouter_request"
)

// externalRun is the setup shared by harnesses that drive a CLI the user
// installed and signed in to themselves.
type externalRun struct {
	ctx       context.Context
	settings  openrouter_request.Settings
	sessionID string
	answer    string
	prompt    string
	parser    *codexEventParser
}

func startExternalRun(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
	sessionEvent string,
) (*externalRun, context.CancelFunc, error) {
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		return nil, nil, err
	}
	if err := openrouter_request.ValidateApprovedWorkspace(job.WorkspaceRoot, settings.Approved_workspace_roots); err != nil {
		return nil, nil, err
	}
	if err := workspacepath.CheckJobDirectory(job.WorkspaceRoot); err != nil {
		return nil, nil, err
	}
	maxDuration := time.Duration(settings.Max_job_seconds) * time.Second
	if maxDuration <= 0 {
		maxDuration = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, maxDuration)

	sessionID, answer, err := codexResumeState(ctx, job, state, sessionEvent)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	prompt := resumePrompt(job, sessionID, answer)
	var transcript []codexTranscriptMessage
	if sessionID != "" {
		if saved, err := state.LoadTranscript(ctx, job.ID); err == nil && len(saved) > 0 {
			_ = json.Unmarshal(saved, &transcript)
		}
	}
	var eventLock sync.Mutex
	lockedEvents := func(event openrouter_request.AgentEvent) error {
		if events == nil {
			return nil
		}
		eventLock.Lock()
		defer eventLock.Unlock()
		return events(event)
	}
	if answer != "" {
		if err := lockedEvents(openrouter_request.AgentEvent{Type: codexInputUsedEvent, Data: job.InputToolCallID}); err != nil {
			cancel()
			return nil, nil, fmt.Errorf("record answer: %w", err)
		}
	}
	return &externalRun{
		ctx: ctx, settings: settings, sessionID: sessionID, answer: answer, prompt: prompt,
		parser: &codexEventParser{
			ctx: ctx, job: job, events: lockedEvents, state: state,
			transcript: append(transcript, codexTranscriptMessage{Role: "user", Content: prompt}),
			idPrefix:   fmt.Sprintf("%s-%d-", job.Harness, time.Now().UnixNano()),
		},
	}, cancel, nil
}

func (r *externalRun) saveTranscript(state openrouter_request.CheckpointStore) {
	saved, err := json.Marshal(r.parser.transcript)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = state.SaveTranscript(ctx, r.parser.job.ID, saved)
}

// result turns the finished run into what the worker expects, including a
// question for the user when the agent ended its turn with one.
func (r *externalRun) result() (string, error) {
	if question := r.parser.question; question != "" {
		return "", &openrouter_request.UserInputRequiredError{
			ToolCallID: fmt.Sprintf("%s%d", codexQuestionIDPrefix, time.Now().UnixNano()),
			Question:   question,
		}
	}
	return r.parser.lastAnswer, nil
}

// findInstalledCLI locates a CLI the user installed. The daemon may run with a
// minimal PATH, so common install locations are checked as well.
func findInstalledCLI(pathEnvironment, name string) (string, bool) {
	if path := strings.TrimSpace(os.Getenv(pathEnvironment)); path != "" {
		return path, true
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, true
	}
	candidates := []string{filepath.Join("/opt/homebrew/bin", name), filepath.Join("/usr/local/bin", name)}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append([]string{
			filepath.Join(home, ".local", "bin", name),
			filepath.Join(home, ".npm-global", "bin", name),
			filepath.Join(home, ".bun", "bin", name),
		}, candidates...)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// environmentWithout copies the environment minus the named variables. Names
// compare without regard to case because Windows treats them that way.
func environmentWithout(names ...string) []string {
	removed := make(map[string]bool, len(names))
	for _, name := range names {
		removed[strings.ToUpper(name)] = true
	}
	var kept []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !removed[strings.ToUpper(name)] {
			kept = append(kept, value)
		}
	}
	return kept
}

// agentPath makes a path from a tool call absolute against the workspace.
func agentPath(workspace, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workspace, path)
}

// agentMayRead allows reads anywhere except credentials and Pearl's own files.
// Codex jobs get the same rule. The user's skills live in Pearl's config folder
// and stay readable because jobs are told to open them.
func agentMayRead(workspace, path string) error {
	path = agentPath(workspace, path)
	if directory, err := openrouter_request.SkillsDirectory(); err == nil && workspacepath.ContainsPath(directory, path) {
		return nil
	}
	if workspacepath.Denied(path) {
		return fmt.Errorf("Pearl doesn't let agents read %s because it may hold credentials", path)
	}
	return nil
}

// agentMayWrite allows writes only inside the job's workspace.
func agentMayWrite(workspace, path string) error {
	path = agentPath(workspace, path)
	if workspacepath.Denied(path) {
		return fmt.Errorf("Pearl doesn't let agents change %s because it may hold credentials", path)
	}
	if !workspacepath.ContainsPath(workspace, path) {
		return errors.New("Pearl only lets this job change files inside its workspace: " + workspace)
	}
	return nil
}
