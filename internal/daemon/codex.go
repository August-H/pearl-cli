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
	"strconv"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	codexAPIKeyEnvironment  = "PEARL_OPENROUTER_API_KEY"
	codexPathEnvironment    = "PEARL_CODEX_PATH"
	codexThreadEvent        = "codex_thread"
	codexMaxToolOutputBytes = 64 << 10
	codexStderrTailBytes    = 4 << 10
	codexContinuePrompt     = "Continue working on the original task. If it is already complete, check the result and summarize it."
	codexModelMetadataError = "Model metadata for"
	codexInputUsedEvent     = "codex_input_used"
	codexQuestionIDPrefix   = "codex-question-"
	codexQuestionMarker     = "PEARL_QUESTION:"
	codexInstructions       = "You are running as an unattended background job for Pearl. Nobody can reply to you during this turn. " +
		"When you need a decision, missing information, or clarification from the user before you can finish, end your turn with a final message whose first line starts with exactly " +
		codexQuestionMarker + " followed by your question. Pearl pauses the job, shows the question to the user, and sends you their answer. " +
		"Never ask the user a question any other way, and never use " + codexQuestionMarker + " when you are not asking."
	codexQuestionReminder = "\n\n(Pearl: if you need to ask me anything, start your final message with " +
		codexQuestionMarker + " and I will answer.)"
)

// CodexRunner runs jobs with the Codex CLI, sending model requests to
// OpenRouter with the model and API key configured in Pearl.
type CodexRunner struct {
	Executable string
}

type codexThreadStore interface {
	EventsOfType(ctx context.Context, jobID, eventType string) ([]store.Event, error)
}

// FindCodex locates the Codex CLI. The daemon may run with a minimal PATH, so
// common install locations are checked as well.
func FindCodex() (string, error) {
	if path := strings.TrimSpace(os.Getenv(codexPathEnvironment)); path != "" {
		return path, nil
	}
	if path, err := exec.LookPath("codex"); err == nil {
		return path, nil
	}
	candidates := []string{"/opt/homebrew/bin/codex", "/usr/local/bin/codex"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append([]string{
			filepath.Join(home, ".local", "bin", "codex"),
			filepath.Join(home, ".npm-global", "bin", "codex"),
			filepath.Join(home, ".bun", "bin", "codex"),
		}, candidates...)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("Codex CLI not found; install it (npm install -g @openai/codex) or set " + codexPathEnvironment)
}

func (r CodexRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		return "", err
	}
	apiKey, err := openrouter_request.LoadOpenRouterAPIKey()
	if err != nil {
		return "", err
	}
	if err := openrouter_request.ValidateApprovedWorkspace(
		job.WorkspaceRoot, settings.Approved_workspace_roots,
	); err != nil {
		return "", err
	}
	executable := strings.TrimSpace(r.Executable)
	if executable == "" {
		executable, err = FindCodex()
		if err != nil {
			return "", err
		}
	}
	maxDuration := time.Duration(settings.Max_job_seconds) * time.Second
	if maxDuration <= 0 {
		maxDuration = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	threadID, answer, err := codexResumeState(ctx, job, state)
	if err != nil {
		return "", err
	}
	prompt := job.Prompt
	var transcript []codexTranscriptMessage
	if threadID != "" {
		if saved, err := state.LoadTranscript(ctx, job.ID); err == nil && len(saved) > 0 {
			_ = json.Unmarshal(saved, &transcript)
		}
	}
	switch {
	case answer != "" && threadID != "":
		prompt = answer
	case answer != "":
		prompt = job.Prompt + "\n\nYour earlier question was answered: " + answer
	case threadID != "":
		prompt = codexContinuePrompt
	}
	if answer != "" && events != nil {
		if err := events(openrouter_request.AgentEvent{
			Type: codexInputUsedEvent, Data: job.InputToolCallID,
		}); err != nil {
			return "", fmt.Errorf("record answer: %w", err)
		}
	}
	transcript = append(transcript, codexTranscriptMessage{Role: "user", Content: prompt})

	command := exec.CommandContext(ctx, executable, codexArguments(
		strings.TrimSpace(settings.Model), codexContextWindow(settings.Model), threadID,
	)...)
	command.Dir = job.WorkspaceRoot
	command.Env = append(os.Environ(), codexAPIKeyEnvironment+"="+apiKey)
	command.Stdin = strings.NewReader(prompt + codexQuestionReminder)
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 5 * time.Second
	var stderr codexTailBuffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("connect to Codex: %w", err)
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start Codex: %w", err)
	}

	parser := &codexEventParser{
		ctx:        ctx,
		job:        job,
		events:     events,
		state:      state,
		transcript: transcript,
		idPrefix:   fmt.Sprintf("codex-%d-", time.Now().UnixNano()),
	}
	parseErr := parser.read(stdout)
	waitErr := command.Wait()
	if saved, err := json.Marshal(parser.transcript); err == nil {
		saveContext, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = state.SaveTranscript(saveContext, job.ID, saved)
		saveCancel()
	}

	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("Codex stopped: %w", ctx.Err())
	case parseErr != nil:
		return "", parseErr
	case parser.failure != "":
		return "", errors.New(parser.failure)
	case waitErr != nil:
		message := strings.TrimSpace(parser.lastError)
		if message == "" {
			message = strings.TrimSpace(stderr.String())
		}
		if message == "" {
			message = waitErr.Error()
		}
		return "", fmt.Errorf("Codex failed: %s", message)
	case !parser.completed:
		return "", errors.New("Codex exited before finishing the task")
	case parser.question != "":
		return "", &openrouter_request.UserInputRequiredError{
			ToolCallID: fmt.Sprintf("%s%d", codexQuestionIDPrefix, time.Now().UnixNano()),
			Question:   parser.question,
		}
	}
	return parser.lastAnswer, nil
}

