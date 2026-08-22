package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	autonomousJobLimit    = 32
	autonomousPromptLimit = 64 << 10
)

type AutonomousOutcome struct {
	Finished bool
	Waiting  bool
	Summary  string
}

type AutonomousTools struct {
	CreateJob func(context.Context, string) (store.Job, error)
	ListJobs  func(context.Context) ([]store.Job, error)
	Reviewing func(context.Context) error
}

type AutonomousRunner interface {
	Run(
		context.Context,
		store.AutonomousSession,
		AutonomousTools,
	) (AutonomousOutcome, error)
}

type OpenRouterAutonomousRunner struct{}

type autonomousJobReport struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Prompt   string `json:"prompt"`
	Result   string `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
	Question string `json:"question,omitempty"`
}

func (OpenRouterAutonomousRunner) Run(
	ctx context.Context,
	session store.AutonomousSession,
	tools AutonomousTools,
) (AutonomousOutcome, error) {
	var finishedSummary string
	reportJobs := func(ctx context.Context) ([]autonomousJobReport, error) {
		jobs, err := tools.ListJobs(ctx)
		if err != nil {
			return nil, err
		}
		reports := make([]autonomousJobReport, 0, len(jobs))
		for _, job := range jobs {
			reports = append(reports, autonomousJobReport{
				ID: job.ID, Status: job.Status, Prompt: job.Prompt,
				Result: job.Result, Error: job.Error, Question: job.Question,
			})
		}
		return reports, nil
	}

	createJob := openrouter_request.Tool{
		Name:        "create_job",
		Description: "Create and immediately start one subagent job in this autonomous session. Use a focused prompt with a concrete deliverable. Create separate jobs only when their work can safely share the same workspace.",
		Parameters: objectSchema(map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Complete assignment for the subagent, including relevant goal context and the expected deliverable",
			},
		}, "prompt"),
		Execute: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if finishedSummary != "" {
				return nil, errors.New("session is already finished")
			}
			var input struct {
				Prompt string `json:"prompt"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, fmt.Errorf("decode create_job arguments: %w", err)
			}
			input.Prompt = strings.TrimSpace(input.Prompt)
			if input.Prompt == "" {
				return nil, errors.New("job prompt cannot be empty")
			}
			if len(input.Prompt) > autonomousPromptLimit {
				return nil, fmt.Errorf("job prompt exceeds %d bytes", autonomousPromptLimit)
			}
			jobs, err := tools.ListJobs(ctx)
			if err != nil {
				return nil, err
			}
			if len(jobs) >= autonomousJobLimit {
				return nil, fmt.Errorf("autonomous session reached its %d-job limit", autonomousJobLimit)
			}
			job, err := tools.CreateJob(ctx, input.Prompt)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": job.ID, "status": job.Status}, nil
		},
	}

	listJobs := openrouter_request.Tool{
		Name:        "list_jobs",
		Description: "List every subagent job in this session, including its prompt, current status, result, error, and pending question. Use this to review completed work before deciding what comes next.",
		Parameters:  objectSchema(nil),
		Execute: func(ctx context.Context, _ json.RawMessage) (any, error) {
			reports, err := reportJobs(ctx)
			if err != nil {
				return nil, err
			}
			if len(reports) > 0 && autonomousReportsSettled(reports) {
				if err := tools.Reviewing(ctx); err != nil {
					return nil, err
				}
			}
			return reports, nil
		},
	}

	waitForJobs := openrouter_request.Tool{
		Name:        "wait_for_jobs",
		Description: "Wait until all current subagent jobs finish or one needs user input, then return every job and its output for review.",
		Parameters:  objectSchema(nil),
		Execute: func(ctx context.Context, _ json.RawMessage) (any, error) {
			for {
				jobs, err := tools.ListJobs(ctx)
				if err != nil {
					return nil, err
				}
				if len(jobs) == 0 {
					return nil, errors.New("create at least one job before waiting")
				}
				settled := true
				for _, job := range jobs {
					if job.Status == store.JobQueued || job.Status == store.JobRunning {
						settled = false
						break
					}
				}
				if settled {
					if err := tools.Reviewing(ctx); err != nil {
						return nil, err
					}
					return reportJobs(ctx)
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
			}
		},
	}

	finish := openrouter_request.Tool{
		Name:        "finish",
		Description: "Finish the autonomous session after you have reviewed every job output and the user's goal is met.",
		Parameters: objectSchema(map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "Concise account of what the subagents completed and the final result",
			},
		}, "summary"),
		Execute: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if finishedSummary != "" {
				return nil, errors.New("finish has already been called")
			}
			var input struct {
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, fmt.Errorf("decode finish arguments: %w", err)
			}
			jobs, err := tools.ListJobs(ctx)
			if err != nil {
				return nil, err
			}
			if len(jobs) == 0 {
				return nil, errors.New("cannot finish before creating and reviewing a job")
			}
			for _, job := range jobs {
				switch job.Status {
				case store.JobQueued, store.JobRunning:
					return nil, fmt.Errorf("job %s is still %s", job.ID, job.Status)
				case store.JobWaitingInput:
					return nil, fmt.Errorf("job %s is waiting for user input", job.ID)
				}
			}
			input.Summary = strings.TrimSpace(input.Summary)
			if input.Summary == "" {
				return nil, errors.New("summary cannot be empty")
			}
			if len(input.Summary) > autonomousPromptLimit {
				return nil, fmt.Errorf("summary exceeds %d bytes", autonomousPromptLimit)
			}
			finishedSummary = input.Summary
			return map[string]string{"status": store.AutonomousCompleted}, nil
		},
	}

	existingJobs, err := reportJobs(ctx)
	if err != nil {
		return AutonomousOutcome{}, err
	}
	existingJSON, err := json.Marshal(existingJobs)
	if err != nil {
		return AutonomousOutcome{}, err
	}
	prompt := fmt.Sprintf(
		"Goal:\n%s\n\nWorkspace:\n%s\n\nJobs already created in this session:\n%s",
		session.Goal, session.WorkspaceRoot, existingJSON,
	)
	answer, err := openrouter_request.Run(ctx, prompt, openrouter_request.RunOptions{
		WorkspaceRoot: session.WorkspaceRoot,
		MaxDuration:   24 * time.Hour,
		MaxToolDepth:  128,
		SystemPrompt:  autonomousSystemPrompt,
		Tools:         []openrouter_request.Tool{createJob, listJobs, waitForJobs, finish},
		OnlyTools:     true,
	})
	if err != nil {
		return AutonomousOutcome{}, err
	}
	if finishedSummary != "" {
		return AutonomousOutcome{Finished: true, Summary: finishedSummary}, nil
	}
	jobs, listErr := tools.ListJobs(ctx)
	if listErr != nil {
		return AutonomousOutcome{}, listErr
	}
	for _, job := range jobs {
		if job.Status == store.JobWaitingInput {
			return AutonomousOutcome{Waiting: true, Summary: strings.TrimSpace(answer)}, nil
		}
	}
	return AutonomousOutcome{}, errors.New("coordinator stopped without calling finish")
}

