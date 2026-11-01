package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/internal/verification"
	"github.com/August-H/pearl-cli/internal/workspacepath"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	antigravityPathEnvironment = "PEARL_ANTIGRAVITY_PATH"
	antigravityHomeEnvironment = "PEARL_ANTIGRAVITY_HOME"
	antigravitySessionEvent    = "antigravity_session"
	antigravityAuthMethod      = "oauth-personal"
	antigravityAuthLinkPrefix  = "Open the following link to authenticate the ACP server: "
	antigravityTokenFile       = "acp_token.json"
	antigravityQuestionPrefix  = "interaction_"
	antigravityFileMaxBytes    = 8 << 20
	antigravityLoginTimeout    = 5 * time.Minute
	antigravitySkillsMaxBytes  = 64 << 10
)

// ErrAntigravitySignIn means the job can't run until the user signs in.
var ErrAntigravitySignIn = errors.New("Antigravity needs you to sign in; run `pearl antigravity login`")

// AntigravityInstall is an ACP server already on this machine, with the agent
// harness that ships beside it.
type AntigravityInstall struct {
	Server  string
	Harness string
}

// FindAntigravity locates an installed Antigravity ACP server. Pearl never
// downloads it. Antigravity.app doesn't include this program, so the usual place
// to find it is the copy T3 Code installed for its own Antigravity provider.
func FindAntigravity() (AntigravityInstall, error) {
	serverName, harnessName := "agy_acp_server.par", "localharness_external"
	if runtime.GOOS == "windows" {
		serverName, harnessName = "agy_acp_server.exe", "localharness_external.exe"
	}
	install := func(server string) (AntigravityInstall, error) {
		harness := filepath.Join(filepath.Dir(server), harnessName)
		if _, err := os.Stat(harness); err != nil {
			return AntigravityInstall{}, fmt.Errorf("Antigravity's %s is missing from %s", harnessName, filepath.Dir(server))
		}
		return AntigravityInstall{Server: server, Harness: harness}, nil
	}
	if path, ok := findInstalledCLI(antigravityPathEnvironment, serverName); ok {
		return install(path)
	}
	if path, ok := findInstalledCLI(antigravityPathEnvironment, "agy_acp_server"); ok {
		return install(path)
	}
	if path := t3AntigravityServer(serverName); path != "" {
		return install(path)
	}
	return AntigravityInstall{}, errors.New("Antigravity's ACP server is not installed. Pearl doesn't download it: enable the Antigravity provider in T3 Code, or set " + antigravityPathEnvironment + " to agy_acp_server")
}

