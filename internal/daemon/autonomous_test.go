package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type autonomousChildRunner struct{}

func (autonomousChildRunner) Run(
	_ context.Context,
	job store.Job,
	_ openrouter_request.EventSink,
	_ openrouter_request.CheckpointStore,
) (string, error) {
	return "reviewed output for " + job.Prompt, nil
}

type autonomousCoordinatorRunner struct{}

func (autonomousCoordinatorRunner) Run(
	ctx context.Context,
	_ store.AutonomousSession,
	tools AutonomousTools,
) (AutonomousOutcome, error) {
	for _, prompt := range []string{"inspect tests", "inspect docs"} {
		if _, err := tools.CreateJob(ctx, prompt); err != nil {
			return AutonomousOutcome{}, err
		}
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		jobs, err := tools.ListJobs(ctx)
		if err != nil {
			return AutonomousOutcome{}, err
		}
		if len(jobs) == 2 && jobs[0].Status == store.JobCompleted &&
			jobs[1].Status == store.JobCompleted {
			if !strings.Contains(jobs[0].Result, "reviewed output") ||
				!strings.Contains(jobs[1].Result, "reviewed output") {
				return AutonomousOutcome{}, fmt.Errorf("job outputs were not available for review: %#v", jobs)
			}
			if err := tools.Reviewing(ctx); err != nil {
				return AutonomousOutcome{}, err
			}
			return AutonomousOutcome{Finished: true, Summary: "reviewed both jobs"}, nil
		}
		select {
		case <-ctx.Done():
			return AutonomousOutcome{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return AutonomousOutcome{}, fmt.Errorf("subagent jobs did not finish")
}

func TestAutonomousManagerCreatesRunsReviewsAndFinishesJobs(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	session, err := state.CreateAutonomousSession(
		context.Background(), "audit the project", t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	jobs := NewManager(state, autonomousChildRunner{})
	jobs.Start(ctx)
	autonomous := NewAutonomousManager(
		state, jobs, autonomousCoordinatorRunner{},
	)
	autonomous.Start(ctx)
	defer func() {
		cancel()
		jobs.Wait()
		autonomous.Wait()
	}()

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		details, err := state.AutonomousDetails(context.Background(), session.ID)
		if err == nil && details.Session.Status == store.AutonomousCompleted {
			if details.Session.Summary != "reviewed both jobs" || len(details.Jobs) != 2 {
				t.Fatalf("completed details = %#v", details)
			}
			for _, job := range details.Jobs {
				if job.Status != store.JobCompleted {
					t.Fatalf("child job did not complete: %#v", job)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("autonomous session did not complete")
}
