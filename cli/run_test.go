package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type retryOutputRunner struct {
	mu       sync.Mutex
	attempts int
}

type preflightFailureRunner struct{}

func (preflightFailureRunner) Run(
	context.Context,
	store.Job,
	openrouter_request.EventSink,
	openrouter_request.CheckpointStore,
) (string, error) {
	return "", errors.New("agent could not start")
}

type answerRunner struct{}

type jobDetailsRunner struct{}

func (answerRunner) Run(
	_ context.Context,
	_ store.Job,
	events openrouter_request.EventSink,
	_ openrouter_request.CheckpointStore,
) (string, error) {
	if err := events(openrouter_request.AgentEvent{Type: "answer", Data: "job finished"}); err != nil {
		return "", err
	}
	return "job finished", nil
}

func (jobDetailsRunner) Run(
	ctx context.Context,
	job store.Job,
	_ openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	transcript := []byte(`[
  {"role":"user","content":"update the docs"},
  {"role":"assistant","reasoning":"I should update the requested file.","tool_calls":[{"id":"call-create","type":"function","function":{"name":"create_file","arguments":"{\"relative_path\":\"docs/readme.md\"}"}}]},
  {"role":"tool","name":"create_file","tool_call_id":"call-create","content":"{\"success\":true,\"result\":\"Successfully created file at: docs/readme.md\"}"},
  {"role":"assistant","content":"Updated the docs."}
]`)
	if err := state.SaveTranscript(ctx, job.ID, transcript); err != nil {
		return "", err
	}
	if err := state.SaveToolResult(
		ctx,
		job.ID,
		"call-create",
		"create_file",
		`{"relative_path":"docs/readme.md"}`,
		[]byte(`{"success":true,"result":"Successfully created file at: docs/readme.md"}`),
	); err != nil {
		return "", err
	}
	if err := state.SaveToolResult(
		ctx,
		job.ID,
		"call-write",
		"write_to_file",
		`{"relative_path":"docs/readme.md","content":"updated"}`,
		[]byte(`{"success":true,"result":"Successfully wrote file at: docs/readme.md"}`),
	); err != nil {
		return "", err
	}
	return "Updated the docs.", nil
}

type userInputRunner struct {
	mu       sync.Mutex
	attempts int
}

func (r *userInputRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()
	if attempt == 1 {
		return "", &openrouter_request.UserInputRequiredError{
			ToolCallID: "call-input",
			Question:   "Which color should I use?",
		}
	}
	response, found, err := state.LoadUserInputResponse(ctx, job.ID, "call-input")
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("resumed job did not receive the user response")
	}
	answer := "continued with " + response
	if err := events(openrouter_request.AgentEvent{Type: "answer", Data: answer}); err != nil {
		return "", err
	}
	return answer, nil
}

func (r *retryOutputRunner) Run(
	_ context.Context,
	_ store.Job,
	events openrouter_request.EventSink,
	_ openrouter_request.CheckpointStore,
) (string, error) {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()

	if attempt == 1 {
		if err := events(openrouter_request.AgentEvent{Type: "answer", Data: "old attempt output"}); err != nil {
			return "", err
		}
		return "", errors.New("first attempt failed")
	}
	if err := events(openrouter_request.AgentEvent{Type: "answer", Data: "retried output"}); err != nil {
		return "", err
	}
	return "retried output", nil
}

func TestRetryStreamsOnlyRetriedAttemptOutput(t *testing.T) {
	client := startTestDaemon(t, &retryOutputRunner{})
	job, err := client.submit(context.Background(), "retry me", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobFailed)

	output, exitCode := captureTestStdout(t, func() int {
		return changeJob(job.ID, "retry")
	})
	if exitCode != 0 {
		t.Fatalf("retry exit code = %d, output = %q", exitCode, output)
	}
	if !strings.Contains(output, "retried output") {
		t.Fatalf("retry output = %q, want retried answer", output)
	}
	if strings.Contains(output, "old attempt output") {
		t.Fatalf("retry replayed old attempt output: %q", output)
	}
}

