package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	usageCommandHelp = "Usage: pearl usage [chatgpt|claude|antigravity]"
	usageBarWidth    = 20
)

// The readers are variables so tests don't need real accounts.
var (
	readClaudeUsage      = daemon.ClaudeUsage
	readCodexUsage       = daemon.CodexUsage
	readAntigravityUsage = daemon.AntigravityUsage
)

func runUsage(args []string) int {
	provider := currentProvider()
	switch {
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Println(usageCommandHelp)
		fmt.Println()
		fmt.Println("  Shows your plan's limits. With no name, shows the plan for the selected model.")
		return 0
	case len(args) == 1:
		provider = strings.ToLower(strings.TrimSpace(args[0]))
		if provider == "gemini" {
			provider = openrouter_request.ProviderAntigravity
		}
	case len(args) > 1:
		fmt.Fprintln(os.Stderr, usageCommandHelp)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var (
		usage daemon.Usage
		err   error
	)
	switch provider {
	case openrouter_request.ProviderClaude:
		usage, err = readClaudeUsage(ctx)
	case openrouter_request.ProviderChatGPT:
		usage, err = readCodexUsage(ctx)
	case openrouter_request.ProviderAntigravity:
		usage, err = readAntigravityUsage(ctx)
	case openrouter_request.ProviderOpenRouter:
		err = daemon.ErrNoPlanLimits
	default:
		fmt.Fprintln(os.Stderr, usageCommandHelp)
		return 2
	}
	if errors.Is(err, daemon.ErrNoPlanLimits) {
		fmt.Println("API")
		fmt.Println("  Billed per token, so there are no plan limits to show.")
		return 0
	}
	if err != nil {
		return printError("Usage", err)
	}
	printUsage(os.Stdout, provider, usage, time.Now())
	if provider == openrouter_request.ProviderChatGPT {
		warnIfOtherChatGPTAccount(ctx, usage.Account)
	}
	return 0
}

func printUsage(output io.Writer, provider string, usage daemon.Usage, now time.Time) {
	fmt.Fprintln(output, usageTitle(provider, usage))
	printUsageWindows(output, usage, now)
}

func usageTitle(provider string, usage daemon.Usage) string {
	title := billingLabel(provider)
	if plan := usagePlanName(usage.Plan); plan != "" {
		title += " (" + plan + ")"
	}
	return title
}

func printUsageWindows(output io.Writer, usage daemon.Usage, now time.Time) {
	if len(usage.Windows) == 0 {
		fmt.Fprintln(output, "  No limits reported.")
		return
	}
	width := 0
	for _, window := range usage.Windows {
		width = max(width, len([]rune(window.Label)))
	}
	for _, window := range usage.Windows {
		line := fmt.Sprintf("  %-*s  %s  %3.0f%% used", width, window.Label, usageBar(window.UsedPercent), window.UsedPercent)
		if reset := usageResetText(window.ResetsAt, now); reset != "" {
			line += "  " + reset
		}
		fmt.Fprintln(output, line)
	}
}

// warnIfOtherChatGPTAccount says when the limits shown belong to a different
// account from the one Pearl's jobs use.
func warnIfOtherChatGPTAccount(ctx context.Context, codexAccount string) {
	accounts, err := newChatGPTClient().Accounts(ctx)
	if err != nil || codexAccount == "" {
		return
	}
	for _, account := range accounts {
		if account.Active && account.Email != "" && !strings.EqualFold(account.Email, codexAccount) {
			fmt.Printf("Note: these limits are for %s, the account Codex is signed in to. Pearl's jobs use %s.\n",
				dashboardText(codexAccount), dashboardText(account.Email))
		}
	}
}

func usagePlanName(plan string) string {
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return ""
	}
	return strings.ToUpper(plan[:1]) + plan[1:]
}

func usageBar(usedPercent float64) string {
	filled := int(math.Round(usedPercent / 100 * usageBarWidth))
	filled = min(max(0, filled), usageBarWidth)
	return strings.Repeat("█", filled) + strings.Repeat("░", usageBarWidth-filled)
}

func usageResetText(resetsAt, now time.Time) string {
	if resetsAt.IsZero() {
		return ""
	}
	wait := resetsAt.Sub(now)
	if wait <= 0 {
		return "resetting now"
	}
	minutes := int(math.Ceil(wait.Minutes()))
	switch {
	case minutes < 60:
		return fmt.Sprintf("resets in %dm", minutes)
	case minutes < 24*60:
		return fmt.Sprintf("resets in %dh %dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("resets in %dd %dh", minutes/(24*60), minutes/60%24)
}
