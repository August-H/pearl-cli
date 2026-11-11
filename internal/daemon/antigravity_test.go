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

// fakeACPServer answers Pearl's requests in order. It reads one request at a
// time and replies with the same id, after sending anything scripted for that
// method. The server also records what it was asked.
func fakeACPServer(t *testing.T, promptSteps string, sessionNew string) (AntigravityInstall, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ACP server needs a POSIX shell")
	}
	directory := t.TempDir()
	record := filepath.Join(directory, "record")
	script := filepath.Join(directory, "agy_acp_server.par")
	body := `#!/bin/sh
env > "` + record + `.env"
pwd > "` + record + `.cwd"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + record + `.requests"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"authMethods":[{"id":"oauth-personal"}]}}\n' "$id" ;;
    *'"method":"authenticate"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id" ;;
    *'"method":"session/new"'*)
      ` + sessionNew + `
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"session-a","configOptions":[{"id":"mode","category":"mode","currentValue":"yolo"},{"id":"model","category":"model","currentValue":"gemini-default"}]}}\n' "$id" ;;
    *'"method":"session/resume"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"configOptions":[]}}\n' "$id" ;;
    *'"method":"session/set_config_option"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id" ;;
    *'"method":"session/prompt"'*)
      ` + promptSteps + `
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}\n' "$id" ;;
    *)
      printf '%s\n' "$line" >> "` + record + `.other" ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(directory, "localharness_external")
	if err := os.WriteFile(harness, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	return AntigravityInstall{Server: script, Harness: harness}, record
}

func setUpAntigravity(t *testing.T, settings string, signedIn bool) string {
	t.Helper()
	writeCodexTestConfig(t, settings)
	profile := t.TempDir()
	t.Setenv(antigravityHomeEnvironment, profile)
	t.Setenv("GEMINI_API_KEY", "must-not-reach-the-server")
	if signedIn {
		directory := filepath.Join(profile, "antigravity-acp")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, antigravityTokenFile), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return profile
}

func acpUpdate(body string) string {
	return `printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-a","update":` + body + `}}'`
}

func TestAntigravityRunnerRunsAJob(t *testing.T) {
	profile := setUpAntigravity(t, `{"model":"m","antigravity_model":"gemini-chosen","max_depth":5,"allow_unsandboxed_commands":true}`, true)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	steps := strings.Join([]string{
		acpUpdate(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}`),
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Run tests","kind":"execute","status":"in_progress","rawInput":{"CommandLine":"go test ./..."}}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","rawOutput":{"combinedOutput":"ok"}}`),
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"t2","title":"Edit","kind":"edit","status":"completed","locations":[{"path":"` + job.WorkspaceRoot + `/a.go"}]}`),
		acpUpdate(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"All "}}`),
		acpUpdate(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`),
	}, "\n      ")
	install, record := fakeACPServer(t, steps, "")
	seen := map[string]string{}
	sink := func(event openrouter_request.AgentEvent) error {
		seen[event.Type] += event.Data + "\n"
		return nil
	}
	answer, err := (AntigravityRunner{Install: &install}).Run(context.Background(), job, sink, state)
	if err != nil || answer != "All done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	requests := readCodexRecord(t, record, ".requests")
	for _, want := range []string{`"protocolVersion":1`, `"readTextFile":true`, `"methodId":"oauth-personal"`, job.WorkspaceRoot, "PEARL_QUESTION", "do the task"} {
		if !strings.Contains(requests, want) {
			t.Fatalf("requests are missing %q:\n%s", want, requests)
		}
	}
	if !strings.Contains(requests, `"configId":"mode"`) || !strings.Contains(requests, `"value":"default"`) {
		t.Fatalf("a permissive mode was left on:\n%s", requests)
	}
	if !strings.Contains(requests, `"configId":"model"`) || !strings.Contains(requests, "gemini-chosen") {
		t.Fatalf("the model wasn't selected:\n%s", requests)
	}
	environment := readCodexRecord(t, record, ".env")
	if strings.Contains(environment, "must-not-reach-the-server") {
		t.Fatal("a Gemini API key would override the Google sign-in")
	}
	for _, want := range []string{"GEMINI_HOME=" + profile, "AGY_ACP_FORCE_FILE_STORAGE=1", "BROWSER=true", "ANTIGRAVITY_HARNESS_PATH=" + install.Harness} {
		if !strings.Contains(environment, want) {
			t.Fatalf("environment is missing %q", want)
		}
	}
	if !strings.Contains(seen["tool"], "shell: go test ./...") || !strings.Contains(seen["tool"], "edit: a.go") {
		t.Fatalf("tool events = %q", seen["tool"])
	}
	if !strings.Contains(seen["verification"], "passed") || seen[antigravitySessionEvent] != "session-a\n" || seen["model"] != "gemini-chosen\n" {
		t.Fatalf("events = %+v", seen)
	}
	tools, err := state.ListToolExecutions(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].ToolName != "run_command" || tools[1].ToolName != "apply_patch" {
		t.Fatalf("tool executions = %+v", tools)
	}
}

func TestAntigravityRunnerNeedsASignIn(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5,"allow_unsandboxed_commands":true}`, false)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	install, record := fakeACPServer(t, "", "")
	_, err := (AntigravityRunner{Install: &install}).Run(context.Background(), job, nil, state)
	if !errors.Is(err, ErrAntigravitySignIn) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(record + ".requests"); statErr == nil {
		t.Fatal("the server started without a sign-in")
	}
}

