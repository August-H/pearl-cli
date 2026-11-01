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
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/internal/verification"
	"github.com/August-H/pearl-cli/internal/workspacepath"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	claudePathEnvironment = "PEARL_CLAUDE_PATH"
	claudeSessionEvent    = "claude_session"
)

// ClaudeRunner runs jobs with the Claude Code CLI the user installed, signed in
// with their own account. Pearl only starts the process and talks to it over
// its stream-json interface, the same one T3 Code uses.
type ClaudeRunner struct {
	Executable string
}

// FindClaude locates the installed Claude Code CLI. Pearl never installs it.
func FindClaude() (string, error) {
	if path, ok := findInstalledCLI(claudePathEnvironment, "claude"); ok {
		return path, nil
	}
	return "", errors.New("Claude Code is not installed; install it from https://claude.com/claude-code and sign in by running `claude`, or set " + claudePathEnvironment)
}

func (r ClaudeRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	executable := strings.TrimSpace(r.Executable)
	if executable == "" {
		found, err := FindClaude()
		if err != nil {
			return "", err
		}
		executable = found
	}
	job.Harness = store.HarnessClaude
	run, cancel, err := startExternalRun(ctx, job, events, state, claudeSessionEvent)
	if err != nil {
		return "", err
	}
	defer cancel()
	permissions, err := openrouter_request.LoadCommandPermissions()
	if err != nil {
		return "", err
	}
	arguments, err := claudeArguments(run.settings.ClaudeModel, run.sessionID)
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(run.ctx, executable, arguments...)
	command.Dir = job.WorkspaceRoot
	command.Env = ClaudeEnvironment()
	command.WaitDelay = 5 * time.Second
	var stderr codexTailBuffer
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("connect to Claude Code: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("connect to Claude Code: %w", err)
	}
	stop, err := openrouter_request.StartProcessGroup(command)
	if err != nil {
		return "", fmt.Errorf("start Claude Code: %w", err)
	}
	defer stop()

	session := &claudeSession{
		parser: run.parser, permissions: permissions, stdin: stdin,
		pending: map[string]claudePendingTool{},
	}
	message := map[string]any{"type": "user", "message": map[string]any{
		"role": "user", "content": []map[string]string{{"type": "text", "text": run.prompt + codexQuestionReminder}},
	}}
	writeErr := session.write(message)
	readErr := session.read(stdout)
	session.closeInput()
	waitErr := command.Wait()
	run.saveTranscript(state)

	switch {
	case run.ctx.Err() != nil:
		return "", fmt.Errorf("Claude Code stopped: %w", run.ctx.Err())
	case readErr != nil:
		return "", readErr
	case session.failure != "":
		return "", errors.New(session.failure)
	case !session.finished:
		detail := strings.TrimSpace(stderr.String())
		if detail == "" && writeErr != nil {
			detail = writeErr.Error()
		}
		if detail == "" && waitErr != nil {
			detail = waitErr.Error()
		}
		if detail == "" {
			detail = "it exited before finishing the task"
		}
		return "", errors.New("Claude Code failed: " + detail)
	}
	return run.result()
}

// ClaudeEnvironment is what Pearl starts Claude Code with. It removes the markers
// that would make Claude Code refuse to start inside a Claude Code session, and
// any shell settings that send it to another endpoint or rename its models. Those
// would make the Anthropic models Pearl lists run on something else.
func ClaudeEnvironment() []string {
	var kept []string
	for _, value := range environmentWithout(
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "OPENROUTER_API_KEY",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	) {
		name := strings.ToUpper(value)
		if !strings.HasPrefix(name, "ANTHROPIC_DEFAULT_") && !strings.HasPrefix(name, "ANTHROPIC_CUSTOM_MODEL_OPTION") {
			kept = append(kept, value)
		}
	}
	return kept
}

// claudeArguments asks for the CLI's streaming interface. Permission requests
// come back over stdio so Pearl can answer them, and Bash runs in Claude Code's
// own sandbox with Pearl's protected paths unreadable.
func claudeArguments(model, sessionID string) ([]string, error) {
	sandbox, err := json.Marshal(map[string]any{"sandbox": map[string]any{
		"enabled":                  true,
		"autoAllowBashIfSandboxed": true,
		"allowUnsandboxedCommands": false,
		"filesystem":               map[string]any{"denyRead": workspacepath.DeniedRoots()},
	}})
	if err != nil {
		return nil, err
	}
	skills, _ := openrouter_request.LoadSkills()
	arguments := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json",
		"--permission-prompt-tool", "stdio", "--permission-mode", "default",
		"--settings", string(sandbox),
		"--append-system-prompt", codexDeveloperInstructions(skills),
	}
	if model = strings.TrimSpace(model); model != "" {
		arguments = append(arguments, "--model", model)
	}
	if sessionID != "" {
		arguments = append(arguments, "--resume", sessionID)
	}
	return arguments, nil
}

