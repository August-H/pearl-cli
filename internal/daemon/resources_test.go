package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"github.com/August-H/pearl-cli/internal/resource"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type usageJobRunner struct{}

func (usageJobRunner) Run(ctx context.Context, _ store.Job, _ openrouter_request.EventSink, _ openrouter_request.CheckpointStore) (string, error) {
	// The manager must catch exhaustion even if a runner ignores a report error.
	_ = resource.Report(ctx, resource.Usage{Tokens: 10, CostUSD: 0.5})
	return "done", nil
}

type usageSessionRunner struct{}

func (usageSessionRunner) Run(ctx context.Context, _ store.AutonomousSession, tools AutonomousTools) (AutonomousOutcome, error) {
	for range 2 {
		if _, err := tools.CreateJob(ctx, "work", testModels[0]); err != nil {
			return AutonomousOutcome{}, err
		}
	}
	return AutonomousOutcome{}, resource.Report(ctx, resource.Usage{Tokens: 10})
}

func TestManagerFailsJobOnReportedUsageCap(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"test/model"}`)
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if err := state.SetResourceLimits(ctx, store.ResourceLimits{MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateJob(ctx, "work", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim %v %v", found, err)
	}
	manager := NewManager(state, usageJobRunner{})
	manager.runJob(ctx, job)
	saved, err := state.GetJob(ctx, job.ID)
	if err != nil || saved.Status != store.JobFailed || !strings.Contains(saved.Error, "token limit 10") {
		t.Fatalf("job %+v, %v", saved, err)
	}
	status, err := state.ResourceStatus(ctx)
	if err != nil || status.Usage.Tokens != 10 || status.Usage.CostUSD != 0.5 {
		t.Fatalf("usage %+v, %v", status, err)
	}
}

func TestSessionBudgetFailureCancelsQueuedJobs(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := state.CreateAutonomousSession(ctx, store.AutonomousConfig{Goal: "g", WorkspaceRoot: t.TempDir(), MaxSubagents: 2, MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	jobs := NewManager(state, usageJobRunner{})
	manager := NewAutonomousManager(state, jobs, usageSessionRunner{})
	manager.startSession(ctx, session)
	manager.Wait()
	details, err := state.AutonomousDetails(ctx, session.ID)
	if err != nil || details.Session.Status != store.AutonomousFailed || !strings.Contains(details.Session.Error, "session token limit 10") || details.Usage.Tokens != 10 {
		t.Fatalf("details %+v, %v", details, err)
	}
	if len(details.Jobs) != 2 {
		t.Fatalf("jobs: %+v", details.Jobs)
	}
	for _, job := range details.Jobs {
		if job.Status != store.JobCancelled {
			t.Fatalf("unstarted job survived exhaustion: %+v", job)
		}
	}
}

func TestClaudeUsageIncludesCacheTokensAndReleasesRequestSlot(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"test/model"}`)
	state, job := openClaudeJob(t, store.HarnessClaude)
	script, _ := writeFakeClaude(t, []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":2,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":1},"total_cost_usd":0.5}`,
	})
	if err := state.SetResourceLimits(context.Background(), store.ResourceLimits{MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	pool := resource.WithConcurrency(context.Background(), 1)
	ctx := state.BudgetContext(pool, job.ID, "")
	_, err := (ClaudeRunner{Executable: script}).Run(ctx, job, nil, state)
	if !errors.Is(err, resource.ErrBudgetExceeded) {
		t.Fatalf("budget error: %v", err)
	}
	status, err := state.ResourceStatus(context.Background())
	if err != nil || status.Usage.Tokens != 10 || status.Usage.CostUSD != 0.5 {
		t.Fatalf("usage %+v, %v", status, err)
	}
	timeout, cancel := context.WithTimeout(pool, time.Second)
	defer cancel()
	release, err := resource.Acquire(timeout)
	if err != nil {
		t.Fatalf("CLI leaked its request slot: %v", err)
	}
	release()
}

func TestCodexParserReportsUsageWithoutCountingCachedTokensTwice(t *testing.T) {
	var usage resource.Usage
	ctx := resource.WithBudget(context.Background(), nil, func(_ context.Context, u resource.Usage) error { usage = u; return nil })
	parser := &codexEventParser{ctx: ctx}
	err := parser.read(strings.NewReader(`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":8,"output_tokens":2}}` + "\n"))
	if err != nil || usage.Tokens != 12 {
		t.Fatalf("usage %+v, %v", usage, err)
	}
}

