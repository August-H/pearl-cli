package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/store"
)

func TestBillingAndHarnessLabels(t *testing.T) {
	for provider, want := range map[string]string{
		"chatgpt": "ChatGPT subscription", "claude": "Claude subscription", "antigravity": "Gemini subscription",
		"openrouter": "API", daemon.BillingAPI: "API", "": "", "other": "other",
	} {
		if got := billingLabel(provider); got != want {
			t.Errorf("billingLabel(%q) = %q, want %q", provider, got, want)
		}
	}
	for harness, want := range map[string]string{
		"claude": "Claude Code", "antigravity": "Antigravity", "codex": "Codex", "pearl": "Pearl", "default": "default",
	} {
		if got := harnessLabel(harness); got != want {
			t.Errorf("harnessLabel(%q) = %q, want %q", harness, got, want)
		}
	}
}

func TestJobLinesUseTheLabels(t *testing.T) {
	details := jobRunDetails(store.Job{Provider: "claude", Model: "opus", Harness: "claude"})
	if !strings.Contains(details, "Claude subscription · opus · Harness: Claude Code") {
		t.Errorf("details = %q", details)
	}
	details = jobRunDetails(store.Job{Provider: "openrouter", Model: "x/y", Harness: "pearl"})
	if !strings.Contains(details, "API · x/y · Harness: Pearl") {
		t.Errorf("details = %q", details)
	}
	if got := jobViewHarness(store.Job{Harness: "antigravity"}); got != "Antigravity" {
		t.Errorf("job view harness = %q", got)
	}
}

func TestDashboardShowsTheBillingLabel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	output := renderDashboardComposer(nil, "", "", 120, false, 0, false,
		dashboardConfigureInactive, false, "opus", "claude", 0, 0, 0, true, "claude")
	if !strings.Contains(output, "Billing: Claude subscription · Model: opus · Harness: Claude Code") {
		t.Errorf("output = %q", output)
	}
	t.Setenv("ANTHROPIC_API_KEY", "key")
	output = renderDashboardComposer(nil, "", "", 120, false, 0, false,
		dashboardConfigureInactive, false, "opus", "claude", 0, 0, 0, true, "claude")
	if !strings.Contains(output, "Billing: API") {
		t.Errorf("with an API key, output = %q", output)
	}
}

func TestPrintUsageShowsEachLimit(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var output strings.Builder
	printUsage(&output, "claude", daemon.Usage{Plan: "pro", Windows: []daemon.UsageWindow{
		{Label: "5-hour", UsedPercent: 50, ResetsAt: now.Add(90*time.Minute + 30*time.Second)},
		{Label: "Weekly · Fable", UsedPercent: 100, ResetsAt: now.Add(49 * time.Hour)},
		{Label: "Monthly spend", UsedPercent: 0},
	}}, now)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if lines[0] != "Claude subscription (Pro)" {
		t.Errorf("title = %q", lines[0])
	}
	if !strings.Contains(lines[1], "5-hour          "+strings.Repeat("█", 10)+strings.Repeat("░", 10)+"   50% used  resets in 1h 31m") {
		t.Errorf("line = %q", lines[1])
	}
	if !strings.Contains(lines[2], strings.Repeat("█", 20)+"  100% used  resets in 2d 1h") {
		t.Errorf("line = %q", lines[2])
	}
	if !strings.HasSuffix(lines[3], "0% used") {
		t.Errorf("line = %q", lines[3])
	}
	output.Reset()
	printUsage(&output, "antigravity", daemon.Usage{}, now)
	if output.String() != "Gemini subscription\n  No limits reported.\n" {
		t.Errorf("empty = %q", output.String())
	}
}

func TestUsageResetText(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for wait, want := range map[time.Duration]string{
		-time.Minute:       "resetting now",
		30 * time.Second:   "resets in 1m",
		59 * time.Minute:   "resets in 59m",
		time.Hour:          "resets in 1h 0m",
		7 * 24 * time.Hour: "resets in 7d 0h",
	} {
		if got := usageResetText(now.Add(wait), now); got != want {
			t.Errorf("%v = %q, want %q", wait, got, want)
		}
	}
	if got := usageResetText(time.Time{}, now); got != "" {
		t.Errorf("unknown reset = %q", got)
	}
}

func stubUsageReaders(t *testing.T) *[]string {
	t.Helper()
	asked := &[]string{}
	reader := func(name string) func(context.Context) (daemon.Usage, error) {
		return func(context.Context) (daemon.Usage, error) {
			*asked = append(*asked, name)
			return daemon.Usage{Plan: "plan", Windows: []daemon.UsageWindow{{Label: "Weekly", UsedPercent: 5}}}, nil
		}
	}
	claude, codex, antigravity := readClaudeUsage, readCodexUsage, readAntigravityUsage
	readClaudeUsage, readCodexUsage, readAntigravityUsage = reader("claude"), reader("codex"), reader("antigravity")
	t.Cleanup(func() { readClaudeUsage, readCodexUsage, readAntigravityUsage = claude, codex, antigravity })
	return asked
}

func TestUsageFollowsTheSelectedModelOrTheNamedProvider(t *testing.T) {
	asked := stubUsageReaders(t)
	writeModelTestSettings(t, `{"provider":"claude","model":"openrouter/free","claude_model":"opus"}`)
	for _, test := range []struct {
		args  []string
		want  string
		title string
	}{
		{nil, "claude", "Claude subscription (Plan)"},
		{[]string{"claude"}, "claude", "Claude subscription (Plan)"},
		{[]string{"chatgpt"}, "codex", "ChatGPT subscription (Plan)"},
		{[]string{"antigravity"}, "antigravity", "Gemini subscription (Plan)"},
		{[]string{"Gemini"}, "antigravity", "Gemini subscription (Plan)"},
	} {
		*asked = nil
		output, exitCode := captureTestStdout(t, func() int { return runUsage(test.args) })
		if exitCode != 0 || len(*asked) != 1 || (*asked)[0] != test.want || !strings.HasPrefix(output, test.title) {
			t.Errorf("usage %v: exit=%d asked=%v output=%q", test.args, exitCode, *asked, output)
		}
	}
}

