package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	accountsUsage     = "Usage: pearl accounts"
	apiLimitsNote     = "Billed per token, so there are no plan limits to show."
	limitsUnavailable = "Limits unavailable: "
)

// accountEntry is one sign-in and what to say about its limits.
type accountEntry struct {
	title string
	usage daemon.Usage
	// tags follow the title, and note replaces the limits when they can't be shown.
	tags  []string
	note  string
	inUse bool
}

func runAccounts(args []string) int {
	switch {
	case len(args) == 0:
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Println(accountsUsage)
		fmt.Println()
		fmt.Println("  Shows every account you're signed in to and how much of its limits you've")
		fmt.Println("  used. It asks each provider directly and uses no tokens.")
		return 0
	default:
		fmt.Fprintln(os.Stderr, accountsUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	selected := currentProvider()
	// Ask every provider at once, then show them in a fixed order as they're ready.
	readers := []func(context.Context, string) []accountEntry{
		chatGPTAccountEntries, claudeAccountEntries, antigravityAccountEntries, openRouterAccountEntries,
	}
	results := make([]chan []accountEntry, len(readers))
	for index, read := range readers {
		results[index] = make(chan []accountEntry, 1)
		go func() { results[index] <- read(ctx, selected) }()
	}
	shown := 0
	for _, result := range results {
		for _, entry := range <-result {
			if shown > 0 {
				fmt.Println()
			}
			printAccountEntry(os.Stdout, entry, time.Now())
			shown++
		}
	}
	if shown == 0 {
		fmt.Println("No accounts are signed in. Run `pearl auth login chatgpt`, `claude` or `antigravity`.")
	}
	return 0
}

func printAccountEntry(output io.Writer, entry accountEntry, now time.Time) {
	parts := []string{entry.title}
	if entry.usage.Account != "" {
		parts = append(parts, dashboardText(entry.usage.Account))
	}
	parts = append(parts, entry.tags...)
	if entry.inUse {
		parts = append(parts, "in use")
	}
	fmt.Fprintln(output, strings.Join(parts, " · "))
	if entry.note != "" {
		fmt.Fprintln(output, "  "+dashboardText(entry.note))
		return
	}
	printUsageWindows(output, entry.usage, now)
}

// accountEntryFromUsage covers a provider with one sign-in.
func accountEntryFromUsage(provider, harness string, usage daemon.Usage, err error, inUse bool) accountEntry {
	entry := accountEntry{title: usageTitle(provider, usage), usage: usage, inUse: inUse}
	switch {
	case errors.Is(err, daemon.ErrNoPlanLimits):
		entry.title = billingLabel(daemon.BillingAPI) + " (" + harness + ")"
		entry.note = apiLimitsNote
	case err != nil:
		entry.note = limitsUnavailable + err.Error()
	}
	return entry
}

func claudeAccountEntries(ctx context.Context, selected string) []accountEntry {
	usage, err := readClaudeUsage(ctx)
	if errors.Is(err, daemon.ErrClaudeSignedOut) {
		return nil
	}
	return []accountEntry{accountEntryFromUsage(
		openrouter_request.ProviderClaude, harnessLabel(store.HarnessClaude), usage, err,
		selected == openrouter_request.ProviderClaude,
	)}
}

func antigravityAccountEntries(ctx context.Context, selected string) []accountEntry {
	usage, err := readAntigravityUsage(ctx)
	if errors.Is(err, daemon.ErrAntigravitySignIn) {
		return nil
	}
	return []accountEntry{accountEntryFromUsage(
		openrouter_request.ProviderAntigravity, harnessLabel(store.HarnessAntigravity), usage, err,
		selected == openrouter_request.ProviderAntigravity,
	)}
}

func openRouterAccountEntries(_ context.Context, selected string) []accountEntry {
	if _, err := openrouter_request.LoadOpenRouterAPIKey(); err != nil {
		return nil
	}
	return []accountEntry{{
		title: billingLabel(openrouter_request.ProviderOpenRouter) + " (OpenRouter)",
		note:  apiLimitsNote,
		inUse: selected == openrouter_request.ProviderOpenRouter,
	}}
}

// chatGPTAccountEntries lists Pearl's ChatGPT sign-ins. OpenAI gives their limits
// only to Codex's own login, so only the account Codex is signed in to has any to
// show. That login gets its own entry when Pearl isn't signed in to it.
func chatGPTAccountEntries(ctx context.Context, selected string) []accountEntry {
	provider := openrouter_request.ProviderChatGPT
	accounts, listErr := newChatGPTClient().Accounts(ctx)
	usage, err := readCodexUsage(ctx)
	var entries []accountEntry
	matched := false
	for _, account := range accounts {
		if !account.Connected {
			continue
		}
		entry := accountEntry{
			title: billingLabel(provider),
			usage: daemon.Usage{Account: account.Email},
			inUse: account.Active && selected == provider,
		}
		switch {
		case errors.Is(err, daemon.ErrNoPlanLimits):
			entry.note = limitsUnavailable + "Pearl reads ChatGPT limits from Codex, which is using an API key."
		case err != nil:
			entry.note = limitsUnavailable + err.Error()
		case usage.Account != "" && strings.EqualFold(account.Email, usage.Account):
			entry.usage = usage
			entry.title = usageTitle(provider, usage)
			matched = true
		case usage.Account != "":
			entry.note = limitsUnavailable + "Pearl reads ChatGPT limits from Codex, which is signed in to " + usage.Account + "."
		default:
			entry.note = limitsUnavailable + "Pearl reads ChatGPT limits from Codex, which is signed in to another account."
		}
		entries = append(entries, entry)
	}
	if listErr != nil {
		entries = append(entries, accountEntry{title: billingLabel(provider), note: "Couldn't read Pearl's sign-ins: " + listErr.Error()})
	}
	switch {
	case err == nil && !matched:
		entries = append(entries, accountEntry{title: usageTitle(provider, usage), usage: usage, tags: []string{"Codex sign-in only"}})
	case len(entries) > 0 || errors.Is(err, daemon.ErrCodexNotInstalled) || errors.Is(err, daemon.ErrCodexSignedOut):
	default:
		// Nothing else would show this sign-in.
		entries = append(entries, accountEntryFromUsage(provider, harnessLabel(store.HarnessCodex), usage, err, false))
	}
	return entries
}
