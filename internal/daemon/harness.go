package daemon

import (
	"context"
	"fmt"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

// HarnessRunner sends each job to the runner for its harness. Jobs created
// without a harness use the default from Pearl's settings when they start.
type HarnessRunner struct {
	Pearl AgentRunner
	Codex AgentRunner
}

type harnessRecorder interface {
	SetJobHarness(ctx context.Context, id, harness string) error
}

func (r HarnessRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	state openrouter_request.CheckpointStore,
) (string, error) {
	if job.Harness == "" {
		job.Harness = DefaultHarness()
		if recorder, ok := state.(harnessRecorder); ok {
			if err := recorder.SetJobHarness(ctx, job.ID, job.Harness); err != nil {
				return "", fmt.Errorf("record job harness: %w", err)
			}
		}
	}
	if job.Harness == store.HarnessCodex {
		return r.Codex.Run(ctx, job, events, state)
	}
	return r.Pearl.Run(ctx, job, events, state)
}

// DefaultHarness returns the harness configured in Pearl's settings.
func DefaultHarness() string {
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		return store.HarnessPearl
	}
	harness, err := store.NormalizeHarness(settings.Harness)
	if err != nil || harness == "" {
		return store.HarnessPearl
	}
	return harness
}