func TestRunCommandRetriesFailedJob(t *testing.T) {
	client := startTestDaemon(t, &retryOutputRunner{})
	job, err := client.submit(context.Background(), "retry through run", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobFailed)

	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"run", job.ID})
	})
	if exitCode != 0 || !strings.Contains(output, "retried output") {
		t.Fatalf("run retry exit=%d output=%q", exitCode, output)
	}
	if strings.Contains(output, "old attempt output") {
		t.Fatalf("run replayed the failed attempt: %q", output)
	}
}

func TestJobWithoutAgentOutputBecomesFailed(t *testing.T) {
	client := startTestDaemon(t, preflightFailureRunner{})
	job, err := client.submit(context.Background(), "wait to run", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobFailed)

	boardOutput, exitCode := captureTestStdout(t, listJobs)
	if exitCode != 0 || !strings.Contains(boardOutput, store.JobFailed) ||
		strings.Contains(boardOutput, store.JobPending) {
		t.Fatalf("job board exit=%d output=%q", exitCode, boardOutput)
	}
}

func TestDaemonClientArchivesAJob(t *testing.T) {
	client := startTestDaemon(t, answerRunner{})
	job, err := client.submitNamed(
		context.Background(), "archive-me", "temporary job", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.archiveJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := client.archivedJobs(context.Background())
	if err != nil || len(archived) != 1 || archived[0].ID != job.ID ||
		archived[0].ArchivedAt == nil {
		t.Fatalf("archived jobs = %#v, err=%v", archived, err)
	}
	jobs, err := client.jobs(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("regular jobs after archive = %#v, err=%v", jobs, err)
	}
	output, exitCode := captureTestStdout(t, listArchivedJobs)
	if exitCode != 0 || !strings.Contains(output, job.ID) ||
		!strings.Contains(output, store.JobPending) {
		t.Fatalf("archive list exit=%d output=%q", exitCode, output)
	}
	details, err := client.jobDetails(context.Background(), job.ID)
	if err != nil || details.Job.ID != job.ID {
		t.Fatalf("archived job details = %#v, err=%v", details, err)
	}
	sections, err := buildJobViewSections(details)
	if err != nil || len(sections) == 0 ||
		!strings.Contains(strings.Join(sections[0].Lines, "\n"), "Archived:") {
		t.Fatalf("archived job overview = %#v, err=%v", sections, err)
	}
}

func TestHelpListsEveryCommandWithoutAdvancedTopic(t *testing.T) {
	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"help"})
	})
	if exitCode != 0 {
		t.Fatalf("help exit code = %d", exitCode)
	}
	for _, command := range []string{
		"pearl run", "<job-id>", "pearl job", "pearl configure", "pearl jobs", "view <job-id>", "pearl archive", "pearl dashboard",
		"pearl attach", "pearl respond", "pearl cancel", "pearl retry",
		"pearl status", "pearl schedule", "add --every", "list",
		"remove <schedule-id>", "pearl daemon", "run", "start", "stop", "restart",
		"status", "install", "uninstall", "pearl help",
	} {
		if !strings.Contains(output, command) {
			t.Fatalf("help output is missing %q:\n%s", command, output)
		}
	}
	if strings.Contains(output, "help advanced") {
		t.Fatalf("help output still references help advanced:\n%s", output)
	}
	if strings.Contains(output, "pearl goal") || strings.Contains(output, "saved goal") {
		t.Fatalf("help output still contains the goal command:\n%s", output)
	}
	if count := strings.Count(output, "pearl schedule"); count != 1 {
		t.Fatalf("pearl schedule appears %d times, want one grouped header:\n%s", count, output)
	}
	if count := strings.Count(output, "pearl daemon"); count != 1 {
		t.Fatalf("pearl daemon appears %d times, want one grouped header:\n%s", count, output)
	}
}

