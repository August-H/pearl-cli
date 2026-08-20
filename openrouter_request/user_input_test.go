package openrouter_request

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type userInputCheckpoint struct {
	response          string
	responseAvailable bool
	transcript        []byte
	toolResult        []byte
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (s *userInputCheckpoint) LoadTranscript(context.Context, string) ([]byte, error) {
	return s.transcript, nil
}

func (s *userInputCheckpoint) SaveTranscript(_ context.Context, _ string, transcript []byte) error {
	s.transcript = append([]byte(nil), transcript...)
	return nil
}

func (s *userInputCheckpoint) LoadUserInputResponse(
	_ context.Context,
	_, _ string,
) (string, bool, error) {
	return s.response, s.responseAvailable, nil
}

func (s *userInputCheckpoint) LoadToolResult(
	_ context.Context,
	_, _ string,
) ([]byte, bool, error) {
	if s.toolResult == nil {
		return nil, false, nil
	}
	return s.toolResult, true, nil
}

func (s *userInputCheckpoint) SaveToolResult(
	_ context.Context,
	_, _, _, _ string,
	result []byte,
) error {
	s.toolResult = append([]byte(nil), result...)
	return nil
}

func TestRequestUserInputPausesAndResumesPendingTranscript(t *testing.T) {
	call := agentToolCall{ID: "call-input", Type: "function"}
	call.Function.Name = requestUserInputTool
	call.Function.Arguments = `{"question":"Which deployment region should I use?"}`
	state := &userInputCheckpoint{}
	options := RunOptions{JobID: "job-1", State: state}

	_, err := executeAgentTool(context.Background(), call, options, t.TempDir(), 1<<20)
	var inputRequired *UserInputRequiredError
	if !errors.As(err, &inputRequired) {
		t.Fatalf("request error = %v, want UserInputRequiredError", err)
	}
	if inputRequired.ToolCallID != call.ID || inputRequired.Question != "Which deployment region should I use?" {
		t.Fatalf("input request = %#v", inputRequired)
	}
	if state.toolResult != nil {
		t.Fatal("input tool result was saved before the user responded")
	}

	state.response = "Use us-west-2"
	state.responseAvailable = true
	messages := []agentMessage{
		{Role: "user", Content: "Deploy the service"},
		{Role: "assistant", Content: "I need one decision.", ToolCalls: []agentToolCall{call}},
	}
	completed, err := completePendingToolCalls(
		context.Background(), messages, options, t.TempDir(), 1<<20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 3 || completed[0].Content != "Deploy the service" || completed[1].ToolCalls[0].ID != call.ID {
		t.Fatalf("completed transcript lost prior context: %#v", completed)
	}
	if completed[2].Role != "tool" || completed[2].ToolCallID != call.ID ||
		!strings.Contains(completed[2].Content, "Use us-west-2") {
		t.Fatalf("user response tool message = %#v", completed[2])
	}
	var persisted []agentMessage
	if err := json.Unmarshal(state.transcript, &persisted); err != nil {
		t.Fatalf("decode persisted transcript: %v", err)
	}
	if len(persisted) != 3 || persisted[0].Content != "Deploy the service" ||
		!strings.Contains(persisted[2].Content, "Use us-west-2") {
		t.Fatalf("persisted resumed transcript = %#v", persisted)
	}
}

func TestRunSendsPriorContextAndUserResponseAfterResume(t *testing.T) {
	call := agentToolCall{ID: "call-input", Type: "function"}
	call.Function.Name = requestUserInputTool
	call.Function.Arguments = `{"question":"Which deployment region should I use?"}`
	initialMessages := []agentMessage{
		{Role: "user", Content: "Deploy the service"},
		{Role: "assistant", Content: "I need one decision.", ToolCalls: []agentToolCall{call}},
	}
	transcript, err := json.Marshal(initialMessages)
	if err != nil {
		t.Fatal(err)
	}
	state := &userInputCheckpoint{
		response:          "Use us-west-2",
		responseAvailable: true,
		transcript:        transcript,
	}

	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	settings := []byte(`{"model":"test/model","max_depth":8}`)
	if err := os.WriteFile(filepath.Join(configDirectory, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}

	var received []agentMessage
	originalClient := sharedAgentHTTPClient
	sharedAgentHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Messages []agentMessage `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		received = payload.Messages
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				"data: {\"choices\":[{\"delta\":{\"content\":\"deployment resumed\"}}]}\n\n" +
					"data: [DONE]\n\n",
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { sharedAgentHTTPClient = originalClient })

	result, err := Run(context.Background(), "Deploy the service", RunOptions{
		JobID:         "job-1",
		WorkspaceRoot: t.TempDir(),
		State:         state,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "deployment resumed" {
		t.Fatalf("result = %q", result)
	}
	if len(received) != 3 || received[0].Content != "Deploy the service" ||
		received[1].ToolCalls[0].ID != call.ID ||
		!strings.Contains(received[2].Content, "Use us-west-2") {
		t.Fatalf("resumed request messages = %#v", received)
	}
}