func TestUsageShowsAPIBillingAndFailures(t *testing.T) {
	stubUsageReaders(t)
	writeModelTestSettings(t, `{"provider":"openrouter","model":"openrouter/free"}`)
	output, exitCode := captureTestStdout(t, func() int { return runUsage(nil) })
	if exitCode != 0 || !strings.HasPrefix(output, "API\n") {
		t.Errorf("openrouter: exit=%d output=%q", exitCode, output)
	}
	readClaudeUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, daemon.ErrNoPlanLimits }
	output, exitCode = captureTestStdout(t, func() int { return runUsage([]string{"claude"}) })
	if exitCode != 0 || !strings.HasPrefix(output, "API\n") {
		t.Errorf("API key: exit=%d output=%q", exitCode, output)
	}
	readClaudeUsage = func(context.Context) (daemon.Usage, error) { return daemon.Usage{}, errors.New("nope") }
	if _, exitCode = captureTestStdout(t, func() int { return runUsage([]string{"claude"}) }); exitCode != 1 {
		t.Errorf("failure exit = %d", exitCode)
	}
	for _, args := range [][]string{{"bogus"}, {"claude", "chatgpt"}} {
		if exitCode := runUsage(args); exitCode != 2 {
			t.Errorf("%v exit = %d", args, exitCode)
		}
	}
	output, exitCode = captureTestStdout(t, func() int { return Run([]string{"usage", "--help"}) })
	if exitCode != 0 || !strings.Contains(output, "pearl usage [chatgpt|claude|antigravity]") {
		t.Errorf("help: exit=%d output=%q", exitCode, output)
	}
}

func TestDashboardOffersUsage(t *testing.T) {
	if _, known := dashboardCommandDescriptions["usage"]; !known {
		t.Fatal("the dashboard rejects usage")
	}
	found := false
	for _, option := range dashboardCommandOptions {
		found = found || option.usage == "usage claude"
	}
	if !found {
		t.Fatal("the dashboard doesn't suggest usage claude")
	}
}

func TestModelLabelUsesTheAccountsNamesThenTheID(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	if got := modelLabel("opus"); got != "opus" {
		t.Errorf("no saved list: %q", got)
	}
	if err := saveModelCatalogs(testModelCatalogs()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"opus": "Opus 5.5", " haiku ": "Haiku 5.5", "gemini-3.8-flash-high": "Gemini 3.8 Flash (High)",
		"sonnet": "sonnet",
	} {
		if got := modelLabel(id); got != want {
			t.Errorf("modelLabel(%q) = %q, want %q", id, got, want)
		}
	}
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	if got := modelLabel("opus"); got != "opus" {
		t.Errorf("another config folder used this one's names: %q", got)
	}
}

func TestReadableModelID(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":           "Opus 5.5",
		"claude-haiku-5-5":          "Haiku 5.5",
		"claude-sonnet-5":           "Sonnet 5",
		"claude-haiku-4-5-20251001": "Haiku 4.5",
		"claude-fable-5-1[1m]":      "Fable 5.1",
		"gpt-6.1-sol":               "GPT-6.1 Sol",
		"gpt-5.6-terra":             "GPT-5.6 Terra",
		"gpt-6":                     "GPT-6",
		"gemini-3.8-flash-low":      "Gemini 3.8 Flash (Low)",
		"gemini-3.8-flash-tiered":   "Gemini 3.8 Flash Tiered",
		"gemini-2.5-pro":            "Gemini 2.5 Pro",
		"openrouter/free":           "openrouter/free",
		"anthropic/claude-opus-5.5": "anthropic/claude-opus-5.5",
		"opus":                      "opus",
		"":                          "",
	} {
		if got := readableModelID(id); got != want {
			t.Errorf("readableModelID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestDashboardAndJobsShowTheModelName(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := saveModelCatalogs(testModelCatalogs()); err != nil {
		t.Fatal(err)
	}
	output := renderDashboardComposer(nil, "", "", 120, false, 0, false,
		dashboardConfigureInactive, false, displayCurrentModel("opus"), "claude", 0, 0, 0, true, "claude")
	if !strings.Contains(output, "Billing: Claude subscription · Model: Opus 5.5 · Harness: Claude Code") {
		t.Errorf("composer = %q", output)
	}
	if strings.Contains(output, "Default") {
		t.Errorf("composer still says default: %q", output)
	}
	if details := jobRunDetails(store.Job{Model: "claude-haiku-5-5"}); !strings.Contains(details, "Haiku 5.5") {
		t.Errorf("job details = %q", details)
	}
	if got := attachModelLine([]string{"claude-opus-5-5"}); got != "[model] Opus 5.5" {
		t.Errorf("attach line = %q", got)
	}
}

func TestDashboardStartChecksForMissingOrOldNames(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	started := stubModelCatalogRefresh(t)
	checkModelCatalogsIfOld()
	if *started != 1 {
		t.Errorf("missing list: started %d", *started)
	}
	saveTestModelCatalogs(t, time.Minute)
	checkModelCatalogsIfOld()
	if *started != 1 {
		t.Errorf("recent list: started %d", *started)
	}
	saveTestModelCatalogs(t, modelCatalogMaxAge+time.Minute)
	checkModelCatalogsIfOld()
	if *started != 2 {
		t.Errorf("old list: started %d", *started)
	}
}