func TestBarePearlDefaultsToDashboard(t *testing.T) {
	output, exitCode := captureTestStderr(t, func() int {
		return Run(nil)
	})
	if exitCode != 1 || !strings.Contains(output, "Dashboard error: requires an interactive terminal") {
		t.Fatalf("bare pearl exit=%d output=%q", exitCode, output)
	}
	if strings.Contains(output, "Pearl CLI") {
		t.Fatalf("bare pearl printed help instead of opening the dashboard: %q", output)
	}
}

func TestInvalidCommandsShowShortHelpHint(t *testing.T) {
	tests := []struct {
		arguments []string
		command   string
	}{
		{arguments: []string{"frobnicate"}, command: "frobnicate"},
		{arguments: []string{"daemon", "frobnicate"}, command: "daemon frobnicate"},
		{arguments: []string{"schedule", "frobnicate"}, command: "schedule frobnicate"},
		{arguments: []string{"jobs", "frobnicate"}, command: "jobs frobnicate"},
	}
	for _, test := range tests {
		output, exitCode := captureTestStderr(t, func() int {
			return Run(test.arguments)
		})
		if exitCode != 2 {
			t.Fatalf("%q exit code = %d, want 2", test.command, exitCode)
		}
		want := fmt.Sprintf(
			"Invalid command %q. Use \"pearl help\" for more information.\n",
			test.command,
		)
		if output != want {
			t.Fatalf("%q output = %q, want %q", test.command, output, want)
		}
		if strings.Contains(output, "Pearl CLI") || strings.Contains(output, "Usage:") {
			t.Fatalf("%q printed the full help text: %q", test.command, output)
		}
	}
}

func TestJobsViewShowsTranscriptAndChangedFiles(t *testing.T) {
	client := startTestDaemon(t, jobDetailsRunner{})
	job, err := client.submitNamed(
		context.Background(), "docs-job", "update the docs", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobCompleted)

	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"jobs", "view", job.ID})
	})
	if exitCode != 0 {
		t.Fatalf("jobs view exit=%d output=%q", exitCode, output)
	}
	for _, expected := range []string{
		"Job: docs-job",
		"Status: completed",
		"Directory:",
		"Changed files",
		"created",
		"docs/readme.md",
		"Tool activity",
		"create_file docs/readme.md  ok",
		"write_to_file docs/readme.md  ok",
		"Transcript",
		"[user]",
		"update the docs",
		"[assistant reasoning]",
		"[assistant]",
		"Updated the docs.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("jobs view output is missing %q:\n%s", expected, output)
		}
	}
}

func TestInteractiveConfigureSelectsFreeOrCustomModel(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantModel  string
		wantPrompt string
	}{
		{
			name:       "free",
			input:      "test-key\ninvalid\nfree\n",
			wantModel:  "openrouter/free",
			wantPrompt: "Enter 1 for free or 2 for custom.",
		},
		{
			name:       "custom",
			input:      "test-key\n2\nanthropic/claude-sonnet-4\n",
			wantModel:  "anthropic/claude-sonnet-4",
			wantPrompt: "Model ID:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDirectory := t.TempDir()
			t.Setenv("PEARL_CONFIG_DIR", configDirectory)
			var output bytes.Buffer
			result, err := configureOpenRouterInteractively(
				strings.NewReader(test.input), &output,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.model != test.wantModel {
				t.Fatalf("configured model = %q, want %q", result.model, test.wantModel)
			}
			if !strings.Contains(output.String(), test.wantPrompt) ||
				!strings.Contains(output.String(), "Configuration saved.") {
				t.Fatalf("configure output = %q", output.String())
			}
			environment, err := os.ReadFile(result.environmentPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(environment), "test-key") {
				t.Fatalf("environment file = %q", environment)
			}
			settingsContents, err := os.ReadFile(result.settingsPath)
			if err != nil {
				t.Fatal(err)
			}
			var settings openrouter_request.Settings
			if err := json.Unmarshal(settingsContents, &settings); err != nil {
				t.Fatal(err)
			}
			if settings.Model != test.wantModel {
				t.Fatalf("settings model = %q, want %q", settings.Model, test.wantModel)
			}
		})
	}
}

