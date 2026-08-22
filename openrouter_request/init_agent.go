package openrouter_request

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/agent_functions"
)

const openRouterChatURL = "https://openrouter.ai/api/v1/chat/completions"

type agentToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type agentMessage struct {
	Role             string            `json:"role"`
	Content          string            `json:"content,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	ToolCalls        []agentToolCall   `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	Name             string            `json:"name,omitempty"`
}

type agentStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type agentStreamDelta struct {
	Content          string                `json:"content"`
	Reasoning        string                `json:"reasoning"`
	ReasoningContent string                `json:"reasoning_content"`
	ReasoningDetails []json.RawMessage     `json:"reasoning_details"`
	ToolCalls        []agentStreamToolCall `json:"tool_calls"`
}

type reasoningDetailText struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Summary string `json:"summary"`
}

type agentStreamChunk struct {
	Choices []struct {
		Delta agentStreamDelta `json:"delta"`
	} `json:"choices"`
	Error *APIError `json:"error,omitempty"`
}

type agentToolResult struct {
	Success bool   `json:"success"`
	Result  any    `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

type AgentEvent struct {
	Type string
	Data string
}

type EventSink func(AgentEvent) error

const requestUserInputTool = "request_user_input"

type UserInputRequiredError struct {
	ToolCallID string
	Question   string
}

func (e *UserInputRequiredError) Error() string {
	return "user input required: " + e.Question
}

// CheckpointStore makes a run resumable and prevents an already-recorded tool
// call from being executed a second time after a retry.
type CheckpointStore interface {
	LoadTranscript(ctx context.Context, jobID string) ([]byte, error)
	SaveTranscript(ctx context.Context, jobID string, transcript []byte) error
	LoadUserInputResponse(ctx context.Context, jobID, toolCallID string) (string, bool, error)
	LoadToolResult(ctx context.Context, jobID, toolCallID string) ([]byte, bool, error)
	SaveToolResult(
		ctx context.Context,
		jobID, toolCallID, toolName, arguments string,
		result []byte,
	) error
}

type RunOptions struct {
	JobID         string
	WorkspaceRoot string
	MaxDuration   time.Duration
	MaxFileBytes  int64
	MaxToolDepth  int
	SystemPrompt  string
	Tools         []Tool
	OnlyTools     bool
	Events        EventSink
	State         CheckpointStore
}

// Tool adds a function that the model can call during a run. Parameters must
// be a JSON Schema object describing the function arguments.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Execute     func(context.Context, json.RawMessage) (any, error)
}

var sharedAgentHTTPClient = &http.Client{Timeout: 5 * time.Minute}

// Init_agent sends a prompt to the configured model.
func Init_agent(prompt string) (string, error) {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	currentSection := ""
	events := func(event AgentEvent) error {
		if event.Type != "reasoning" && event.Type != "answer" {
			if event.Type == "tool" {
				fmt.Printf("[tool] %s\n", event.Data)
			}
			return nil
		}
		if currentSection != event.Type {
			if currentSection != "" {
				fmt.Println()
			}
			fmt.Printf("[%s]\n", event.Type)
			currentSection = event.Type
		}
		fmt.Print(event.Data)
		return nil
	}
	result, err := Run(context.Background(), prompt, RunOptions{
		WorkspaceRoot: workspaceRoot,
		Events:        events,
	})
	if currentSection != "" {
		fmt.Println()
	}
	return result, err
}

// Run executes one durable agent job. The caller owns process lifecycle,
// cancellation, event persistence, and checkpoint storage.
func Run(ctx context.Context, prompt string, options RunOptions) (string, error) {
	if len(strings.TrimSpace(prompt)) == 0 {
		return "", errors.New("prompt cannot be empty")
	}

	apiKey, err := loadOpenRouterAPIKey()
	if err != nil {
		return "", err
	}

	settings, err := loadAgentSettings()
	if err != nil {
		return "", err
	}

	maxDepth := options.MaxToolDepth
	if maxDepth <= 0 {
		maxDepth = settings.Max_depth
	}
	if maxDepth <= 0 {
		maxDepth = 8
	}

	maxDuration := options.MaxDuration
	if maxDuration <= 0 && settings.Max_job_seconds > 0 {
		maxDuration = time.Duration(settings.Max_job_seconds) * time.Second
	}
	if maxDuration <= 0 {
		maxDuration = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	workspaceRoot := strings.TrimSpace(options.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	workspaceInfo, err := os.Stat(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("inspect workspace root: %w", err)
	}
	if !workspaceInfo.IsDir() {
		return "", fmt.Errorf("workspace root %q is not a directory", workspaceRoot)
	}
	if err := validateApprovedWorkspace(workspaceRoot, settings.Approved_workspace_roots); err != nil {
		return "", err
	}

	maxFileBytes := options.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = settings.Max_file_bytes
	}
	if maxFileBytes <= 0 {
		maxFileBytes = 4 << 20
	}

	messages := make([]agentMessage, 0, 2)
	if systemPrompt := strings.TrimSpace(options.SystemPrompt); systemPrompt != "" {
		messages = append(messages, agentMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, agentMessage{Role: "user", Content: prompt})
	loadedTranscript := false
	if options.State != nil && options.JobID != "" {
		transcript, err := options.State.LoadTranscript(ctx, options.JobID)
		if err != nil {
			return "", fmt.Errorf("load agent transcript: %w", err)
		}
		if len(transcript) > 0 {
			loadedTranscript = true
			if err := json.Unmarshal(transcript, &messages); err != nil {
				return "", fmt.Errorf("decode agent transcript: %w", err)
			}
			if len(messages) == 0 {
				messages = []agentMessage{{Role: "user", Content: prompt}}
			}
		} else if err := saveTranscript(ctx, options, messages); err != nil {
			return "", err
		}
	}

	if loadedTranscript && len(messages) > 0 {
		lastMessage := messages[len(messages)-1]
		if lastMessage.Role == "assistant" && len(lastMessage.ToolCalls) == 0 {
			return lastMessage.Content, nil
		}
	}
	completedMessages, err := completePendingToolCalls(
		ctx, messages, options, workspaceRoot, maxFileBytes,
	)
	if err != nil {
		return "", err
	}
	messages = completedMessages

	toolDepth := countToolRounds(messages)
	for {
		assistantMessage, err := requestAgentCompletion(
			ctx,
			sharedAgentHTTPClient,
			apiKey,
			settings.Model,
			messages,
			options.Events,
			agentTools(options),
		)
		if err != nil {
			return "", err
		}

		messages = append(messages, assistantMessage)
		if err := saveTranscript(ctx, options, messages); err != nil {
			return "", err
		}
		if len(assistantMessage.ToolCalls) == 0 {
			return assistantMessage.Content, nil
		}
		if toolDepth >= maxDepth {
			return "", fmt.Errorf("agent exceeded maximum tool depth of %d", maxDepth)
		}

		for _, call := range assistantMessage.ToolCalls {
			if options.Events != nil {
				if err := options.Events(AgentEvent{Type: "tool", Data: call.Function.Name}); err != nil {
					return "", fmt.Errorf("emit tool event: %w", err)
				}
			}
			toolMessage, err := executeAgentTool(
				ctx, call, options, workspaceRoot, maxFileBytes,
			)
			if err != nil {
				return "", err
			}
			messages = append(messages, toolMessage)
			if err := saveTranscript(ctx, options, messages); err != nil {
				return "", err
			}
		}
		toolDepth++
	}
}

func countToolRounds(messages []agentMessage) int {
	rounds := 0
	for _, message := range messages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			rounds++
		}
	}
	return rounds
}

func completePendingToolCalls(
	ctx context.Context,
	messages []agentMessage,
	options RunOptions,
	workspaceRoot string,
	maxFileBytes int64,
) ([]agentMessage, error) {
	assistantIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "assistant" {
			assistantIndex = index
			break
		}
	}
	if assistantIndex < 0 || len(messages[assistantIndex].ToolCalls) == 0 {
		return messages, nil
	}
	completed := make(map[string]bool)
	for _, message := range messages[assistantIndex+1:] {
		if message.Role == "tool" {
			completed[message.ToolCallID] = true
		}
	}
	for _, call := range messages[assistantIndex].ToolCalls {
		if completed[call.ID] {
			continue
		}
		if options.Events != nil {
			if err := options.Events(AgentEvent{Type: "tool", Data: call.Function.Name}); err != nil {
				return nil, fmt.Errorf("emit resumed tool event: %w", err)
			}
		}
		toolMessage, err := executeAgentTool(ctx, call, options, workspaceRoot, maxFileBytes)
		if err != nil {
			return nil, err
		}
		messages = append(messages, toolMessage)
		if err := saveTranscript(ctx, options, messages); err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func saveTranscript(ctx context.Context, options RunOptions, messages []agentMessage) error {
	if options.State == nil || options.JobID == "" {
		return nil
	}
	transcript, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("encode agent transcript: %w", err)
	}
	if err := options.State.SaveTranscript(ctx, options.JobID, transcript); err != nil {
		return fmt.Errorf("save agent transcript: %w", err)
	}
	return nil
}

func validateApprovedWorkspace(workspaceRoot string, approvedRoots []string) error {
	if len(approvedRoots) == 0 {
		return nil
	}
	for _, approvedRoot := range approvedRoots {
		approvedRoot = strings.TrimSpace(approvedRoot)
		if approvedRoot == "" {
			continue
		}
		absoluteRoot, err := filepath.Abs(approvedRoot)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(absoluteRoot, workspaceRoot)
		if err == nil && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return nil
		}
	}
	return fmt.Errorf("workspace %q is not beneath an approved workspace root", workspaceRoot)
}

func loadAgentSettings() (Settings, error) {
	settingsFiles := pearlConfigFiles("settings.json", true)
	for _, settingsPath := range settingsFiles {
		settingsFile, err := os.Open(settingsPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Settings{}, fmt.Errorf("open %s: %w", settingsPath, err)
		}

		var settings Settings
		decodeErr := json.NewDecoder(settingsFile).Decode(&settings)
		closeErr := settingsFile.Close()
		if decodeErr != nil {
			return Settings{}, fmt.Errorf("decode %s: %w", settingsPath, decodeErr)
		}
		if closeErr != nil {
			return Settings{}, fmt.Errorf("close %s: %w", settingsPath, closeErr)
		}
		if settings.Model == "" {
			return Settings{}, fmt.Errorf("%s does not specify a model", settingsPath)
		}
		return settings, nil
	}

	return Settings{}, fmt.Errorf(
		"settings.json not found; create %s",
		settingsFiles[0],
	)
}

func requestAgentCompletion(
	ctx context.Context,
	client *http.Client,
	apiKey string,
	model string,
	messages []agentMessage,
	events EventSink,
	tools []map[string]any,
) (agentMessage, error) {
	payload := map[string]any{
		"model":               model,
		"messages":            messages,
		"reasoning":           Reasoning{Enabled: true},
		"stream":              true,
		"tools":               tools,
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return agentMessage{}, fmt.Errorf("encode agent request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		openRouterChatURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return agentMessage{}, fmt.Errorf("create OpenRouter request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return agentMessage{}, fmt.Errorf("send OpenRouter request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if readErr != nil {
			return agentMessage{}, fmt.Errorf("read OpenRouter error response: %w", readErr)
		}
		return agentMessage{}, fmt.Errorf(
			"OpenRouter returned %s: %s",
			resp.Status,
			strings.TrimSpace(string(responseBody)),
		)
	}

	assistantMessage := agentMessage{Role: "assistant"}
	streamScanner := bufio.NewScanner(resp.Body)
	streamScanner.Buffer(make([]byte, 64*1024), 4<<20)
	sawChoice := false
	emit := func(eventType, data string) error {
		if data == "" || events == nil {
			return nil
		}
		return events(AgentEvent{Type: eventType, Data: data})
	}

	for streamScanner.Scan() {
		line := streamScanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var chunk agentStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return agentMessage{}, fmt.Errorf("decode OpenRouter stream: %w", err)
		}
		if chunk.Error != nil {
			return agentMessage{}, fmt.Errorf(
				"OpenRouter stream error (%v): %s",
				chunk.Error.Code,
				chunk.Error.Message,
			)
		}

		for _, choice := range chunk.Choices {
			sawChoice = true
			reasoning := choice.Delta.Reasoning
			if reasoning == "" {
				reasoning = choice.Delta.ReasoningContent
			}
			if reasoning != "" {
				assistantMessage.Reasoning += reasoning
				if err := emit("reasoning", reasoning); err != nil {
					return agentMessage{}, fmt.Errorf("emit reasoning event: %w", err)
				}
			}

			for _, detail := range choice.Delta.ReasoningDetails {
				assistantMessage.ReasoningDetails = append(
					assistantMessage.ReasoningDetails,
					detail,
				)
				if reasoning == "" {
					if err := emit("reasoning", printableReasoningDetail(detail)); err != nil {
						return agentMessage{}, fmt.Errorf("emit reasoning event: %w", err)
					}
				}
			}

			if choice.Delta.Content != "" {
				assistantMessage.Content += choice.Delta.Content
				if err := emit("answer", choice.Delta.Content); err != nil {
					return agentMessage{}, fmt.Errorf("emit answer event: %w", err)
				}
			}
			for _, toolCallDelta := range choice.Delta.ToolCalls {
				if err := mergeAgentToolCall(
					&assistantMessage.ToolCalls,
					toolCallDelta,
				); err != nil {
					return agentMessage{}, err
				}
			}
		}
	}

	if err := streamScanner.Err(); err != nil {
		return agentMessage{}, fmt.Errorf("read OpenRouter stream: %w", err)
	}
	if !sawChoice {
		return agentMessage{}, errors.New("OpenRouter stream returned no choices")
	}
	if len(assistantMessage.ReasoningDetails) > 0 {
		assistantMessage.Reasoning = ""
	}

	return assistantMessage, nil
}

func printableReasoningDetail(detail json.RawMessage) string {
	var reasoningDetail reasoningDetailText
	if err := json.Unmarshal(detail, &reasoningDetail); err != nil {
		return ""
	}

	switch reasoningDetail.Type {
	case "reasoning.text":
		return reasoningDetail.Text
	case "reasoning.summary":
		return reasoningDetail.Summary
	default:
		return ""
	}
}

func mergeAgentToolCall(
	toolCalls *[]agentToolCall,
	delta agentStreamToolCall,
) error {
	if delta.Index < 0 || delta.Index > 1000 {
		return fmt.Errorf("invalid streamed tool-call index %d", delta.Index)
	}

	for len(*toolCalls) <= delta.Index {
		*toolCalls = append(*toolCalls, agentToolCall{})
	}

	toolCall := &(*toolCalls)[delta.Index]
	if delta.ID != "" {
		toolCall.ID = delta.ID
	}
	if delta.Type != "" {
		toolCall.Type = delta.Type
	}
	toolCall.Function.Name += delta.Function.Name
	toolCall.Function.Arguments += delta.Function.Arguments

	return nil
}

func newAgentTool(
	name string,
	description string,
	properties map[string]any,
	required ...string,
) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters": map[string]any{
				"type":                 "object",
				"properties":           properties,
				"required":             required,
				"additionalProperties": false,
			},
		},
	}
}

func allowedAgentTools() []map[string]any {
	path := map[string]any{
		"type":        "string",
		"description": "Path relative to the current project root",
	}

	return []map[string]any{
		newAgentTool(
			requestUserInputTool,
			"Pause this job and ask the user one concrete question when their decision, clarification, or authorization is required before work can safely continue. The job resumes with the same context after the user responds.",
			map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The specific question the user must answer to unblock the job",
				},
			},
			"question",
		),
		newAgentTool(
			"view_file_tree",
			"List files and directories beneath a project directory.",
			map[string]any{"relative_path": path},
			"relative_path",
		),
		newAgentTool(
			"read_file_contents",
			"Read the complete contents of a project file before editing it.",
			map[string]any{"relative_path": path},
			"relative_path",
		),
		newAgentTool(
			"create_file",
			"Create a new empty file. If it already exists, report that without changing it.",
			map[string]any{"relative_path": path},
			"relative_path",
		),
		newAgentTool(
			"write_to_file",
			"Replace the complete contents of an existing project file.",
			map[string]any{
				"relative_path": path,
				"content": map[string]any{
					"type":        "string",
					"description": "Complete replacement contents for the file",
				},
			},
			"relative_path",
			"content",
		),
	}
}

func agentTools(options RunOptions) []map[string]any {
	tools := make([]map[string]any, 0, len(options.Tools)+5)
	if !options.OnlyTools {
		tools = append(tools, allowedAgentTools()...)
	}
	for _, tool := range options.Tools {
		if strings.TrimSpace(tool.Name) == "" || tool.Execute == nil {
			continue
		}
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			}
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return tools
}

func executeAgentTool(
	ctx context.Context,
	call agentToolCall,
	options RunOptions,
	workspaceRoot string,
	maxFileBytes int64,
) (agentMessage, error) {
	if err := ctx.Err(); err != nil {
		return agentMessage{}, err
	}
	if options.State != nil && options.JobID != "" && call.ID != "" {
		cached, found, err := options.State.LoadToolResult(ctx, options.JobID, call.ID)
		if err != nil {
			return agentMessage{}, fmt.Errorf("load tool result: %w", err)
		}
		if found {
			if options.Events != nil {
				if err := options.Events(AgentEvent{Type: "tool_cached", Data: call.Function.Name}); err != nil {
					return agentMessage{}, fmt.Errorf("emit cached tool event: %w", err)
				}
			}
			return toolResultMessage(call, cached), nil
		}
	}

	var result agentToolResult
	if call.Function.Name == requestUserInputTool {
		resolved, err := resolveUserInputTool(ctx, call, options)
		if err != nil {
			return agentMessage{}, err
		}
		result = resolved
	} else if custom, found := customAgentTool(options.Tools, call.Function.Name); found {
		value, err := custom.Execute(ctx, json.RawMessage(call.Function.Arguments))
		if err != nil {
			result = agentToolResult{Error: err.Error()}
		} else {
			result = agentToolResult{Success: true, Result: value}
		}
	} else {
		result = runAgentTool(call, workspaceRoot, maxFileBytes)
	}
	content, err := json.Marshal(result)
	if err != nil {
		content = []byte(`{"success":false,"error":"could not encode tool result"}`)
	}
	if options.State != nil && options.JobID != "" && call.ID != "" {
		if err := options.State.SaveToolResult(
			ctx, options.JobID, call.ID, call.Function.Name,
			call.Function.Arguments, content,
		); err != nil {
			return agentMessage{}, fmt.Errorf("save tool result: %w", err)
		}
	}
	if options.Events != nil {
		if err := options.Events(AgentEvent{Type: "tool_result", Data: string(content)}); err != nil {
			return agentMessage{}, fmt.Errorf("emit tool result event: %w", err)
		}
	}
	return toolResultMessage(call, content), nil
}

func customAgentTool(tools []Tool, name string) (Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name && tool.Execute != nil {
			return tool, true
		}
	}
	return Tool{}, false
}

func resolveUserInputTool(
	ctx context.Context,
	call agentToolCall,
	options RunOptions,
) (agentToolResult, error) {
	var arguments struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return agentToolResult{Error: fmt.Sprintf("decode arguments: %v", err)}, nil
	}
	question := strings.TrimSpace(arguments.Question)
	if question == "" {
		return agentToolResult{Error: "question cannot be empty"}, nil
	}
	if len(question) > 16<<10 {
		return agentToolResult{Error: "question is too long"}, nil
	}
	if call.ID == "" {
		return agentToolResult{}, errors.New("request_user_input tool call is missing an ID")
	}
	if options.State == nil || options.JobID == "" {
		return agentToolResult{Error: "interactive user input is unavailable for this run"}, nil
	}
	response, found, err := options.State.LoadUserInputResponse(ctx, options.JobID, call.ID)
	if err != nil {
		return agentToolResult{}, fmt.Errorf("load user input response: %w", err)
	}
	if !found {
		return agentToolResult{}, &UserInputRequiredError{
			ToolCallID: call.ID,
			Question:   question,
		}
	}
	return agentToolResult{
		Success: true,
		Result:  map[string]string{"answer": response},
	}, nil
}

func toolResultMessage(call agentToolCall, content []byte) agentMessage {
	return agentMessage{
		Role:       "tool",
		Content:    string(content),
		ToolCallID: call.ID,
		Name:       call.Function.Name,
	}
}

func runAgentTool(call agentToolCall, workspaceRoot string, maxFileBytes int64) agentToolResult {
	type pathArguments struct {
		RelativePath string `json:"relative_path"`
	}
	type writeArguments struct {
		RelativePath string `json:"relative_path"`
		Content      string `json:"content"`
	}

	fail := func(err error) agentToolResult {
		return agentToolResult{Error: err.Error()}
	}

	switch call.Function.Name {
	case "view_file_tree":
		var arguments pathArguments
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return fail(fmt.Errorf("decode arguments: %w", err))
		}
		path, err := safeAgentPath(workspaceRoot, arguments.RelativePath)
		if err != nil {
			return fail(err)
		}
		result, err := agent_functions.View_fileTree(path)
		if err != nil {
			return fail(err)
		}
		return agentToolResult{Success: true, Result: result}

	case "read_file_contents":
		var arguments pathArguments
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return fail(fmt.Errorf("decode arguments: %w", err))
		}
		path, err := safeAgentPath(workspaceRoot, arguments.RelativePath)
		if err != nil {
			return fail(err)
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			return fail(err)
		}
		if fileInfo.Size() > maxFileBytes {
			return fail(fmt.Errorf("file is %d bytes; maximum readable size is %d", fileInfo.Size(), maxFileBytes))
		}
		result, err := agent_functions.Read_fileContents(path)
		if err != nil {
			return fail(err)
		}
		return agentToolResult{Success: true, Result: result}

	case "create_file":
		var arguments pathArguments
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return fail(fmt.Errorf("decode arguments: %w", err))
		}
		path, err := safeAgentPath(workspaceRoot, arguments.RelativePath)
		if err != nil {
			return fail(err)
		}
		result, err := agent_functions.Create_file(path)
		if err != nil {
			return fail(err)
		}
		return agentToolResult{Success: true, Result: result}

	case "write_to_file":
		var arguments writeArguments
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return fail(fmt.Errorf("decode arguments: %w", err))
		}
		path, err := safeAgentPath(workspaceRoot, arguments.RelativePath)
		if err != nil {
			return fail(err)
		}
		if int64(len(arguments.Content)) > maxFileBytes {
			return fail(fmt.Errorf("content is %d bytes; maximum writable size is %d", len(arguments.Content), maxFileBytes))
		}
		if err := agent_functions.Write_ToFile(path, arguments.Content); err != nil {
			return fail(err)
		}
		return agentToolResult{
			Success: true,
			Result:  "Successfully wrote file at: " + path,
		}

	default:
		return fail(fmt.Errorf("tool %q is not allowed", call.Function.Name))
	}
}

func safeAgentPath(workspaceRoot, relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" {
		return "", errors.New("relative_path cannot be empty")
	}
	if filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" {
		return "", errors.New("only paths relative to the project root are allowed")
	}

	cleanPath := filepath.Clean(relativePath)
	for _, part := range strings.Split(cleanPath, string(os.PathSeparator)) {
		if strings.EqualFold(part, ".git") ||
			strings.HasPrefix(strings.ToLower(part), ".env") {
			return "", fmt.Errorf("access to %q is not allowed", relativePath)
		}
	}

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root symlinks: %w", err)
	}
	target := filepath.Join(root, cleanPath)
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if errors.Is(err, os.ErrNotExist) {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(target))
		if parentErr != nil {
			return "", fmt.Errorf("resolve target parent: %w", parentErr)
		}
		resolvedTarget = filepath.Join(resolvedParent, filepath.Base(target))
	} else if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}
	pathFromRoot, err := filepath.Rel(root, resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("validate project path: %w", err)
	}
	if pathFromRoot == ".." ||
		strings.HasPrefix(pathFromRoot, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes the project root")
	}

	return resolvedTarget, nil
}
