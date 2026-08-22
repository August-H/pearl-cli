package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestRenderDashboardShowsOnlyActiveJobsWithColors(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 30, 0, 0, time.UTC)
	started := now.Add(-45 * time.Second)
	jobs := []store.Job{
		{
			ID:            "job_running",
			Status:        store.JobRunning,
			Prompt:        "run tests",
			WorkspaceRoot: "/work/pearl-cli",
			CreatedAt:     now.Add(-time.Minute),
			StartedAt:     &started,
		},
		{
			ID:            "job_waiting",
			Status:        store.JobWaitingInput,
			Prompt:        "deploy app",
			Question:      "Which region?",
			WorkspaceRoot: "/work/app",
			CreatedAt:     now.Add(-2 * time.Minute),
		},
		{
			ID:            "job_queued",
			Status:        store.JobQueued,
			Prompt:        "format files",
			WorkspaceRoot: "/work/tools",
			CreatedAt:     now.Add(-10 * time.Second),
		},
		{
			ID:            "job_finished",
			Status:        store.JobCompleted,
			Prompt:        "do not show this",
			WorkspaceRoot: "/work/old",
			CreatedAt:     now.Add(-time.Hour),
		},
	}

	output := renderDashboard(jobs, now, 100, true, nil)
	for _, expected := range []string{
		"Pearl", "1 running", "1 queued", "1 waiting",
		"● Daemon active", "job_running", "job_waiting", "job_queued", "Which region?",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dashboard is missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "Pearl jobs") {
		t.Fatalf("dashboard still uses the old title:\n%s", output)
	}
	if strings.Contains(output, "job_finished") || strings.Contains(output, "do not show this") {
		t.Fatalf("dashboard included a finished job:\n%s", output)
	}
	if !strings.Contains(output, ansiGreen) || !strings.Contains(output, ansiYellow) ||
		!strings.Contains(output, ansiMagenta) {
		t.Fatalf("dashboard is missing status colors:\n%q", output)
	}
	if !strings.Contains(output, ansiGreen+"● Daemon active"+ansiReset) {
		t.Fatalf("dashboard daemon indicator is not green:\n%q", output)
	}

	offline := renderDashboard(nil, now, 100, true, fmt.Errorf("disconnected"))
	if !strings.Contains(offline, ansiRed+"● Daemon not active"+ansiReset) {
		t.Fatalf("offline daemon indicator is not red:\n%q", offline)
	}
}

func TestDashboardTerminalFrameUsesRawModeLineEndings(t *testing.T) {
	frame := "Pearl\n──────────\n"
	want := "Pearl\r\n──────────\r\n"
	if got := dashboardTerminalFrame(frame); got != want {
		t.Fatalf("dashboardTerminalFrame() = %q, want %q", got, want)
	}
}

func TestDashboardInvalidCommandNoticeIsShort(t *testing.T) {
	want := `Invalid command "frobnicate". Use "pearl help" for more information.`
	if got := dashboardInvalidCommandNotice("frobnicate"); got != want {
		t.Fatalf("dashboard invalid command notice = %q, want %q", got, want)
	}
}

func TestDashboardJobsViewNavigatesAndUsesDashboardBackLabel(t *testing.T) {
	view := dashboardJobsView{open: true, bodyHeight: 5}
	dashboardApplyJobsViewAction(&view, "next", 10)
	if view.selected != 1 {
		t.Fatalf("next selected %d, want 1", view.selected)
	}
	dashboardApplyJobsViewAction(&view, "page_down", 10)
	if view.selected != 5 {
		t.Fatalf("page down selected %d, want 5", view.selected)
	}
	dashboardApplyJobsViewAction(&view, "end", 10)
	if view.selected != 9 {
		t.Fatalf("end selected %d, want 9", view.selected)
	}
	dashboardApplyJobsViewAction(&view, "next", 10)
	if view.selected != 9 {
		t.Fatalf("selection escaped the jobs list: %d", view.selected)
	}

	screen := renderJobsListScreenWithQuitLabel([]store.Job{{
		ID: "swift-delta", Status: store.JobCompleted, Prompt: "explain this folder",
		CreatedAt: time.Now(),
	}}, 80, 10, 0, 0, false, "q back")
	for _, expected := range []string{"› swift-delta", "Enter view", "q back"} {
		if !strings.Contains(screen.Frame, expected) {
			t.Fatalf("dashboard jobs list is missing %q:\n%s", expected, screen.Frame)
		}
	}
	if strings.Contains(screen.Frame, "q exit") {
		t.Fatalf("dashboard jobs list still says q exits:\n%s", screen.Frame)
	}
}