func TestJobCommandCreatesNamedDetachedJob(t *testing.T) {
	client := startTestDaemon(t, &userInputRunner{})
	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"job", "-n", "release prep", "ship", "it"})
	})
	if exitCode != 0 {
		t.Fatalf("job exit code = %d, output = %q", exitCode, output)
	}
	if !strings.Contains(output, "Job: release prep") || strings.Contains(output, "Name:") {
		t.Fatalf("job output = %q", output)
	}

	jobs, err := client.jobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "release prep" || jobs[0].Prompt != "ship it" ||
		jobs[0].Status != store.JobPending {
		t.Fatalf("created jobs = %#v", jobs)
	}
	loaded, err := client.job(context.Background(), "release prep")
	if err != nil || loaded.ID != "release prep" {
		t.Fatalf("load custom job ID = %#v, err=%v", loaded, err)
	}
	boardOutput, exitCode := captureTestStdout(t, listJobs)
	if exitCode != 0 || !strings.Contains(boardOutput, "release prep") {
		t.Fatalf("job board exit=%d output=%q", exitCode, boardOutput)
	}
	header, _, _ := strings.Cut(boardOutput, "\n")
	if strings.Contains(header, "NAME") {
		t.Fatalf("job board still has a separate name column: %q", header)
	}
	if strings.Contains(header, "WORKSPACE") {
		t.Fatalf("job board still has a workspace column: %q", header)
	}
	if strings.Contains(header, "QUESTION") {
		t.Fatalf("job board still has a question column: %q", header)
	}
}

func TestRunCommandRunsPendingAndCompletedJobByID(t *testing.T) {
	startTestDaemon(t, answerRunner{})
	if output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"job", "-n", "release prep", "ship", "it"})
	}); exitCode != 0 || !strings.Contains(output, "Job: release prep") {
		t.Fatalf("job exit=%d output=%q", exitCode, output)
	}

	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"run", "release prep"})
	})
	if exitCode != 0 || !strings.Contains(output, "Job: release prep") ||
		!strings.Contains(output, "job finished") {
		t.Fatalf("run exit=%d output=%q", exitCode, output)
	}

	rerunOutput, rerunExitCode := captureTestStdout(t, func() int {
		return Run([]string{"run", "release prep"})
	})
	if rerunExitCode != 0 || !strings.Contains(rerunOutput, "job finished") {
		t.Fatalf("rerun exit=%d output=%q", rerunExitCode, rerunOutput)
	}
}

func TestRestartDaemonWaitsForShutdownBeforeStarting(t *testing.T) {
	client := startTestDaemon(t, answerRunner{})
	started := false
	output, exitCode := captureTestStdout(t, func() int {
		return restartDaemonWithStart(func() int {
			started = true
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_, err := client.status(ctx)
			cancel()
			if err == nil {
				t.Error("replacement started while the old daemon was still reachable")
			}
			return 0
		})
	})
	if exitCode != 0 || !started || !strings.Contains(output, "Pearl daemon is stopping") {
		t.Fatalf("restart exit=%d started=%v output=%q", exitCode, started, output)
	}
}

func TestJobBoardStatusUsesStateColors(t *testing.T) {
	tests := []struct {
		status string
		color  string
	}{
		{status: store.JobRunning, color: ansiGreen},
		{status: store.JobQueued, color: ansiYellow},
		{status: store.JobPending, color: ansiYellow},
		{status: store.JobWaitingInput, color: ansiMagenta},
		{status: store.JobFailed, color: ansiRed},
	}
	for _, test := range tests {
		colored := jobBoardStatus(test.status, true)
		if !strings.Contains(colored, test.color) || !strings.Contains(colored, test.status) {
			t.Fatalf("colored status %q = %q", test.status, colored)
		}
		plain := jobBoardStatus(test.status, false)
		if plain != test.status || strings.Contains(plain, "\x1b[") {
			t.Fatalf("plain status %q = %q", test.status, plain)
		}
	}
}