var t3ReleaseID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// t3AntigravityServer returns the server T3 Code marked as active, or "".
func t3AntigravityServer(serverName string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	platform, arch := runtime.GOOS, runtime.GOARCH
	if platform == "windows" {
		platform = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	directory := filepath.Join(home, ".t3", "tools", "antigravity-acp", platform+"-"+arch)
	data, err := os.ReadFile(filepath.Join(directory, "active.json"))
	if err != nil {
		return ""
	}
	var active struct {
		ReleaseID string `json:"releaseId"`
	}
	if json.Unmarshal(data, &active) != nil || !t3ReleaseID.MatchString(active.ReleaseID) {
		return ""
	}
	path := filepath.Join(directory, "versions", active.ReleaseID, serverName)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// AntigravityProfile returns Pearl's private Antigravity folder. Like T3 Code,
// Pearl keeps its sign-in apart from the user's other tools. It sits outside
// Pearl's config folder because the sandbox blocks that from every agent.
func AntigravityProfile() (string, error) {
	if path := strings.TrimSpace(os.Getenv(antigravityHomeEnvironment)); path != "" {
		return filepath.Abs(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "pearl-antigravity"), nil
}

func prepareAntigravityProfile() (string, error) {
	profile, err := AntigravityProfile()
	if err != nil {
		return "", err
	}
	for _, directory := range []string{profile, filepath.Join(profile, "antigravity-acp"), filepath.Join(profile, "tmp")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return "", fmt.Errorf("create Antigravity profile: %w", err)
		}
	}
	settings := `{"auth":{"type":"` + antigravityAuthMethod + `"}}`
	if err := os.WriteFile(filepath.Join(profile, "antigravity-acp", "settings.json"), []byte(settings), 0o600); err != nil {
		return "", fmt.Errorf("write Antigravity profile: %w", err)
	}
	return profile, nil
}

// AntigravitySignedIn reports whether Pearl's profile holds a saved sign-in.
func AntigravitySignedIn() bool {
	profile, err := AntigravityProfile()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(profile, "antigravity-acp", antigravityTokenFile))
	return err == nil
}

// AntigravityLogout removes Pearl's saved sign-in.
func AntigravityLogout() error {
	profile, err := AntigravityProfile()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(profile, "antigravity-acp", antigravityTokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func antigravityEnvironment(install AntigravityInstall, profile string, browser bool) []string {
	// Credentials in the environment would override the Google sign-in.
	environment := environmentWithout(
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_QUOTA_PROJECT", "GOOGLE_GENAI_USE_VERTEXAI", "GCLOUD_PROJECT",
		"CLOUDSDK_CORE_PROJECT", "AGY_ACP_CCPA_PROJECT", "AGY_ACP_ENABLE_OAUTH", "GEMINI_HOME",
		"AGY_ACP_FORCE_FILE_STORAGE", "ANTIGRAVITY_HARNESS_PATH", "BROWSER", "PYTHONUNBUFFERED",
		"OPENROUTER_API_KEY",
	)
	temporary := filepath.Join(profile, "tmp")
	environment = append(environment,
		"GEMINI_HOME="+profile,
		"AGY_ACP_FORCE_FILE_STORAGE=1",
		"PYTHONUNBUFFERED=1",
		"ANTIGRAVITY_HARNESS_PATH="+install.Harness,
	)
	if !browser && runtime.GOOS != "windows" {
		// A background job must never open a browser for the user.
		environment = append(environment, "BROWSER=true")
	}
	if runtime.GOOS == "windows" {
		return append(environment, "TEMP="+temporary, "TMP="+temporary)
	}
	return append(environment, "TMPDIR="+temporary)
}

func antigravityServerArguments() []string {
	if runtime.GOOS == "linux" {
		return []string{"--uid="}
	}
	return nil
}

// AntigravityLogin signs in to Antigravity with a Google account. It runs the
// server outside the sandbox so it can open the browser, and writes the sign-in
// link to output in case that doesn't work.
func AntigravityLogin(ctx context.Context, output io.Writer) error {
	install, err := FindAntigravity()
	if err != nil {
		return err
	}
	profile, err := prepareAntigravityProfile()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, antigravityLoginTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, install.Server, antigravityServerArguments()...)
	command.Dir = filepath.Join(profile, "tmp")
	command.Env = antigravityEnvironment(install, profile, true)
	client, stop, err := startAntigravity(command, nil, func(line string) {
		if link, ok := strings.CutPrefix(line, antigravityAuthLinkPrefix); ok {
			fmt.Fprintln(output, "Sign in with your Google account in the browser. If it didn't open, use this link:")
			fmt.Fprintln(output, strings.TrimSpace(link))
		}
	})
	if err != nil {
		return err
	}
	defer func() {
		stop()
		_ = client.wait()
	}()
	if err := client.initialize(ctx); err != nil {
		return err
	}
	if err := client.call(ctx, "authenticate", map[string]any{"methodId": antigravityAuthMethod}, nil); err != nil {
		if ctx.Err() != nil {
			return errors.New("sign-in timed out after 5 minutes")
		}
		return fmt.Errorf("sign in to Antigravity: %w", err)
	}
	if !AntigravitySignedIn() {
		return errors.New("Antigravity finished without saving a sign-in")
	}
	return nil
}

// AntigravityRunner runs jobs with an installed Antigravity ACP server, over the
// Agent Client Protocol, the same connection T3 Code uses.
type AntigravityRunner struct {
	Install *AntigravityInstall
}

func (r AntigravityRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	var install AntigravityInstall
	if r.Install != nil {
		install = *r.Install
	} else {
		found, err := FindAntigravity()
		if err != nil {
			return "", err
		}
		install = found
	}
	if !AntigravitySignedIn() {
		return "", ErrAntigravitySignIn
	}
	profile, err := prepareAntigravityProfile()
	if err != nil {
		return "", err
	}
	job.Harness = store.HarnessAntigravity
	run, cancel, err := startExternalRun(ctx, job, events, state, antigravitySessionEvent)
	if err != nil {
		return "", err
	}
	defer cancel()
	permissions, err := openrouter_request.LoadCommandPermissions()
	if err != nil {
		return "", err
	}
	// The server runs its own commands, so the sandbox has to go around all of
	// it. It needs the network to reach the model, and its commands inherit that.
	// It runs them in a pseudo-terminal, so they need to be able to open one.
	program, arguments, err := openrouter_request.SandboxedTerminalProcess(
		job.WorkspaceRoot, []string{profile}, true, install.Server, antigravityServerArguments(), job.WorkspaceRoot,
	)
	if err != nil {
		if !errors.Is(err, openrouter_request.ErrNoSandbox) || !permissions.AllowUnsandboxed {
			return "", err
		}
		program, arguments = install.Server, antigravityServerArguments()
	}
	command := exec.CommandContext(run.ctx, program, arguments...)
	command.Dir = job.WorkspaceRoot
	command.Env = antigravityEnvironment(install, profile, false)

	session := &antigravitySession{parser: run.parser, tools: map[string]*acpToolCall{}, recorded: map[string]bool{}}
	runContext, cancelRun := context.WithCancel(run.ctx)
	defer cancelRun()
	var needsSignIn sync.Once
	signIn := make(chan struct{})
	client, stop, err := startAntigravity(command, session, func(line string) {
		if strings.HasPrefix(line, antigravityAuthLinkPrefix) {
			// The saved sign-in stopped working and the server is now waiting for
			// a browser nobody is watching, so end the job instead of hanging.
			needsSignIn.Do(func() {
				close(signIn)
				cancelRun()
			})
		}
	})
	if err != nil {
		return "", err
	}
	defer stop()
	runErr := session.run(runContext, run)
	stop()
	_ = client.wait() // Its exit status means nothing once Pearl has stopped it.
	run.saveTranscript(state)

	signedOut := false
	select {
	case <-signIn:
		signedOut = true
	default:
	}
	switch {
	case signedOut:
		return "", ErrAntigravitySignIn
	case run.ctx.Err() != nil:
		return "", fmt.Errorf("Antigravity stopped: %w", run.ctx.Err())
	case runErr != nil:
		if detail := client.errorDetail(); detail != "" {
			return "", fmt.Errorf("Antigravity failed: %w (%s)", runErr, detail)
		}
		return "", fmt.Errorf("Antigravity failed: %w", runErr)
	}
	return run.result()
}

// antigravityInstructions is sent with the first prompt, because ACP has no
// system prompt. Skills go in whole since the sandbox hides their folder.
func antigravityInstructions() string {
	instructions := codexInstructions
	skills, _ := openrouter_request.LoadSkills()
	remaining := antigravitySkillsMaxBytes
	for _, skill := range skills {
		data, err := os.ReadFile(skill.Path)
		if err != nil || len(data) > remaining {
			continue
		}
		remaining -= len(data)
		instructions += "\n\nThe user saved this skill. Follow it when it fits the task.\n<skill name=\"" + skill.Name + "\">\n" + string(data) + "\n</skill>"
	}
	return instructions
}

// --- ACP session ---

type acpConfigOption struct {
	ID           string           `json:"id"`
	Category     string           `json:"category"`
	CurrentValue string           `json:"currentValue"`
	Options      []acpConfigValue `json:"options"`
}

// acpConfigValue is a choice, or a named group of choices.
type acpConfigValue struct {
	Value   string           `json:"value"`
	Name    string           `json:"name"`
	Options []acpConfigValue `json:"options"`
}

type acpSessionSetup struct {
	SessionID     string            `json:"sessionId"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
}

type acpToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	RawInput   map[string]any  `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations"`
	Content []struct {
		Type    string `json:"type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"content"`
}

// antigravitySession handles one job's conversation. Its lock covers everything
// shared between the job's goroutine and the one reading server messages.
type antigravitySession struct {
	lock      sync.Mutex
	client    *acpClient
	parser    *codexEventParser
	sessionID string
	message   strings.Builder
	thought   strings.Builder
	tools     map[string]*acpToolCall
	recorded  map[string]bool
	written   map[string]bool
	question  string
}

func (s *antigravitySession) attach(client *acpClient) {
	s.client = client
}

func (s *antigravitySession) emit(eventType, data string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.parser.emit(eventType, data)
}

func (s *antigravitySession) run(ctx context.Context, run *externalRun) error {
	if err := s.client.initialize(ctx); err != nil {
		return err
	}
	if err := s.client.call(ctx, "authenticate", map[string]any{"methodId": antigravityAuthMethod}, nil); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	workspace := s.parser.job.WorkspaceRoot
	prompt := run.prompt
	var setup acpSessionSetup
	resumed := false
	if run.sessionID != "" {
		err := s.client.call(ctx, "session/resume", map[string]any{"sessionId": run.sessionID, "cwd": workspace, "mcpServers": []any{}}, &setup)
		if err == nil {
			resumed = true
			setup.SessionID = run.sessionID
		} else {
			if ctx.Err() != nil {
				return err
			}
			// The saved conversation is gone, so start over with the whole task.
			prompt = resumePrompt(s.parser.job, "", run.answer)
			if emitErr := s.emit("context", "Couldn't resume the Antigravity session, so Pearl started a new one."); emitErr != nil {
				return emitErr
			}
		}
	}
	if !resumed {
		if err := s.client.call(ctx, "session/new", map[string]any{"cwd": workspace, "mcpServers": []any{}}, &setup); err != nil {
			return fmt.Errorf("start session: %w", err)
		}
		if setup.SessionID == "" {
			return errors.New("the server didn't return a session")
		}
		prompt = antigravityInstructions() + "\n\n" + prompt
		if err := s.emit(antigravitySessionEvent, setup.SessionID); err != nil {
			return err
		}
	}
	s.lock.Lock()
	s.sessionID = setup.SessionID
	s.lock.Unlock()
	if err := s.configure(ctx, setup, strings.TrimSpace(run.settings.AntigravityModel)); err != nil {
		return err
	}
	var finished struct {
		StopReason string `json:"stopReason"`
	}
	err := s.client.call(ctx, "session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt + codexQuestionReminder}},
	}, &finished)
	s.lock.Lock()
	flushErr := s.flush()
	question := s.question
	if question != "" {
		s.parser.question = question
	}
	s.lock.Unlock()
	// Asking a question cancels the turn on purpose, so that isn't a failure.
	if question != "" {
		return nil
	}
	if err == nil {
		err = flushErr
	}
	if err != nil {
		return err
	}
	switch finished.StopReason {
	case "", "end_turn":
		return nil
	case "max_tokens", "max_turn_requests":
		return fmt.Errorf("it stopped before finishing (%s)", finished.StopReason)
	case "refusal":
		return errors.New("the model refused the request")
	}
	return fmt.Errorf("it stopped early (%s)", finished.StopReason)
}

// configure keeps permission requests coming to Pearl, and selects the model.
// A resumed session may have kept a more permissive mode.
func (s *antigravitySession) configure(ctx context.Context, setup acpSessionSetup, model string) error {
	set := func(id, value string) error {
		return s.client.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.sessionID, "configId": id, "value": value}, nil)
	}
	current := ""
	for _, option := range setup.ConfigOptions {
		switch {
		case option.Category == "mode" && option.CurrentValue != "default":
			if err := set(option.ID, "default"); err != nil {
				return fmt.Errorf("set permission mode: %w", err)
			}
		case option.ID == "model":
			current = option.CurrentValue
		}
	}
	if model != "" && model != current {
		if err := set("model", model); err != nil {
			return fmt.Errorf("Antigravity model %q is unavailable for this account: %w", model, err)
		}
		current = model
	}
	if current != "" {
		return s.emit("model", current)
	}
	return nil
}

