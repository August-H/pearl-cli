package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
)

const testSubagentRequest = `<pearl_job_request>
{"issue":"Parser drops escaped quotes","task":"Fix parser.go and add coverage for escaped quotes"}
</pearl_job_request>`

func TestSubagentRequestsGoToOrchestratorForApprovalAndModelSelection(t *testing.T) {
	for _, accept := range []bool{true, false} {
		name := "decline"
		if accept {
			name = "accept"
		}
		t.Run(name, func(t *testing.T) {
			child := &childRunner{answer: func(job store.Job) (string, error) {
				if !strings.HasSuffix(job.Prompt, autonomousSubagentInstructions) {
					t.Errorf("job %s didn't receive request instructions", job.ID)
				}
				if strings.HasPrefix(job.Prompt, "inspect parser") {
					return "Found a bug.\n" + testSubagentRequest, nil
				}
				return "Fixed and verified", nil
			}}
			script := &scriptedOrchestrator{replies: []func(string) string{
				reply(decisionWith(orchestratorJobRequest{Model: "claude/opus", Prompt: "inspect parser"})),
				func(prompt string) string {
					for _, want := range []string{
						`"job_requests": [`, `"issue": "Parser drops escaped quotes"`,
						`"task": "Fix parser.go and add coverage for escaped quotes"`, `"new_report": true`,
					} {
						if !strings.Contains(prompt, want) {
							t.Errorf("orchestrator didn't receive %s", want)
						}
					}
					if accept {
						return decisionWith(orchestratorJobRequest{Model: "chatgpt/gpt-6", Prompt: "fix escaped quotes in parser.go"})
					}
					return `{"plan":"Declined because the behavior is intentional","finish":"Reviewed; no changes needed"}`
				},
				reply(`{"finish":"Fixed the parser"}`),
			}}
			fixture := startAutonomous(t, 1, script, child)
			details := fixture.waitForStatus(t, store.AutonomousCompleted)
			wantJobs := 1
			if accept {
				wantJobs = 2
			}
			if len(details.Jobs) != wantJobs {
				t.Fatalf("got %d jobs, want %d", len(details.Jobs), wantJobs)
			}
			for _, job := range details.Jobs {
				if job.Prompt == "fix escaped quotes in parser.go" &&
					(job.AssignedProvider != "chatgpt" || job.AssignedModel != "gpt-6") {
					t.Errorf("follow-up didn't use the orchestrator's chosen model: %+v", job)
				}
				if strings.Contains(job.Prompt, autonomousSubagentInstructions) {
					t.Error("dispatch instructions were added to the saved assignment")
				}
			}
			if turns := len(script.turns()); turns != wantJobs+1 {
				t.Errorf("got %d turns, want %d; a handled request may have been reconsidered", turns, wantJobs+1)
			}
		})
	}
}

func TestSubagentRequestsSurviveShortenedReports(t *testing.T) {
	padding := strings.Repeat("x", orchestratorNewResultBytes)
	job := store.Job{ID: "a", Status: store.JobCompleted, Prompt: "inspect", Result: padding + testSubagentRequest + padding}
	session := store.AutonomousSession{AllowedModels: testModels, MaxSubagents: 1}
	for _, reviewed := range []map[string]string{{}, {job.ID: job.Status}} {
		prompt, err := orchestratorPrompt(session, "", []store.Job{job}, reviewed, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, "[... cut ...]") || !strings.Contains(prompt, `"issue": "Parser drops escaped quotes"`) {
			t.Fatalf("request was lost when the report was shortened:\n%s", prompt)
		}
	}
}

func TestSubagentRequestsRejectMalformedOrIncompleteBlocksAndBoundContext(t *testing.T) {
	report := `<pearl_job_request>not JSON</pearl_job_request>
<pearl_job_request>{"issue":"missing task"}</pearl_job_request>
<pearl_job_request>{"issue":" ","task":"something"}</pearl_job_request>` + testSubagentRequest
	requests := readSubagentJobRequests(report, orchestratorNewResultBytes)
	if len(requests) != 1 || requests[0].Issue != "Parser drops escaped quotes" {
		t.Fatalf("requests = %+v", requests)
	}
	longRequest := `<pearl_job_request>{"issue":"` + strings.Repeat("i", 4000) + `","task":"` + strings.Repeat("t", 4000) + `"}</pearl_job_request>`
	requests = readSubagentJobRequests(strings.Repeat(longRequest, subagentJobRequestLimit+1), orchestratorOldResultBytes)
	if len(requests) != subagentJobRequestLimit {
		t.Fatalf("got %d requests, want %d", len(requests), subagentJobRequestLimit)
	}
	total := 0
	for _, request := range requests {
		total += len(request.Issue) + len(request.Task)
	}
	// shortenMiddle adds its cut marker in addition to the retained text.
	if total > orchestratorOldResultBytes+2*subagentJobRequestLimit*len("\n[... cut ...]\n") {
		t.Fatalf("request context grew to %d bytes", total)
	}
}

func TestOrdinaryJobsDontReceiveOrchestratorInstructions(t *testing.T) {
	writeCodexTestConfig(t, `{"provider":"openrouter","model":"m"}`)
	state, job := openClaudeJob(t, store.HarnessPearl)
	child := &childRunner{answer: func(received store.Job) (string, error) {
		if received.Prompt != job.Prompt {
			t.Errorf("ordinary job prompt changed: %q", received.Prompt)
		}
		return "done", nil
	}}
	NewManager(state, child).runJob(context.Background(), job)
}
