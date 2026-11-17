package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	// Keep CLI source changes in Go's test-cache dependency graph. Commands below
	// still execute the compiled binary instead of calling cli.Run directly.
	_ "github.com/August-H/pearl-cli/cli"
	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/pearlpaths"
)

var pearlBinary string

func TestMain(m *testing.M) {
	// Crash tests run the production daemon in a disposable process. Only the
	// provider transport differs, just as it does for in-process fixtures.
	if endpoint := os.Getenv("PEARL_FUNCTIONAL_DAEMON_ENDPOINT"); endpoint != "" {
		restore, err := redirectProvider(endpoint)
		if err == nil {
			defer restore()
			var paths pearlpaths.Paths
			paths, err = pearlpaths.Resolve()
			if err == nil {
				err = daemon.Run(context.Background(), paths, daemon.OpenRouterRunner{})
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	directory, err := os.MkdirTemp("", "pearl-functional-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pearlBinary = filepath.Join(directory, "pearl")
	if runtime.GOOS == "windows" {
		pearlBinary += ".exe"
	}
	buildArgs := []string{"build"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				buildArgs = append(buildArgs, "-race")
			}
		}
	}
	buildArgs = append(buildArgs, "-o", pearlBinary, "./cmd/pearl")
	build := exec.Command("go", buildArgs...)
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build functional-test CLI: %v\n%s", err, output)
		os.RemoveAll(directory)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(directory)
	os.Exit(code)
}

// These types describe the public JSON contract. Deliberately accept extra
// fields so adding metadata does not break refactors or older scripts.
type job struct {
	ID                  string `json:"id"`
	Prompt              string `json:"prompt"`
	Status              string `json:"status"`
	WorkspaceRoot       string `json:"workspace_root"`
	Folder              string `json:"folder"`
	Result              string `json:"result"`
	Error               string `json:"error"`
	Question            string `json:"question"`
	Verification        string `json:"verification"`
	VerificationCommand string `json:"verification_command"`
}

type details struct {
	Job            job       `json:"job"`
	Transcript     []message `json:"transcript"`
	ToolExecutions []struct {
		ToolName string `json:"tool_name"`
	} `json:"tool_executions"`
	StatusEvents []struct {
		Data string `json:"data"`
	} `json:"status_events"`
	ChangeRuns []struct {
		Report struct {
			Changes []struct {
				Path string `json:"path"`
				Diff string `json:"diff"`
			} `json:"changes"`
		} `json:"report"`
	} `json:"change_runs"`
}

type schedule struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Prompt          string    `json:"prompt"`
	WorkspaceRoot   string    `json:"workspace_root"`
	IntervalSeconds int64     `json:"interval_seconds"`
	NextRunAt       time.Time `json:"next_run_at"`
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func call(id, name, arguments string) toolCall {
	c := toolCall{ID: id, Type: "function"}
	c.Function.Name, c.Function.Arguments = name, arguments
	return c
}

type completionRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type completion struct {
	text       string
	calls      []toolCall
	incomplete bool
	tokens     int64
	cost       float64
}

type commandResult struct {
	stdout, stderr string
	code           int
}

type fixture struct {
	t                       *testing.T
	root, workspace, config string
	env                     []string
	client                  *http.Client
	stopDaemon              context.CancelFunc
	daemonDone              chan error
	mu                      sync.Mutex
	requests                []completionRequest
	respond                 func(http.ResponseWriter, *http.Request, completionRequest)
	providerURL             string
}

// Only the provider boundary is replaced. Queueing, checkpointing, tools,
// snapshots, SQLite, Unix-socket HTTP, command parsing, and exit codes are real.
// Tests stay serial because the agent's default HTTP transport is process-wide.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "pf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	f := &fixture{t: t, root: root, workspace: filepath.Join(root, "workspace"), config: filepath.Join(root, "config")}
	for _, path := range []string{f.workspace, f.config, filepath.Join(root, "home")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(root, "p.sock")
	t.Setenv("PEARL_CONFIG_DIR", f.config)
	t.Setenv("PEARL_SOCKET", socket)
	t.Setenv("OPENROUTER_API_KEY", "functional-dummy-key")
	f.writeSettings(`{"provider":"openrouter","model":"fixture/model","harness":"pearl","max_depth":16,"max_job_seconds":20}`)
	// Child CLIs never inherit provider credentials or the user's home/config.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TMP", "TEMP", "TMPDIR":
			f.env = append(f.env, entry)
		}
	}
	f.env = append(f.env, "PEARL_CONFIG_DIR="+f.config, "PEARL_SOCKET="+socket,
		"HOME="+filepath.Join(root, "home"), "USERPROFILE="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "home"), "APPDATA="+filepath.Join(root, "home"),
		"OPENROUTER_API_KEY=functional-dummy-key", "NO_COLOR=1", "TERM=dumb",
		"GORACE=atexit_sleep_ms=0")

	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}}
	f.client = &http.Client{Transport: transport, Timeout: 2 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	f.respond = func(w http.ResponseWriter, _ *http.Request, _ completionRequest) {
		sendCompletion(w, completion{text: "fixture finished"})
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		f.mu.Unlock()
		f.respond(w, r, request)
	}))
	t.Cleanup(provider.Close)
	f.providerURL = provider.URL
	restore, err := redirectProvider(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	t.Cleanup(f.stop)
	return f
}