func TestAntigravityJobEndsWhenTheSignInStopsWorking(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5,"allow_unsandboxed_commands":true}`, true)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	// session/new blocks on a browser login, as the real server does.
	install, _ := fakeACPServer(t, "", `echo '`+antigravityAuthLinkPrefix+`https://accounts.google.com/x' >&2; sleep 600`)
	_, err := (AntigravityRunner{Install: &install}).Run(context.Background(), job, nil, state)
	if !errors.Is(err, ErrAntigravitySignIn) {
		t.Fatalf("err = %v", err)
	}
}

func TestAntigravityQuestionPausesTheJob(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5,"allow_unsandboxed_commands":true}`, true)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	question := `printf '%s\n' '{"jsonrpc":"2.0","id":900,"method":"session/request_permission","params":{"sessionId":"session-a","toolCall":{"toolCallId":"interaction_1","title":"Which database?"},"options":[{"optionId":"a","name":"Postgres","kind":"allow_once"},{"optionId":"b","name":"SQLite","kind":"allow_once"}]}}'; IFS= read -r reply; printf '%s\n' "$reply" >> "$0.replies"; IFS= read -r cancel; printf '%s\n' "$cancel" >> "$0.replies"`
	install, _ := fakeACPServer(t, question, "")
	_, err := (AntigravityRunner{Install: &install}).Run(context.Background(), job, nil, state)
	var inputRequired *openrouter_request.UserInputRequiredError
	if !errors.As(err, &inputRequired) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"Which database?", "Postgres", "SQLite"} {
		if !strings.Contains(inputRequired.Question, want) {
			t.Fatalf("question = %q", inputRequired.Question)
		}
	}
	replies, err := os.ReadFile(install.Server + ".replies")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(replies), `"outcome":"cancelled"`) || !strings.Contains(string(replies), "session/cancel") {
		t.Fatalf("replies = %s", replies)
	}
}

func TestAntigravityResumesItsSession(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5,"allow_unsandboxed_commands":true}`, true)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	ctx := context.Background()
	if _, err := state.AppendEvent(ctx, job.ID, antigravitySessionEvent, "session-saved"); err != nil {
		t.Fatal(err)
	}
	install, record := fakeACPServer(t, acpUpdate(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"resumed"}}`), "")
	answer, err := (AntigravityRunner{Install: &install}).Run(ctx, job, nil, state)
	if err != nil || answer != "resumed" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	requests := readCodexRecord(t, record, ".requests")
	if !strings.Contains(requests, `"method":"session/resume"`) || !strings.Contains(requests, "session-saved") || strings.Contains(requests, `"method":"session/new"`) {
		t.Fatalf("requests = %s", requests)
	}
}