// codexResumeState returns the Codex session to resume, if any, and the user's
// answer to a question Codex asked that has not been sent to Codex yet.
func codexResumeState(
	ctx context.Context,
	job store.Job,
	state openrouter_request.CheckpointStore,
) (threadID, answer string, err error) {
	threads, ok := state.(codexThreadStore)
	if !ok {
		return "", "", nil
	}
	previous, err := threads.EventsOfType(ctx, job.ID, codexThreadEvent)
	if err != nil {
		return "", "", fmt.Errorf("load Codex session: %w", err)
	}
	if len(previous) > 0 {
		threadID = previous[len(previous)-1].Data
	}
	if !strings.HasPrefix(job.InputToolCallID, codexQuestionIDPrefix) {
		return threadID, "", nil
	}
	response, found, err := state.LoadUserInputResponse(ctx, job.ID, job.InputToolCallID)
	if err != nil || !found {
		return threadID, "", err
	}
	used, err := threads.EventsOfType(ctx, job.ID, codexInputUsedEvent)
	if err != nil {
		return "", "", fmt.Errorf("load answered questions: %w", err)
	}
	for _, event := range used {
		if event.Data == job.InputToolCallID {
			return threadID, "", nil
		}
	}
	return threadID, response, nil
}

// codexQuestion returns the question in a message that asks the user one, or "".
// Models often wrap the marker line in Markdown, as in **PEARL_QUESTION: ...**.
func codexQuestion(message string) string {
	const markdown = "*_#>` \t"
	lines := strings.Split(message, "\n")
	for index, line := range lines {
		trimmed := strings.TrimLeft(strings.TrimSpace(line), markdown)
		if !strings.HasPrefix(trimmed, codexQuestionMarker) {
			continue
		}
		first := strings.TrimPrefix(trimmed, codexQuestionMarker)
		first = strings.TrimRight(strings.TrimLeft(first, markdown), markdown)
		rest := append([]string{first}, lines[index+1:]...)
		return strings.TrimSpace(strings.Join(rest, "\n"))
	}
	return ""
}

func codexArguments(model string, contextWindow int64, threadID string) []string {
	arguments := []string{"exec"}
	if threadID != "" {
		arguments = append(arguments, "resume")
	}
	arguments = append(arguments,
		"--json", "--skip-git-repo-check", "--ignore-user-config",
		"-c", `model_provider="openrouter"`,
		"-c", `model_providers.openrouter.name="OpenRouter"`,
		"-c", `model_providers.openrouter.base_url="https://openrouter.ai/api/v1"`,
		"-c", `model_providers.openrouter.env_key="`+codexAPIKeyEnvironment+`"`,
		"-c", `model_providers.openrouter.wire_api="responses"`,
		"-c", `sandbox_mode="workspace-write"`,
		"-c", `approval_policy="never"`,
		"-c", "developer_instructions="+strconv.Quote(codexInstructions),
	)
	if contextWindow > 0 {
		arguments = append(arguments, "-c", "model_context_window="+strconv.FormatInt(contextWindow, 10))
	}
	arguments = append(arguments, "-m", model)
	if threadID != "" {
		arguments = append(arguments, threadID)
	}
	return append(arguments, "-")
}