func TestDashboardJobDetailsActionsKeepExpandableView(t *testing.T) {
	view := dashboardJobDetailsView{
		sections: []jobViewSection{
			{Title: "Overview", Expanded: true},
			{Title: "Transcript"},
		},
		bodyHeight: 5,
		totalLines: 12,
	}
	dashboardApplyJobDetailsAction(&view, "next")
	dashboardApplyJobDetailsAction(&view, "toggle")
	if view.selected != 1 || !view.sections[1].Expanded || !view.keepSelected {
		t.Fatalf("job details action state = %#v", view)
	}
	dashboardApplyJobDetailsAction(&view, "page_down")
	if view.scroll != 4 {
		t.Fatalf("job details page down scroll = %d, want 4", view.scroll)
	}
	dashboardApplyJobDetailsAction(&view, "collapse_all")
	for _, section := range view.sections {
		if section.Expanded {
			t.Fatalf("collapse all left a section expanded: %#v", view.sections)
		}
	}
}

func TestRenderDashboardSupportsNoColorAndSanitizesControlCharacters(t *testing.T) {
	now := time.Now()
	output := renderDashboard([]store.Job{{
		ID:            "job_queued",
		Status:        store.JobQueued,
		Prompt:        "safe\x1b[31m text\nnext line",
		WorkspaceRoot: "/work/project",
		CreatedAt:     now,
	}}, now, 80, false, nil)
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("no-color dashboard contains ANSI escapes: %q", output)
	}
	if !strings.Contains(output, "safe [31m text next line") {
		t.Fatalf("dashboard did not sanitize job text: %q", output)
	}
}

func TestParseDashboardCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "plain arguments",
			input: "cancel job_123",
			want:  []string{"cancel", "job_123"},
		},
		{
			name:  "double quoted job ID",
			input: `run "fix login"`,
			want:  []string{"run", "fix login"},
		},
		{
			name:  "single quoted answer",
			input: `respond job_123 'use us west'`,
			want:  []string{"respond", "job_123", "use us west"},
		},
		{
			name:  "escaped spaces",
			input: `run release\ prep`,
			want:  []string{"run", "release prep"},
		},
		{
			name:  "empty quoted argument",
			input: `respond job_123 ""`,
			want:  []string{"respond", "job_123", ""},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseDashboardCommand(test.input)
			if err != nil {
				t.Fatalf("parseDashboardCommand returned an error: %v", err)
			}
			if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
				t.Fatalf("parseDashboardCommand(%q) = %#v, want %#v", test.input, got, test.want)
			}
		})
	}
}

