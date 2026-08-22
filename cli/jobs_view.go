package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/August-H/pearl-cli/internal/store"
	"golang.org/x/term"
)

type jobTranscriptMessage struct {
	Role       string                  `json:"role"`
	Content    string                  `json:"content"`
	Reasoning  string                  `json:"reasoning"`
	Name       string                  `json:"name"`
	ToolCallID string                  `json:"tool_call_id"`
	ToolCalls  []jobTranscriptToolCall `json:"tool_calls"`
}

type jobTranscriptToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type storedToolResult struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
}

type jobFileChange struct {
	Path   string
	Action string
}

type jobViewSection struct {
	Title    string
	Summary  string
	Lines    []string
	Expanded bool
}

func parseShowAllFlag(arguments []string) (bool, []string) {
	showAll := false
	var rest []string
	for _, argument := range arguments {
		switch argument {
		case "-a", "--all":
			showAll = true
		default:
			rest = append(rest, argument)
		}
	}
	return showAll, rest
}

func runJobs(args []string) int {
	showAll, rest := parseShowAllFlag(args)
	switch {
	case len(rest) == 0:
		return listJobs(showAll)
	case len(rest) == 2 && rest[0] == "view":
		return viewJob(rest[1])
	case len(rest) > 0 && rest[0] != "view":
		return printInvalidCommand("jobs " + rest[0])
	default:
		fmt.Fprintln(os.Stderr, "Usage: pearl jobs [--all] [view <job-id>]")
		return 2
	}
}

func viewJob(jobID string) int {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		fmt.Fprintln(os.Stderr, "Usage: pearl jobs view <job-id>")
		return 2
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Jobs", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	details, err := client.jobDetails(ctx, jobID)
	cancel()
	if err != nil {
		return printError("Jobs", err)
	}
	sections, err := buildJobViewSections(details)
	if err != nil {
		return printError("Jobs", err)
	}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := runJobDetailsTUI(os.Stdin, os.Stdout, details.Job, sections); err != nil {
			return printError("Jobs", err)
		}
		return 0
	}
	if err := writeJobDetails(os.Stdout, details, sections); err != nil {
		return printError("Jobs", err)
	}
	return 0
}

func writeJobDetails(output io.Writer, details jobDetails, sections []jobViewSection) error {
	fmt.Fprintf(output, "Job: %s\n", jobViewText(details.Job.ID))
	for _, section := range sections {
		fmt.Fprintf(output, "\n%s\n", section.Title)
		if len(section.Lines) == 0 {
			fmt.Fprintln(output, "  None recorded")
			continue
		}
		for _, line := range section.Lines {
			fmt.Fprintln(output, "  "+line)
		}
	}
	return nil
}

func buildJobViewSections(details jobDetails) ([]jobViewSection, error) {
	job := details.Job
	overview := []string{
		"Status: " + job.Status,
		"Created: " + formatJobCreatedAt(job.CreatedAt),
	}
	if job.ArchivedAt != nil {
		overview = append(overview, "Archived: "+formatJobCreatedAt(*job.ArchivedAt))
	}
	if job.StartedAt != nil {
		overview = append(overview, "Started: "+formatJobCreatedAt(*job.StartedAt))
	}
	if job.FinishedAt != nil {
		overview = append(overview, "Finished: "+formatJobCreatedAt(*job.FinishedAt))
	}
	if duration := jobViewDuration(job, details.StatusEvents); duration != "" {
		overview = append(overview, "Duration: "+duration)
	}
	overview = append(overview, "Directory: "+jobViewText(displayJobDirectory(job.WorkspaceRoot)))
	sections := []jobViewSection{{
		Title:    "Overview",
		Summary:  job.Status,
		Lines:    overview,
		Expanded: true,
	}, {
		Title:   "Prompt",
		Summary: jobViewOneLine(job.Prompt),
		Lines:   jobViewValueLines(job.Prompt),
	}}
	if job.Question != "" {
		sections = append(sections, jobViewSection{
			Title: "Waiting for input", Summary: jobViewOneLine(job.Question),
			Lines: jobViewValueLines(job.Question),
		})
	}
	if job.Error != "" {
		message := formatAgentError(job.Error)
		sections = append(sections, jobViewSection{
			Title: "Error", Summary: jobViewOneLine(message), Lines: jobViewValueLines(message),
		})
	}

	changes := changedFilesForJob(details.ToolExecutions)
	changeLines := make([]string, 0, len(changes))
	for _, change := range changes {
		changeLines = append(changeLines, fmt.Sprintf("%-8s %s", change.Action, jobViewText(change.Path)))
	}
	sections = append(sections, jobViewSection{
		Title: "Changed files", Summary: jobViewCountSummary(len(changes), "file", "files"), Lines: changeLines,
	})

	toolLines := make([]string, 0, len(details.ToolExecutions))
	for _, execution := range details.ToolExecutions {
		status := "failed"
		if toolExecutionSucceeded(execution.Result) {
			status = "ok"
		}
		summary := toolArgumentsSummary(execution.Arguments)
		if summary != "" {
			summary = " " + summary
		}
		toolLines = append(toolLines,
			jobViewText(execution.ToolName)+jobViewText(summary)+"  "+status)
	}
	sections = append(sections, jobViewSection{
		Title:   "Tool activity",
		Summary: jobViewCountSummary(len(details.ToolExecutions), "call", "calls"),
		Lines:   toolLines,
	})

	var transcript []jobTranscriptMessage
	if len(details.Transcript) > 0 {
		if err := json.Unmarshal(details.Transcript, &transcript); err != nil {
			return nil, fmt.Errorf("decode job transcript: %w", err)
		}
	}
	var transcriptOutput strings.Builder
	for _, message := range transcript {
		writeTranscriptMessage(&transcriptOutput, message)
	}
	sections = append(sections, jobViewSection{
		Title: "Transcript", Summary: jobViewCountSummary(len(transcript), "message", "messages"),
		Lines: jobViewValueLines(transcriptOutput.String()),
	})
	if len(transcript) == 0 && job.Result != "" {
		sections = append(sections, jobViewSection{
			Title: "Result", Summary: jobViewOneLine(job.Result), Lines: jobViewValueLines(job.Result),
		})
	}
	return sections, nil
}

