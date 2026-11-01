package daemon

import (
	"context"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestTheModelsProviderDecidesTheHarness(t *testing.T) {
	for provider, want := range map[string]string{
		"chatgpt": store.HarnessCodex, "claude": store.HarnessClaude,
		"antigravity": store.HarnessAntigravity, "openrouter": store.HarnessPearl,
	} {
		writeCodexTestConfig(t, `{"provider":"`+provider+`","model":"m","max_depth":5}`)
		if got := DefaultHarness(); got != want {
			t.Errorf("provider %s runs %s, want %s", provider, got, want)
		}
	}
	// An old "harness" setting no longer has any say.
	writeCodexTestConfig(t, `{"provider":"claude","harness":"codex","model":"m","max_depth":5}`)
	if got := DefaultHarness(); got != store.HarnessClaude {
		t.Errorf("a leftover harness setting won: %s", got)
	}
}

func TestResolveJobHarness(t *testing.T) {
	for _, test := range []struct {
		name, provider, recorded, input, want string
	}{
		{"new job", "claude", "", "", store.HarnessClaude},
		{"retry follows the selected model", "claude", store.HarnessCodex, "", store.HarnessClaude},
		{"retry away from Claude", "chatgpt", store.HarnessClaude, "", store.HarnessCodex},
		{"retry stays when nothing changed", "chatgpt", store.HarnessCodex, "", store.HarnessCodex},
		{"answer stays in Claude's session", "chatgpt", store.HarnessClaude, "codex-question-1", store.HarnessClaude},
		{"answer stays in Antigravity's session", "claude", store.HarnessAntigravity, "followup-1", store.HarnessAntigravity},
		{"answer stays in Codex while it can run", "openrouter", store.HarnessCodex, "codex-question-1", store.HarnessCodex},
		{"answer moves when Codex has no provider", "claude", store.HarnessCodex, "codex-question-1", store.HarnessClaude},
		{"answer moves when Pearl has no provider", "antigravity", store.HarnessPearl, "tool-1", store.HarnessAntigravity},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeCodexTestConfig(t, `{"provider":"`+test.provider+`","model":"m","max_depth":5}`)
			job := store.Job{Harness: test.recorded, InputToolCallID: test.input}
			if got := resolveJobHarness(job); got != test.want {
				t.Fatalf("harness = %s, want %s", got, test.want)
			}
		})
	}
}

func TestHarnessRunnerRecordsTheHarnessThatRan(t *testing.T) {
	writeCodexTestConfig(t, `{"provider":"antigravity","model":"m","max_depth":5}`)
	state, job := openClaudeJob(t, store.HarnessCodex)
	var pearl, codex, claude, antigravity string
	runner := HarnessRunner{
		Pearl: recordingRunner{harness: &pearl}, Codex: recordingRunner{harness: &codex},
		Claude: recordingRunner{harness: &claude}, Antigravity: recordingRunner{harness: &antigravity},
	}
	ctx := context.Background()
	if _, err := runner.Run(ctx, job, nil, state); err != nil {
		t.Fatal(err)
	}
	if antigravity != store.HarnessAntigravity || codex != "" {
		t.Fatalf("ran antigravity=%q codex=%q", antigravity, codex)
	}
	if stored, err := state.GetJob(ctx, job.ID); err != nil || stored.Harness != store.HarnessAntigravity {
		t.Fatalf("stored = %q, %v", stored.Harness, err)
	}
}
