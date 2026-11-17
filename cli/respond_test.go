package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestRespondPromptsAndResumesWaitingJob(t *testing.T) {
	for _, mode := range []string{"bare", "job ID", "inline"} {
		t.Run(mode, func(t *testing.T) {
			client := startTestDaemon(t, &userInputRunner{})
			directory := t.TempDir()
			t.Chdir(directory)
			job, err := client.submit(context.Background(), "pick a color", directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
				t.Fatal(err)
			}
			waitForTestJobStatus(t, client, job.ID, store.JobWaitingInput)

			args := []string{"respond"}
			if mode != "bare" {
				args = append(args, job.ID)
			}
			if mode == "inline" {
				args = append(args, "Blue", "please")
			}
			setRespondTestInput(t, "Blue please\n")
			output, exitCode := captureTestStdout(t, func() int { return Run(args) })
			if exitCode != 0 || !strings.Contains(output, "continued with Blue please") {
				t.Fatalf("respond exit=%d output=%q", exitCode, output)
			}
			if mode != "inline" && (!strings.Contains(output, "Which color should I use?") || !strings.Contains(output, "Answer: ")) {
				t.Fatalf("missing interactive question: %q", output)
			}
			waitForTestJobStatus(t, client, job.ID, store.JobCompleted)
		})
	}
}

func TestRespondWithoutLocalWaitingJobsDoesNotReadInput(t *testing.T) {
	client := startTestDaemon(t, &userInputRunner{})
	other := t.TempDir()
	t.Chdir(t.TempDir())
	job, err := client.submit(context.Background(), "pick a color elsewhere", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobWaitingInput)
	setRespondTestInput(t, "do not send this\n")
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"respond"}) })
	if exitCode != 0 || !strings.Contains(output, "No jobs are waiting for input in this workspace.") {
		t.Fatalf("respond exit=%d output=%q", exitCode, output)
	}
	remaining, err := io.ReadAll(os.Stdin)
	if err != nil || string(remaining) != "do not send this\n" {
		t.Fatalf("unexpected input consumption: %q, %v", remaining, err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobWaitingInput)
}

func TestPromptForJobResponseSelectionAndValidation(t *testing.T) {
	jobs := []store.Job{
		{ID: "first", Question: "Which color?"},
		{ID: "second job", Question: "Which port?"},
	}
	for _, test := range []struct {
		name, input, wantID, wantAnswer string
		wantEOF                         bool
	}{
		{name: "number", input: "2\n8080\n", wantID: "second job", wantAnswer: "8080"},
		{name: "ID", input: "second job\n8080\n", wantID: "second job", wantAnswer: "8080"},
		{name: "invalid selection and blank answer", input: "0\n3\nmissing\n1\n  \nBlue\n", wantID: "first", wantAnswer: "Blue"},
		{name: "answer without newline", input: "1\nBlue", wantID: "first", wantAnswer: "Blue"},
		{name: "selection EOF", input: "", wantEOF: true},
		{name: "answer EOF", input: "1\n", wantEOF: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			job, answer, err := promptForJobResponse(jobs, strings.NewReader(test.input), &output)
			if test.wantEOF {
				if !errors.Is(err, io.EOF) || job.ID != "" || answer != "" {
					t.Fatalf("EOF result = %q, %q, %v", job.ID, answer, err)
				}
				return
			}
			if err != nil || job.ID != test.wantID || answer != test.wantAnswer {
				t.Fatalf("response = %q, %q, %v", job.ID, answer, err)
			}
			if !strings.Contains(output.String(), job.Question+"\nAnswer: ") {
				t.Fatalf("selected question was not shown: %q", output.String())
			}
		})
	}
}

func TestDashboardRespondDraft(t *testing.T) {
	workspace := t.TempDir()
	waiting := store.Job{ID: "waiting job", WorkspaceRoot: workspace, Status: store.JobWaitingInput, Question: "Which color?"}
	second := store.Job{ID: "second", WorkspaceRoot: workspace, Status: store.JobWaitingInput, Question: "Which port?"}
	other := store.Job{ID: "elsewhere", WorkspaceRoot: t.TempDir(), Status: store.JobWaitingInput}
	finished := store.Job{ID: "finished", WorkspaceRoot: workspace, Status: store.JobCompleted}
	for _, test := range []struct {
		name      string
		args      []string
		jobs      []store.Job
		wantDraft string
		wantText  string
		wantError bool
	}{
		{name: "one local waiting job", jobs: []store.Job{other, finished, waiting}, wantDraft: `respond waiting\ job `, wantText: waiting.Question},
		{name: "several jobs", jobs: []store.Job{waiting, second}, wantDraft: "respond ", wantText: "Choose a waiting job"},
		{name: "no local waiting jobs", jobs: []store.Job{other, finished}, wantText: "No jobs are waiting"},
		{name: "explicit ID", args: []string{second.ID}, jobs: []store.Job{waiting, second}, wantDraft: "respond second ", wantText: second.Question},
		{name: "finished ID", args: []string{finished.ID}, jobs: []store.Job{finished}, wantError: true},
		{name: "missing ID", args: []string{"missing"}, jobs: []store.Job{waiting}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			draft, text, err := dashboardRespondDraft(test.args, test.jobs, workspace)
			if (err != nil) != test.wantError || draft != test.wantDraft || !strings.Contains(text, test.wantText) {
				t.Fatalf("draft = %q, %q, %v", draft, text, err)
			}
		})
	}
}

func setRespondTestInput(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	t.Cleanup(func() {
		os.Stdin = original
		input.Close()
	})
}