const autonomousSystemPrompt = `You are Pearl's autonomous coordinator. You manage subagents; you do not edit the workspace yourself.

Break the goal into focused jobs and create them with create_job. Jobs share one workspace, so do not run overlapping file edits at the same time. Use wait_for_jobs after creating work. Read every result and error, judge it against the original goal, and create follow-up jobs when something is missing or failed. Never assume that a job succeeded from its status alone. Review its output.

Call finish only after all jobs have settled and their combined work meets the goal. If a job is waiting for user input, do not replace it or call finish. Stop so the session can resume after the user answers. You may create at most 32 jobs.`

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
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
			CreateJob: func(ctx context.Context, prompt string) (store.Job, error) {
				job, err := manager.store.CreateAutonomousJob(ctx, session.ID, prompt)
				if err == nil {
					manager.jobs.Wake()
				}
				return job, err
			},
			ListJobs: func(ctx context.Context) ([]store.Job, error) {
				return manager.store.ListAutonomousJobs(ctx, session.ID)
			},
			Reviewing: func(ctx context.Context) error {
				return manager.store.UpdateAutonomousSession(
					ctx, session.ID, store.AutonomousReviewing, "", "",
				)
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
				"coordinator stopped before the goal was complete",
			)
		}
	}()
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

func autonomousReportsSettled(reports []autonomousJobReport) bool {
	for _, report := range reports {
		if report.Status == store.JobQueued || report.Status == store.JobRunning {
			return false
		}
	}
	return true
}
