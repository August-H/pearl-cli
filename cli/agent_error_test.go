package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestFormatAgentErrorHumanizesRateLimitJSON(t *testing.T) {
	payload := `{"error":{"message":"Rate limit exceeded: free-models-per-day. Add 10 credits to unlock 1000 free model requests per day","code":429,"metadata":{"headers":{"X-RateLimit-Limit":"50","X-RateLimit-Remaining":"0","X-RateLimit-Reset":"1786320000000"},"limit_source":"openrouter_free_tier_daily"}}}`
	message := formatAgentError(payload)
	if strings.Contains(message, "{") || !strings.Contains(message, "Rate limit exceeded") {
		t.Fatalf("formatAgentError() = %q", message)
	}
	wantReset := time.Unix(1786320000000/1000, 0).Local().Format("January 2 3:04pm MST")
	if !strings.Contains(message, "Resets "+wantReset) {
		t.Fatalf("formatAgentError() = %q, want reset time %q", message, wantReset)
	}
}

func TestFormatAgentErrorPassesThroughPlainTextAndOtherJSON(t *testing.T) {
	for _, data := range []string{
		"agent could not start",
		`{"error":{"message":"","code":500}}`,
		`{"other":"shape"}`,
	} {
		if got := formatAgentError(data); got != data {
			t.Fatalf("formatAgentError(%q) = %q", data, got)
		}
	}
}

func TestJobBoardTextTruncatesWithoutSplittingRunes(t *testing.T) {
	prompt := strings.Repeat("データ", 30)
	truncated := jobBoardText(prompt)
	if len([]rune(truncated)) != 60 {
		t.Fatalf("jobBoardText() rune count = %d, want 60", len([]rune(truncated)))
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Fatalf("jobBoardText() = %q, want ellipsis suffix", truncated)
	}
	if short := jobBoardText("short prompt"); short != "short prompt" {
		t.Fatalf("jobBoardText(short) = %q", short)
	}
}

func TestJobViewDurationExcludesWaitingForInputTime(t *testing.T) {
	started := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	finished := started.Add(100 * time.Second)
	job := store.Job{
		Status:     store.JobCompleted,
		StartedAt:  &started,
		FinishedAt: &finished,
	}
	statusEvents := []store.Event{
		{Type: "status", Data: store.JobQueued, CreatedAt: started.Add(-time.Second)},
		{Type: "status", Data: store.JobRunning, CreatedAt: started},
		{Type: "status", Data: store.JobWaitingInput, CreatedAt: started.Add(10 * time.Second)},
		{Type: "status", Data: store.JobQueued, CreatedAt: started.Add(90 * time.Second)},
		{Type: "status", Data: store.JobRunning, CreatedAt: started.Add(91 * time.Second)},
		{Type: "status", Data: store.JobCompleted, CreatedAt: finished},
	}
	if got, want := jobViewDuration(job, statusEvents), "20s"; got != want {
		t.Fatalf("jobViewDuration() = %q, want %q", got, want)
	}

	waiting := store.Job{Status: store.JobWaitingInput, StartedAt: &started}
	stillPaused := statusEvents[:3]
	if got := jobViewDuration(waiting, stillPaused); got != "10s" {
		t.Fatalf("waiting duration = %q, want 10s excluding the open pause", got)
	}
}
