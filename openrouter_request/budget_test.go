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

	"github.com/August-H/pearl-cli/internal/resource"
)

func TestOpenRouterUsageStopsToolExecutionAndFurtherRequests(t *testing.T) {
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"model":"test/model","max_depth":4}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls, executed, reports := 0, 0, 0
	original := sharedAgentHTTPClient
	sharedAgentHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		stream := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"noop","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
			`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10,"cost":0.25}}` + "\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}
	t.Cleanup(func() { sharedAgentHTTPClient = original })
	exhausted := false
	ctx := resource.WithBudget(context.Background(), func(context.Context) error {
		if exhausted {
			return resource.ErrBudgetExceeded
		}
		return nil
	}, func(_ context.Context, u resource.Usage) error {
		reports++
		if u.Tokens != 10 || u.CostUSD != 0.25 {
			t.Fatalf("usage %+v", u)
		}
		exhausted = true
		return resource.ErrBudgetExceeded
	})
	_, err := Run(ctx, "work", RunOptions{WorkspaceRoot: t.TempDir(), OnlyTools: true, Tools: []Tool{{Name: "noop", Description: "noop", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, json.RawMessage) (any, error) { executed++; return "ok", nil }}}})
	if !errors.Is(err, resource.ErrBudgetExceeded) || calls != 1 || executed != 0 || reports != 1 {
		t.Fatalf("err=%v requests=%d tools=%d reports=%d", err, calls, executed, reports)
	}
	_, err = requestAgentCompletion(ctx, sharedAgentHTTPClient, "test-key", "test/model", nil, nil, nil)
	if !errors.Is(err, resource.ErrBudgetExceeded) || calls != 1 {
		t.Fatalf("request bypassed budget: %v, %d calls", err, calls)
	}
}
