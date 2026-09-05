package cli

import (
	"context"
	"encoding/json"
	"github.com/August-H/pearl-cli/internal/store"
	"os"
	"strings"
	"testing"
)

func TestJSONQueriesAndWorkspaceSelection(t *testing.T) {
	client := startTestDaemon(t, answerRunner{})
	root, other := t.TempDir(), t.TempDir()
	job, err := client.submitNamed(context.Background(), "json job", "test", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.submitNamed(context.Background(), "other job", "test", other); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Getwd()
	stdout, stderr, code := captureUpdateOutput(t, func() int { return Run([]string{"--workspace", root, "jobs", "--json"}) })
	var jobs []store.Job
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &jobs) != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("JSON jobs code=%d out=%s err=%s", code, stdout, stderr)
	}
	after, _ := os.Getwd()
	if after != before {
		t.Fatal("global workspace did not restore cwd")
	}
	stdout, stderr, code = captureUpdateOutput(t, func() int { return Run([]string{"jobs", "view", job.ID, "--json"}) })
	var details struct {
		Job        store.Job
		Transcript []json.RawMessage
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &details) != nil || details.Job.ID != job.ID || details.Transcript == nil {
		t.Fatalf("JSON details code=%d out=%s err=%s", code, stdout, stderr)
	}
	stdout, stderr, code = captureUpdateOutput(t, func() int { return Run([]string{"jobs", "view", "missing", "--json"}) })
	var failure map[string]string
	if code == 0 || stdout != "" || json.Unmarshal([]byte(stderr), &failure) != nil || failure["error"] == "" {
		t.Fatalf("JSON error code=%d out=%s err=%s", code, stdout, stderr)
	}
}

func TestAllCommandHelpIsReadOnly(t *testing.T) {
	for _, command := range []string{"configure", "job", "run", "model", "jobs", "archive", "dashboard", "autonomous", "status", "version", "attach", "cancel", "retry", "respond", "schedule", "schedule add", "schedule list", "schedule remove", "jobs view", "daemon", "daemon start", "daemon stop", "update"} {
		t.Run(command, func(t *testing.T) {
			args := append(strings.Fields(command), "--help")
			stdout, stderr, code := captureUpdateOutput(t, func() int { return Run(args) })
			if code != 0 || !strings.Contains(stdout+stderr, "Usage") {
				t.Fatalf("help code=%d out=%s err=%s", code, stdout, stderr)
			}
		})
	}
}

func TestPatchedFilesAppearInJobReport(t *testing.T) {
	changes := changedFilesForJob([]store.ToolExecution{{ToolName: "apply_patch", Arguments: `{"relative_path":"main.go"}`, Result: `{"success":true}`}})
	if len(changes) != 1 || changes[0].Path != "main.go" || changes[0].Action != "modified" {
		t.Fatalf("changes=%v", changes)
	}
}
