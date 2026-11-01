package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Model struct {
	ID            string `json:"slug"`
	Name          string `json:"display_name"`
	Visibility    string `json:"visibility"`
	ContextWindow int64  `json:"context_window"`
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, errors.New("could not load ChatGPT models")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ResponseError(resp.StatusCode, "")
	}
	var catalog struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, errors.New("invalid ChatGPT model catalog")
	}
	var models []Model
	for _, model := range catalog.Models {
		if model.Visibility == "list" && model.ID != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return nil, errors.New("ChatGPT returned no available models for this account")
	}
	return models, nil
}

// ResponseError keeps credentials and arbitrary server response bodies out of logs.
func ResponseError(status int, code string) error {
	switch code {
	case "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable":
		return errors.New("ChatGPT plan usage is unavailable or its limit was reached. Manage usage at " + UsageURL + " or switch with `pearl provider openrouter`")
	}
	switch status {
	case 401:
		return errors.New("ChatGPT session was rejected; run `pearl auth login chatgpt`")
	case 403:
		return errors.New("ChatGPT plan access was denied; check account permissions or sign in again")
	case 429:
		return errors.New("ChatGPT usage limit reached; retry later or manage usage at " + UsageURL)
	default:
		detail := ""
		if status > 0 {
			detail = fmt.Sprintf("HTTP %d", status)
		}
		if len(code) <= 128 && code != "" && strings.IndexFunc(code, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.')
		}) < 0 {
			if detail != "" {
				detail += ", "
			}
			detail += "code=" + code
		}
		if detail != "" {
			return fmt.Errorf("ChatGPT request failed (%s); retry or check account access", detail)
		}
		return errors.New("ChatGPT request failed; retry or check account access")
	}
}
