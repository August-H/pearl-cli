package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	autonomousJobLimit    = 100
	autonomousPromptLimit = 64 << 10
	autonomousModelLimit  = 1000

	// AutonomousMaxSubagents bounds how many jobs a session runs at once.
	AutonomousMaxSubagents = 16

	autonomousDefaultDuration = 2 * time.Hour
	autonomousMaxDuration     = 6 * time.Hour
)

// autonomousSessionLimit is how long the orchestrator may keep going. Its jobs run
// alongside it, so it gets a multiple of one job's budget, bounded to avoid runaway
// cost.
func autonomousSessionLimit() time.Duration {
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil || settings.Max_job_seconds <= 0 {
		return autonomousDefaultDuration
	}
	return min(max(time.Duration(settings.Max_job_seconds)*time.Second*4, time.Hour), autonomousMaxDuration)
}

func validateAutonomousConfig(config *store.AutonomousConfig) error {
	config.Goal = strings.TrimSpace(config.Goal)
	switch {
	case config.Goal == "":
		return errors.New("goal cannot be empty")
	case len(config.Goal) > autonomousPromptLimit:
		return fmt.Errorf("goal exceeds %d bytes", autonomousPromptLimit)
	case config.MaxSubagents < 1 || config.MaxSubagents > AutonomousMaxSubagents:
		return fmt.Errorf("max subagents must be between 1 and %d", AutonomousMaxSubagents)
	case len(config.AllowedModels) == 0:
		return errors.New("allow at least one subagent model")
	case len(config.AllowedModels) > autonomousModelLimit:
		return fmt.Errorf("allow at most %d subagent models", autonomousModelLimit)
	}
	for _, model := range append([]store.AutonomousModel{config.Orchestrator}, config.AllowedModels...) {
		if strings.TrimSpace(model.ID) == "" {
			return errors.New("a model has no ID")
		}
		switch model.Provider {
		case openrouter_request.ProviderOpenRouter, openrouter_request.ProviderChatGPT,
			openrouter_request.ProviderClaude, openrouter_request.ProviderAntigravity:
		default:
			return fmt.Errorf("model %q has an unknown provider %q", model.ID, model.Provider)
		}
	}
	return nil
}

type AutonomousOutcome struct {
	Finished bool
	Waiting  bool
	Summary  string
}

type AutonomousTools struct {
	CreateJob func(ctx context.Context, prompt string, model store.AutonomousModel) (store.Job, error)
	ListJobs  func(context.Context) ([]store.Job, error)
	// Reviewing and Working report what the orchestrator is doing.
	Reviewing func(context.Context) error
	Working   func(context.Context) error
	SavePlan  func(context.Context, string) error
	// TurnStarted, TurnProgress and TurnFinished record each turn, for the screen.
	TurnStarted  func(ctx context.Context, trigger string) (int, error)
	TurnProgress func(turn int, reasoning string, decisionBytes int)
	TurnFinished func(turn int, outcome string)
}

type AutonomousRunner interface {
	Run(
		context.Context,
		store.AutonomousSession,
		AutonomousTools,
	) (AutonomousOutcome, error)
}

type AutonomousManager struct {
	store  *store.Store
	jobs   *Manager
	runner AutonomousRunner
	wake   chan struct{}

	mu      sync.Mutex
	running map[string]context.CancelFunc
	wait    sync.WaitGroup
}

func NewAutonomousManager(
	state *store.Store,
	jobs *Manager,
	runner AutonomousRunner,
) *AutonomousManager {
	return &AutonomousManager{
		store: state, jobs: jobs, runner: runner,
		wake: make(chan struct{}, 1), running: make(map[string]context.CancelFunc),
	}
}

func (manager *AutonomousManager) Start(ctx context.Context) {
	manager.wait.Add(1)
	go func() {
		defer manager.wait.Done()
		manager.loop(ctx)
	}()
	manager.Wake()
}

func (manager *AutonomousManager) Wait() { manager.wait.Wait() }

func (manager *AutonomousManager) Wake() {
	select {
	case manager.wake <- struct{}{}:
	default:
	}
}

func (manager *AutonomousManager) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			manager.cancelAll()
			return
		case <-manager.wake:
		case <-ticker.C:
		}
		sessions, err := manager.store.ListOpenAutonomousSessions(ctx)
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if session.Status == store.AutonomousWaitingInput {
				jobs, err := manager.store.ListAutonomousJobs(ctx, session.ID)
				if err != nil || autonomousJobsWaiting(jobs) {
					continue
				}
				if err := manager.store.UpdateAutonomousSession(
					ctx, session.ID, store.AutonomousRunning, session.Summary, "",
				); err != nil {
					continue
				}
				session.Status = store.AutonomousRunning
			}
			manager.startSession(ctx, session)
		}
	}
}