// claudeToolDecision answers one permission request. Anything Pearl doesn't
// recognize is refused, and the reason goes back to the model.
func claudeToolDecision(tool string, input map[string]any, workspace string, permissions openrouter_request.CommandPermissions) error {
	switch tool {
	case "Read", "Glob", "Grep", "LS", "NotebookRead":
		for _, key := range []string{"file_path", "path", "notebook_path"} {
			if path, _ := input[key].(string); path != "" {
				if err := agentMayRead(workspace, path); err != nil {
					return err
				}
			}
		}
		return nil
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		for _, key := range []string{"file_path", "notebook_path"} {
			if path, _ := input[key].(string); path != "" {
				return agentMayWrite(workspace, path)
			}
		}
		return errors.New("Pearl can't tell which file this edit changes")
	case "Bash":
		// Sandboxed commands are allowed without asking, so one that reaches
		// Pearl can't be sandboxed on this system.
		if permissions.AllowUnsandboxed {
			return nil
		}
		return errors.New("this command can't run in a sandbox here. Set allow_unsandboxed_commands in Pearl's settings to allow it")
	case "SandboxNetworkAccess", "WebFetch":
		if permissions.AllowNetwork {
			return nil
		}
		return errors.New("network access is off for Pearl jobs. Set allow_command_network in Pearl's settings to allow it")
	case "WebSearch", "TodoWrite", "Task", "Agent", "TaskOutput", "TaskStop", "BashOutput", "KillShell", "ToolSearch", "Skill":
		return nil
	case "AskUserQuestion":
		return errors.New("nobody can answer during this turn. End your turn with a final message that starts with " + codexQuestionMarker + " and your question")
	}
	return fmt.Errorf("Pearl doesn't allow %s in unattended jobs", tool)
}

type claudePendingTool struct {
	name  string
	input map[string]any
}

type claudeEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	RequestID string `json:"request_id"`
	Parent    string `json:"parent_tool_use_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Errors    []any  `json:"errors"`
	Request   *struct {
		Subtype  string         `json:"subtype"`
		ToolName string         `json:"tool_name"`
		Input    map[string]any `json:"input"`
	} `json:"request"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeSession struct {
	parser      *codexEventParser
	permissions openrouter_request.CommandPermissions
	stdin       io.WriteCloser
	writeLock   sync.Mutex
	closeOnce   sync.Once
	pending     map[string]claudePendingTool
	reported    bool
	finished    bool
	failure     string
}

func (s *claudeSession) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

func (s *claudeSession) closeInput() {
	s.closeOnce.Do(func() { _ = s.stdin.Close() })
}

func (s *claudeSession) read(output io.Reader) error {
	reader := bufio.NewReaderSize(output, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var event claudeEvent
			if json.Unmarshal(trimmed, &event) == nil {
				if handleErr := s.handle(event); handleErr != nil {
					return handleErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Claude Code output: %w", err)
		}
	}
}

func (s *claudeSession) handle(event claudeEvent) error {
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			return s.started(event)
		}
	case "control_request":
		return s.answerControlRequest(event)
	case "assistant":
		return s.assistantMessage(event)
	case "user":
		return s.toolResults(event)
	case "result":
		s.finished = true
		// Nothing more will be asked, so let the process exit.
		s.closeInput()
		if event.IsError || (event.Subtype != "" && event.Subtype != "success") {
			detail := strings.TrimSpace(event.Result)
			if detail == "" && len(event.Errors) > 0 {
				detail = strings.TrimSpace(fmt.Sprint(event.Errors...))
			}
			if detail == "" {
				detail = event.Subtype
			}
			s.failure = "Claude Code failed: " + detail
			return nil
		}
		if s.parser.lastAnswer == "" && s.parser.question == "" {
			return s.parser.completeItem(codexItem{Type: "agent_message", Text: event.Result})
		}
	}
	return nil
}

func (s *claudeSession) started(event claudeEvent) error {
	if event.SessionID != "" {
		if err := s.parser.emit(claudeSessionEvent, event.SessionID); err != nil {
			return err
		}
	}
	if event.Model != "" && !s.reported {
		s.reported = true
		return s.parser.emit("model", event.Model)
	}
	return nil
}

