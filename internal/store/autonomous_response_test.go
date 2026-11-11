package store

import (
	"context"
	"testing"
)

func TestAutonomousResponseOnlyAnswersTheCurrentQuestionInItsSession(t *testing.T) {
	for _, scenario := range []string{"answer", "wrong session", "empty session", "empty input ID", "new question", "user answered", "completed job", "cancelled session"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			state := openTestStore(t)
			session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "fix parser", WorkspaceRoot: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			job, err := state.CreateAutonomousJob(ctx, session.ID, "fix parser", "claude", "opus")
			if err != nil {
				t.Fatal(err)
			}
			claim := func() {
				t.Helper()
				if claimed, found, err := state.ClaimNextJob(ctx); err != nil || !found || claimed.ID != job.ID {
					t.Fatalf("claimed = %+v, found = %v, err = %v", claimed, found, err)
				}
			}
			claim()
			if err := state.PauseForInput(ctx, job.ID, "q1", "Which API?"); err != nil {
				t.Fatal(err)
			}
			sessionID, inputID := session.ID, "q1"
			switch scenario {
			case "wrong session":
				other, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "other", WorkspaceRoot: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				sessionID = other.ID
			case "empty session":
				sessionID = ""
			case "empty input ID":
				inputID = ""
			case "new question", "user answered", "completed job":
				if _, _, err := state.RespondToJob(ctx, job.ID, "user's answer"); err != nil {
					t.Fatal(err)
				}
				if scenario != "user answered" {
					claim()
					if scenario == "new question" {
						err = state.PauseForInput(ctx, job.ID, "q2", "Which tests?")
					} else {
						err = state.FinishJob(ctx, job.ID, JobCompleted, "done", "")
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			case "cancelled session":
				if err := state.UpdateAutonomousSession(ctx, session.ID, AutonomousCancelled, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			before, err := state.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = state.RespondToAutonomousJob(ctx, sessionID, job.ID, inputID, "existing API")
			if scenario == "answer" {
				if err != nil {
					t.Fatal(err)
				}
				answer, found, err := state.LoadUserInputResponse(ctx, job.ID, "q1")
				if err != nil || !found || answer != "existing API" {
					t.Fatalf("saved answer = %q, found = %v, err = %v", answer, found, err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid or stale response was accepted")
			}
			after, err := state.GetJob(ctx, job.ID)
			if err != nil || after.Status != before.Status || after.InputResponse != before.InputResponse || after.InputToolCallID != before.InputToolCallID || after.Question != before.Question {
				t.Fatalf("rejected response changed job: before = %+v, after = %+v, err = %v", before, after, err)
			}
			events, err := state.EventsOfType(ctx, job.ID, "orchestrator_response")
			if err != nil || len(events) != 0 {
				t.Fatalf("rejected response recorded an answer: %+v, err = %v", events, err)
			}
		})
	}
}
