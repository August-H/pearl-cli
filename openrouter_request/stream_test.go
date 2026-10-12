package openrouter_request

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsModelsChosenByTheFreeTierRouter(t *testing.T) {
	toolCall := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-%d","type":"function",` +
		`"function":{"name":"noop","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"model":"%s"}`
	answer := `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"model":"%s"}`
	responses := []string{
		fmt.Sprintf(toolCall, 1, "meta/routed-a"),
		fmt.Sprintf(toolCall, 2, "meta/routed-a"),
		fmt.Sprintf(answer, "qwen/routed-b"),
	}
	for _, test := range []struct {
		model string
		want  []string
	}{
		{FreeTierModel, []string{"meta/routed-a", "qwen/routed-b"}},
		{"anthropic/claude-opus-5.5", nil},
	} {
		t.Run(test.model, func(t *testing.T) {
			configDirectory := t.TempDir()
			t.Setenv("PEARL_CONFIG_DIR", configDirectory)
			t.Setenv("OPENROUTER_API_KEY", "test-key")
			settings := []byte(`{"model":"` + test.model + `","max_depth":8}`)
			if err := os.WriteFile(filepath.Join(configDirectory, "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			request := 0
			originalClient := sharedAgentHTTPClient
			sharedAgentHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := "data: " + responses[min(request, len(responses)-1)] + "\n\ndata: [DONE]\n\n"
				request++
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			t.Cleanup(func() { sharedAgentHTTPClient = originalClient })

			var models []string
			state := &userInputCheckpoint{}
			_, err := Run(context.Background(), "do the thing", RunOptions{
				JobID: "job-1", WorkspaceRoot: t.TempDir(), State: state, OnlyTools: true,
				Tools: []Tool{{
					Name: "noop", Description: "Does nothing",
					Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
					Execute:    func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
				}},
				Events: func(event AgentEvent) error {
					if event.Type == "model" {
						models = append(models, event.Data)
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(models, ",") != strings.Join(test.want, ",") {
				t.Fatalf("model events = %v, want %v", models, test.want)
			}
			if strings.Contains(string(state.transcript), "routed") {
				t.Fatalf("routed model leaked into the transcript: %s", state.transcript)
			}
		})
	}
}

func TestCompletionRequiresCompleteStream(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantError  bool
	}{
		{"early EOF", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", true},
		{"token limit", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", true},
		{"filtered", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n", true},
		{"complete", "data: {\"choices\":[{\"delta\":{\"content\":\"complete\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			_, err := requestAgentCompletion(context.Background(), client, "dummy", "dummy", nil, nil, nil)
			if (err != nil) != test.wantError {
				t.Fatalf("completion error = %v", err)
			}
		})
	}
}
