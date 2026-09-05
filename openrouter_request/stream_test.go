package openrouter_request

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

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
