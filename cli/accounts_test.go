package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
)

// stubAccounts replaces every provider. Each reader starts out signed out.
func stubAccounts(t *testing.T) {
	t.Helper()
	claude, codex, antigravity := readClaudeUsage, readCodexUsage, readAntigravityUsage
	readClaudeUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, daemon.ErrClaudeSignedOut }
	readCodexUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, daemon.ErrCodexSignedOut }
	readAntigravityUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, daemon.ErrAntigravitySignIn }
	t.Cleanup(func() { readClaudeUsage, readCodexUsage, readAntigravityUsage = claude, codex, antigravity })
	t.Setenv("OPENROUTER_API_KEY", "")
	// Pearl also looks for a key in the user's config folder.
	t.Setenv("HOME", t.TempDir())
	writeModelTestSettings(t, `{"provider":"claude","model":"openrouter/free","claude_model":"opus"}`)
}

func usageFor(account, plan string, used float64) func(context.Context) (daemon.Usage, error) {
	return func(context.Context) (daemon.Usage, error) {
		return daemon.Usage{Account: account, Plan: plan, Windows: []daemon.UsageWindow{{Label: "Weekly", UsedPercent: used}}}, nil
	}
}

func TestAccountsSaysSoWhenNothingIsSignedIn(t *testing.T) {
	stubAccounts(t)
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
	if exitCode != 0 || !strings.HasPrefix(output, "No accounts are signed in.") {
		t.Fatalf("exit=%d output=%q", exitCode, output)
	}
}

