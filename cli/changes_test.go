package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/internal/workspacechanges"
)

func TestJobReportsPreferFilesystemDiffsAndExposeCaptureLimits(t *testing.T) {
	now := time.Now()
	details := jobDetails{
		ToolExecutions: []store.ToolExecution{{ToolName: "write_to_file", Arguments: `{"relative_path":"phantom.txt"}`, Result: `{"success":true}`}},
		ChangeRuns: []store.JobChangeRun{{StartedAt: now, FinishedAt: &now, Report: workspacechanges.Report{
			Changes: []workspacechanges.Change{{Path: "shell.txt", Action: "deleted", Diff: "--- a/shell.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-old\n"}},
			Notes:   []string{"large.bin: file exceeds 1 MiB; contents not captured"},
		}}},
	}
	sections, err := buildJobViewSections(details)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, section := range sections {
		if section.Title == "Changed files" || section.Title == "Diffs" {
			text.WriteString(strings.Join(section.Lines, "\n"))
		}
	}
	if strings.Contains(text.String(), "phantom") || !strings.Contains(text.String(), "shell.txt") || !strings.Contains(text.String(), "-old") || !strings.Contains(text.String(), "contents not captured") {
		t.Fatalf("report = %s", text.String())
	}
	details.ChangeRuns = nil
	if sections := jobChangeSections(details); !strings.Contains(sections[0].Summary, "tool records") {
		t.Fatal("legacy report claims actual file changes")
	}
}

func TestJSONJobReportIncludesFilesystemDiffs(t *testing.T) {
	client := startTestDaemon(t, jobDetailsRunner{})
	job, err := client.submitNamed(context.Background(), "diff-json", "update docs", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.startJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	waitForTestJobStatus(t, client, job.ID, store.JobCompleted)
	stdout, stderr, code := captureUpdateOutput(t, func() int { return Run([]string{"jobs", "view", job.ID, "--json"}) })
	var details struct {
		ChangeRuns []store.JobChangeRun `json:"change_runs"`
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &details) != nil || len(details.ChangeRuns) != 1 || len(details.ChangeRuns[0].Report.Changes) != 1 {
		t.Fatalf("JSON report code=%d out=%s err=%s", code, stdout, stderr)
	}
	if !strings.Contains(details.ChangeRuns[0].Report.Changes[0].Diff, "+updated") {
		t.Fatalf("JSON diff = %+v", details.ChangeRuns)
	}
}
