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
	"time"
)

func configureTestChatGPT(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", directory)
	t.Setenv("OPENROUTER_API_KEY", "")
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(`{"provider":"chatgpt","model":"openrouter/saved","chatgpt_model":"account-model","max_depth":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"active_client_id": "oaiapp_test", "accounts": []any{map[string]any{"client_id": "oaiapp_test", "access_token": "chatgpt-secret", "scopes": "resource.invoke chatgpt.tokens.use.direct", "expires_at": time.Now().Add(time.Hour)}}})
	if err := os.WriteFile(filepath.Join(directory, "chatgpt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTRunUsesResponsesAndReplaysToolCalls(t *testing.T) {
	configureTestChatGPT(t)
	previous := sharedAgentHTTPClient
	defer func() { sharedAgentHTTPClient = previous }()
	requestCount, executions := 0, 0
	sharedAgentHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestCount++
		if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer chatgpt-secret" {
			t.Errorf("request = %s", r.URL)
		}
		var payload struct {
			Model  string           `json:"model"`
			Store  bool             `json:"store"`
			Stream bool             `json:"stream"`
			Input  []map[string]any `json:"input"`
			Tools  []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload.Model != "account-model" || payload.Store || !payload.Stream || payload.Tools[0]["type"] != "namespace" {
			t.Error("invalid Responses contract")
		}
		for _, item := range payload.Input {
			if item["role"] == "system" {
				t.Error("sent system role")
			}
		}
		output := `[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-reasoning","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","namespace":"pearl","name":"noop","arguments":"{}"}]`
		if requestCount == 2 {
			foundCall, foundResult, foundReasoning := false, false, false
			for _, item := range payload.Input {
				foundCall = foundCall || item["type"] == "function_call" && item["call_id"] == "call_1"
				foundResult = foundResult || item["type"] == "function_call_output" && item["call_id"] == "call_1"
				foundReasoning = foundReasoning || item["encrypted_content"] == "opaque-reasoning"
			}
			if !foundCall || !foundResult || !foundReasoning {
				t.Error("lost tool or reasoning replay")
			}
			output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]`
		}
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(output), &items); err != nil {
			t.Fatal(err)
		}
		body := ""
		for index, item := range items {
			body += fmt.Sprintf("data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", index, item)
		}
		body += "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"account-model\",\"output\":[]}}\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	result, err := Run(context.Background(), "do the task", RunOptions{WorkspaceRoot: t.TempDir(), OnlyTools: true, Tools: []Tool{{Name: "noop", Description: "test tool", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, json.RawMessage) (any, error) { executions++; return "ok", nil }}}})
	if err != nil || result != "done" || requestCount != 2 || executions != 1 {
		t.Fatalf("run=%q, err=%v, requests=%d, tools=%d", result, err, requestCount, executions)
	}
}

func TestProviderModelPreferencesStaySeparate(t *testing.T) {
	configureTestChatGPT(t)
	if _, err := ConfigureAgentModel("another-account-model"); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadAgentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if AgentModel(settings) != "another-account-model" || settings.Model != "openrouter/saved" {
		t.Fatal("ChatGPT changed saved OpenRouter model")
	}
	if _, err := ConfigureProvider(ProviderOpenRouter); err != nil {
		t.Fatal(err)
	}
	settings, _ = LoadAgentSettings()
	if AgentModel(settings) != "openrouter/saved" {
		t.Fatal("provider switch lost OpenRouter model")
	}
}

func TestChatGPTRetryDoesNotReplayAnotherAccountsEncryptedState(t *testing.T) {
	configureTestChatGPT(t)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "old-encrypted-state") || !strings.Contains(string(body), "earlier answer") {
			t.Error("retry did not rebuild readable history for the new account")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"done\",\"type\":\"output_text\"}]}]}}\n\n"))}, nil
	})}
	_, err := requestChatGPTCompletion(WithChatGPT(context.Background()), client, "account-model", []agentMessage{{Role: "assistant", Content: "earlier answer", ResponsesAccountID: "previous-account", ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"old-encrypted-state"}`)}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTRecoversRejectedSavedHistory(t *testing.T) {
	configureTestChatGPT(t)
	calls, recoveries := 0, 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if calls == 1 {
			if !strings.Contains(string(body), "opaque-state") {
				t.Error("did not initially preserve response output")
			}
			return &http.Response{StatusCode: 400, Header: http.Header{"X-Request-Id": []string{"req_test"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_request_error","param":"input[1]"}}`))}, nil
		}
		if strings.Contains(string(body), "opaque-state") || !strings.Contains(string(body), "earlier answer") || !strings.Contains(string(body), "continue the task") {
			t.Error("recovery lost readable conversation or retained rejected state")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"continued\"}]}]}}\n\n"))}, nil
	})}
	messages := []agentMessage{{Role: "assistant", Content: "earlier answer", ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque-state"}`)}}, {Role: "user", Content: "continue the task"}}
	result, err := requestChatGPTCompletion(WithChatGPT(context.Background()), client, "account-model", messages, func(e AgentEvent) error {
		if e.Type == "recovery" {
			recoveries++
		}
		return nil
	}, nil)
	if err != nil || result.Content != "continued" || !result.ResponsesRebuilt || calls != 2 || recoveries != 1 {
		t.Fatalf("recovery result=%q calls=%d events=%d err=%v", result.Content, calls, recoveries, err)
	}
}

func TestChatGPTHistoryRecoveryPersistsAcrossToolRounds(t *testing.T) {
	configureTestChatGPT(t)
	oldCall := testAgentToolCall("old-call", "old_tool", "{}")
	history := []agentMessage{
		{Role: "user", Content: "original task"},
		{Role: "assistant", ToolCalls: []agentToolCall{oldCall}, ResponsesAccountID: "oaiapp_test", ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"rejected-state"}`)}},
		{Role: "tool", ToolCallID: "old-call", Content: `{"success":true,"result":"saved result"}`},
		{Role: "assistant", Content: "earlier answer", ResponsesAccountID: "oaiapp_test", ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"earlier answer"}]}`)}},
	}
	data, _ := json.Marshal(history)
	state := &contextCheckpoint{userInputCheckpoint: userInputCheckpoint{transcript: data}, agentContext: data}
	previous := sharedAgentHTTPClient
	defer func() { sharedAgentHTTPClient = previous }()
	calls, executions := 0, 0
	sharedAgentHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if calls == 1 {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_request_error"}}`))}, nil
		}
		if strings.Contains(string(body), "rejected-state") || !strings.Contains(string(body), "saved result") || !strings.Contains(string(body), `"phase":"final_answer"`) {
			t.Error("subsequent request lost saved results/phase or retained rejected state")
		}
		if strings.Contains(string(body), `"call_id":"old-call"`) {
			t.Error("reconstructed a formal tool sequence without its required reasoning")
		}
		output := `[{"type":"function_call","call_id":"new-call","name":"noop","arguments":"{}"}]`
		if calls >= 3 {
			output = `[{"type":"message","content":[{"type":"output_text","text":"done"}]}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":" + output + "}}\n\n"))}, nil
	})}
	result, err := Run(context.Background(), "original task", RunOptions{JobID: "job", State: state, FollowUp: "continue", WorkspaceRoot: t.TempDir(), OnlyTools: true, Tools: []Tool{{Name: "noop", Description: "test", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, json.RawMessage) (any, error) { executions++; return "ok", nil }}, {Name: "old_tool", Execute: func(context.Context, json.RawMessage) (any, error) {
		t.Error("executed completed work again")
		return "ok", nil
	}}}})
	if err != nil || result != "done" || calls != 3 || executions != 1 {
		t.Fatalf("result=%q calls=%d executions=%d err=%v", result, calls, executions, err)
	}
	if strings.Contains(string(state.transcript), "rejected-state") || strings.Contains(string(state.agentContext), "rejected-state") {
		t.Fatal("rejected state survived in durable checkpoints")
	}
}