func TestAntigravityPermissionsAndFiles(t *testing.T) {
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	location := func(kind, path string) acpToolCall {
		tool := acpToolCall{Kind: kind}
		tool.Locations = append(tool.Locations, struct {
			Path string `json:"path"`
		}{path})
		return tool
	}
	for _, test := range []struct {
		name    string
		tool    acpToolCall
		allowed bool
	}{
		{"edit inside", location("edit", filepath.Join(workspace, "a.go")), true},
		{"edit outside", location("edit", filepath.Join(other, "a.go")), false},
		{"delete outside", location("delete", filepath.Join(other, "a.go")), false},
		{"read outside", location("read", filepath.Join(other, "a.go")), true},
		{"read Pearl's config", location("read", filepath.Join(config, ".env")), false},
		{"path in tool input", acpToolCall{Kind: "edit", RawInput: map[string]any{"TargetFile": filepath.Join(other, "a")}}, false},
		{"command without a path", acpToolCall{Kind: "execute", RawInput: map[string]any{"CommandLine": "go test"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := antigravityPermissionDenied(test.tool, workspace); (err == nil) != test.allowed {
				t.Fatalf("decision = %v, want allowed=%v", err, test.allowed)
			}
		})
	}

	session := &antigravitySession{parser: &codexEventParser{job: store.Job{WorkspaceRoot: workspace}}}
	request := func(method string, params map[string]any) (any, *acpError) {
		t.Helper()
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		return session.request(method, data)
	}
	path := filepath.Join(workspace, "sub", "note.txt")
	if _, failure := request("fs/write_text_file", map[string]any{"path": path, "content": "one\ntwo\nthree"}); failure != nil {
		t.Fatal(failure)
	}
	result, failure := request("fs/read_text_file", map[string]any{"path": path, "line": 2, "limit": 1})
	if failure != nil || result.(map[string]string)["content"] != "two" {
		t.Fatalf("read = %v, %v", result, failure)
	}
	outside := filepath.Join(other, "escape.txt")
	if _, failure := request("fs/write_text_file", map[string]any{"path": outside, "content": "x"}); failure == nil {
		t.Fatal("wrote outside the workspace")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the outside file exists")
	}
	if err := os.WriteFile(filepath.Join(config, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, failure := request("fs/read_text_file", map[string]any{"path": filepath.Join(config, ".env")}); failure == nil {
		t.Fatal("read Pearl's API key file")
	}
	if _, failure := request("terminal/create", map[string]any{}); failure == nil {
		t.Fatal("accepted an unsupported request")
	}
}

func TestFindAntigravity(t *testing.T) {
	directory := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(antigravityPathEnvironment, "")
	if _, err := FindAntigravity(); err == nil || !strings.Contains(err.Error(), "doesn't download") {
		t.Fatalf("missing server error = %v", err)
	}

	server := filepath.Join(directory, "custom_server")
	if err := os.WriteFile(server, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(antigravityPathEnvironment, server)
	if _, err := FindAntigravity(); err == nil || !strings.Contains(err.Error(), "localharness_external") {
		t.Fatalf("missing harness error = %v", err)
	}
	harnessName := "localharness_external"
	if runtime.GOOS == "windows" {
		harnessName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(directory, harnessName), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	install, err := FindAntigravity()
	if err != nil || install.Server != server || install.Harness != filepath.Join(directory, harnessName) {
		t.Fatalf("install = %+v, %v", install, err)
	}

	// T3 Code's copy is used only through its own record of the active release.
	t.Setenv(antigravityPathEnvironment, "")
	platform, arch := runtime.GOOS, runtime.GOARCH
	if platform == "windows" {
		platform = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	serverName := "agy_acp_server.par"
	if runtime.GOOS == "windows" {
		serverName = "agy_acp_server.exe"
	}
	managed := filepath.Join(home, ".t3", "tools", "antigravity-acp", platform+"-"+arch)
	release := strings.Repeat("a", 64)
	version := filepath.Join(managed, "versions", release)
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{serverName, harnessName} {
		if err := os.WriteFile(filepath.Join(version, name), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(managed, "active.json"), []byte(`{"releaseId":"../../evil"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindAntigravity(); err == nil {
		t.Fatal("followed an unsafe release ID")
	}
	if err := os.WriteFile(filepath.Join(managed, "active.json"), []byte(`{"releaseId":"`+release+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	install, err = FindAntigravity()
	if err != nil || install.Server != filepath.Join(version, serverName) {
		t.Fatalf("T3 install = %+v, %v", install, err)
	}
}

func TestAntigravityLoginPrintsTheLinkAndSavesTheSignIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ACP server needs a POSIX shell")
	}
	profile := setUpAntigravity(t, `{"model":"m","max_depth":5}`, false)
	directory := t.TempDir()
	server := filepath.Join(directory, "agy_acp_server.par")
	body := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id" ;;
    *'"method":"authenticate"'*)
      echo '` + antigravityAuthLinkPrefix + `https://accounts.google.com/o/oauth2/v2/auth?state=abc' >&2
      sleep 1
      printf '{}' > "$GEMINI_HOME/antigravity-acp/` + antigravityTokenFile + `"
      printf 'BROWSER=[%s]\n' "$BROWSER" > "$GEMINI_HOME/browser.txt"
      printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id" ;;
  esac
done
`
	if err := os.WriteFile(server, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "localharness_external"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(antigravityPathEnvironment, server)
	t.Setenv("BROWSER", "/inherited/browser")
	if AntigravitySignedIn() {
		t.Fatal("signed in before logging in")
	}
	var output strings.Builder
	if err := AntigravityLogin(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if !AntigravitySignedIn() {
		t.Fatal("the sign-in wasn't saved")
	}
	if !strings.Contains(output.String(), "https://accounts.google.com/o/oauth2/v2/auth?state=abc") {
		t.Fatalf("output = %q", output.String())
	}
	// Signing in has to be able to open the user's default browser.
	if browser := readCodexRecord(t, filepath.Join(profile, "browser"), ".txt"); !strings.Contains(browser, "BROWSER=[]") {
		t.Fatalf("browser = %q", browser)
	}
	if err := AntigravityLogout(); err != nil {
		t.Fatal(err)
	}
	if AntigravitySignedIn() {
		t.Fatal("still signed in after logging out")
	}
}

// These updates are shaped like the real server's, which a simpler fake hid:
// "content" is a list in tool calls, and a failed tool's output is a plain string.
func TestAntigravityHandlesTheRealServersToolUpdates(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5,"allow_unsandboxed_commands":true}`, true)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	file := job.WorkspaceRoot + "/n.txt"
	steps := strings.Join([]string{
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"run1","title":"pwd","kind":"execute","status":"pending","rawInput":{"CommandLine":"pwd"}}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"run1","status":"failed","rawOutput":"Tool execution failed"}`),
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"edit1","title":"Run client_create_file?","kind":"edit","status":"pending","rawInput":{"code_content":"ok","target_file":"` + file + `"},"content":[{"path":"` + file + `","newText":"ok","_meta":{"kind":"add"},"type":"diff"}],"locations":[{"path":"` + file + `"}]}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"edit1","title":"Running client_create_file","kind":"edit","status":"in_progress"}`),
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"call_3","title":"Running view_file","kind":"read","status":"in_progress","rawInput":{"absolute_path":"` + file + `"}}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"call_3","status":"completed"}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"edit1","status":"completed"}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"never-announced","status":"completed"}`),
		acpUpdate(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`),
	}, "\n      ")
	install, _ := fakeACPServer(t, steps, "")
	var tools []string
	sink := func(event openrouter_request.AgentEvent) error {
		if event.Type == "tool" {
			tools = append(tools, event.Data)
		}
		return nil
	}
	if answer, err := (AntigravityRunner{Install: &install}).Run(context.Background(), job, sink, state); err != nil || answer != "done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if got := strings.Join(tools, "|"); got != "shell: pwd|edit: n.txt|read: n.txt" {
		t.Fatalf("tool events = %q", got)
	}
	executions, err := state.ListToolExecutions(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.ToolExecution{}
	for _, execution := range executions {
		byName[execution.ToolName] = execution
	}
	if !strings.Contains(byName["run_command"].Result, "Tool execution failed") {
		t.Fatalf("the failure reason was lost: %+v", byName["run_command"])
	}
	if !strings.Contains(byName["apply_patch"].Arguments, `"relative_path":"n.txt"`) {
		t.Fatalf("the changed file wasn't recorded: %+v", byName)
	}
	if !strings.Contains(byName["read_file"].Arguments, "n.txt") {
		t.Fatalf("the read file wasn't recorded: %+v", byName["read_file"])
	}
}

