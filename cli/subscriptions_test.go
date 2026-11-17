package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/chatgpt"
	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/store"
)

func stubSubscriptionChecks(t *testing.T, settings string) {
	t.Helper()
	writeModelTestSettings(t, settings)
	claude, google, gpt := checkClaudeSubscription, checkAntigravitySubscription, checkChatGPTConnection
	checkClaudeSubscription = func(context.Context) (bool, error) { return false, daemon.ErrClaudeSignedOut }
	checkAntigravitySubscription = func(context.Context) (bool, error) { return false, daemon.ErrAntigravitySignIn }
	checkChatGPTConnection = func(context.Context, *chatgpt.Client, string) error { return nil }
	t.Cleanup(func() {
		checkClaudeSubscription, checkAntigravitySubscription, checkChatGPTConnection = claude, google, gpt
	})
}

func TestSubscriptionsCheckInactiveAccountsAndAllProviders(t *testing.T) {
	stubSubscriptionChecks(t, `{"provider":"openrouter","model":"openrouter/free"}`)
	credentials := `{"active_client_id":"active","accounts":[
{"client_id":"active","email":"active@example.com","access_token":"test"},
{"client_id":"inactive","email":"inactive@example.com","access_token":"test"},
{"client_id":"logged-out","email":"out@example.com"}]}`
	if err := os.WriteFile(filepath.Join(os.Getenv("PEARL_CONFIG_DIR"), "chatgpt.json"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	checked := make(chan string, 4)
	checkChatGPTConnection = func(_ context.Context, _ *chatgpt.Client, id string) error {
		checked <- id
		if id == "inactive" {
			return chatgpt.ErrSignInRequired
		}
		return nil
	}
	checkClaudeSubscription = func(context.Context) (bool, error) { checked <- "claude"; return true, daemon.ErrSubscriptionInvalid }
	checkAntigravitySubscription = func(context.Context) (bool, error) { checked <- "google"; return true, daemon.ErrSubscriptionInvalid }
	warnings := checkSubscriptions(context.Background())
	if len(checked) != 4 || len(warnings) != 3 {
		t.Fatalf("checked=%d warnings=%+v", len(checked), warnings)
	}
	if warnings[0].account != "ChatGPT (inactive@example.com)" || !strings.Contains(warnings[0].command, `--account "inactive"`) {
		t.Fatalf("wrong reconnect command: %+v", warnings[0])
	}
	if warnings[1].command != "pearl auth login claude" || warnings[2].command != "pearl auth login antigravity" {
		t.Fatalf("provider commands = %+v", warnings)
	}
	for _, warning := range warnings {
		if !warning.invalid {
			t.Fatalf("rejected subscription wasn't flagged: %+v", warning)
		}
	}
	accounts, err := newChatGPTClient().Accounts(context.Background())
	if err != nil || !accounts[0].Active || accounts[1].Active {
		t.Fatalf("active account changed: %+v, %v", accounts, err)
	}
}

func TestSubscriptionsKeepUnusedProvidersQuietAndWarnOnSelectedSignOut(t *testing.T) {
	stubSubscriptionChecks(t, `{"provider":"openrouter","model":"openrouter/free"}`)
	if warnings := checkSubscriptions(context.Background()); len(warnings) != 0 {
		t.Fatalf("unused connections = %+v", warnings)
	}
	path := filepath.Join(os.Getenv("PEARL_CONFIG_DIR"), "settings.json")
	if err := os.WriteFile(path, []byte(`{"provider":"claude","model":"openrouter/free"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings := checkSubscriptions(context.Background())
	if len(warnings) != 1 || !warnings[0].invalid || warnings[0].command != "pearl auth login claude" {
		t.Fatalf("selected sign-out = %+v", warnings)
	}
	checkClaudeSubscription = func(context.Context) (bool, error) { return true, errors.New("network timeout; secret=never-print") }
	warnings = checkSubscriptions(context.Background())
	if len(warnings) != 1 || warnings[0].invalid || strings.Contains(warnings[0].message(), "secret") {
		t.Fatalf("temporary outage = %+v", warnings)
	}
	checkClaudeSubscription = func(context.Context) (bool, error) { return false, daemon.ErrNoPlanLimits }
	if warnings := checkSubscriptions(context.Background()); len(warnings) != 0 {
		t.Fatalf("API key treated as a subscription: %+v", warnings)
	}
}

func TestSubscriptionProbesRunInParallel(t *testing.T) {
	stubSubscriptionChecks(t, `{"provider":"openrouter","model":"openrouter/free"}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	probe := func(ctx context.Context) (bool, error) {
		started <- struct{}{}
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	checkClaudeSubscription, checkAntigravitySubscription = probe, probe
	done := make(chan []subscriptionWarning, 1)
	go func() { done <- checkSubscriptions(ctx) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("providers were checked serially")
		}
	}
	close(release)
	if warnings := <-done; len(warnings) != 0 {
		t.Fatalf("valid subscriptions = %+v", warnings)
	}
}

func TestSubscriptionMonitorDiscardsStaleChecksAfterReauthorization(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m := newSubscriptionMonitor(context.Background(), func(context.Context) []subscriptionWarning {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return []subscriptionWarning{{account: "stale", invalid: true}}
		}
		return nil
	})
	defer m.close()
	m.refresh()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background check didn't start")
	}
	m.refresh()
	close(release)
	select {
	case result := <-m.updates:
		if result.revision != 2 || len(result.warnings) != 0 {
			t.Fatalf("stale check published: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("reauthorization didn't trigger a fresh check")
	}
}

func TestSubscriptionMonitorCancelsChecksOnExit(t *testing.T) {
	started := make(chan struct{})
	m := newSubscriptionMonitor(context.Background(), func(ctx context.Context) []subscriptionWarning { close(started); <-ctx.Done(); return nil })
	m.refresh()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background check didn't start")
	}
	done := make(chan struct{})
	go func() { m.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("exit waited for network timeout")
	}
}

func TestSubscriptionWarningsStayVisibleAboveComposer(t *testing.T) {
	warnings := []subscriptionWarning{{account: "Claude", command: "pearl auth login claude", invalid: true}}
	jobs := make([]store.Job, 50)
	for i := range jobs {
		jobs[i] = store.Job{ID: "running-job", Status: store.JobRunning}
	}
	body, composer := renderDashboardParts(jobs, time.Now(), 80, false, nil, "draft", "another notice", 0, dashboardCommandView{subscriptions: warnings})
	var scroll dashboardBodyScroll
	frame := scroll.fit(body, composer, 80, 24, false)
	for _, expected := range []string{"subscription connection is not valid", "pearl auth login claude", "another notice", "draft"} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("missing %q in frame:\n%s", expected, frame)
		}
	}
	for _, line := range strings.Split(subscriptionWarningLines(warnings, 24, false), "\n") {
		if len([]rune(line)) > 24 {
			t.Fatalf("warning clipped: %s", line)
		}
	}
}
