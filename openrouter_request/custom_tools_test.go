package openrouter_request

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOnlyCustomToolsExcludesWorkspaceToolsAndExecutesHandler(t *testing.T) {
	called := false
	custom := Tool{
		Name:        "create_job",
		Description: "Create a child job.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string"},
			},
			"required": []string{"prompt"},
		},
		Execute: func(_ context.Context, raw json.RawMessage) (any, error) {
			called = true
			var input map[string]string
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, err
			}
			return map[string]string{"created": input["prompt"]}, nil
		},
	}
	options := RunOptions{Tools: []Tool{custom}, OnlyTools: true}
	definitions := agentTools(options)
	if len(definitions) != 1 {
		t.Fatalf("custom tool definitions = %#v", definitions)
	}
	function, ok := definitions[0]["function"].(map[string]any)
	if !ok || function["name"] != "create_job" {
		t.Fatalf("custom tool definition = %#v", definitions[0])
	}

	message, err := executeAgentTool(context.Background(), agentToolCall{
		ID: "call-1",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "create_job", Arguments: `{"prompt":"inspect tests"}`},
	}, options, t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !called || message.Role != "tool" ||
		!strings.Contains(message.Content, "inspect tests") {
		t.Fatalf("custom tool result = %#v, called=%v", message, called)
	}
}