func TestJobBoardColorMarkersDoNotLeakIntoOutput(t *testing.T) {
	var output bytes.Buffer
	writer := tabwriter.NewWriter(&output, 0, 4, 2, ' ', tabwriter.StripEscape)
	fmt.Fprintf(writer, "ID\t%s\njob_1\t%s\n",
		jobBoardPaint(true, ansiBold+ansiCyan, "STATUS"),
		jobBoardStatus(store.JobCompleted, true),
	)
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "ÿ") || bytes.Contains(output.Bytes(), []byte{tabwriter.Escape}) {
		t.Fatalf("job table leaked tabwriter escape markers: %q", output.String())
	}
	if !strings.Contains(output.String(), ansiGreen+store.JobCompleted) {
		t.Fatalf("job table lost status color: %q", output.String())
	}
}

func TestFormatJobCreatedAtUsesReadableLocalTimeWithoutSeconds(t *testing.T) {
	createdAt := time.Date(2026, time.August, 19, 13, 5, 47, 0, time.Local)
	if got, want := formatJobCreatedAt(createdAt), "August 19 1:05pm"; got != want {
		t.Fatalf("formatJobCreatedAt() = %q, want %q", got, want)
	}
}

func TestWaitingJobAppearsOnBoardAndRespondStreamsContinuation(t *testing.T) {
	client := startTestDaemon(t, &userInputRunner{})
	job, err := client.submit(context.Background(), "pick a color", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobWaitingInput)

	attachOutput, exitCode := captureTestStdout(t, func() int {
		return attachWithClient(client, job.ID)
	})
	if exitCode != 0 || !strings.Contains(attachOutput, "Which color should I use?") ||
		!strings.Contains(attachOutput, "pearl respond "+job.ID) {
		t.Fatalf("waiting attach exit=%d output=%q", exitCode, attachOutput)
	}

	boardOutput, exitCode := captureTestStdout(t, listJobs)
	if exitCode != 0 || !strings.Contains(boardOutput, store.JobWaitingInput) ||
		strings.Contains(boardOutput, "Which color should I use?") {
		t.Fatalf("job board exit=%d output=%q", exitCode, boardOutput)
	}

	responseOutput, exitCode := captureTestStdout(t, func() int {
		return respondToJob(job.ID, "Blue")
	})
	if exitCode != 0 || !strings.Contains(responseOutput, "continued with Blue") {
		t.Fatalf("respond exit=%d output=%q", exitCode, responseOutput)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobCompleted)
}

func startTestDaemon(t *testing.T, runner daemon.AgentRunner) *daemonClient {
	t.Helper()
	configDirectory, err := os.MkdirTemp("/tmp", "pearl-cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(configDirectory) })
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	paths, err := pearlpaths.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	daemonResult := make(chan error, 1)
	go func() {
		daemonResult <- daemon.Run(ctx, paths, runner)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-daemonResult:
			if err != nil {
				t.Errorf("stop daemon: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})

	return waitForTestDaemon(t)
}

func waitForTestDaemon(t *testing.T) *daemonClient {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		client, err := newDaemonClient()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, statusErr := client.status(ctx)
			cancel()
			if statusErr == nil {
				return client
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
	return nil
}

func waitForTestJobStatus(t *testing.T, client *daemonClient, jobID, status string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		job, err := client.job(context.Background(), jobID)
		if err == nil && job.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach status %s", jobID, status)
}

func captureTestStdout(t *testing.T, run func() int) (string, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	exitCode := run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = original
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output), exitCode
}

func captureTestStderr(t *testing.T, run func() int) (string, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	exitCode := run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = original
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output), exitCode
}
