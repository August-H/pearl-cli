package chatgpt

import (
	"errors"
	"fmt"
	"strings"
)

// RequestError retains useful diagnostics without retaining credentials or input.
type RequestError struct {
	Status    int
	Code      string
	Param     string
	RequestID string
	Cause     error
}

func (e *RequestError) Error() string {
	message := e.Cause.Error()
	if safeDiagnostic(e.Param) {
		message += "; parameter=" + e.Param
	}
	if safeDiagnostic(e.RequestID) {
		message += "; request=" + e.RequestID
	}
	return message
}

func safeDiagnostic(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.[/]", r))
	}) < 0
}

func HistoryRejected(err error) bool {
	var request *RequestError
	return errors.As(err, &request) && request.Status == 400 && (request.Code == "invalid_request_error" || request.Code == "invalid_encrypted_content" || strings.HasPrefix(request.Param, "input"))
}

func (e *RequestError) Unwrap() error { return e.Cause }

type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func temporary(err error) error {
	return &transientError{fmt.Errorf("ChatGPT connection interrupted: %w", err)}
}
