package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

func writeCodexTestConfig(t *testing.T, settings string) {
	t.Helper()
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	if err := os.WriteFile(filepath.Join(configDirectory, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, ".env"), []byte("OPENROUTER_API_KEY=test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeFakeCodex writes a script that records how it was called and prints
// the given JSONL events, then exits with exitCode.
func writeFakeCodex(t *testing.T, events []string, exitCode int) (script, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Codex script needs a POSIX shell")
	}
	directory := t.TempDir()
	record = filepath.Join(directory, "record")
	eventsFile := filepath.Join(directory, "events.jsonl")
	script = filepath.Join(directory, "codex")
	setFakeCodexEvents(t, script, events)
	contents := `#!/bin/sh
printf '%s\n' "$@" > "` + record + `.args"
printf '%s' "$` + codexAPIKeyEnvironment + `" > "` + record + `.key"
cat > "` + record + `.stdin"
pwd > "` + record + `.cwd"
cat "` + eventsFile + `"
exit ` + string(rune('0'+exitCode)) + `
`
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, record
}

func setFakeCodexEvents(t *testing.T, script string, events []string) {
	t.Helper()
	eventsFile := filepath.Join(filepath.Dir(script), "events.jsonl")
	if err := os.WriteFile(eventsFile, []byte(strings.Join(events, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCodexRecord(t *testing.T, record, suffix string) string {
	t.Helper()
	contents, err := os.ReadFile(record + suffix)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestCodexRunnerUsesOpenRouterAndRecordsTheRun(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"openrouter/free","max_job_seconds":60}`)
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(workspace, "notes.md")
	script, record := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for ` + "`openrouter/free`" + ` not found."}}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"Checking files"}}`,
		`{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"/bin/zsh -lc 'ls -a'","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"/bin/zsh -lc 'ls -a'","aggregated_output":"a.txt\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_3","type":"file_change","changes":[{"path":"` + notes + `","kind":"add"}],"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_4","type":"agent_message","text":"\n\n"}}`,
		`{"type":"item.completed","item":{"id":"item_5","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`,
	}, 0)

	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	job, err := state.CreatePendingJob(ctx, "", "list the files", workspace, store.HarnessCodex)
	if err != nil {
		t.Fatal(err)
	}
	var emitted []string
	events := func(event openrouter_request.AgentEvent) error {
		emitted = append(emitted, event.Type+"="+event.Data)
		_, err := state.AppendEvent(ctx, job.ID, event.Type, event.Data)
		return err
	}

	runner := CodexRunner{Executable: script}
	result, err := runner.Run(ctx, job, events, state)
	if err != nil || result != "done" {
		t.Fatalf("Run() = %q, %v", result, err)
	}
	want := []string{
		"codex_thread=thread-1", "reasoning=Checking files", "tool=shell: ls -a",
		"tool=edit: notes.md", "answer=done",
	}
	if strings.Join(emitted, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q, want %q", emitted, want)
	}

	arguments := strings.Split(strings.TrimSpace(readCodexRecord(t, record, ".args")), "\n")
	joined := strings.Join(arguments, " ")
	for _, expected := range []string{
		"exec --json", `model_provider="openrouter"`,
		`model_providers.openrouter.base_url="https://openrouter.ai/api/v1"`,
		`model_providers.openrouter.env_key="` + codexAPIKeyEnvironment + `"`,
		`sandbox_mode="workspace-write"`, "-m openrouter/free",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Codex arguments %q are missing %q", arguments, expected)
		}
	}
	if arguments[len(arguments)-1] != "-" || strings.Contains(joined, "resume") {
		t.Fatalf("first run arguments = %q", arguments)
	}
	if key := readCodexRecord(t, record, ".key"); key != "test-key" {
		t.Fatalf("Codex API key = %q", key)
	}
	if stdin := readCodexRecord(t, record, ".stdin"); stdin != "list the files"+codexQuestionReminder {
		t.Fatalf("Codex prompt = %q", stdin)
	}
	if !strings.Contains(joined, "developer_instructions=") || !strings.Contains(joined, codexQuestionMarker) {
		t.Fatalf("Codex arguments are missing the question instructions: %q", arguments)
	}
	if cwd := strings.TrimSpace(readCodexRecord(t, record, ".cwd")); cwd != workspace {
		t.Fatalf("Codex ran in %q, want %q", cwd, workspace)
	}

	executions, err := state.ListToolExecutions(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 2 ||
		executions[0].ToolName != "run_command" || executions[0].Arguments != `{"command":"ls -a"}` ||
		executions[1].ToolName != "create_file" || executions[1].Arguments != `{"relative_path":"notes.md"}` {
		t.Fatalf("tool executions = %#v", executions)
	}
	transcript, err := state.LoadTranscript(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var messages []codexTranscriptMessage
	if err := json.Unmarshal(transcript, &messages); err != nil {
		t.Fatal(err)
	}
	last := messages[len(messages)-1]
	if messages[0].Role != "user" || messages[0].Content != "list the files" ||
		last.Role != "assistant" || last.Content != "done" {
		t.Fatalf("transcript = %s", transcript)
	}

	// Retrying resumes the same Codex session and keeps earlier tool records.
	if _, err := runner.Run(ctx, job, events, state); err != nil {
		t.Fatal(err)
	}
	arguments = strings.Split(strings.TrimSpace(readCodexRecord(t, record, ".args")), "\n")
	if arguments[1] != "resume" || arguments[len(arguments)-2] != "thread-1" {
		t.Fatalf("retry arguments = %q", arguments)
	}
	if stdin := readCodexRecord(t, record, ".stdin"); stdin != codexContinuePrompt+codexQuestionReminder {
		t.Fatalf("retry prompt = %q", stdin)
	}
	if executions, err := state.ListToolExecutions(ctx, job.ID); err != nil || len(executions) != 4 {
		t.Fatalf("tool executions after retry = %d, %v", len(executions), err)
	}
}

func TestCodexQuestionsPauseTheJobAndTheAnswerResumesCodex(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"openrouter/free"}`)
	script, record := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thread-q"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Looking around."}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"\n\nPEARL_QUESTION: Which file\nshould I fix?"}}`,
		`{"type":"turn.completed","usage":{}}`,
	}, 0)
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	if _, err := state.CreateJob(ctx, "fix the bug", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim job: found=%v err=%v", found, err)
	}
	var emitted []string
	events := func(event openrouter_request.AgentEvent) error {
		emitted = append(emitted, event.Type+"="+event.Data)
		_, err := state.AppendEvent(ctx, job.ID, event.Type, event.Data)
		return err
	}
	runner := CodexRunner{Executable: script}

	_, err = runner.Run(ctx, job, events, state)
	var inputRequired *openrouter_request.UserInputRequiredError
	if !errors.As(err, &inputRequired) || inputRequired.Question != "Which file\nshould I fix?" ||
		!strings.HasPrefix(inputRequired.ToolCallID, codexQuestionIDPrefix) {
		t.Fatalf("Run() error = %v", err)
	}
	for _, event := range emitted {
		if strings.Contains(event, codexQuestionMarker) {
			t.Fatalf("the question marker leaked into events: %q", emitted)
		}
	}
	if err := state.PauseForInput(ctx, job.ID, inputRequired.ToolCallID, inputRequired.Question); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.RespondToJob(ctx, job.ID, "main.go"); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim answered job: found=%v err=%v", found, err)
	}

	setFakeCodexEvents(t, script, []string{
		`{"type":"thread.started","thread_id":"thread-q"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Fixed main.go."}}`,
		`{"type":"turn.completed","usage":{}}`,
	})
	result, err := runner.Run(ctx, job, events, state)
	if err != nil || result != "Fixed main.go." {
		t.Fatalf("answered Run() = %q, %v", result, err)
	}
	arguments := strings.Split(strings.TrimSpace(readCodexRecord(t, record, ".args")), "\n")
	if arguments[1] != "resume" || arguments[len(arguments)-2] != "thread-q" {
		t.Fatalf("answered run arguments = %q", arguments)
	}
	if stdin := readCodexRecord(t, record, ".stdin"); stdin != "main.go"+codexQuestionReminder {
		t.Fatalf("answered run prompt = %q", stdin)
	}

	// A later retry continues the session instead of resending the answer.
	if _, err := runner.Run(ctx, job, events, state); err != nil {
		t.Fatal(err)
	}
	if stdin := readCodexRecord(t, record, ".stdin"); stdin != codexContinuePrompt+codexQuestionReminder {
		t.Fatalf("retry prompt = %q", stdin)
	}
}

func TestCodexQuestionFindsTheMarker(t *testing.T) {
	for message, want := range map[string]string{
		"PEARL_QUESTION: Which branch?":                    "Which branch?",
		"I looked around.\nPEARL_QUESTION: Use tabs?":      "Use tabs?",
		"  PEARL_QUESTION:\nWhich one?\nA or B":            "Which one?\nA or B",
		"**PEARL_QUESTION: What format?**\n- JSON\n- YAML": "What format?\n- JSON\n- YAML",
		"> **PEARL_QUESTION:** Which port?":                "Which port?",
		"Done. No PEARL_QUESTION: needed here":             "",
		"All finished.":                                    "",
	} {
		if got := codexQuestion(message); got != want {
			t.Fatalf("codexQuestion(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestCodexRunnerReportsAFailedTurn(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"openrouter/free"}`)
	script, _ := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thread-2"}`,
		`{"type":"error","message":"stream disconnected"}`,
		`{"type":"turn.failed","error":{"message":"model not available"}}`,
	}, 1)
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	job, err := state.CreatePendingJob(context.Background(), "", "fail", t.TempDir(), store.HarnessCodex)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CodexRunner{Executable: script}.Run(context.Background(), job, nil, state)
	if err == nil || !strings.Contains(err.Error(), "model not available") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestCodexDisplayCommandRemovesTheShellWrapper(t *testing.T) {
	for input, want := range map[string]string{
		"/bin/zsh -lc ls":                                 "ls",
		"/bin/bash -lc 'git status'":                      "git status",
		`/bin/zsh -lc "go test ./..."`:                    "go test ./...",
		`/bin/zsh -lc 'echo '\''hi'\'''`:                  "echo 'hi'",
		"python3 script.py":                               "python3 script.py",
		"/bin/zsh -lc \"cat > a.json << 'EOF'\n{}\nEOF\"": "cat > a.json << 'EOF'\n{}\nEOF",
	} {
		if got := codexDisplayCommand(input); got != want {
			t.Fatalf("codexDisplayCommand(%q) = %q, want %q", input, got, want)
		}
	}
}

type recordingRunner struct {
	harness *string
}

func (r recordingRunner) Run(
	_ context.Context,
	job store.Job,
	_ openrouter_request.EventSink,
	_ openrouter_request.CheckpointStore,
) (string, error) {
	*r.harness = job.Harness
	return "ok", nil
}

func TestHarnessRunnerUsesTheDefaultHarnessAndRecordsIt(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"openrouter/free","harness":"codex"}`)
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	var pearlRan, codexRan string
	runner := HarnessRunner{
		Pearl: recordingRunner{harness: &pearlRan},
		Codex: recordingRunner{harness: &codexRan},
	}

	defaultJob, err := state.CreatePendingJob(ctx, "", "default", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, defaultJob, nil, state); err != nil {
		t.Fatal(err)
	}
	if codexRan != store.HarnessCodex || pearlRan != "" {
		t.Fatalf("default job ran with pearl=%q codex=%q", pearlRan, codexRan)
	}
	if stored, err := state.GetJob(ctx, defaultJob.ID); err != nil || stored.Harness != store.HarnessCodex {
		t.Fatalf("stored harness = %q, %v", stored.Harness, err)
	}

	pearlJob, err := state.CreatePendingJob(ctx, "", "explicit", t.TempDir(), store.HarnessPearl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, pearlJob, nil, state); err != nil {
		t.Fatal(err)
	}
	if pearlRan != store.HarnessPearl {
		t.Fatalf("explicit Pearl job ran with %q", pearlRan)
	}
}