// flush needs the lock held.
func (s *antigravitySession) flush() error {
	if text := s.thought.String(); text != "" {
		s.thought.Reset()
		if err := s.parser.completeItem(codexItem{Type: "reasoning", Text: text}); err != nil {
			return err
		}
	}
	if text := s.message.String(); text != "" {
		s.message.Reset()
		return s.parser.completeItem(codexItem{Type: "agent_message", Text: text})
	}
	return nil
}

func (s *antigravitySession) notification(method string, params json.RawMessage) error {
	if method != "session/update" {
		return nil
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	var envelope struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &envelope) != nil {
		return nil
	}
	// The rest depends on the kind: "content" is an object in message chunks and
	// a list in tool calls.
	var update struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(envelope.Update, &update) != nil {
		return nil
	}
	var chunk struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	switch update.Kind {
	case "agent_message_chunk":
		if json.Unmarshal(envelope.Update, &chunk) == nil && (chunk.Content.Type == "text" || chunk.Content.Type == "") {
			s.message.WriteString(chunk.Content.Text)
		}
	case "agent_thought_chunk":
		if json.Unmarshal(envelope.Update, &chunk) == nil {
			s.thought.WriteString(chunk.Content.Text)
		}
	case "tool_call", "tool_call_update":
		if err := s.flush(); err != nil {
			return err
		}
		return s.toolUpdate(envelope.Update)
	}
	return nil
}

