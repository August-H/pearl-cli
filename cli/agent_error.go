package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type openRouterErrorPayload struct {
	Error struct {
		Message  string `json:"message"`
		Code     any    `json:"code"`
		Metadata struct {
			Headers map[string]string `json:"headers"`
		} `json:"metadata"`
	} `json:"error"`
}

func formatAgentError(data string) string {
	var payload openRouterErrorPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil ||
		strings.TrimSpace(payload.Error.Message) == "" {
		return data
	}
	message := strings.TrimSpace(payload.Error.Message)
	if isRateLimited(payload.Error.Code, message) {
		if reset := rateLimitResetTime(
			payload.Error.Metadata.Headers["X-RateLimit-Reset"],
		); !reset.IsZero() {
			message += fmt.Sprintf(" Resets %s.", reset.Local().Format("January 2 3:04pm MST"))
		}
	}
	return message
}

func isRateLimited(code any, message string) bool {
	if limit, ok := code.(float64); ok && int(limit) == http.StatusTooManyRequests {
		return true
	}
	return strings.Contains(strings.ToLower(message), "rate limit")
}

func rateLimitResetTime(value string) time.Time {
	milliseconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || milliseconds <= 0 {
		return time.Time{}
	}
	return time.Unix(milliseconds/1000, (milliseconds%1000)*int64(time.Millisecond))
}