func (s *claudeSession) answerControlRequest(event claudeEvent) error {
	if event.Request == nil || event.Request.Subtype != "can_use_tool" {
		return s.write(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": event.RequestID, "error": "Pearl doesn't support this request",
		}})
	}
	input := event.Request.Input
	if input == nil {
		input = map[string]any{}
	}
	decision := map[string]any{"behavior": "allow", "updatedInput": input}
	if err := claudeToolDecision(event.Request.ToolName, input, s.parser.job.WorkspaceRoot, s.permissions); err != nil {
		decision = map[string]any{"behavior": "deny", "message": err.Error()}
	}
	return s.write(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": event.RequestID, "response": decision,
	}})
}

func claudeBlocks(event claudeEvent) []claudeBlock {
	if event.Message == nil {
		return nil
	}
	var blocks []claudeBlock
	if json.Unmarshal(event.Message.Content, &blocks) != nil {
		return nil
	}
	return blocks
}

func (s *claudeSession) assistantMessage(event claudeEvent) error {
	for _, block := range claudeBlocks(event) {
		var err error
		switch block.Type {
		case "text":
			// A subagent's messages are working notes, not the job's answer.
			if event.Parent == "" {
				err = s.parser.completeItem(codexItem{Type: "agent_message", Text: block.Text})
			}
		case "thinking":
			err = s.parser.completeItem(codexItem{Type: "reasoning", Text: block.Thinking})
		case "tool_use":
			err = s.toolStarted(block)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *claudeSession) toolStarted(block claudeBlock) error {
	s.pending[block.ID] = claudePendingTool{name: block.Name, input: block.Input}
	path, _ := block.Input["file_path"].(string)
	switch block.Name {
	case "Bash":
		command, _ := block.Input["command"].(string)
		if verification.Shell(command) {
			if err := s.parser.emit("verification", verification.Event("running", command)); err != nil {
				return err
			}
		}
		return s.parser.emit("tool", "shell: "+command)
	case "Edit", "MultiEdit", "Write":
		if verification.AffectsTests(path) {
			if err := s.parser.emit("verification", verification.Event("not_run", "")); err != nil {
				return err
			}
		}
		return s.parser.emit("tool", "edit: "+codexRelativePath(s.parser.job.WorkspaceRoot, path))
	}
	return s.parser.emit("tool", block.Name)
}

func (s *claudeSession) toolResults(event claudeEvent) error {
	for _, block := range claudeBlocks(event) {
		if block.Type != "tool_result" {
			continue
		}
		tool, ok := s.pending[block.ToolUseID]
		if !ok {
			continue
		}
		delete(s.pending, block.ToolUseID)
		if err := s.recordTool(block, tool); err != nil {
			return err
		}
	}
	return nil
}

func (s *claudeSession) recordTool(block claudeBlock, tool claudePendingTool) error {
	output := claudeToolOutput(block.Content)
	failure := ""
	if block.IsError {
		failure = strings.TrimSpace(output)
		if failure == "" {
			failure = "tool failed"
		}
	}
	name, arguments := claudeToolRecord(tool, s.parser.job.WorkspaceRoot)
	if tool.name == "Bash" {
		command, _ := tool.input["command"].(string)
		if verification.Shell(command) {
			status := "passed"
			if block.IsError {
				status = "failed"
			}
			if err := s.parser.emit("verification", verification.Event(status, command)); err != nil {
				return err
			}
		}
	}
	return s.parser.recordTool(block.ToolUseID, name, arguments, codexTruncate(output), !block.IsError, failure)
}

// claudeToolRecord names a tool the way Pearl's job view expects, so changed
// files show up the same as with the other harnesses.
func claudeToolRecord(tool claudePendingTool, workspace string) (name, arguments string) {
	path, _ := tool.input["file_path"].(string)
	relative := func() string {
		data, _ := json.Marshal(map[string]string{"relative_path": codexRelativePath(workspace, path)})
		return string(data)
	}
	switch tool.name {
	case "Bash":
		command, _ := tool.input["command"].(string)
		data, _ := json.Marshal(map[string]string{"command": command})
		return "run_command", string(data)
	case "Write":
		return "create_file", relative()
	case "Edit", "MultiEdit":
		return "apply_patch", relative()
	case "Read":
		return "read_file", relative()
	}
	data, err := json.Marshal(tool.input)
	if err != nil {
		return tool.name, "{}"
	}
	return tool.name, codexTruncate(string(data))
}

// claudeToolOutput reads a tool result, which is either a string or a list of
// content blocks.
func claudeToolOutput(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
