package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

func waitingReport(t *testing.T, prompt string) orchestratorJobReport {
	t.Helper()
	_, reports, found := strings.Cut(prompt, "\nJobs so far (new_report marks what you haven't seen):\n")
	var jobs []orchestratorJobReport
	if !found {
		t.Error("the prompt has no job reports")
		return orchestratorJobReport{}
	}
	if err := json.NewDecoder(strings.NewReader(reports)).Decode(&jobs); err != nil {
		t.Error(err)
	}
	for _, job := range jobs {
		if job.Status == store.JobWaitingInput {
			return job
		}
	}
	t.Error("the prompt has no waiting job")
	return orchestratorJobReport{}
}

func responseDecision(responses ...orchestratorJobResponse) string {
	data, _ := json.Marshal(orchestratorDecision{Plan: "answer from the project context", Responses: responses})
	return string(data)
}

func TestOrchestratorAnswersSuccessiveQuestionsInTheSameJob(t *testing.T) {
	child := &childRunner{answer: func(job store.Job) (string, error) {
		switch job.InputResponse {
		case "":
			return "", &openrouter_request.UserInputRequiredError{ToolCallID: "q1", Question: "Which API?"}
		case "use the existing API":
			return "", &openrouter_request.UserInputRequiredError{ToolCallID: "q2", Question: "Which tests?"}
		case "add parser tests":
			return "implemented and tested", nil
		default:
			t.Errorf("unexpected input: %q", job.InputResponse)
			return "unexpected input", nil
		}
	}}
	script := &scriptedOrchestrator{replies: []func(string) string{
		reply(decisionWith(orchestratorJobRequest{Model: "claude/opus", Prompt: "fix parser"})),
		func(prompt string) string {
			job := waitingReport(t, prompt)
			if job.Question != "Which API?" {
				t.Errorf("question = %q", job.Question)
			}
			return responseDecision(orchestratorJobResponse{JobID: job.ID, Response: "use the existing API"})
		},
		func(prompt string) string {
			job := waitingReport(t, prompt)
			if job.Question != "Which tests?" || !job.New {
				t.Errorf("second question wasn't a fresh report: %+v", job)
			}
			return responseDecision(orchestratorJobResponse{JobID: job.ID, Response: "add parser tests"})
		},
		reply(`{"finish":"done"}`),
	}}
	fixture := startAutonomous(t, 1, script, child)
	details := fixture.waitForStatus(t, store.AutonomousCompleted)
	if len(details.Jobs) != 1 || details.Jobs[0].Result != "implemented and tested" {
		t.Fatalf("jobs = %+v", details.Jobs)
	}
	for _, turn := range details.Turns[1:3] {
		if turn.Outcome != "Answered 1 question" {
			t.Errorf("response turn = %+v", turn)
		}
	}
	events, err := fixture.state.EventsOfType(context.Background(), details.Jobs[0].ID, "orchestrator_response")
	if err != nil || len(events) != 2 || events[0].Data != "use the existing API" || events[1].Data != "add parser tests" {
		t.Fatalf("recorded answers = %+v, err = %v", events, err)
	}
}

func TestOrchestratorCanCorrectARejectedResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		response func(string) orchestratorJobResponse
		want     string
	}{
		{"unknown job", func(string) orchestratorJobResponse {
			return orchestratorJobResponse{JobID: "other", Response: "answer"}
		}, "not waiting for input in your report"},
		{"empty answer", func(id string) orchestratorJobResponse { return orchestratorJobResponse{JobID: id, Response: "  "} }, "response was empty"},
		{"oversized answer", func(id string) orchestratorJobResponse {
			return orchestratorJobResponse{JobID: id, Response: strings.Repeat("x", autonomousPromptLimit+1)}
		}, "response exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			child := &childRunner{answer: func(job store.Job) (string, error) {
				if job.InputResponse == "" {
					return "", &openrouter_request.UserInputRequiredError{ToolCallID: "q", Question: "Which API?"}
				}
				if job.InputResponse != "existing API" {
					t.Errorf("unexpected answer = %q", job.InputResponse)
				}
				return "done", nil
			}}
			script := &scriptedOrchestrator{replies: []func(string) string{
				reply(decisionWith(orchestratorJobRequest{Model: "claude/opus", Prompt: "fix parser"})),
				func(prompt string) string { return responseDecision(test.response(waitingReport(t, prompt).ID)) },
				func(prompt string) string {
					if !strings.Contains(prompt, test.want) {
						t.Errorf("missing response correction %q", test.want)
					}
					return responseDecision(orchestratorJobResponse{JobID: waitingReport(t, prompt).ID, Response: "existing API"})
				},
				reply(`{"finish":"done"}`),
			}}
			fixture := startAutonomous(t, 1, script, child)
			details := fixture.waitForStatus(t, store.AutonomousCompleted)
			if len(details.Jobs) != 1 || len(script.turns()) != 4 {
				t.Fatalf("jobs = %+v, turns = %d", details.Jobs, len(script.turns()))
			}
		})
	}
}