func codexContextWindow(model string) int64 {
	models, _, ok := openrouter_request.LoadCachedModels(30 * 24 * time.Hour)
	if !ok {
		return 0
	}
	for _, candidate := range models {
		if candidate.ID == strings.TrimSpace(model) {
			return candidate.ContextLength
		}
	}
	return 0
}

type codexTranscriptMessage struct {
	Role       string                `json:"role"`
	Content    string                `json:"content,omitempty"`
	Reasoning  string                `json:"reasoning,omitempty"`
	Name       string                `json:"name,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolCalls  []codexTranscriptCall `json:"tool_calls,omitempty"`
}

type codexTranscriptCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type codexEvent struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Message  string     `json:"message"`
	Item     *codexItem `json:"item"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Message          string `json:"message"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	Status           string `json:"status"`
	Query            string `json:"query"`
	Server           string `json:"server"`
	Tool             string `json:"tool"`
	Changes          []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
}

type codexEventParser struct {
	ctx        context.Context
	job        store.Job
	events     openrouter_request.EventSink
	state      openrouter_request.CheckpointStore
	transcript []codexTranscriptMessage
	idPrefix   string

	reasoning     string
	lastEmitted   string
	lastAnswer    string
	question      string
	lastError     string
	failure       string
	completed     bool
	startedEvents map[string]bool
}

func (p *codexEventParser) read(output io.Reader) error {
	reader := bufio.NewReaderSize(output, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var event codexEvent
			if json.Unmarshal(trimmed, &event) == nil {
				if handleErr := p.handle(event); handleErr != nil {
					return handleErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Codex output: %w", err)
		}
	}
}

func (p *codexEventParser) handle(event codexEvent) error {
	switch event.Type {
	case "thread.started":
		if event.ThreadID != "" {
			return p.emit(codexThreadEvent, event.ThreadID)
		}
	case "turn.completed":
		p.completed = true
	case "turn.failed":
		if event.Error != nil {
			p.failure = "Codex failed: " + event.Error.Message
		} else {
			p.failure = "Codex failed"
		}
	case "error":
		p.lastError = event.Message
	case "item.started":
		if event.Item != nil && event.Item.Type == "command_execution" {
			if p.startedEvents == nil {
				p.startedEvents = make(map[string]bool)
			}
			p.startedEvents[event.Item.ID] = true
			return p.emit("tool", "shell: "+codexDisplayCommand(event.Item.Command))
		}
	case "item.completed":
		if event.Item != nil {
			return p.completeItem(*event.Item)
		}
	}
	return nil
}

func (p *codexEventParser) completeItem(item codexItem) error {
	switch item.Type {
	case "agent_message":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil
		}
		if question := codexQuestion(text); question != "" {
			p.question = question
			p.transcript = append(p.transcript, codexTranscriptMessage{
				Role: "assistant", Content: question, Reasoning: p.takeReasoning(),
			})
			return nil
		}
		p.question = ""
		p.lastAnswer = text
		p.transcript = append(p.transcript, codexTranscriptMessage{
			Role: "assistant", Content: text, Reasoning: p.takeReasoning(),
		})
		return p.emitSection("answer", text)
	case "reasoning":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil
		}
		if p.reasoning != "" {
			p.reasoning += "\n\n"
		}
		p.reasoning += text
		return p.emitSection("reasoning", text)
	case "command_execution":
		command := codexDisplayCommand(item.Command)
		if !p.startedEvents[item.ID] {
			if err := p.emit("tool", "shell: "+command); err != nil {
				return err
			}
		}
		success := item.ExitCode != nil && *item.ExitCode == 0
		failure := ""
		if !success {
			failure = "command failed"
			if item.ExitCode != nil {
				failure = fmt.Sprintf("exit code %d", *item.ExitCode)
			}
		}
		arguments, _ := json.Marshal(map[string]string{"command": command})
		return p.recordTool(item.ID, "run_command", string(arguments),
			codexTruncate(item.AggregatedOutput), success, failure)
	case "file_change":
		success := item.Status == "" || item.Status == "completed"
		for index, change := range item.Changes {
			name := "apply_patch"
			switch change.Kind {
			case "add":
				name = "create_file"
			case "delete":
				name = "delete_file"
			}
			path := codexRelativePath(p.job.WorkspaceRoot, change.Path)
			if err := p.emit("tool", "edit: "+path); err != nil {
				return err
			}
			arguments, _ := json.Marshal(map[string]string{"relative_path": path})
			failure := ""
			if !success {
				failure = "file change " + item.Status
			}
			if err := p.recordTool(fmt.Sprintf("%s-%d", item.ID, index), name,
				string(arguments), change.Kind+" "+path, success, failure); err != nil {
				return err
			}
		}
	case "mcp_tool_call":
		name := "mcp: " + item.Server + "/" + item.Tool
		if err := p.emit("tool", name); err != nil {
			return err
		}
		success := item.Status == "completed"
		failure := ""
		if !success {
			failure = "tool call " + item.Status
		}
		return p.recordTool(item.ID, name, "{}", "", success, failure)
	case "web_search":
		if err := p.emit("tool", "web search: "+item.Query); err != nil {
			return err
		}
		arguments, _ := json.Marshal(map[string]string{"question": item.Query})
		return p.recordTool(item.ID, "web_search", string(arguments), "", true, "")
	case "error":
		message := strings.TrimSpace(item.Message)
		if message != "" && !strings.HasPrefix(message, codexModelMetadataError) {
			return p.emit("context", message)
		}
	}
	return nil
}

func (p *codexEventParser) recordTool(
	itemID, name, arguments, output string,
	success bool,
	failure string,
) error {
	callID := p.idPrefix + itemID
	result, err := json.Marshal(map[string]any{
		"success": success, "result": output, "error": failure,
	})
	if err != nil {
		return err
	}
	if err := p.state.SaveToolResult(p.ctx, p.job.ID, callID, name, arguments, result); err != nil {
		return fmt.Errorf("save Codex tool result: %w", err)
	}
	call := codexTranscriptCall{ID: callID, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = arguments
	p.transcript = append(p.transcript,
		codexTranscriptMessage{
			Role: "assistant", Reasoning: p.takeReasoning(),
			ToolCalls: []codexTranscriptCall{call},
		},
		codexTranscriptMessage{
			Role: "tool", Name: name, ToolCallID: callID, Content: string(result),
		},
	)
	return nil
}

func (p *codexEventParser) takeReasoning() string {
	reasoning := p.reasoning
	p.reasoning = ""
	return reasoning
}

// emitSection separates consecutive messages of the same kind, which attach
// prints under a single heading.
func (p *codexEventParser) emitSection(eventType, text string) error {
	if p.lastEmitted == eventType {
		text = "\n\n" + text
	}
	return p.emit(eventType, text)
}

func (p *codexEventParser) emit(eventType, data string) error {
	p.lastEmitted = eventType
	if p.events == nil {
		return nil
	}
	if err := p.events(openrouter_request.AgentEvent{Type: eventType, Data: data}); err != nil {
		return fmt.Errorf("emit %s event: %w", eventType, err)
	}
	return nil
}

var codexShellWrapper = regexp.MustCompile(`(?s)^\S*/(?:ba|z)?sh -lc (.+)$`)

// codexDisplayCommand removes the login-shell wrapper Codex adds to commands.
func codexDisplayCommand(command string) string {
	command = strings.TrimSpace(command)
	match := codexShellWrapper.FindStringSubmatch(command)
	if match == nil {
		return command
	}
	inner := match[1]
	if len(inner) >= 2 && inner[0] == '\'' && inner[len(inner)-1] == '\'' {
		inner = strings.ReplaceAll(inner[1:len(inner)-1], `'\''`, `'`)
	} else if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
		inner = inner[1 : len(inner)-1]
	}
	return inner
}

func codexRelativePath(workspace, path string) string {
	if relative, err := filepath.Rel(workspace, path); err == nil &&
		relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(path)
}

func codexTruncate(value string) string {
	if len(value) <= codexMaxToolOutputBytes {
		return value
	}
	return strings.ToValidUTF8(value[:codexMaxToolOutputBytes], "") + "\n[output truncated]"
}

type codexTailBuffer struct {
	data []byte
}

func (b *codexTailBuffer) Write(chunk []byte) (int, error) {
	b.data = append(b.data, chunk...)
	if len(b.data) > codexStderrTailBytes {
		b.data = b.data[len(b.data)-codexStderrTailBytes:]
	}
	return len(chunk), nil
}

func (b *codexTailBuffer) String() string {
	lines := strings.Split(string(b.data), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, "PATH aliases") || strings.Contains(line, "Reading additional input from stdin") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