func (s *antigravitySession) toolUpdate(data json.RawMessage) error {
	var update acpToolCall
	if json.Unmarshal(data, &update) != nil || update.ToolCallID == "" || strings.HasPrefix(update.ToolCallID, antigravityQuestionPrefix) {
		return nil
	}
	tool, known := s.tools[update.ToolCallID]
	if !known {
		tool = &acpToolCall{ToolCallID: update.ToolCallID}
		s.tools[update.ToolCallID] = tool
	}
	if update.Title != "" {
		tool.Title = update.Title
	}
	if update.Kind != "" {
		tool.Kind = update.Kind
	}
	if update.Status != "" {
		tool.Status = update.Status
	}
	if update.RawInput != nil {
		tool.RawInput = update.RawInput
	}
	if len(update.RawOutput) > 0 {
		tool.RawOutput = update.RawOutput
	}
	if len(update.Locations) > 0 {
		tool.Locations = update.Locations
	}
	if len(update.Content) > 0 {
		tool.Content = update.Content
	}
	if !known {
		if err := s.toolStarted(tool); err != nil {
			return err
		}
	}
	if (tool.Status == "completed" || tool.Status == "failed") && !s.recorded[tool.ToolCallID] {
		s.recorded[tool.ToolCallID] = true
		return s.toolFinished(tool)
	}
	return nil
}

