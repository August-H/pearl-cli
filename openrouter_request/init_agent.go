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

// Init_agent sends a prompt to the configured model.
func Init_agent(prompt string) (string, error) {
	if len(strings.TrimSpace(prompt)) == 0 {
		return "", errors.New("prompt cannot be empty")
	}

	loadPearlEnvironment()
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return "", errors.New("OPENROUTER_API_KEY is not set")
	}

	settings, err := loadAgentSettings()
	if err != nil {
		return "", err
	}

	maxDepth := settings.Max_depth
	if maxDepth <= 0 {
		maxDepth = 8
	}

	messages := []agentMessage{{Role: "user", Content: prompt}}
	client := &http.Client{Timeout: 2 * time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for toolDepth := 0; ; {
		assistantMessage, err := requestAgentCompletion(
			ctx,
			client,
			apiKey,
			settings.Model,
			messages,
		)
		if err != nil {
			return "", err
		}

		messages = append(messages, assistantMessage)
		if len(assistantMessage.ToolCalls) == 0 {
			return assistantMessage.Content, nil
		}
		if toolDepth >= maxDepth {
			return "", fmt.Errorf("agent exceeded maximum tool depth of %d", maxDepth)
		}

		for _, call := range assistantMessage.ToolCalls {
			fmt.Printf("[tool] %s\n", call.Function.Name)
			messages = append(messages, executeAgentTool(call))
		}
		toolDepth++
	}
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
) (agentMessage, error) {
	payload := map[string]any{
		"model":               model,
		"messages":            messages,
		"reasoning":           Reasoning{Enabled: true},
		"stream":              true,
		"tools":               allowedAgentTools(),
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
	printedOutput := false
	currentSection := ""
	printSection := func(section string, text string) {
		if text == "" {
			return
		}
		if currentSection != section {
			if printedOutput {
				fmt.Println()
			}
			fmt.Printf("[%s]\n", section)
			currentSection = section
		}
		fmt.Print(text)
		printedOutput = true
	}
	defer func() {
		if printedOutput {
			fmt.Println()
		}
	}()

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
				printSection("reasoning", reasoning)
			}

			for _, detail := range choice.Delta.ReasoningDetails {
				assistantMessage.ReasoningDetails = append(
					assistantMessage.ReasoningDetails,
					detail,
				)
				if reasoning == "" {
					printSection("reasoning", printableReasoningDetail(detail))
				}
			}

			if choice.Delta.Content != "" {
				assistantMessage.Content += choice.Delta.Content
				printSection("answer", choice.Delta.Content)
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
			"Create a new empty file. This fails if the file already exists.",
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

func executeAgentTool(call agentToolCall) agentMessage {
	result := runAgentTool(call)
	content, err := json.Marshal(result)
	if err != nil {
		content = []byte(`{"success":false,"error":"could not encode tool result"}`)
	}
	return agentMessage{
		Role:       "tool",
		Content:    string(content),
		ToolCallID: call.ID,
		Name:       call.Function.Name,
	}
}

func runAgentTool(call agentToolCall) agentToolResult {
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
		path, err := safeAgentPath(arguments.RelativePath)
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
		path, err := safeAgentPath(arguments.RelativePath)
		if err != nil {
			return fail(err)
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
		path, err := safeAgentPath(arguments.RelativePath)
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
		path, err := safeAgentPath(arguments.RelativePath)
		if err != nil {
			return fail(err)
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

func safeAgentPath(relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" {
		return "", errors.New("relative_path cannot be empty")
	}
	if filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" {
		return "", errors.New("only paths relative to the project root are allowed")
	}

	cleanPath := filepath.Clean(relativePath)
	for _, part := range strings.Split(cleanPath, string(os.PathSeparator)) {
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".env") {
			return "", fmt.Errorf("access to %q is not allowed", relativePath)
		}
	}

	root, err := filepath.Abs(".")
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	target := filepath.Join(root, cleanPath)
	pathFromRoot, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("validate project path: %w", err)
	}
	if pathFromRoot == ".." ||
		strings.HasPrefix(pathFromRoot, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes the project root")
	}

	return cleanPath, nil
}
