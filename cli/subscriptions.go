package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/August-H/pearl-cli/internal/chatgpt"
	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const subscriptionCheckTimeout = 25 * time.Second

var (
	checkClaudeSubscription      = daemon.CheckClaudeSubscription
	checkAntigravitySubscription = daemon.CheckAntigravitySubscription
	checkChatGPTConnection       = func(ctx context.Context, client *chatgpt.Client, accountID string) error {
		return client.CheckConnection(ctx, accountID)
	}
)

type subscriptionWarning struct {
	account string
	command string
	invalid bool
}

func (w subscriptionWarning) message() string {
	if w.invalid {
		return w.account + " subscription connection is not valid. Sign in again."
	}
	return "Could not verify " + w.account + " subscription connection. Check your network and reopen Pearl to retry."
}

func subscriptionFailure(account, command string, err error) []subscriptionWarning {
	if err == nil || errors.Is(err, daemon.ErrNoPlanLimits) {
		return nil
	}
	invalid := errors.Is(err, chatgpt.ErrSignInRequired) || errors.Is(err, daemon.ErrSubscriptionInvalid) ||
		errors.Is(err, daemon.ErrClaudeSignedOut) || errors.Is(err, daemon.ErrAntigravitySignIn)
	return []subscriptionWarning{{account: account, command: command, invalid: invalid}}
}

// Check saved subscriptions, including accounts that are not currently selected.
// No probe sends a model prompt or changes the selected billing provider.
func checkSubscriptions(parent context.Context) []subscriptionWarning {
	ctx, cancel := context.WithTimeout(parent, subscriptionCheckTimeout)
	defer cancel()
	settings, _ := openrouter_request.LoadAgentSettings()
	selected := openrouter_request.AgentProvider(settings)
	configured := map[string]bool{
		"claude":      selected == "claude" || settings.ClaudeModel != "",
		"antigravity": selected == "antigravity" || settings.AntigravityModel != "",
	}
	if catalogs, _, _ := readSavedModelCatalogs(); len(catalogs) > 0 {
		for _, catalog := range catalogs {
			configured[catalog.provider] = configured[catalog.provider] || catalog.connected
		}
	}
	readers := []func() []subscriptionWarning{
		func() []subscriptionWarning {
			client := newChatGPTClient()
			accounts, err := client.Accounts(ctx)
			if err != nil {
				return subscriptionFailure("ChatGPT", "pearl auth login chatgpt", err)
			}
			var warnings []subscriptionWarning
			active := false
			results := make([][]subscriptionWarning, len(accounts))
			var wait sync.WaitGroup
			for i, account := range accounts {
				active = active || account.Active && account.Connected
				if !account.Connected {
					continue
				}
				wait.Go(func() {
					name := "ChatGPT"
					if account.Email != "" {
						name += " (" + dashboardText(account.Email) + ")"
					}
					command := fmt.Sprintf("pearl auth login chatgpt --account %q", account.ClientID)
					results[i] = subscriptionFailure(name, command, checkChatGPTConnection(ctx, client, account.ClientID))
				})
			}
			wait.Wait()
			for _, result := range results {
				warnings = append(warnings, result...)
			}
			if !active && selected == "chatgpt" {
				warnings = append(warnings, subscriptionFailure("ChatGPT", "pearl auth login chatgpt", chatgpt.ErrSignInRequired)...)
			}
			return warnings
		},
		func() []subscriptionWarning {
			connected, err := checkClaudeSubscription(ctx)
			if !connected && !configured["claude"] {
				return nil
			}
			return subscriptionFailure("Claude", "pearl auth login claude", err)
		},
		func() []subscriptionWarning {
			connected, err := checkAntigravitySubscription(ctx)
			if !connected && !configured["antigravity"] {
				return nil
			}
			return subscriptionFailure("Gemini / Antigravity", "pearl auth login antigravity", err)
		},
	}
	results := make([][]subscriptionWarning, len(readers))
	var wait sync.WaitGroup
	for i, read := range readers {
		wait.Go(func() { results[i] = read() })
	}
	wait.Wait()
	var warnings []subscriptionWarning
	for _, result := range results {
		warnings = append(warnings, result...)
	}
	return warnings
}

type subscriptionUpdate struct {
	revision uint64
	warnings []subscriptionWarning
}

type subscriptionMonitor struct {
	requests chan struct{}
	updates  chan subscriptionUpdate
	revision atomic.Uint64
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func newSubscriptionMonitor(parent context.Context, check func(context.Context) []subscriptionWarning) *subscriptionMonitor {
	ctx, cancel := context.WithCancel(parent)
	m := &subscriptionMonitor{requests: make(chan struct{}, 1), updates: make(chan subscriptionUpdate, 1), cancel: cancel}
	m.wait.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.requests:
			}
			revision := m.revision.Load()
			warnings := check(ctx)
			if ctx.Err() != nil {
				return
			}
			if revision != m.revision.Load() {
				continue
			}
			select {
			case m.updates <- subscriptionUpdate{revision, warnings}:
			case <-ctx.Done():
				return
			}
		}
	})
	return m
}

func (m *subscriptionMonitor) refresh() {
	m.revision.Add(1)
	select {
	case m.requests <- struct{}{}:
	default:
	}
}

func (m *subscriptionMonitor) close() { m.cancel(); m.wait.Wait() }

func subscriptionCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "auth" || args[0] == "claude" || args[0] == "antigravity")
}

func subscriptionWarningLines(warnings []subscriptionWarning, width int, color bool) string {
	var text strings.Builder
	for _, warning := range warnings {
		for _, line := range jobViewWrapLine("Warning: "+warning.message(), max(1, width)) {
			fmt.Fprintln(&text, dashboardPaint(color, ansiYellow, line))
		}
		if warning.invalid {
			for _, line := range jobViewWrapLine("Run: "+warning.command, max(1, width)) {
				fmt.Fprintln(&text, dashboardPaint(color, ansiYellow, line))
			}
		}
	}
	if len(warnings) > 0 {
		text.WriteByte('\n')
	}
	return text.String()
}
