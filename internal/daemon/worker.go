package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type AgentRunner interface {
	Run(
		ctx context.Context,
		job store.Job,
		events openrouter_request.EventSink,
		state openrouter_request.CheckpointStore,
	) (string, error)
}

type OpenRouterRunner struct{}

func (OpenRouterRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	return openrouter_request.Run(ctx, job.Prompt, openrouter_request.RunOptions{
		JobID:         job.ID,
		WorkspaceRoot: job.WorkspaceRoot,
		Events:        events,
		State:         state,
	})
}

type Manager struct {
	store  *store.Store
	runner AgentRunner
	wake   chan struct{}

	mu        sync.Mutex
	currentID string
	cancel    context.CancelFunc
	wait      sync.WaitGroup
}

func NewManager(store *store.Store, runner AgentRunner) *Manager {
	return &Manager{
		store:  store,
		runner: runner,
		wake:   make(chan struct{}, 1),
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.wait.Add(2)
	go func() {
		defer m.wait.Done()
		m.workerLoop(ctx)
	}()
	go func() {
		defer m.wait.Done()
		m.scheduleLoop(ctx)
	}()
	m.Wake()
}

func (m *Manager) Wait() { m.wait.Wait() }

func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) Current() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentID
}

func (m *Manager) Cancel(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentID == jobID && m.cancel != nil {
		m.cancel()
	}
}

func (m *Manager) setCurrent(jobID string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.currentID = jobID
	m.cancel = cancel
	m.mu.Unlock()
}

func (m *Manager) clearCurrent(jobID string) {
	m.mu.Lock()
	if m.currentID == jobID {
		m.currentID = ""
		m.cancel = nil
	}
	m.mu.Unlock()
}

func (m *Manager) workerLoop(ctx context.Context) {
	retry := time.NewTicker(2 * time.Second)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Cancel(m.Current())
			return
		case <-m.wake:
		case <-retry.C:
		}

		for ctx.Err() == nil {
			job, found, err := m.store.ClaimNextJob(ctx)
			if err != nil {
				log.Printf("Pearl queue error: %v", err)
				break
			}
			if !found {
				break
			}
			m.runJob(ctx, job)
		}
	}
}

func (m *Manager) runJob(parent context.Context, job store.Job) {
	jobContext, cancel := context.WithCancel(parent)
	m.setCurrent(job.ID, cancel)
	defer func() {
		cancel()
		m.clearCurrent(job.ID)
	}()

	events := func(event openrouter_request.AgentEvent) error {
		writeContext, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer writeCancel()
		_, err := m.store.AppendEvent(writeContext, job.ID, event.Type, event.Data)
		return err
	}
	result, err := m.runner.Run(jobContext, job, events, m.store)
	if err == nil {
		m.finishJob(job.ID, store.JobCompleted, result, "")
		return
	}
	var inputRequired *openrouter_request.UserInputRequiredError
	if errors.As(err, &inputRequired) {
		inputContext, inputCancel := context.WithTimeout(context.Background(), 5*time.Second)
		pauseErr := m.store.PauseForInput(
			inputContext, job.ID, inputRequired.ToolCallID, inputRequired.Question,
		)
		inputCancel()
		if pauseErr == nil {
			return
		}
		err = fmt.Errorf("pause job for user input: %w", pauseErr)
	}

	status := store.JobFailed
	if errors.Is(err, context.Canceled) {
		status = store.JobCancelled
	}
	errorContext, errorCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if _, appendErr := m.store.AppendEvent(errorContext, job.ID, "error", err.Error()); appendErr != nil {
		log.Printf("Pearl could not persist error event for %s: %v", job.ID, appendErr)
	}
	errorCancel()
	m.finishJob(job.ID, status, "", err.Error())
}

func (m *Manager) finishJob(jobID, status, result, errorText string) {
	var lastError error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastError = m.store.FinishJob(ctx, jobID, status, result, errorText)
		cancel()
		if lastError == nil {
			return
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	log.Printf("Pearl could not finish job %s: %v", jobID, lastError)
}

func (m *Manager) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			schedules, err := m.store.DueSchedules(ctx, now)
			if err != nil {
				log.Printf("Pearl schedule query error: %v", err)
				continue
			}
			for _, schedule := range schedules {
				if _, fired, err := m.store.FireSchedule(ctx, schedule, now); err != nil {
					log.Printf("Pearl schedule %s error: %v", schedule.ID, err)
				} else if fired {
					m.Wake()
				}
			}
		}
	}
}