func (manager *AutonomousManager) startSession(
	parent context.Context,
	session store.AutonomousSession,
) {
	manager.mu.Lock()
	if _, found := manager.running[session.ID]; found {
		manager.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	manager.running[session.ID] = cancel
	manager.wait.Add(1)
	manager.mu.Unlock()

	go func() {
		defer manager.wait.Done()
		defer func() {
			manager.mu.Lock()
			delete(manager.running, session.ID)
			manager.mu.Unlock()
		}()
		outcome, err := manager.runner.Run(ctx, session, AutonomousTools{
			CreateJob: func(ctx context.Context, prompt string, model store.AutonomousModel) (store.Job, error) {
				job, err := manager.store.CreateAutonomousJob(ctx, session.ID, prompt, model.Provider, model.ID)
				if err == nil {
					manager.jobs.Wake()
				}
				return job, err
			},
			ListJobs: func(ctx context.Context) ([]store.Job, error) {
				return manager.store.ListAutonomousJobs(ctx, session.ID)
			},
			Reviewing: func(ctx context.Context) error {
				return manager.store.UpdateAutonomousSession(ctx, session.ID, store.AutonomousReviewing, "", "")
			},
			Working: func(ctx context.Context) error {
				return manager.store.UpdateAutonomousSession(ctx, session.ID, store.AutonomousRunning, "", "")
			},
			SavePlan: func(ctx context.Context, plan string) error {
				return manager.store.SetAutonomousPlan(ctx, session.ID, plan)
			},
			TurnStarted: func(ctx context.Context, trigger string) (int, error) {
				return manager.store.StartAutonomousTurn(ctx, session.ID, trigger)
			},
			TurnProgress: func(turn int, reasoning string, decisionBytes int) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = manager.store.UpdateAutonomousTurn(ctx, session.ID, turn, reasoning, decisionBytes)
			},
			// These run after a stop too, so they don't use the session's context.
			TurnFinished: func(turn int, outcome string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = manager.store.FinishAutonomousTurn(ctx, session.ID, turn, outcome)
			},
		})
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			_ = manager.store.UpdateAutonomousSession(
				context.Background(), session.ID, store.AutonomousFailed, "", err.Error(),
			)
			manager.cancelQueuedJobs(session.ID)
		case outcome.Finished:
			_ = manager.store.UpdateAutonomousSession(
				context.Background(), session.ID, store.AutonomousCompleted,
				outcome.Summary, "",
			)
		case outcome.Waiting:
			_ = manager.store.UpdateAutonomousSession(
				context.Background(), session.ID, store.AutonomousWaitingInput,
				outcome.Summary, "",
			)
		default:
			_ = manager.store.UpdateAutonomousSession(
				context.Background(), session.ID, store.AutonomousFailed, "",
				"the orchestrator stopped before the goal was complete",
			)
		}
	}()
}

// cancelQueuedJobs keeps a failed session's unstarted jobs from running with nobody
// to read their results. Jobs already running finish on their own.
func (manager *AutonomousManager) cancelQueuedJobs(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobs, err := manager.store.ListAutonomousJobs(ctx, sessionID)
	if err != nil {
		return
	}
	for _, job := range jobs {
		if job.Status == store.JobQueued {
			_, _ = manager.store.RequestCancel(ctx, job.ID)
		}
	}
}

// Stop ends a session and every job it still has going.
func (manager *AutonomousManager) Stop(ctx context.Context, sessionID string) error {
	session, err := manager.store.GetAutonomousSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.Terminal() {
		return nil
	}
	// Mark it first, so a turn finishing now can't add jobs or reopen it.
	if err := manager.store.UpdateAutonomousSession(ctx, sessionID, store.AutonomousCancelled, "", ""); err != nil {
		return err
	}
	manager.mu.Lock()
	if cancel, found := manager.running[sessionID]; found {
		cancel()
	}
	manager.mu.Unlock()
	jobs, err := manager.store.ListAutonomousJobs(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Terminal() {
			continue
		}
		if _, err := manager.store.RequestCancel(ctx, job.ID); err != nil {
			return err
		}
		manager.jobs.Cancel(job.ID)
	}
	return nil
}

func (manager *AutonomousManager) cancelAll() {
	manager.mu.Lock()
	for _, cancel := range manager.running {
		cancel()
	}
	manager.mu.Unlock()
}

func autonomousJobsWaiting(jobs []store.Job) bool {
	for _, job := range jobs {
		if job.Status == store.JobWaitingInput {
			return true
		}
	}
	return false
}
