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

func TestCompactAgentContextPreservesPromptAndCompleteRecentToolRound(t *testing.T) {
	oldCall := testAgentToolCall("old-call", "read_file", `{"path":"old.txt"}`)
	recentCall := testAgentToolCall("recent-call", "read_file", `{"path":"recent.txt"}`)
	messages := []agentMessage{
		{Role: "system", Content: "Work carefully."},
		{Role: "user", Content: "Fix the repository."},
		{Role: "assistant", Content: strings.Repeat("old analysis ", 200), ToolCalls: []agentToolCall{oldCall}},
		{Role: "tool", ToolCallID: oldCall.ID, Name: oldCall.Function.Name, Content: strings.Repeat("old result ", 200)},
		{Role: "assistant", Content: "Check the recent file.", ToolCalls: []agentToolCall{recentCall}},
		{Role: "tool", ToolCallID: recentCall.ID, Name: recentCall.Function.Name, Content: "recent result"},
	}

	var payload map[string]json.RawMessage
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		return streamingResponse(request, "Old file inspection found a stale setting."), nil
	})}

	compacted, result := compactAgentContext(
		context.Background(), client, "key", "test/model", messages, nil, 100, 200,
	)
	if !result.Compacted || result.Fallback {
		t.Fatalf("compaction result = %#v", result)
	}
	if result.MessageCount != 2 || result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("compaction sizes = %#v", result)
	}
	if len(compacted) != 5 || compacted[0].Role != "system" ||
		compacted[1].Content != "Fix the repository." ||
		compacted[2].Name != contextSummaryName ||
		!strings.Contains(compacted[2].Content, "stale setting") {
		t.Fatalf("compacted context = %#v", compacted)
	}
	if compacted[3].ToolCalls[0].ID != recentCall.ID || compacted[4].ToolCallID != recentCall.ID {
		t.Fatalf("recent tool round was split: %#v", compacted[3:])
	}
	if _, found := payload["tools"]; found {
		t.Fatal("summary request unexpectedly included tools")
	}
	if _, found := payload["tool_choice"]; found {
		t.Fatal("summary request unexpectedly included tool_choice")
	}
}

func TestCompactAgentContextFallsBackWhenSummaryRequestFails(t *testing.T) {
	messages := []agentMessage{
		{Role: "user", Content: "Continue the job."},
		{Role: "assistant", Content: strings.Repeat("important earlier result ", 200)},
		{Role: "user", Content: "Most recent instruction."},
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("summary service unavailable")
	})}

	compacted, result := compactAgentContext(
		context.Background(), client, "key", "test/model", messages, nil, 100, 20,
	)
	if !result.Compacted || !result.Fallback {
		t.Fatalf("compaction result = %#v", result)
	}
	if len(compacted) != 3 || compacted[1].Name != contextSummaryName ||
		!strings.Contains(compacted[1].Content, "compacted locally") {
		t.Fatalf("fallback context = %#v", compacted)
	}
}

func TestCompactAgentContextDoesNotRecompactAnUnchangedSummary(t *testing.T) {
	messages := []agentMessage{
		{Role: "user", Content: "Continue the job."},
		{Role: "user", Name: contextSummaryName, Content: strings.Repeat("summary ", 200)},
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unchanged summary should not make another model request")
		return nil, nil
	})}

	compacted, result := compactAgentContext(
		context.Background(), client, "key", "test/model", messages, nil, 100, 20,
	)
	if result.Compacted || len(compacted) != len(messages) {
		t.Fatalf("compaction result = %#v, messages = %#v", result, compacted)
	}
}

func TestSaveAgentStateKeepsTranscriptSeparateFromModelContext(t *testing.T) {
	state := &contextCheckpoint{}
	transcript := []agentMessage{
		{Role: "user", Content: "original prompt"},
		{Role: "assistant", Content: "old work"},
		{Role: "assistant", Content: "recent work"},
	}
	modelContext := []agentMessage{
		{Role: "user", Content: "original prompt"},
		{Role: "user", Name: contextSummaryName, Content: "old work summarized"},
		{Role: "assistant", Content: "recent work"},
	}

	if err := saveAgentState(context.Background(), RunOptions{
		JobID: "job-1",
		State: state,
	}, transcript, modelContext); err != nil {
		t.Fatal(err)
	}
	var savedTranscript, savedContext []agentMessage
	if err := json.Unmarshal(state.transcript, &savedTranscript); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(state.agentContext, &savedContext); err != nil {
		t.Fatal(err)
	}
	if len(savedTranscript) != 3 || savedTranscript[1].Content != "old work" {
		t.Fatalf("saved transcript = %#v", savedTranscript)
	}
	if len(savedContext) != 3 || savedContext[1].Name != contextSummaryName {
		t.Fatalf("saved context = %#v", savedContext)
	}
}