func (t *acpToolCall) command() string {
	for _, key := range []string{"command", "CommandLine", "command_line", "commandLine"} {
		if value, _ := t.RawInput[key].(string); value != "" {
			return value
		}
	}
	return t.Title
}

func (t *acpToolCall) path() string {
	if len(t.Locations) > 0 {
		return t.Locations[0].Path
	}
	for _, key := range []string{"target_file", "absolute_path", "file_path", "path"} {
		if value, _ := t.RawInput[key].(string); value != "" {
			return value
		}
	}
	return ""
}

// exitCode returns the command's exit code, if the server reported one.
func (t *acpToolCall) exitCode() (int, bool) {
	var raw map[string]any
	if json.Unmarshal(t.RawOutput, &raw) != nil {
		return 0, false
	}
	for _, key := range []string{"exitCode", "exit_code"} {
		if value, ok := raw[key].(float64); ok {
			return int(value), true
		}
	}
	return 0, false
}

func (t *acpToolCall) output() string {
	// A failed tool reports a plain string here, and others report an object.
	var message string
	if json.Unmarshal(t.RawOutput, &message) == nil && message != "" {
		return message
	}
	var raw map[string]any
	if json.Unmarshal(t.RawOutput, &raw) == nil {
		for _, key := range []string{"combinedOutput", "combined_output", "output"} {
			if value, _ := raw[key].(string); value != "" {
				return value
			}
		}
	}
	parts := make([]string, 0, len(t.Content))
	for _, item := range t.Content {
		if item.Content.Text != "" {
			parts = append(parts, item.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *antigravitySession) toolStarted(tool *acpToolCall) error {
	workspace := s.parser.job.WorkspaceRoot
	switch tool.Kind {
	case "execute":
		if verification.Shell(tool.command()) {
			if err := s.parser.emit("verification", verification.Event("running", tool.command())); err != nil {
				return err
			}
		}
		return s.parser.emit("tool", "shell: "+tool.command())
	case "edit", "delete", "move":
		if verification.AffectsTests(tool.path()) {
			if err := s.parser.emit("verification", verification.Event("not_run", "")); err != nil {
				return err
			}
		}
		return s.parser.emit("tool", "edit: "+codexRelativePath(workspace, tool.path()))
	case "read":
		if path := tool.path(); path != "" {
			return s.parser.emit("tool", "read: "+codexRelativePath(workspace, path))
		}
	}
	if tool.Title != "" {
		return s.parser.emit("tool", tool.Title)
	}
	if tool.Kind != "" {
		return s.parser.emit("tool", tool.Kind)
	}
	return nil
}

func (s *antigravitySession) toolFinished(tool *acpToolCall) error {
	success := tool.Status == "completed"
	if code, ok := tool.exitCode(); ok && code != 0 {
		success = false
	}
	// When a step is denied, the server marks every open tool call as failed,
	// including writes Pearl already made for it.
	if tool.Kind == "edit" && s.written[workspacepath.CanonicalPath(agentPath(s.parser.job.WorkspaceRoot, tool.path()))] {
		success = true
	}
	failure := ""
	if !success {
		failure = strings.TrimSpace(tool.output())
		if failure == "" {
			failure = "tool failed"
		}
	}
	name, arguments := tool.Kind, "{}"
	relative, _ := json.Marshal(map[string]string{"relative_path": codexRelativePath(s.parser.job.WorkspaceRoot, tool.path())})
	switch tool.Kind {
	case "execute":
		command := tool.command()
		if verification.Shell(command) {
			status := "passed"
			if !success {
				status = "failed"
			}
			if err := s.parser.emit("verification", verification.Event(status, command)); err != nil {
				return err
			}
		}
		data, _ := json.Marshal(map[string]string{"command": command})
		name, arguments = "run_command", string(data)
	case "edit", "move":
		name, arguments = "apply_patch", string(relative)
	case "delete":
		name, arguments = "delete_file", string(relative)
	case "read":
		name, arguments = "read_file", string(relative)
	case "":
		name = "tool"
	}
	return s.parser.recordTool(tool.ToolCallID, name, arguments, codexTruncate(tool.output()), success, failure)
}

// request answers what the server asks of Pearl.
func (s *antigravitySession) request(method string, params json.RawMessage) (any, *acpError) {
	s.lock.Lock()
	defer s.lock.Unlock()
	workspace := s.parser.job.WorkspaceRoot
	switch method {
	case "session/request_permission":
		return s.permission(params), nil
	case "fs/read_text_file":
		var input struct {
			Path  string `json:"path"`
			Line  *int   `json:"line"`
			Limit *int   `json:"limit"`
		}
		if json.Unmarshal(params, &input) != nil || !filepath.IsAbs(input.Path) {
			return nil, &acpError{Code: -32602, Message: "a read needs an absolute path"}
		}
		if err := agentMayRead(workspace, input.Path); err != nil {
			return nil, &acpError{Code: -32602, Message: err.Error()}
		}
		content, err := readAgentTextFile(input.Path, input.Line, input.Limit)
		if err != nil {
			return nil, &acpError{Code: -32002, Message: err.Error()}
		}
		return map[string]string{"content": content}, nil
	case "fs/write_text_file":
		var input struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(params, &input) != nil || !filepath.IsAbs(input.Path) {
			return nil, &acpError{Code: -32602, Message: "a write needs an absolute path"}
		}
		if err := agentMayWrite(workspace, input.Path); err != nil {
			return nil, &acpError{Code: -32602, Message: err.Error()}
		}
		path := workspacepath.CanonicalPath(input.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, &acpError{Code: -32603, Message: "could not create the folder"}
		}
		if err := os.WriteFile(path, []byte(input.Content), 0o644); err != nil {
			return nil, &acpError{Code: -32603, Message: "could not write the file"}
		}
		if s.written == nil {
			s.written = map[string]bool{}
		}
		s.written[path] = true
		return map[string]any{}, nil
	}
	return nil, &acpError{Code: -32601, Message: "Pearl doesn't support " + method}
}

func readAgentTextFile(path string, line, limit *int) (string, error) {
	path = workspacepath.CanonicalPath(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", errors.New("file not found")
	}
	if !info.Mode().IsRegular() || info.Size() > antigravityFileMaxBytes {
		return "", fmt.Errorf("not a readable text file under %d bytes", antigravityFileMaxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("could not read the file")
	}
	text := string(data)
	if line == nil && limit == nil {
		return text, nil
	}
	lines := strings.Split(text, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = min(*line-1, len(lines))
	}
	end := len(lines)
	if limit != nil && *limit >= 0 {
		end = min(start+*limit, len(lines))
	}
	return strings.Join(lines[start:end], "\n"), nil
}

type acpPermissionRequest struct {
	ToolCall acpToolCall `json:"toolCall"`
	Options  []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

func (s *antigravitySession) permission(params json.RawMessage) any {
	cancelled := map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	var request acpPermissionRequest
	if json.Unmarshal(params, &request) != nil {
		return cancelled
	}
	// Antigravity's questions to the user use this same request. Nobody can
	// answer now, so stop the turn and ask through Pearl's job flow.
	if strings.HasPrefix(request.ToolCall.ToolCallID, antigravityQuestionPrefix) {
		question := strings.TrimSpace(request.ToolCall.Title)
		if question == "" {
			question = "Choose an option."
		}
		for _, option := range request.Options {
			question += "\n- " + option.Name
		}
		s.question = question
		_ = s.client.notify("session/cancel", map[string]string{"sessionId": s.sessionID})
		return cancelled
	}
	kind := "allow_once"
	if antigravityPermissionDenied(request.ToolCall, s.parser.job.WorkspaceRoot) != nil {
		kind = "reject_once"
	}
	for _, option := range request.Options {
		if option.Kind == kind {
			return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option.OptionID}}
		}
	}
	return cancelled
}

// antigravityPermissionDenied applies Pearl's path rules to a tool call. The
// sandbox around the server is what confines commands; this refuses earlier, and
// covers systems where the user opted out of the sandbox.
func antigravityPermissionDenied(tool acpToolCall, workspace string) error {
	var paths []string
	for _, location := range tool.Locations {
		paths = append(paths, location.Path)
	}
	for key, value := range tool.RawInput {
		lower := strings.ToLower(key)
		if text, ok := value.(string); ok && text != "" && (strings.Contains(lower, "path") || strings.Contains(lower, "file")) {
			paths = append(paths, text)
		}
	}
	changes := tool.Kind == "edit" || tool.Kind == "delete" || tool.Kind == "move"
	for _, path := range paths {
		var err error
		if changes {
			err = agentMayWrite(workspace, path)
		} else {
			err = agentMayRead(workspace, path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// --- ACP transport ---

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *acpError) Error() string { return e.Message }

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpResult struct {
	result json.RawMessage
	err    error
}

type acpHandler interface {
	attach(client *acpClient)
	request(method string, params json.RawMessage) (any, *acpError)
	notification(method string, params json.RawMessage) error
}

// acpClient speaks JSON-RPC to the server, one JSON message per line.
type acpClient struct {
	stdin     io.WriteCloser
	handler   acpHandler
	writeLock sync.Mutex
	lock      sync.Mutex
	nextID    int
	pending   map[int]chan acpResult
	closed    chan struct{}
	readErr   error
	handleErr error
	stderr    codexTailBuffer
	command   *exec.Cmd
	waitOnce  sync.Once
	waitErr   error
	readers   sync.WaitGroup
}

// startAntigravity starts the server and reads its output until it exits.
// onLog sees every line of diagnostic output, which is where the server writes
// its sign-in link.
func startAntigravity(command *exec.Cmd, handler acpHandler, onLog func(string)) (*acpClient, func(), error) {
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to Antigravity: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to Antigravity: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to Antigravity: %w", err)
	}
	command.WaitDelay = 5 * time.Second
	kill, err := openrouter_request.StartProcessGroup(command)
	if err != nil {
		return nil, nil, fmt.Errorf("start Antigravity: %w", err)
	}
	client := &acpClient{
		stdin: stdin, handler: handler, pending: map[int]chan acpResult{},
		closed: make(chan struct{}), command: command,
	}
	if handler != nil {
		handler.attach(client)
	}
	client.readers.Add(2)
	go func() {
		defer client.readers.Done()
		client.readLogs(stderr, onLog)
	}()
	go func() {
		defer client.readers.Done()
		client.readMessages(stdout, onLog)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = stdin.Close()
			kill()
		})
	}
	return client, stop, nil
}

// wait returns after the server's process and output have finished. Call it
// only after stopping the server.
func (c *acpClient) wait() error {
	c.waitOnce.Do(func() {
		finished := make(chan struct{})
		go func() {
			c.readers.Wait()
			close(finished)
		}()
		// Wait closes Pearl's end of the pipes, which frees the readers if a
		// process that outlived the server is still holding them open.
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
		}
		c.waitErr = c.command.Wait()
		<-finished
	})
	return c.waitErr
}

func (c *acpClient) readLogs(stderr io.Reader, onLog func(string)) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if onLog != nil {
			onLog(line)
		}
		// Keep errors for reporting. Never keep a sign-in link.
		if !strings.Contains(line, antigravityAuthLinkPrefix) {
			c.lock.Lock()
			_, _ = c.stderr.Write([]byte(line + "\n"))
			c.lock.Unlock()
		}
	}
}

