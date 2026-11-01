package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

// writeFakeClaude writes a CLI that records how it was started, prints events,
// then, for each control request, reads Pearl's answer from stdin.
func writeFakeClaude(t *testing.T, lines []string) (script, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Claude Code script needs a POSIX shell")
	}
	directory := t.TempDir()
	record = filepath.Join(directory, "record")
	body := "#!/bin/sh\n" +
		`printf '%s\n' "$@" > "` + record + `.args"` + "\n" +
		`printf '%s' "$CLAUDECODE" > "` + record + `.marker"` + "\n" +
		`pwd > "` + record + `.cwd"` + "\n" +
		`IFS= read -r first; printf '%s\n' "$first" > "` + record + `.stdin"` + "\n"
	for _, line := range lines {
		body += "cat <<'PEARL_EVENT'\n" + line + "\nPEARL_EVENT\n"
		if strings.Contains(line, `"control_request"`) {
			body += `IFS= read -r answer; printf '%s\n' "$answer" >> "` + record + `.answers"` + "\n"
		}
	}
	script = filepath.Join(directory, "claude")
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, record
}

func openClaudeJob(t *testing.T, harness string) (*store.Store, store.Job) {
	t.Helper()
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreatePendingJob(context.Background(), "", "do the task", workspace, harness)
	if err != nil {
		t.Fatal(err)
	}
	return state, job
}