func TestAntigravityRecordsWhatReallyHappened(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5,"allow_unsandboxed_commands":true}`, true)
	state, job := openClaudeJob(t, store.HarnessAntigravity)
	file := job.WorkspaceRoot + "/f.txt"
	write := `printf '%s\n' '{"jsonrpc":"2.0","id":901,"method":"fs/write_text_file","params":{"sessionId":"session-a","path":"` + file + `","content":"ok"}}'; IFS= read -r reply`
	steps := strings.Join([]string{
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"bad","title":"go test","kind":"execute","status":"pending","rawInput":{"CommandLine":"go test ./..."}}`),
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"bad","status":"completed","rawOutput":{"exitCode":1,"combinedOutput":"FAIL"}}`),
		acpUpdate(`{"sessionUpdate":"tool_call","toolCallId":"edit","title":"Create","kind":"edit","status":"in_progress","locations":[{"path":"` + file + `"}]}`),
		write,
		acpUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"edit","status":"failed","rawOutput":"Denied by user"}`),
	}, "\n      ")
	install, _ := fakeACPServer(t, steps, "")
	seen := map[string]string{}
	sink := func(event openrouter_request.AgentEvent) error {
		seen[event.Type] += event.Data + "\n"
		return nil
	}
	if _, err := (AntigravityRunner{Install: &install}).Run(context.Background(), job, sink, state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen["verification"], "failed") || strings.Contains(seen["verification"], "passed") {
		t.Fatalf("a failing test run wasn't reported as failed: %q", seen["verification"])
	}
	executions, err := state.ListToolExecutions(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range executions {
		wantSuccess := execution.ToolName == "apply_patch"
		if strings.Contains(execution.Result, `"success":true`) != wantSuccess {
			t.Errorf("%s recorded as %s", execution.ToolName, execution.Result)
		}
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "ok" {
		t.Fatalf("file = %q, %v", content, err)
	}
}
