package chatgpt

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	budgets "github.com/August-H/pearl-cli/internal/resource"
)

func TestResponsesUsageExhaustsBudgetBeforeAnotherRequest(t *testing.T) {
	c := NewClient()
	testCredentials(t, c, time.Now().Add(time.Hour))
	calls, reports := 0, 0
	exhausted := false
	ctx := budgets.WithBudget(context.Background(), func(context.Context) error {
		if exhausted {
			return budgets.ErrBudgetExceeded
		}
		return nil
	}, func(_ context.Context, u budgets.Usage) error {
		reports++
		if u.Tokens != 12 {
			t.Fatalf("usage %+v", u)
		}
		exhausted = true
		return budgets.ErrBudgetExceeded
	})
	client := &http.Client{Transport: recoveryTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}` + "\n\n"))}, nil
	})}
	for range 2 {
		if _, err := c.RespondWithRecovery(ctx, client, map[string]any{"input": []any{}}, nil, nil); !errors.Is(err, budgets.ErrBudgetExceeded) {
			t.Fatalf("budget: %v", err)
		}
	}
	if calls != 1 || reports != 1 {
		t.Fatalf("calls=%d reports=%d", calls, reports)
	}
}