func TestAccountsListsEveryProviderInOrderAndMarksTheOneInUse(t *testing.T) {
	stubAccounts(t)
	readClaudeUsage = usageFor("claude@example.com", "pro", 10)
	readCodexUsage = usageFor("codex@example.com", "plus", 20)
	readAntigravityUsage = usageFor("google@example.com", "", 30)
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
	if exitCode != 0 {
		t.Fatalf("exit=%d", exitCode)
	}
	for _, want := range []string{
		"ChatGPT subscription (Plus) · codex@example.com · Codex sign-in only\n",
		"Claude subscription (Pro) · claude@example.com · in use\n",
		"Gemini subscription · google@example.com\n",
		" 20% used", " 10% used", " 30% used",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	if strings.Index(output, "ChatGPT") > strings.Index(output, "Claude") || strings.Index(output, "Claude") > strings.Index(output, "Gemini") {
		t.Errorf("order changed:\n%s", output)
	}
}

func TestAccountsShowsAProblemWithOneProviderAndStillTheOthers(t *testing.T) {
	stubAccounts(t)
	readClaudeUsage = func(context.Context) (daemon.Usage, error) {
		return daemon.Usage{Account: "claude@example.com"}, errors.New("it broke")
	}
	readAntigravityUsage = usageFor("google@example.com", "", 30)
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
	if exitCode != 0 || !strings.Contains(output, "claude@example.com") || !strings.Contains(output, "Limits unavailable: it broke") ||
		!strings.Contains(output, " 30% used") {
		t.Fatalf("exit=%d output:\n%s", exitCode, output)
	}
}

func TestAccountsLabelsAPIKeys(t *testing.T) {
	stubAccounts(t)
	readClaudeUsage = func(context.Context) (daemon.Usage, error) {
		return daemon.Usage{Account: "claude@example.com"}, daemon.ErrNoPlanLimits
	}
	t.Setenv("OPENROUTER_API_KEY", "secret-key-value")
	output, _ := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
	for _, want := range []string{"API (Claude Code) · claude@example.com", "API (OpenRouter)", apiLimitsNote} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	if strings.Contains(output, "secret-key-value") {
		t.Errorf("the API key was printed:\n%s", output)
	}
}

// Only the login Codex holds can read limits, so each Pearl account says whether it
// has them. These tests use saved credentials the way Pearl stores them.
func writeChatGPTAccounts(t *testing.T, contents string) {
	t.Helper()
	settings := os.Getenv("PEARL_CONFIG_DIR")
	if settings == "" {
		t.Fatal("the test settings didn't set PEARL_CONFIG_DIR")
	}
	if err := os.WriteFile(filepath.Join(settings, "chatgpt.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

const twoChatGPTAccounts = `{"active_client_id":"a","accounts":[
	{"client_id":"a","email":"first@example.com","access_token":"t"},
	{"client_id":"b","email":"second@example.com","access_token":"t"},
	{"client_id":"c","email":"gone@example.com"}]}`

func TestAccountsMatchesChatGPTAccountsToCodexsLogin(t *testing.T) {
	stubAccounts(t)
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openrouter/free","chatgpt_model":"gpt-6.1-sol"}`)
	writeChatGPTAccounts(t, twoChatGPTAccounts)
	readCodexUsage = usageFor("SECOND@example.com", "pro", 40)
	output, _ := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
	for _, want := range []string{
		"ChatGPT subscription · first@example.com · in use\n  Limits unavailable: Pearl reads ChatGPT limits from Codex, which is signed in to SECOND@example.com.",
		"ChatGPT subscription (Pro) · SECOND@example.com\n",
		" 40% used",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	if strings.Contains(output, "gone@example.com") {
		t.Errorf("a signed-out account was listed:\n%s", output)
	}
	if strings.Contains(output, "Codex sign-in only") {
		t.Errorf("the matching login was listed twice:\n%s", output)
	}
}

func TestAccountsExplainsWhenCodexCantReadLimits(t *testing.T) {
	stubAccounts(t)
	writeChatGPTAccounts(t, twoChatGPTAccounts)
	for _, test := range []struct {
		err  error
		want string
	}{
		{daemon.ErrCodexSignedOut, "codex login"},
		{daemon.ErrCodexNotInstalled, "npm install -g @openai/codex"},
		{daemon.ErrNoPlanLimits, "which is using an API key"},
	} {
		readCodexUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, test.err }
		output, _ := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
		if !strings.Contains(output, "first@example.com") || !strings.Contains(output, test.want) {
			t.Errorf("%v: output:\n%s", test.err, output)
		}
	}
}

func TestAccountsHidesAMissingCodexWhenPearlHasNoChatGPTAccount(t *testing.T) {
	stubAccounts(t)
	readClaudeUsage = usageFor("claude@example.com", "pro", 10)
	for _, err := range []error{daemon.ErrCodexSignedOut, daemon.ErrCodexNotInstalled} {
		readCodexUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, err }
		output, _ := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
		if strings.Contains(output, "ChatGPT") {
			t.Errorf("%v: output:\n%s", err, output)
		}
	}
	readCodexUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, errors.New("it broke") }
	output, _ := captureTestStdout(t, func() int { return Run([]string{"accounts"}) })
	if !strings.Contains(output, "ChatGPT subscription\n  Limits unavailable: it broke") {
		t.Errorf("a failed read must still show:\n%s", output)
	}
}

func TestAccountsRejectsArgumentsAndShowsHelp(t *testing.T) {
	stubAccounts(t)
	if exitCode := runAccounts([]string{"claude"}); exitCode != 2 {
		t.Errorf("exit = %d", exitCode)
	}
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"help", "accounts"}) })
	if exitCode != 0 || !strings.Contains(output, accountsUsage) {
		t.Errorf("exit=%d output=%q", exitCode, output)
	}
	if _, known := dashboardCommandDescriptions["accounts"]; !known {
		t.Error("the dashboard rejects accounts")
	}
}

func TestPrintAccountEntryCleansWhatItShows(t *testing.T) {
	var output bytes.Buffer
	printAccountEntry(&output, accountEntry{
		title: "T", usage: daemon.Usage{Account: "a\x1b[31m@example.com"}, note: "bad\x1b[2Jnote",
	}, time.Now())
	if strings.Contains(output.String(), "\x1b") {
		t.Errorf("escape codes got through: %q", output.String())
	}
}

func TestCodexCommandPointsToYourOwnInstall(t *testing.T) {
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"codex", "install"}) })
	if exitCode != 0 || !strings.Contains(output, "doesn't install Codex") {
		t.Errorf("exit=%d output=%q", exitCode, output)
	}
	t.Setenv("PEARL_CODEX_PATH", filepath.Join(t.TempDir(), "codex"))
	output, exitCode = captureTestStdout(t, func() int { return Run([]string{"codex"}) })
	if exitCode != 0 || !strings.Contains(output, "Codex:") {
		t.Errorf("exit=%d output=%q", exitCode, output)
	}
}