func (c *acpClient) errorDetail() string {
	c.lock.Lock()
	defer c.lock.Unlock()
	lines := strings.Split(strings.TrimSpace(c.stderr.String()), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		// Absl log lines start with a severity letter, and only E or F matter here.
		if strings.HasPrefix(lines[index], "E") || strings.HasPrefix(lines[index], "F") {
			return strings.TrimSpace(lines[index])
		}
	}
	return ""
}

func (c *acpClient) readMessages(stdout io.Reader, onLog func(string)) {
	reader := bufio.NewReaderSize(stdout, 64<<10)
	var failure error
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var message acpMessage
			if json.Unmarshal(trimmed, &message) == nil && message.JSONRPC != "" {
				if handleErr := c.dispatch(message); handleErr != nil {
					failure = handleErr
					break
				}
			} else if onLog != nil {
				onLog(string(trimmed))
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				failure = err
			}
			break
		}
	}
	c.lock.Lock()
	c.readErr = failure
	pending := c.pending
	c.pending = map[int]chan acpResult{}
	c.lock.Unlock()
	closedErr := failure
	if closedErr == nil {
		closedErr = errors.New("the server exited unexpectedly")
	}
	for _, reply := range pending {
		reply <- acpResult{err: closedErr}
	}
	close(c.closed)
	// Drain anything left so the process can't block writing to a full pipe.
	_, _ = io.Copy(io.Discard, reader)
}

