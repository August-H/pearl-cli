package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PrepareRequest enforces the documented ChatGPT plan Responses contract.
// It is shared by Pearl's agent and the Codex transport.
func PrepareRequest(payload map[string]any) error {
	if payload == nil {
		return errors.New("invalid ChatGPT request")
	}
	payload["store"] = false
	payload["stream"] = true
	for _, field := range []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent", "prompt", "prompt_cache_retention", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user", "previous_response_id"} {
		delete(payload, field)
	}
	var tools, functions []any
	if raw, ok := payload["tools"]; ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var existing []map[string]any
		if err := json.Unmarshal(encoded, &existing); err != nil {
			return errors.New("invalid ChatGPT tool definitions")
		}
		for _, tool := range existing {
			switch tool["type"] {
			case "function", "custom":
				functions = append(functions, tool)
			case "namespace", "web_search", "web_search_preview":
				tools = append(tools, tool)
			default:
				return fmt.Errorf("tool %v is not supported by ChatGPT plan usage", tool["type"])
			}
		}
		if len(functions) > 0 {
			tools = append(tools, map[string]any{"type": "namespace", "name": "functions", "description": "Tools available to the agent", "tools": functions})
		}
		payload["tools"] = tools
	}
	return nil
}

type Response struct {
	Model  string            `json:"model"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Error  *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (c *Client) Respond(ctx context.Context, client *http.Client, payload map[string]any, delta func(string, string) error) (Response, error) {
	return c.RespondWithRecovery(ctx, client, payload, delta, nil)
}

// Retry only before displaying output. Completed tool calls are returned only
// after response.completed, so a retry never executes a tool a second time.
func (c *Client) RespondWithRecovery(ctx context.Context, client *http.Client, payload map[string]any, delta func(string, string) error, recovery func(string) error) (Response, error) {
	for attempt := 0; ; attempt++ {
		visible := false
		result, err := c.respondOnce(ctx, client, payload, func(kind, value string) error {
			if value != "" {
				visible = true
			}
			if delta != nil {
				return delta(kind, value)
			}
			return nil
		})
		var transient *transientError
		var request *RequestError
		retry := errors.As(err, &transient) || errors.As(err, &request) && (request.Status == 502 || request.Status == 503 || request.Status == 504) && request.Code != "subscription_sharing_usage_unavailable"
		if err == nil || !retry || visible || attempt >= 2 || ctx.Err() != nil {
			return result, err
		}
		if recovery != nil {
			if eventErr := recovery(fmt.Sprintf("Connection interrupted. Retrying request %d/2; completed tool work is saved.", attempt+1)); eventErr != nil {
				return Response{}, eventErr
			}
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
}

func (c *Client) respondOnce(ctx context.Context, client *http.Client, payload map[string]any, delta func(string, string) error) (Response, error) {
	var result Response
	if err := PrepareRequest(payload); err != nil {
		return result, err
	}
	token, err := c.AccessToken(ctx)
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL()+"/responses", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Do not follow redirects with account credentials.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, temporary(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var failure struct {
			Error struct {
				Code  string `json:"code"`
				Type  string `json:"type"`
				Param string `json:"param"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&failure)
		if failure.Error.Code == "" {
			failure.Error.Code = failure.Error.Type
		}
		return result, &RequestError{Status: resp.StatusCode, Code: failure.Error.Code, Param: failure.Error.Param, RequestID: resp.Header.Get("X-Request-ID"), Cause: ResponseError(resp.StatusCode, failure.Error.Code)}
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var data []string
	// ChatGPT plan streams can omit output from response.completed. Retain the
	// completed items as they arrive so tool calls and answers are not lost.
	items := make(map[int]json.RawMessage)
	consume := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		var event struct {
			Type        string          `json:"type"`
			Delta       string          `json:"delta"`
			Code        string          `json:"code"`
			Response    Response        `json:"response"`
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return false, errors.New("invalid ChatGPT response stream")
		}
		switch event.Type {
		case "response.output_item.done":
			if len(event.Item) == 0 || string(event.Item) == "null" {
				return false, errors.New("ChatGPT stream returned an empty output item")
			}
			items[event.OutputIndex] = event.Item
		case "response.output_text.delta", "response.refusal.delta":
			if delta != nil {
				return false, delta("answer", event.Delta)
			}
		case "response.reasoning_summary_text.delta":
			if delta != nil {
				return false, delta("reasoning", event.Delta)
			}
		case "response.completed":
			if event.Response.Status != "completed" {
				return false, errors.New("ChatGPT response did not complete")
			}
			result = event.Response
			if len(result.Output) == 0 {
				indexes := make([]int, 0, len(items))
				for index := range items {
					indexes = append(indexes, index)
				}
				sort.Ints(indexes)
				for _, index := range indexes {
					result.Output = append(result.Output, items[index])
				}
			}
			return true, nil
		case "response.failed", "error":
			code := event.Code
			if event.Response.Error != nil {
				code = event.Response.Error.Code
			}
			return false, ResponseError(0, code)
		case "response.incomplete":
			return false, errors.New("ChatGPT response was incomplete; retry the job to continue")
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := consume()
			if err != nil || done {
				return result, err
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, temporary(err)
	}
	if done, err := consume(); err != nil || done {
		return result, err
	}
	return result, &transientError{errors.New("ChatGPT stream ended before response.completed; retry the job to continue")}
}