func redirectProvider(endpointURL string) (func(), error) {
	endpoint, err := url.Parse(endpointURL)
	if err != nil {
		return nil, err
	}
	localTransport := &http.Transport{Proxy: nil}
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "openrouter.ai" || r.URL.Path != "/api/v1/chat/completions" {
			return nil, fmt.Errorf("functional test blocked unexpected outbound request to %s", r.URL.Redacted())
		}
		local := r.Clone(r.Context())
		local.URL.Scheme, local.URL.Host = endpoint.Scheme, endpoint.Host
		return localTransport.RoundTrip(local)
	})
	return func() { http.DefaultTransport = previous; localTransport.CloseIdleConnections() }, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sendCompletion(w http.ResponseWriter, c completion) {
	w.Header().Set("Content-Type", "text/event-stream")
	for i := range c.calls {
		c.calls[i].Index = i
	}
	delta := map[string]any{"content": c.text}
	finish := "stop"
	if len(c.calls) > 0 {
		delta["tool_calls"] = c.calls
		finish = "tool_calls"
	}
	if c.incomplete {
		finish = "length"
	}
	body, _ := json.Marshal(map[string]any{"model": "fixture/model", "choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"total_tokens": c.tokens, "cost": c.cost}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
}

func (f *fixture) script(steps ...func(completionRequest) completion) {
	f.t.Helper()
	index := 0
	f.respond = func(w http.ResponseWriter, _ *http.Request, r completionRequest) {
		// Model calls for ordinary jobs are serialized by the real daemon.
		if index >= len(steps) {
			f.t.Errorf("unexpected provider request %d: %+v", index+1, r.Messages)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		step := steps[index]
		index++
		sendCompletion(w, step(r))
	}
	f.t.Cleanup(func() {
		f.stop()
		if index != len(steps) {
			f.t.Errorf("provider received %d requests, want %d", index, len(steps))
		}
	})
}

func (f *fixture) writeSettings(settings string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.config, "settings.json"), []byte(settings), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) command(input string, args ...string) commandResult {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pearlBinary, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = f.workspace, f.env, strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := commandResult{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			result.code = exit.ExitCode()
		} else {
			f.t.Fatalf("execute %v: %v", args, err)
		}
	}
	if ctx.Err() != nil {
		f.t.Fatalf("command timed out: %v\nstdout: %s\nstderr: %s", args, result.stdout, result.stderr)
	}
	return result
}

func (f *fixture) ok(args ...string) string {
	f.t.Helper()
	r := f.command("", args...)
	if r.code != 0 || r.stderr != "" {
		f.t.Fatalf("pearl %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r.stdout
}

func decode[T any](t *testing.T, text string) T {
	t.Helper()
	var value T
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, text)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("expected exactly one JSON value, trailing data in %q", text)
	}
	return value
}

func (f *fixture) start() {
	f.t.Helper()
	paths, err := pearlpaths.Resolve()
	if err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.stopDaemon, f.daemonDone = cancel, make(chan error, 1)
	go func() { f.daemonDone <- daemon.Run(ctx, paths, daemon.OpenRouterRunner{}) }()
	f.eventually(func() bool {
		r, err := f.client.Get("http://pearl/v1/status")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == http.StatusOK
	}, "daemon to start")
}

func (f *fixture) stop() {
	f.t.Helper()
	if f.stopDaemon == nil {
		return
	}
	f.stopDaemon()
	select {
	case err := <-f.daemonDone:
		if err != nil {
			f.t.Errorf("stop daemon: %v", err)
		}
	case <-time.After(15 * time.Second):
		f.t.Error("daemon did not stop")
	}
	f.stopDaemon = nil
}

func (f *fixture) eventually(check func() bool, description string) {
	f.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s", description)
}

func (f *fixture) view(id string) details {
	f.t.Helper()
	return decode[details](f.t, f.ok("jobs", "view", id, "--json"))
}

func (f *fixture) waitStatus(id, status string) details {
	f.t.Helper()
	var d details
	f.eventually(func() bool { d = f.view(id); return d.Job.Status == status }, id+" to become "+status)
	return d
}

func (f *fixture) api(method, path, body string) (int, string) {
	f.t.Helper()
	r, err := http.NewRequest(method, "http://pearl"+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return response.StatusCode, string(data)
}

func requireContains(t *testing.T, text string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("missing %q in %s", fragment, text)
		}
	}
}

func requireToolResult(t *testing.T, r completionRequest, id, content string) {
	t.Helper()
	for _, m := range r.Messages {
		if m.Role == "tool" && m.ToolCallID == id {
			requireContains(t, m.Content, content)
			return
		}
	}
	t.Errorf("provider did not receive result for %q: %+v", id, r.Messages)
}
