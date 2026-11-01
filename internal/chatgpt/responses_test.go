package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recoveryTransport func(*http.Request) (*http.Response, error)

func (f recoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRetriesInterruptedRequestsButNotVisibleOutputOrQuota(t *testing.T) {
	for _, mode := range []string{"connection", "unavailable", "partial", "quota", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			c := NewClient()
			testCredentials(t, c, time.Now().Add(time.Hour))
			calls, recoveries := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &http.Client{Transport: recoveryTransport(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					switch mode {
					case "connection":
						return nil, errors.New("connection reset by peer")
					case "cancelled":
						cancel()
						return nil, context.Canceled
					case "unavailable":
						return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"server_error"}}`))}, nil
					case "partial":
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n"))}, nil
					case "quota":
						return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`))}, nil
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))}, nil
			})}
			_, err := c.RespondWithRecovery(ctx, client, map[string]any{"input": []any{}}, func(string, string) error { return nil }, func(string) error { recoveries++; return nil })
			if mode == "connection" || mode == "unavailable" {
				if err != nil || calls != 2 || recoveries != 1 {
					t.Fatalf("retry calls=%d events=%d err=%v", calls, recoveries, err)
				}
			} else if err == nil || calls != 1 || recoveries != 0 {
				t.Fatalf("unsafe retry calls=%d events=%d err=%v", calls, recoveries, err)
			}
		})
	}
}

func TestRespondRequiresCompletedAndPropagatesLimits(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"completed", `data: {"type":"response.output_text.delta","delta":"done"}

data: {"type":"response.completed","response":{"status":"completed","model":"account-model","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}

`, ""},
		{"truncated", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", "before response.completed"},
		{"completed with streamed output", "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"account-model\",\"output\":[]}}\n\n", ""},
		{"limit after delta", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", "Manage usage"},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", "incomplete"},
		{"malformed", "data: not-json\n\n", "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer access-secret" || r.URL.Path != "/responses" {
					t.Error("wrong Responses credential or URL")
				}
				var payload map[string]any
				json.NewDecoder(r.Body).Decode(&payload)
				if payload["store"] != false || payload["stream"] != true || payload["temperature"] != nil {
					t.Error("wrong plan request contract")
				}
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			c := &Client{apiURL: server.URL}
			testCredentials(t, c, time.Now().Add(time.Hour))
			result, err := c.Respond(context.Background(), server.Client(), map[string]any{"model": "account-model", "input": []any{}, "temperature": 1}, nil)
			if test.want == "" {
				if err != nil || result.Model != "account-model" || len(result.Output) != 1 {
					t.Fatalf("result=%v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProxyAuthenticatesLocallyAndRefreshesUpstream(t *testing.T) {
	var authorization string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()
	c := &Client{apiURL: server.URL}
	testCredentials(t, c, time.Now().Add(time.Hour))
	p, err := c.StartProxy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	request := func(path, key string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", p.BaseURL()+path, strings.NewReader(`{"model":"account-model","input":[],"max_output_tokens":100,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := request("/responses", "wrong")
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Error("proxy accepted invalid key")
	}
	resp = request("/other", p.Key)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Error("proxy exposed another endpoint")
	}
	resp = request("/responses", p.Key)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(data), "response.completed") || authorization != "Bearer access-secret" || p.Key == "access-secret" {
		t.Fatal("proxy did not forward with private upstream credentials")
	}
	if body["max_output_tokens"] != nil || body["store"] != false {
		t.Error("proxy did not apply plan contract")
	}
	tools := body["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "namespace" {
		t.Error("function tools were not namespaced")
	}
	if err := PrepareRequest(map[string]any{"tools": []any{map[string]any{"type": "tool_search"}}}); err == nil {
		t.Fatal("accepted unsupported tool_search")
	}
}

func TestResponseErrorIncludesSafeDiagnostics(t *testing.T) {
	message := ResponseError(400, "invalid_encrypted_content").Error()
	if !strings.Contains(message, "HTTP 400") || !strings.Contains(message, "invalid_encrypted_content") {
		t.Fatalf("missing request diagnostics: %s", message)
	}
	if message := ResponseError(400, "untrusted response body with credentials").Error(); strings.Contains(message, "credentials") {
		t.Fatal("arbitrary server text appeared in diagnostics")
	}
}