func TestClaudeRunnerStreamsAJobAndAnswersPermissionRequests(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"openrouter/saved","claude_model":"claude-test-model","max_depth":5}`)
	t.Setenv("CLAUDECODE", "1")
	state, job := openClaudeJob(t, store.HarnessClaude)
	inside := filepath.Join(job.WorkspaceRoot, "new.go")
	outside := filepath.Join(t.TempDir(), "outside.go")
	script, record := writeFakeClaude(t, []string{
		`{"type":"system","subtype":"init","session_id":"session-1","model":"claude-test-model"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"planning"},{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}`,
		`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Write","input":{"file_path":"` + inside + `","content":"x"}}}`,
		`{"type":"control_request","request_id":"r2","request":{"subtype":"can_use_tool","tool_name":"Write","input":{"file_path":"` + outside + `","content":"x"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-2","name":"Write","input":{"file_path":"` + inside + `"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-2","content":[{"type":"text","text":"created"}]}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tool-9","message":{"content":[{"type":"text","text":"subagent note"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"All done"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"All done","session_id":"session-1"}`,
	})
	var events []openrouter_request.AgentEvent
	sink := func(event openrouter_request.AgentEvent) error {
		events = append(events, event)
		return nil
	}
	answer, err := (ClaudeRunner{Executable: script}).Run(context.Background(), job, sink, state)
	if err != nil || answer != "All done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}

	args := readCodexRecord(t, record, ".args")
	for _, want := range []string{"stream-json", "--permission-prompt-tool", "stdio", "claude-test-model", `"enabled":true`, "denyRead", "PEARL_QUESTION"} {
		if !strings.Contains(args, want) {
			t.Fatalf("arguments are missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "skip-permissions") || strings.Contains(args, "bypassPermissions") {
		t.Fatalf("permissions were bypassed:\n%s", args)
	}
	if marker := readCodexRecord(t, record, ".marker"); marker != "" {
		t.Fatalf("Claude Code would see that it is nested: %q", marker)
	}
	if !strings.Contains(readCodexRecord(t, record, ".stdin"), "do the task") {
		t.Fatalf("prompt = %s", readCodexRecord(t, record, ".stdin"))
	}

	var behaviors []string
	for _, line := range strings.Split(strings.TrimSpace(readCodexRecord(t, record, ".answers")), "\n") {
		var reply struct {
			Response struct {
				Response struct {
					Behavior string `json:"behavior"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatal(err)
		}
		behaviors = append(behaviors, reply.Response.Response.Behavior)
	}
	if strings.Join(behaviors, ",") != "allow,deny" {
		t.Fatalf("permission answers = %v", behaviors)
	}

	seen := map[string]string{}
	for _, event := range events {
		seen[event.Type] += event.Data + "\n"
	}
	if !strings.Contains(seen["tool"], "shell: go test ./...") || !strings.Contains(seen["tool"], "edit: new.go") {
		t.Fatalf("tool events = %q", seen["tool"])
	}
	if strings.Contains(seen["answer"], "subagent note") {
		t.Fatalf("subagent text reached the answer: %q", seen["answer"])
	}
	if !strings.Contains(seen["verification"], "passed") || seen[claudeSessionEvent] != "session-1\n" || seen["model"] != "claude-test-model\n" {
		t.Fatalf("events = %+v", seen)
	}
	tools, err := state.ListToolExecutions(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].ToolName != "run_command" || tools[1].ToolName != "create_file" {
		t.Fatalf("tool executions = %+v", tools)
	}
}

func TestClaudeRunnerResumesItsSessionAfterAQuestion(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"m","max_depth":5}`)
	state, job := openClaudeJob(t, store.HarnessClaude)
	script, record := writeFakeClaude(t, []string{
		`{"type":"system","subtype":"init","session_id":"session-7"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"PEARL_QUESTION: Which folder?"}]}}`,
		`{"type":"result","subtype":"success","result":"PEARL_QUESTION: Which folder?"}`,
	})
	sink := func(event openrouter_request.AgentEvent) error {
		_, err := state.AppendEvent(context.Background(), job.ID, event.Type, event.Data)
		return err
	}
	_, err := (ClaudeRunner{Executable: script}).Run(context.Background(), job, sink, state)
	inputRequired, ok := err.(*openrouter_request.UserInputRequiredError)
	if !ok || inputRequired.Question != "Which folder?" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(readCodexRecord(t, record, ".args"), "--resume") {
		t.Fatal("a first run has nothing to resume")
	}
	// The fake asks the same question again, so only the arguments matter here.
	_, _ = (ClaudeRunner{Executable: script}).Run(context.Background(), job, sink, state)
	if !strings.Contains(readCodexRecord(t, record, ".args"), "--resume\nsession-7") {
		t.Fatalf("second run arguments = %s", readCodexRecord(t, record, ".args"))
	}
}

func TestClaudeRunnerReportsFailures(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"m","max_depth":5}`)
	state, job := openClaudeJob(t, store.HarnessClaude)
	script, _ := writeFakeClaude(t, []string{
		`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`,
	})
	if _, err := (ClaudeRunner{Executable: script}).Run(context.Background(), job, nil, state); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("logged-out error = %v", err)
	}
	script, _ = writeFakeClaude(t, nil)
	if _, err := (ClaudeRunner{Executable: script}).Run(context.Background(), job, nil, state); err == nil || !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("early exit error = %v", err)
	}
}

func TestClaudeToolDecisions(t *testing.T) {
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	skills, err := openrouter_request.SkillsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	none := openrouter_request.CommandPermissions{}
	open := openrouter_request.CommandPermissions{AllowUnsandboxed: true, AllowNetwork: true}
	for _, test := range []struct {
		name        string
		tool        string
		input       map[string]any
		permissions openrouter_request.CommandPermissions
		allowed     bool
	}{
		{"read in workspace", "Read", map[string]any{"file_path": "a.go"}, none, true},
		{"read elsewhere", "Read", map[string]any{"file_path": filepath.Join(other, "a.go")}, none, true},
		{"read Pearl's config", "Read", map[string]any{"file_path": filepath.Join(config, ".env")}, none, false},
		{"grep Pearl's config", "Grep", map[string]any{"path": config}, none, false},
		{"read a skill", "Read", map[string]any{"file_path": filepath.Join(skills, "ship.md")}, none, true},
		{"edit in workspace", "Edit", map[string]any{"file_path": filepath.Join(workspace, "a.go")}, none, true},
		{"relative new file", "Write", map[string]any{"file_path": "sub/new.go"}, none, true},
		{"write elsewhere", "Write", map[string]any{"file_path": filepath.Join(other, "a.go")}, open, false},
		{"parent escape", "Write", map[string]any{"file_path": "../a.go"}, open, false},
		{"write Pearl's config", "Write", map[string]any{"file_path": filepath.Join(config, "settings.json")}, open, false},
		{"edit without a path", "Edit", map[string]any{}, open, false},
		{"unsandboxed command", "Bash", map[string]any{"command": "ls"}, none, false},
		{"unsandboxed command opted in", "Bash", map[string]any{"command": "ls"}, open, true},
		{"network off", "SandboxNetworkAccess", map[string]any{"host": "example.com"}, none, false},
		{"network on", "SandboxNetworkAccess", map[string]any{"host": "example.com"}, open, true},
		{"fetch off", "WebFetch", map[string]any{"url": "https://example.com"}, none, false},
		{"todo list", "TodoWrite", map[string]any{}, none, true},
		{"question", "AskUserQuestion", map[string]any{}, open, false},
		{"mcp tool", "mcp__server__tool", map[string]any{}, open, false},
		{"unknown tool", "SomethingNew", map[string]any{}, open, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := claudeToolDecision(test.tool, test.input, workspace, test.permissions)
			if (err == nil) != test.allowed {
				t.Fatalf("decision = %v, want allowed=%v", err, test.allowed)
			}
		})
	}
	if err := claudeToolDecision("AskUserQuestion", nil, workspace, none); err == nil || !strings.Contains(err.Error(), codexQuestionMarker) {
		t.Fatalf("question refusal should explain Pearl's marker: %v", err)
	}
}

func TestClaudeUsesItsOwnBillingLabel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	settings := openrouter_request.Settings{Model: "openrouter/saved", ClaudeModel: " claude-x ", AntigravityModel: "gemini-x"}
	if provider, model := harnessBilling(store.HarnessClaude, settings); provider != "claude" || model != "claude-x" {
		t.Fatalf("claude = %s %s", provider, model)
	}
	if provider, model := harnessBilling(store.HarnessAntigravity, settings); provider != "antigravity" || model != "gemini-x" {
		t.Fatalf("antigravity = %s %s", provider, model)
	}
	if provider, model := harnessBilling(store.HarnessCodex, settings); provider != "openrouter" || model != "openrouter/saved" {
		t.Fatalf("codex = %s %s", provider, model)
	}
	t.Setenv("ANTHROPIC_API_KEY", "key")
	if provider, _ := harnessBilling(store.HarnessClaude, settings); provider != BillingAPI {
		t.Fatalf("claude with an API key = %s", provider)
	}
	if provider, _ := harnessBilling(store.HarnessAntigravity, settings); provider != "antigravity" {
		t.Fatalf("an Anthropic key changed Antigravity's billing: %s", provider)
	}
}

func TestClaudeEnvironmentIgnoresEndpointAndModelOverrides(t *testing.T) {
	for _, name := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME", "ANTHROPIC_CUSTOM_MODEL_OPTION",
		"CLAUDECODE",
	} {
		t.Setenv(name, "gpt-5.6-sol")
	}
	t.Setenv("ANTHROPIC_API_KEY", "kept")
	t.Setenv("PEARL_TEST_VALUE", "kept")
	kept := map[string]string{}
	for _, value := range ClaudeEnvironment() {
		name, setting, _ := strings.Cut(value, "=")
		kept[name] = setting
		if setting == "gpt-5.6-sol" {
			t.Errorf("%s reached Claude Code", name)
		}
	}
	if kept["ANTHROPIC_API_KEY"] != "kept" || kept["PEARL_TEST_VALUE"] != "kept" {
		t.Errorf("removed too much: %v", kept)
	}
}
