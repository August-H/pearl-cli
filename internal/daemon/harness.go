package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

// HarnessRunner sends each job to the runner for the selected model's harness.
type HarnessRunner struct {
	Pearl       AgentRunner
	Codex       AgentRunner
	Claude      AgentRunner
	Antigravity AgentRunner
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
	if harness := resolveJobHarness(job); harness != job.Harness {
		job.Harness = harness
		if recorder, ok := state.(harnessRecorder); ok {
			if err := recorder.SetJobHarness(ctx, job.ID, job.Harness); err != nil {
				return "", fmt.Errorf("record job harness: %w", err)
			}
		}
	}
	switch job.Harness {
	case store.HarnessCodex:
		return r.Codex.Run(ctx, job, events, state)
	case store.HarnessClaude:
		return r.Claude.Run(ctx, job, events, state)
	case store.HarnessAntigravity:
		return r.Antigravity.Run(ctx, job, events, state)
	}
	return r.Pearl.Run(ctx, job, events, state)
}

// harnessBilling returns the provider and model to show for a run. Claude Code
// and Antigravity use their own sign-in, not Pearl's selected provider.
func harnessBilling(harness string, settings openrouter_request.Settings) (provider, model string) {
	switch harness {
	case store.HarnessClaude:
		return BillingFor(openrouter_request.ProviderClaude), strings.TrimSpace(settings.ClaudeModel)
	case store.HarnessAntigravity:
		return openrouter_request.ProviderAntigravity, strings.TrimSpace(settings.AntigravityModel)
	}
	return openrouter_request.AgentProvider(settings), openrouter_request.AgentModel(settings)
}

// BillingAPI marks a run paid for with an API key instead of a subscription.
const BillingAPI = "api"

// BillingFor returns how a provider's usage is paid for here. Claude Code prefers
// an API key over the sign-in when it finds one.
func BillingFor(provider string) string {
	if provider == openrouter_request.ProviderClaude && strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return BillingAPI
	}
	return provider
}

// DefaultHarness returns the harness for the selected model.
func DefaultHarness() string {
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		return store.HarnessPearl
	}
	return harnessForProvider(openrouter_request.AgentProvider(settings))
}

// harnessForProvider is the one place that says which harness runs which models.
func harnessForProvider(provider string) string {
	switch provider {
	case openrouter_request.ProviderChatGPT:
		return store.HarnessCodex
	case openrouter_request.ProviderClaude:
		return store.HarnessClaude
	case openrouter_request.ProviderAntigravity:
		return store.HarnessAntigravity
	}
	return store.HarnessPearl
}

// resolveJobHarness picks the harness for a job about to start: the selected
// model's, including for retries. Answering a question or following up continues
// the conversation in the harness that holds it, if it can still run.
func resolveJobHarness(job store.Job) string {
	if job.AssignedProvider != "" {
		return harnessForProvider(job.AssignedProvider)
	}
	selected := DefaultHarness()
	if job.Harness == "" || job.InputToolCallID == "" {
		return selected
	}
	switch job.Harness {
	case store.HarnessClaude, store.HarnessAntigravity:
		// These use their own sign-in, whichever model is selected now.
		return job.Harness
	case store.HarnessPearl, store.HarnessCodex:
		if selected == store.HarnessPearl || selected == store.HarnessCodex {
			return job.Harness
		}
	}
	return selected
}