func TestParseDashboardCommandRejectsIncompleteInput(t *testing.T) {
	for _, input := range []string{`run "unfinished`, `run unfinished\`} {
		if _, err := parseDashboardCommand(input); err == nil {
			t.Fatalf("parseDashboardCommand(%q) did not return an error", input)
		}
	}
}

func TestParseDashboardAutonomousCommand(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      dashboardAutonomousCommand
	}{
		{name: "latest", want: dashboardAutonomousCommand{latest: true}},
		{
			name: "new goal", arguments: []string{"audit", "the", "release"},
			want: dashboardAutonomousCommand{goal: "audit the release"},
		},
		{
			name: "resume", arguments: []string{"--resume", "auto_123"},
			want: dashboardAutonomousCommand{resumeID: "auto_123"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseDashboardAutonomousCommand(test.arguments)
			if err != nil || got != test.want {
				t.Fatalf("autonomous command = %#v, err=%v, want %#v", got, err, test.want)
			}
		})
	}
	for _, arguments := range [][]string{{"--resume"}, {"--resume", "one", "two"}, {"--bad"}} {
		if _, err := parseDashboardAutonomousCommand(arguments); err == nil {
			t.Fatalf("invalid autonomous arguments were accepted: %#v", arguments)
		}
	}
}

func TestRenderDashboardShowsCommandPromptAndNotice(t *testing.T) {
	output := renderDashboardWithInput(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		80,
		false,
		nil,
		`respond job_123 "use us west"`,
		"Omit the pearl prefix. Type the command directly.",
	)
	for _, expected := range []string{
		"╭", "│ › respond job_123 \"use us west\"█",
		"Omit the pearl prefix", "Enter to run", "omit pearl prefix",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dashboard command prompt is missing %q:\n%s", expected, output)
		}
	}
}

func TestDashboardCommandSuggestionsFilterNavigateAndFill(t *testing.T) {
	suggestions := dashboardCommandSuggestions("r")
	want := []string{"run", "respond", "retry"}
	if len(suggestions) != len(want) {
		t.Fatalf("dashboardCommandSuggestions(\"r\") returned %d suggestions, want %d", len(suggestions), len(want))
	}
	for index, usage := range want {
		if suggestions[index].usage != usage {
			t.Fatalf("suggestion %d = %q, want %q", index, suggestions[index].usage, usage)
		}
	}
	if got := dashboardMoveSuggestion("r", 0, -1); got != 2 {
		t.Fatalf("moving up from the first suggestion selected %d, want 2", got)
	}
	if got := dashboardFillSuggestion("r", 1); got != "respond" {
		t.Fatalf("filling the selected suggestion returned %q, want %q", got, "respond")
	}
	if got := dashboardFillSuggestion("respond job_123", 0); got != "respond job_123" {
		t.Fatalf("filling a command with arguments returned %q", got)
	}
	if got := dashboardFillSuggestion("respond job_123 ", 0); got != "respond job_123 " {
		t.Fatalf("filling after an argument returned %q", got)
	}
	detached := dashboardCommandSuggestions("run --")
	if len(detached) != 1 || detached[0].usage != "--detach <job-id>" {
		t.Fatalf("dashboardCommandSuggestions(\"run --\") = %#v, want run --detach", detached)
	}
	runItems := dashboardCommandSuggestions("run ")
	if len(runItems) != 2 || runItems[0].usage != "<job-id>" ||
		runItems[1].usage != "--detach <job-id>" {
		t.Fatalf("dashboardCommandSuggestions(\"run \") = %#v", runItems)
	}
	if got := dashboardFillSuggestion("run ", 1); got != "run --detach " {
		t.Fatalf("filling a detached run item returned %q", got)
	}
	jobItems := dashboardCommandSuggestions("job ")
	if len(jobItems) != 2 || jobItems[0].usage != "[-n name] <prompt>" ||
		jobItems[1].usage != "--directory [-n name] <prompt>" {
		t.Fatalf("dashboardCommandSuggestions(\"job \") = %#v", jobItems)
	}
	if got := dashboardFillSuggestion("job ", 1); got != "job --directory " {
		t.Fatalf("filling the directory picker item returned %q", got)
	}
	daemonItems := dashboardCommandSuggestions("daemon re")
	if len(daemonItems) != 1 || daemonItems[0].usage != "restart" ||
		daemonItems[0].completion != "daemon restart" {
		t.Fatalf("dashboard daemon restart suggestions = %#v", daemonItems)
	}
	all := dashboardCommandSuggestions(" ")
	if len(all) != len(dashboardCommandDescriptions) {
		t.Fatalf("typing a space returned %d commands, want %d", len(all), len(dashboardCommandDescriptions))
	}
	if got := dashboardFillSuggestion(" ", 2); got != "jobs" {
		t.Fatalf("filling from the full command list returned %q, want jobs", got)
	}
	for _, suggestion := range all {
		if suggestion.usage == "goal" {
			t.Fatalf("full command list still includes goal: %#v", all)
		}
	}
	archive := dashboardCommandSuggestions("arc")
	if len(archive) != 1 || archive[0].usage != "archive" ||
		archive[0].completion != "archive" {
		t.Fatalf("archive suggestions = %#v", archive)
	}
	if got := dashboardMoveSuggestion(" ", 0, -1); got != len(all)-1 {
		t.Fatalf("moving above the full command list selected %d, want %d", got, len(all)-1)
	}
	autonomous := dashboardCommandSuggestions("aut")
	if len(autonomous) != 1 || autonomous[0].usage != "autonomous" {
		t.Fatalf("autonomous top-level suggestions = %#v", autonomous)
	}
	resume := dashboardCommandSuggestions("autonomous --")
	if len(resume) != 1 || resume[0].usage != "--resume <session-id>" ||
		resume[0].completion != "autonomous --resume " {
		t.Fatalf("autonomous resume suggestions = %#v", resume)
	}
}

func TestRunAutocompleteUsesRunnableAndRetryableJobIDs(t *testing.T) {
	workspace := t.TempDir()
	jobs := []store.Job{
		{
			ID:            "amber-fox",
			Status:        store.JobPending,
			Prompt:        "fix the tests",
			WorkspaceRoot: workspace,
		},
		{ID: "release prep", Status: store.JobPending, Prompt: "prepare the release"},
		{ID: "failed-job", Status: store.JobFailed, Prompt: "try this again"},
		{ID: "finished-job", Status: store.JobCompleted, Prompt: "already done"},
		{ID: "active-job", Status: store.JobRunning, Prompt: "still running"},
		{ID: "waiting-job", Status: store.JobWaitingInput, Prompt: "needs an answer"},
	}
	suggestions := dashboardCommandSuggestionsWithJobs("run ", jobs)
	if len(suggestions) != 5 || suggestions[0].usage != "amber-fox" ||
		suggestions[1].usage != "release prep" || suggestions[2].usage != "failed-job" ||
		suggestions[3].usage != "finished-job" || suggestions[4].usage != "--detach <job-id>" {
		t.Fatalf("run suggestions = %#v", suggestions)
	}
	wantDescription := "fix the tests  " + displayJobDirectory(workspace)
	if suggestions[0].description != wantDescription {
		t.Fatalf("run suggestion description = %q, want %q",
			suggestions[0].description, wantDescription)
	}
	filtered := dashboardCommandSuggestionsWithJobs("run rel", jobs)
	if len(filtered) != 1 || filtered[0].usage != "release prep" {
		t.Fatalf("filtered run suggestions = %#v", filtered)
	}
	if got := dashboardFillSuggestionForStepWithJobs(
		"run rel", dashboardConfigureInactive, 0, jobs,
	); got != `run release\ prep` {
		t.Fatalf("filled custom job ID = %q", got)
	}
	detached := dashboardCommandSuggestionsWithJobs("run --detach ", jobs)
	if len(detached) != 4 || detached[0].completion != "run --detach amber-fox" ||
		detached[1].completion != `run --detach release\ prep` ||
		detached[2].completion != "run --detach failed-job" ||
		detached[3].completion != "run --detach finished-job" {
		t.Fatalf("detached run suggestions = %#v", detached)
	}

	output := renderDashboardWithSelection(
		jobs[:4],
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		240,
		false,
		nil,
		"run ",
		"",
		0,
	)
	for _, expected := range []string{
		"amber-fox", "release prep", "failed-job", "finished-job",
		"fix the tests", displayJobDirectory(workspace),
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("run autocomplete is missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "active-job") || strings.Contains(output, "still running") {
		t.Fatalf("run autocomplete included an active job:\n%s", output)
	}
}

func TestJobsViewAutocompleteUsesEveryJobID(t *testing.T) {
	jobs := []store.Job{
		{ID: "amber-fox", Status: store.JobPending, Prompt: "fix tests", WorkspaceRoot: "/work/app"},
		{ID: "release prep", Status: store.JobCompleted, Prompt: "ship it", WorkspaceRoot: "/work/release"},
	}
	command := dashboardCommandSuggestionsWithJobs("jobs ", jobs)
	if len(command) != 1 || command[0].usage != "view <job-id>" ||
		command[0].completion != "jobs view " {
		t.Fatalf("jobs suggestions = %#v", command)
	}
	jobIDs := dashboardCommandSuggestionsWithJobs("jobs view ", jobs)
	if len(jobIDs) != 2 || jobIDs[0].completion != "jobs view amber-fox" ||
		jobIDs[1].completion != `jobs view release\ prep` {
		t.Fatalf("jobs view suggestions = %#v", jobIDs)
	}
	filtered := dashboardCommandSuggestionsWithJobs("jobs view rel", jobs)
	if len(filtered) != 1 || filtered[0].usage != "release prep" ||
		!strings.Contains(filtered[0].description, store.JobCompleted) {
		t.Fatalf("filtered jobs view suggestions = %#v", filtered)
	}
}

func TestDashboardCommandSuggestionsShowItemsAfterEveryCommand(t *testing.T) {
	for _, command := range dashboardCommandSuggestions(" ") {
		expectedItems := 0
		for _, option := range dashboardCommandOptions {
			if strings.HasPrefix(option.usage, command.usage+" ") {
				expectedItems++
			}
		}
		items := dashboardCommandSuggestions(command.usage + " ")
		if len(items) != expectedItems {
			t.Fatalf("%q has %d item suggestions, want %d", command.usage, len(items), expectedItems)
		}
		for _, item := range items {
			if strings.HasPrefix(item.usage, command.usage+" ") {
				t.Fatalf("%q item still includes its top-level command: %q", command.usage, item.usage)
			}
		}
	}
}

func TestDashboardConfigureModelSuggestions(t *testing.T) {
	suggestions := dashboardComposerSuggestions("", dashboardConfigureModel)
	if len(suggestions) != 2 || suggestions[0].usage != "free" ||
		suggestions[1].usage != "custom" {
		t.Fatalf("configure model suggestions = %#v", suggestions)
	}
	if got := dashboardMoveSuggestionForStep(
		"", dashboardConfigureModel, 0, -1,
	); got != 1 {
		t.Fatalf("moving above free selected %d, want custom", got)
	}
	if got := dashboardFillSuggestionForStep(
		"", dashboardConfigureModel, 1,
	); got != "custom" {
		t.Fatalf("filling custom returned %q", got)
	}
	if got := dashboardComposerSuggestions("f", dashboardConfigureModel); len(got) != 1 ||
		got[0].usage != "free" {
		t.Fatalf("filtered configure model suggestions = %#v", got)
	}
}

func TestRenderDashboardShowsAutocompleteDropdown(t *testing.T) {
	output := renderDashboardWithSelection(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		80,
		false,
		nil,
		"r",
		"",
		1,
	)
	for _, expected := range []string{
		"├", "│ › respond  Answer a paused job", "Tab to fill", "↑/↓ choose",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dashboard autocomplete is missing %q:\n%s", expected, output)
		}
	}
}

func TestRenderDashboardScrollsTheFullCommandListAfterSpace(t *testing.T) {
	output := renderDashboardWithSelection(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		80,
		false,
		nil,
		" ",
		"",
		len(dashboardCommandDescriptions)-1,
	)
	for _, expected := range []string{
		"│ › exit  Close the dashboard",
		fmt.Sprintf("↑/↓ choose %d/%d", len(dashboardCommandDescriptions), len(dashboardCommandDescriptions)),
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("full command dropdown is missing %q:\n%s", expected, output)
		}
	}
	visibleSuggestions := 0
	inDropdown := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "├"):
			inDropdown = true
		case strings.HasPrefix(line, "╰"):
			inDropdown = false
		case inDropdown && strings.HasPrefix(line, "│"):
			visibleSuggestions++
		}
	}
	if visibleSuggestions != dashboardSuggestionLimit {
		t.Fatalf("dropdown shows %d suggestions, want %d:\n%s",
			visibleSuggestions, dashboardSuggestionLimit, output)
	}
}

func TestRenderDashboardShowsCommandOutput(t *testing.T) {
	output := renderDashboardState(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		80,
		false,
		nil,
		"",
		"",
		0,
		dashboardCommandView{
			command:  "jobs",
			output:   "first line\nsecond line\n",
			exitCode: 0,
		},
	)
	for _, expected := range []string{"$ pearl jobs", "done", "first line", "second line"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dashboard command output is missing %q:\n%s", expected, output)
		}
	}
}

func TestRenderDashboardRunsConfigureInsideComposer(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC)
	apiKeyOutput := renderDashboardState(
		nil, now, 80, false, nil, "sk-visible-key", "", 0,
		dashboardCommandView{configureStep: dashboardConfigureAPIKey},
	)
	if !strings.Contains(apiKeyOutput, "sk-visible-key█") ||
		!strings.Contains(apiKeyOutput, "Enter to continue") {
		t.Fatalf("dashboard API-key step =\n%s", apiKeyOutput)
	}

	modelOutput := renderDashboardState(
		nil, now, 80, false, nil, "", "", 0,
		dashboardCommandView{configureStep: dashboardConfigureModel},
	)
	for _, expected := range []string{
		"Choose free or custom", "› free  Use openrouter/free", "custom  Enter a model ID",
	} {
		if !strings.Contains(modelOutput, expected) {
			t.Fatalf("dashboard model step is missing %q:\n%s", expected, modelOutput)
		}
	}

	customOutput := renderDashboardState(
		nil, now, 80, false, nil, "", "", 0,
		dashboardCommandView{configureStep: dashboardConfigureCustomModel},
	)
	if !strings.Contains(customOutput, "OpenRouter model ID") {
		t.Fatalf("dashboard custom-model step =\n%s", customOutput)
	}
}

func TestRenderDashboardColorsJobsCommandStatuses(t *testing.T) {
	output := renderDashboardState(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		100,
		true,
		nil,
		"",
		"",
		0,
		dashboardCommandView{
			command: "jobs",
			output: "ID      STATUS   CREATED\njob_1  running  August 19 1:05pm\n" +
				"job_2  failed   August 19 1:06pm\n" +
				"job_3  pending  August 19 1:07pm\n",
		},
	)
	for _, expected := range []string{
		ansiBold + ansiCyan + "STATUS",
		ansiGreen + "running",
		ansiRed + "failed",
		ansiYellow + "pending",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("dashboard jobs output is missing color %q:\n%q", expected, output)
		}
	}
}

func TestRenderDashboardKeepsJobCountsAboveCommandBox(t *testing.T) {
	output := renderDashboardState(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		80,
		false,
		nil,
		"r",
		"",
		0,
		dashboardCommandView{command: "jobs", output: "No jobs.\n"},
	)
	lines := strings.Split(output, "\n")
	countsLine := "0 running, 0 queued, 0 waiting  ● Daemon active"
	countsIndex := -1
	composerIndex := -1
	for index, line := range lines {
		if line == countsLine {
			countsIndex = index
		}
		if strings.HasPrefix(line, "╭") {
			composerIndex = index
		}
	}
	if countsIndex == -1 || composerIndex != countsIndex+1 {
		t.Fatalf("job counts are not directly above the command box:\n%s", output)
	}
	if strings.Count(output, countsLine) != 1 {
		t.Fatalf("job counts appear more than once:\n%s", output)
	}
}

func TestRenderDashboardLimitsVisibleCommandOutput(t *testing.T) {
	untruncatedLines := make([]string, dashboardCommandOutputLimit)
	for index := range untruncatedLines {
		untruncatedLines[index] = fmt.Sprintf("line %d", index+1)
	}
	untruncated := dashboardCommandOutputLines(strings.Join(untruncatedLines, "\n"))
	if len(untruncated) != dashboardCommandOutputLimit || untruncated[0] != "line 1" {
		t.Fatalf("output at the %d-line limit was truncated", dashboardCommandOutputLimit)
	}

	lines := make([]string, dashboardCommandOutputLimit+3)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index+1)
	}
	visible := dashboardCommandOutputLines(strings.Join(lines, "\n"))
	if len(visible) != dashboardCommandOutputLimit+1 {
		t.Fatalf("visible command output has %d lines, want %d", len(visible), dashboardCommandOutputLimit+1)
	}
	if visible[0] != "... 3 earlier lines" || visible[1] != "line 4" {
		t.Fatalf("visible command output starts with %#v", visible[:2])
	}
}

func TestRenderDashboardUsesTheRequestedWidth(t *testing.T) {
	for _, width := range []int{48, 91, 180} {
		output := renderDashboardWithInput(
			nil,
			time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
			width,
			false,
			nil,
			"jobs",
			"",
		)
		lines := strings.Split(output, "\n")
		dividerFound := false
		composerTopFound := false
		composerBottomFound := false
		for _, line := range lines {
			switch {
			case line != "" && strings.Trim(line, "─") == "":
				dividerFound = true
				if len([]rune(line)) != width {
					t.Fatalf("divider width = %d, want %d: %q", len([]rune(line)), width, line)
				}
			case strings.HasPrefix(line, "╭"):
				composerTopFound = true
				if len([]rune(line)) != width {
					t.Fatalf("composer top width = %d, want %d: %q", len([]rune(line)), width, line)
				}
			case strings.HasPrefix(line, "╰"):
				composerBottomFound = true
				if len([]rune(line)) != width {
					t.Fatalf("composer bottom width = %d, want %d: %q", len([]rune(line)), width, line)
				}
			case strings.HasPrefix(line, "│"):
				if len([]rune(line)) != width {
					t.Fatalf("composer body width = %d, want %d: %q", len([]rune(line)), width, line)
				}
			}
		}
		if !dividerFound || !composerTopFound || !composerBottomFound {
			t.Fatalf("dashboard at width %d is missing a divider or composer border:\n%s", width, output)
		}
	}
}

func TestRenderDashboardComposerShowsPlaceholder(t *testing.T) {
	output := renderDashboardWithInput(
		nil,
		time.Date(2026, time.August, 19, 12, 30, 0, 0, time.UTC),
		80,
		false,
		nil,
		"",
		"",
	)
	if !strings.Contains(output, "│ › Type a Pearl command") {
		t.Fatalf("dashboard composer is missing its placeholder:\n%s", output)
	}
	if strings.Contains(output, "├") {
		t.Fatalf("empty dashboard command unexpectedly shows suggestions:\n%s", output)
	}
}
