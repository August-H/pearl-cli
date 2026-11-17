package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/August-H/pearl-cli/internal/resource"
)

func resourceSession(t *testing.T, s *Store, config AutonomousConfig) AutonomousSession {
	t.Helper()
	config.Goal = "work"
	config.WorkspaceRoot = t.TempDir()
	if config.MaxSubagents == 0 {
		config.MaxSubagents = 4
	}
	session, err := s.CreateAutonomousSession(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestDefaultGlobalConcurrencyAcrossSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for range 3 {
		session := resourceSession(t, s, AutonomousConfig{MaxSubagents: 3})
		for range 3 {
			if _, err := s.CreateAutonomousJob(ctx, session.ID, "work", "", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if jobs := claimAll(t, s); len(jobs) != 4 {
		t.Fatalf("claimed %d jobs across sessions, want daemon ceiling 4", len(jobs))
	}
}

func TestGlobalConcurrencyIncludesOrdinaryJobsAndReleasesSlots(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SetResourceLimits(ctx, ResourceLimits{MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.CreateJob(ctx, "ordinary", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := resourceSession(t, s, AutonomousConfig{MaxSubagents: 4})
	for range 5 {
		if _, err := s.CreateAutonomousJob(ctx, session.ID, "work", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	claims := make(chan Job, 12)
	failures := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			job, found, err := s.ClaimNextJob(ctx)
			if err != nil {
				failures <- err
			}
			if found {
				claims <- job
			}
		})
	}
	wg.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var jobs []Job
	for job := range claims {
		jobs = append(jobs, job)
	}
	if len(jobs) != 2 {
		t.Fatalf("claimed %d, want 2", len(jobs))
	}
	stored, err := s.GetJob(ctx, ordinary.ID)
	if err != nil || stored.Status != JobRunning {
		t.Fatalf("ordinary: %+v, %v", stored, err)
	}
	if err := s.FinishJob(ctx, ordinary.ID, JobFailed, "", "test failure"); err != nil {
		t.Fatal(err)
	}
	if next := claimAll(t, s); len(next) != 1 {
		t.Fatalf("freed slot claimed %d jobs, want 1", len(next))
	}
}

func TestSessionJobLimitCountsFinishedAndArchivedJobs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SetResourceLimits(ctx, ResourceLimits{MaxSessionJobs: 1, SessionMaxTokens: 20, SessionMaxCostUSD: 2}); err != nil {
		t.Fatal(err)
	}
	session := resourceSession(t, s, AutonomousConfig{})
	if session.MaxJobs != 1 || session.MaxTokens != 20 || session.MaxCostUSD != 2 {
		t.Fatalf("defaults: %+v", session)
	}
	job, err := s.CreateAutonomousJob(ctx, session.ID, "work", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim %v, %v", found, err)
	}
	if err := s.FinishJob(ctx, job.ID, JobCompleted, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAutonomousJob(ctx, session.ID, "more", "", ""); err == nil || !strings.Contains(err.Error(), "limit of 1 jobs") {
		t.Fatalf("limit error: %v", err)
	}
	override := resourceSession(t, s, AutonomousConfig{MaxJobs: 2, MaxTokens: 30, MaxCostUSD: 3})
	stored, err := s.GetAutonomousSession(ctx, override.ID)
	if err != nil || stored.MaxJobs != 2 || stored.MaxTokens != 30 || stored.MaxCostUSD != 3 {
		t.Fatalf("override: %+v, %v", stored, err)
	}
}

func TestUsageBudgetsPersistAndBlockOnlyTheirScope(t *testing.T) {
	for _, kind := range []string{"tokens", "cost"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "pearl.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			limits := ResourceLimits{MaxConcurrency: 2}
			config := AutonomousConfig{}
			firstReport := resource.Usage{}
			secondReport := resource.Usage{}
			if kind == "tokens" {
				limits.MaxTokens = 20
				config.MaxTokens = 10
				firstReport.Tokens = 10
				secondReport.Tokens = 10
			} else {
				limits.MaxCostUSD = 2
				config.MaxCostUSD = 1
				firstReport.CostUSD = 1
				secondReport.CostUSD = 1
			}
			if err := s.SetResourceLimits(ctx, limits); err != nil {
				t.Fatal(err)
			}
			session := resourceSession(t, s, config)
			job, err := s.CreateAutonomousJob(ctx, session.ID, "work", "", "")
			if err != nil {
				t.Fatal(err)
			}
			// Orchestrator usage and job usage share the session cap.
			if err := s.RecordUsage(ctx, "", session.ID, firstReport); !errors.Is(err, resource.ErrBudgetExceeded) {
				t.Fatalf("session cap error: %v", err)
			}
			if _, err := s.CreateAutonomousJob(ctx, session.ID, "more", "", ""); !errors.Is(err, resource.ErrBudgetExceeded) {
				t.Fatalf("creation bypassed budget: %v", err)
			}
			ordinary, err := s.CreateJob(ctx, "ordinary", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			claimed, found, err := s.ClaimNextJob(ctx)
			if err != nil || !found || claimed.ID != ordinary.ID {
				t.Fatalf("claim exhausted session's job %s instead of %s: found=%v err=%v", claimed.ID, ordinary.ID, found, err)
			}
			if err := s.RecordUsage(ctx, ordinary.ID, "", secondReport); !errors.Is(err, resource.ErrBudgetExceeded) {
				t.Fatalf("daemon cap error: %v", err)
			}
			// Reports after exhaustion still count, including usage received during shutdown.
			cancelled, cancel := context.WithCancel(s.BudgetContext(ctx, job.ID, ""))
			cancel()
			if err := resource.Report(cancelled, resource.Usage{Tokens: 1}); !errors.Is(err, resource.ErrBudgetExceeded) {
				t.Fatalf("late report: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			status, err := s.ResourceStatus(ctx)
			if err != nil || status.Exhausted == "" {
				t.Fatalf("status: %+v, %v", status, err)
			}
			details, err := s.AutonomousDetails(ctx, session.ID)
			if err != nil || details.Usage.Tokens != firstReport.Tokens+1 || details.Usage.CostUSD != firstReport.CostUSD {
				t.Fatalf("session usage: %+v, %v", details.Usage, err)
			}
			if _, found, err := s.ClaimNextJob(ctx); err != nil || found {
				t.Fatalf("global budget bypassed: %v %v", found, err)
			}
			if err := s.SetResourceLimits(ctx, ResourceLimits{}); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckResourceBudget(ctx, "", ""); err != nil {
				t.Fatalf("disabling daemon cap: %v", err)
			}
		})
	}
}

func TestInvalidResourceLimitsAreRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, limits := range []ResourceLimits{{MaxConcurrency: -1}, {MaxSessionJobs: -1}, {MaxTokens: -1}, {MaxCostUSD: math.NaN()}, {SessionMaxCostUSD: math.Inf(1)}} {
		if err := s.SetResourceLimits(ctx, limits); err == nil {
			t.Fatalf("accepted %+v", limits)
		}
	}
	for _, config := range []AutonomousConfig{{Goal: "g", WorkspaceRoot: t.TempDir(), MaxJobs: -1}, {Goal: "g", WorkspaceRoot: t.TempDir(), MaxTokens: -1}, {Goal: "g", WorkspaceRoot: t.TempDir(), MaxCostUSD: -1}} {
		if _, err := s.CreateAutonomousSession(ctx, config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
}

func TestFinishedSessionJobsCanBeRetriedAndFollowedUpWithinBudget(t *testing.T) {
	for _, action := range []string{"retry", "follow-up"} {
		t.Run(action, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			session := resourceSession(t, s, AutonomousConfig{MaxJobs: 1, MaxTokens: 10})
			job, err := s.CreateAutonomousJob(ctx, session.ID, "work", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.ClaimNextJob(ctx); err != nil || !found {
				t.Fatalf("claim %v %v", found, err)
			}
			if err := s.FinishJob(ctx, job.ID, JobCompleted, "done", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateAutonomousSession(ctx, session.ID, AutonomousCompleted, "done", ""); err != nil {
				t.Fatal(err)
			}
			if action == "retry" {
				_, _, err = s.RetryJob(ctx, job.ID)
			} else {
				_, _, err = s.RespondToJob(ctx, job.ID, "continue")
			}
			if err != nil {
				t.Fatal(err)
			}
			claimed, found, err := s.ClaimNextJob(ctx)
			if err != nil || !found || claimed.ID != job.ID {
				t.Fatalf("finished session's %s was stranded: %+v found=%v err=%v", action, claimed, found, err)
			}
		})
	}
}