func (c *acpClient) dispatch(message acpMessage) error {
	switch {
	case message.Method != "" && len(message.ID) > 0:
		var result any = map[string]any{}
		var failure *acpError
		if c.handler != nil {
			result, failure = c.handler.request(message.Method, message.Params)
		} else {
			failure = &acpError{Code: -32601, Message: "Pearl isn't running a job"}
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": message.ID}
		if failure != nil {
			reply["error"] = failure
		} else {
			reply["result"] = result
		}
		return c.write(reply)
	case message.Method != "":
		if c.handler != nil {
			return c.handler.notification(message.Method, message.Params)
		}
		return nil
	case len(message.ID) > 0:
		var id int
		if json.Unmarshal(message.ID, &id) != nil {
			return nil
		}
		c.lock.Lock()
		reply, ok := c.pending[id]
		delete(c.pending, id)
		c.lock.Unlock()
		if !ok {
			return nil
		}
		if message.Error != nil {
			reply <- acpResult{err: message.Error}
		} else {
			reply <- acpResult{result: message.Result}
		}
	}
	return nil
}

func (c *acpClient) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeLock.Lock()
	defer c.writeLock.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *acpClient) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *acpClient) call(ctx context.Context, method string, params, result any) error {
	reply := make(chan acpResult, 1)
	c.lock.Lock()
	c.nextID++
	id := c.nextID
	c.pending[id] = reply
	c.lock.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		select {
		case response := <-reply:
			if response.err != nil {
				return response.err
			}
		default:
		}
		c.lock.Lock()
		failure := c.readErr
		c.lock.Unlock()
		if failure != nil {
			return failure
		}
		return errors.New("the server exited unexpectedly")
	case response := <-reply:
		if response.err != nil {
			return response.err
		}
		if result != nil && len(response.result) > 0 {
			return json.Unmarshal(response.result, result)
		}
		return nil
	}
}

func (c *acpClient) initialize(ctx context.Context) error {
	var result struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "pearl", "version": "1"},
	}, &result)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if result.ProtocolVersion != 1 {
		return fmt.Errorf("the server uses protocol version %d, and Pearl supports 1", result.ProtocolVersion)
	}
	return nil
}