func TestRunPersistsCompactedContextWithoutChangingFullTranscript(t *testing.T) {
	call := testAgentToolCall("finished-call", "read_file", `{"path":"large.txt"}`)
	initialTranscript := []agentMessage{
		{Role: "user", Content: "Finish the job."},
		{Role: "assistant", Content: strings.Repeat("earlier analysis ", 200), ToolCalls: []agentToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: strings.Repeat("earlier result ", 200)},
	}
	encodedTranscript, err := json.Marshal(initialTranscript)
	if err != nil {
		t.Fatal(err)
	}
	state := &contextCheckpoint{}
	state.transcript = encodedTranscript

	configDirectory := t.TempDir()
	t.Setenv(pearlConfigDirectoryEnvironment, configDirectory)
	t.Setenv(openRouterAPIKeyEnvironment, "test-key")
	settings := []byte(`{"model":"test/model","max_depth":8}`)
	if err := os.WriteFile(filepath.Join(configDirectory, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}

	requestCount := 0
	var mainRequest []agentMessage
	originalClient := sharedAgentHTTPClient
	sharedAgentHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		var payload struct {
			Messages []agentMessage `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if requestCount == 1 {
			return streamingResponse(request, "The large file was already inspected."), nil
		}
		mainRequest = payload.Messages
		return streamingResponse(request, "job complete"), nil
	})}
	t.Cleanup(func() { sharedAgentHTTPClient = originalClient })

	result, err := Run(context.Background(), "Finish the job.", RunOptions{
		JobID:                   "job-1",
		WorkspaceRoot:           t.TempDir(),
		State:                   state,
		ContextCompactionTokens: 100,
		ContextKeepTokens:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "job complete" || requestCount != 2 {
		t.Fatalf("result = %q, requests = %d", result, requestCount)
	}
	if len(mainRequest) != 2 || mainRequest[1].Name != contextSummaryName {
		t.Fatalf("main request context = %#v", mainRequest)
	}

	var savedTranscript, savedContext []agentMessage
	if err := json.Unmarshal(state.transcript, &savedTranscript); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(state.agentContext, &savedContext); err != nil {
		t.Fatal(err)
	}
	if len(savedTranscript) != 4 || savedTranscript[1].ToolCalls[0].ID != call.ID ||
		savedTranscript[2].ToolCallID != call.ID || savedTranscript[3].Content != "job complete" {
		t.Fatalf("saved full transcript = %#v", savedTranscript)
	}
	if len(savedContext) != 3 || savedContext[1].Name != contextSummaryName ||
		savedContext[2].Content != "job complete" {
		t.Fatalf("saved model context = %#v", savedContext)
	}
}

type contextCheckpoint struct {
	userInputCheckpoint
	agentContext []byte
}

func (s *contextCheckpoint) LoadAgentContext(context.Context, string) ([]byte, error) {
	return append([]byte(nil), s.agentContext...), nil
}

func (s *contextCheckpoint) SaveAgentCheckpoint(
	_ context.Context,
	_ string,
	transcript, agentContext []byte,
) error {
	s.transcript = append([]byte(nil), transcript...)
	s.agentContext = append([]byte(nil), agentContext...)
	return nil
}

func testAgentToolCall(id, name, arguments string) agentToolCall {
	call := agentToolCall{ID: id, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = arguments
	return call
}

func streamingResponse(request *http.Request, content string) *http.Response {
	encoded, _ := json.Marshal(content)
	body := "data: {\"choices\":[{\"delta\":{\"content\":" + string(encoded) + "}}]}\n\n" +
		"data: [DONE]\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
