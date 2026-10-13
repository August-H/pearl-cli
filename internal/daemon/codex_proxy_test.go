package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const codexProxyTestStream = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"r1","model":"vendor/exact-model:free"}}` + "\n\n" +
	`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"r1","model":"vendor/exact-model:free"}}` + "\n\n"

func TestCodexModelProxyForwardsRequestsAndReportsTheRoutedModel(t *testing.T) {
	var gotPath, gotAuthorization, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotAuthorization = request.Header.Get("Authorization")
		body, _ := io.ReadAll(request.Body)
		gotBody = string(body)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, codexProxyTestStream)
	}))
	defer upstream.Close()

	var lock sync.Mutex
	var reported []string
	proxy, err := startCodexModelProxy(upstream.URL, func(model string) {
		lock.Lock()
		defer lock.Unlock()
		reported = append(reported, model)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()

	request, _ := http.NewRequest(http.MethodPost, proxy.baseURL()+"/responses",
		strings.NewReader(`{"model":"openrouter/free"}`))
	request.Header.Set("Authorization", "Bearer test-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()

	if string(body) != codexProxyTestStream {
		t.Fatalf("proxy changed the response: %q", body)
	}
	if gotPath != "/api/v1/responses" || gotAuthorization != "Bearer test-key" ||
		gotBody != `{"model":"openrouter/free"}` {
		t.Fatalf("upstream got path %q, auth %q, body %q", gotPath, gotAuthorization, gotBody)
	}
	lock.Lock()
	defer lock.Unlock()
	if !slices.Equal(reported, []string{"vendor/exact-model:free", "vendor/exact-model:free"}) {
		t.Fatalf("reported models = %q", reported)
	}
}

func TestCodexResponseModelReadsEventsAndPlainJSON(t *testing.T) {
	for line, want := range map[string]string{
		`data: {"response":{"model":"a/b"}}`: "a/b",
		`{"id":"x","model":"c/d"}`:           "c/d",
		`data: {"delta":"text"}`:             "",
		`data: [DONE]`:                       "",
		`event: response.created`:            "",
	} {
		if got := codexResponseModel([]byte(line)); got != want {
			t.Errorf("codexResponseModel(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestCodexInstructionsListSkillsAsValidTOML(t *testing.T) {
	if got := codexDeveloperInstructions(nil); got != codexInstructions {
		t.Fatalf("instructions without skills = %q", got)
	}
	instructions := codexDeveloperInstructions([]openrouter_request.Skill{
		{Name: "deploy", Description: `Use "make ship"`, Path: "/skills/deploy.md"},
	})
	if !strings.Contains(instructions, `- deploy: Use "make ship" (/skills/deploy.md)`) {
		t.Fatalf("instructions = %q", instructions)
	}
	if got := codexTOMLString("a \"b\" \\ \n\x01é"); got != `"a \"b\" \\ \n\u0001é"` {
		t.Fatalf("TOML string = %s", got)
	}
}

func TestCodexRunnerRecordsTheModelOpenRouterRoutedTo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Codex script needs a POSIX shell")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	writeCodexTestConfig(t, `{"model":"openrouter/free"}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, codexProxyTestStream)
	}))
	defer upstream.Close()

	directory := t.TempDir()
	script := filepath.Join(directory, "codex")
	contents := `#!/bin/sh
cat > /dev/null
base=$(printf '%s\n' "$@" | sed -n 's/^model_providers.openrouter.base_url="\(.*\)"$/\1/p')
curl -sf -H "Authorization: Bearer $` + codexAPIKeyEnvironment + `" -d '{}' "$base/responses" > /dev/null || exit 3
echo '{"type":"thread.started","thread_id":"thread-1"}'
echo '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done"}}'
echo '{"type":"turn.completed"}'
`
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}

	state, err := store.Open(filepath.Join(t.TempDir(), "pearl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, err := state.CreatePendingJob(ctx, "", "say done", workspace, store.HarnessCodex)
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	events := func(event openrouter_request.AgentEvent) error {
		if event.Type == "model" {
			models = append(models, event.Data)
		}
		return nil
	}
	runner := CodexRunner{Executable: script, OpenRouterURL: upstream.URL}
	if result, err := runner.Run(ctx, job, events, state); err != nil || result != "done" {
		t.Fatalf("Run() = %q, %v", result, err)
	}
	if !slices.Equal(models, []string{"vendor/exact-model:free"}) {
		t.Fatalf("model events = %q, want the routed model once", models)
	}
}