func TestCodexProxyRecordsReportedCostAndBlocksAnotherRequest(t *testing.T) {
	var total resource.Usage
	var usageLock sync.Mutex
	ctx := resource.WithBudget(context.Background(), func(context.Context) error {
		usageLock.Lock()
		defer usageLock.Unlock()
		if total.Tokens >= 10 {
			return resource.ErrBudgetExceeded
		}
		return nil
	}, func(_ context.Context, u resource.Usage) error {
		usageLock.Lock()
		defer usageLock.Unlock()
		total.Tokens += u.Tokens
		total.CostUSD += u.CostUSD
		return nil
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":8,"output_tokens":2,"cost":0.25}}}`+"\n\n")
	}))
	defer upstream.Close()
	proxy, err := startCodexModelProxyWithBudget(ctx, upstream.URL, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	response, err := http.Post(proxy.baseURL()+"/responses", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	usageLock.Lock()
	got := total
	usageLock.Unlock()
	if got.Tokens != 10 || got.CostUSD != 0.25 {
		t.Fatalf("usage %+v", got)
	}
	response, err = http.Post(proxy.baseURL()+"/responses", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("budget preflight status: %d", response.StatusCode)
	}
}

func TestDaemonRejectsNegativeResourceLimitsAtStartup(t *testing.T) {
	writeCodexTestConfig(t, `{"model":"test/model","max_concurrency":-1}`)
	directory := socketTempDir(t, "pearl-budget-test-")
	paths := pearlpaths.Paths{Directory: directory, Database: filepath.Join(directory, "pearl.db"), Socket: filepath.Join(directory, "pearl.sock"), Log: filepath.Join(directory, "pearl.log")}
	if err := Run(context.Background(), paths, usageJobRunner{}); err == nil || !strings.Contains(err.Error(), "resource limits") {
		t.Fatalf("startup error: %v", err)
	}
}

func TestPausedSessionFailsWhenGlobalBudgetIsExhausted(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := state.SetResourceLimits(ctx, store.ResourceLimits{MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	session, err := state.CreateAutonomousSession(ctx, store.AutonomousConfig{Goal: "g", WorkspaceRoot: t.TempDir(), MaxSubagents: 1})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := state.CreateAutonomousJob(ctx, session.ID, "ask a question", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim %v %v", found, err)
	}
	if err := state.PauseForInput(ctx, waiting.ID, "question", "which one?"); err != nil {
		t.Fatal(err)
	}
	queued, err := state.CreateAutonomousJob(ctx, session.ID, "later", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.UpdateAutonomousSession(ctx, session.ID, store.AutonomousWaitingInput, "waiting", ""); err != nil {
		t.Fatal(err)
	}
	// Unrelated work exhausts the shared cap while this session awaits an answer.
	if err := state.RecordUsage(ctx, "", "", resource.Usage{Tokens: 10}); !errors.Is(err, resource.ErrBudgetExceeded) {
		t.Fatalf("usage %v", err)
	}
	manager := NewAutonomousManager(state, NewManager(state, usageJobRunner{}), usageSessionRunner{})
	manager.Start(ctx)
	defer func() { cancel(); manager.Wait() }()
	deadline := time.Now().Add(2 * time.Second)
	var saved store.AutonomousSession
	for time.Now().Before(deadline) {
		saved, err = state.GetAutonomousSession(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Status == store.AutonomousFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if saved.Status != store.AutonomousFailed || !strings.Contains(saved.Error, "daemon token limit 10") {
		t.Fatalf("paused session %+v", saved)
	}
	savedJob, err := state.GetJob(ctx, queued.ID)
	if err != nil || savedJob.Status != store.JobCancelled {
		t.Fatalf("queued job %+v %v", savedJob, err)
	}
}

func TestExternalRunnersPreserveUsageRecordingErrors(t *testing.T) {
	for _, harness := range []string{store.HarnessClaude, store.HarnessCodex} {
		t.Run(harness, func(t *testing.T) {
			writeCodexTestConfig(t, `{"model":"test/model"}`)
			state, job := openClaudeJob(t, harness)
			reportError := errors.New("usage ledger unavailable")
			ctx := resource.WithBudget(context.Background(), nil, func(context.Context, resource.Usage) error { return reportError })
			var runner AgentRunner
			if harness == store.HarnessClaude {
				script, _ := writeFakeClaude(t, []string{`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":1}}`})
				runner = ClaudeRunner{Executable: script}
			} else {
				script, _ := writeFakeCodex(t, []string{`{"type":"turn.completed","usage":{"input_tokens":1}}`}, 0)
				runner = CodexRunner{Executable: script}
			}
			_, err := runner.Run(ctx, job, nil, state)
			if !errors.Is(err, reportError) || errors.Is(err, context.Canceled) {
				t.Fatalf("usage recording error masked by cleanup: %v", err)
			}
		})
	}
}