func writeJobViewSection(output io.Writer, title, value string) {
	fmt.Fprintf(output, "\n%s\n", title)
	value = strings.TrimSpace(jobViewText(value))
	if value == "" {
		fmt.Fprintln(output, "  None")
		return
	}
	for _, line := range strings.Split(value, "\n") {
		fmt.Fprintln(output, "  "+line)
	}
}

func writeTranscriptMessage(output io.Writer, message jobTranscriptMessage) {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	if role == "assistant" && strings.TrimSpace(message.Reasoning) != "" {
		writeJobViewSection(output, "[assistant reasoning]", message.Reasoning)
	}
	if strings.TrimSpace(message.Content) != "" {
		title := "[" + role + "]"
		if role == "tool" && message.Name != "" {
			title = "[tool result: " + jobViewText(message.Name) + "]"
		}
		content := message.Content
		if role == "tool" {
			content = readableStoredToolResult(content)
		}
		writeJobViewSection(output, title, content)
	}
	for _, call := range message.ToolCalls {
		summary := toolArgumentsSummary(call.Function.Arguments)
		if summary != "" {
			summary = " " + summary
		}
		fmt.Fprintf(output, "\n[tool call] %s%s\n",
			jobViewText(call.Function.Name), jobViewText(summary))
	}
}

func jobViewValueLines(value string) []string {
	value = strings.TrimSpace(jobViewText(value))
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func jobViewOneLine(value string) string {
	return strings.Join(strings.Fields(jobViewText(value)), " ")
}

func jobViewCountSummary(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func readableStoredToolResult(value string) string {
	var result storedToolResult
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return value
	}
	if result.Error != "" {
		return "Error: " + result.Error
	}
	if len(result.Result) == 0 {
		if result.Success {
			return "OK"
		}
		return value
	}
	var text string
	if json.Unmarshal(result.Result, &text) == nil {
		return text
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, result.Result, "", "  ") == nil {
		return formatted.String()
	}
	return string(result.Result)
}

func changedFilesForJob(executions []store.ToolExecution) []jobFileChange {
	var changes []jobFileChange
	indexes := make(map[string]int)
	for _, execution := range executions {
		if !toolExecutionSucceeded(execution.Result) ||
			(execution.ToolName != "create_file" && execution.ToolName != "write_to_file") {
			continue
		}
		var arguments struct {
			RelativePath string `json:"relative_path"`
		}
		if json.Unmarshal([]byte(execution.Arguments), &arguments) != nil ||
			strings.TrimSpace(arguments.RelativePath) == "" {
			continue
		}
		if execution.ToolName == "create_file" &&
			strings.Contains(strings.ToLower(readableStoredToolResult(execution.Result)), "already") {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(arguments.RelativePath))
		action := "modified"
		if execution.ToolName == "create_file" {
			action = "created"
		}
		if index, found := indexes[path]; found {
			if changes[index].Action != "created" {
				changes[index].Action = action
			}
			continue
		}
		indexes[path] = len(changes)
		changes = append(changes, jobFileChange{Path: path, Action: action})
	}
	return changes
}

func toolExecutionSucceeded(value string) bool {
	var result storedToolResult
	return json.Unmarshal([]byte(value), &result) == nil && result.Success && result.Error == ""
}

func toolArgumentsSummary(value string) string {
	var arguments struct {
		RelativePath string `json:"relative_path"`
		Question     string `json:"question"`
	}
	if json.Unmarshal([]byte(value), &arguments) != nil {
		return ""
	}
	if strings.TrimSpace(arguments.RelativePath) != "" {
		return filepath.ToSlash(filepath.Clean(arguments.RelativePath))
	}
	return strings.TrimSpace(arguments.Question)
}

func jobViewDuration(job store.Job, statusEvents []store.Event) string {
	if job.StartedAt == nil {
		return ""
	}
	end := time.Now()
	if job.FinishedAt != nil {
		end = *job.FinishedAt
	}
	duration := end.Sub(*job.StartedAt) - pausedDuration(*job.StartedAt, end, statusEvents)
	if duration < 0 {
		duration = 0
	}
	return duration.Round(time.Second).String()
}

func pausedDuration(start, end time.Time, statusEvents []store.Event) time.Duration {
	var paused time.Duration
	var pauseStart time.Time
	for _, event := range statusEvents {
		if event.Type != "status" {
			continue
		}
		timestamp := event.CreatedAt.Local()
		if timestamp.Before(start) {
			continue
		}
		if pauseStart.IsZero() {
			if event.Data == store.JobWaitingInput {
				pauseStart = timestamp
			}
			continue
		}
		if event.Data != store.JobWaitingInput {
			paused += timestamp.Sub(pauseStart)
			pauseStart = time.Time{}
		}
	}
	if !pauseStart.IsZero() {
		if end.Before(pauseStart) {
			end = pauseStart
		}
		paused += end.Sub(pauseStart)
	}
	if paused < 0 {
		return 0
	}
	return paused
}

func jobViewText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}
