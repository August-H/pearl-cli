package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type serialTestRunner struct {
	mu        sync.Mutex
	active    int
	maxActive int
}

func (r *serialTestRunner) Run(
	ctx context.Context,
	job store.Job,
	events openrouter_request.EventSink,
	_ openrouter_request.CheckpointStore,
) (string, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()
	if err := events(openrouter_request.AgentEvent{Type: "answer", Data: job.Prompt}); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(30 * time.Millisecond):
	}
	return "done: " + job.Prompt, nil
}

func unixHTTPClient(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
}

func waitForDaemon(t *testing.T, client *http.Client) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get("http://pearl/v1/status")
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
}

func submitTestJob(t *testing.T, client *http.Client, prompt, workspace string) store.Job {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{
		"prompt": prompt, "workspace_root": workspace,
	})
	response, err := client.Post("http://pearl/v1/jobs", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("submit status=%s body=%s", response.Status, body)
	}
	var job store.Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	return job
}

func runTestJob(t *testing.T, client *http.Client, id string) store.Job {
	t.Helper()
	response, err := client.Post(
		"http://pearl/v1/jobs/"+id+"/run",
		"application/json",
		bytes.NewReader([]byte(`{}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("run status=%s body=%s", response.Status, body)
	}
	var result jobActionResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result.Job
}

func getTestJob(t *testing.T, client *http.Client, id string) store.Job {
	t.Helper()
	response, err := client.Get("http://pearl/v1/jobs/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var job store.Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestServerProcessesQueuedJobsWithOneAgent(t *testing.T) {
	runtimeDirectory, err := os.MkdirTemp("/tmp", "pearl-daemon-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	paths := pearlpaths.Paths{
		Directory: runtimeDirectory,
		Database:  filepath.Join(runtimeDirectory, "pearl.db"),
		Socket:    filepath.Join(runtimeDirectory, "pearl.sock"),
		Log:       filepath.Join(runtimeDirectory, "pearl.log"),
	}
	runner := &serialTestRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, paths, runner) }()
	client := unixHTTPClient(paths.Socket)
	waitForDaemon(t, client)

	workspace := t.TempDir()
	first := submitTestJob(t, client, "first", workspace)
	second := submitTestJob(t, client, "second", workspace)
	if first.Status != store.JobPending || second.Status != store.JobPending {
		t.Fatalf("new jobs are not pending: first=%#v second=%#v", first, second)
	}
	first = runTestJob(t, client, first.ID)
	second = runTestJob(t, client, second.ID)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		first = getTestJob(t, client, first.ID)
		second = getTestJob(t, client, second.ID)
		if first.Terminal() && second.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.Status != store.JobCompleted || second.Status != store.JobCompleted {
		t.Fatalf("jobs did not complete: first=%#v second=%#v", first, second)
	}
	runner.mu.Lock()
	maxActive := runner.maxActive
	runner.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("max active agents = %d, want 1", maxActive)
	}

	response, err := client.Get(fmt.Sprintf("http://pearl/v1/jobs/%s/events", first.ID))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !bytes.Contains(body, []byte(`"type":"answer"`)) || !bytes.Contains(body, []byte(`"data":"first"`)) {
		t.Fatalf("event stream = %s", body)
	}

	detailStore, err := store.Open(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	transcript := []byte(`[{"role":"user","content":"first"},{"role":"assistant","content":"done"}]`)
	if err := detailStore.SaveTranscript(ctx, first.ID, transcript); err != nil {
		t.Fatal(err)
	}
	if err := detailStore.SaveToolResult(
		ctx,
		first.ID,
		"call-1",
		"write_to_file",
		`{"relative_path":"README.md","content":"updated"}`,
		[]byte(`{"success":true,"result":"written"}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := detailStore.Close(); err != nil {
		t.Fatal(err)
	}
	response, err = client.Get(fmt.Sprintf("http://pearl/v1/jobs/%s/details", first.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var details jobDetailsResponse
	if err := json.NewDecoder(response.Body).Decode(&details); err != nil {
		t.Fatal(err)
	}
	if details.Job.ID != first.ID || string(details.Transcript) != string(transcript) ||
		len(details.ToolExecutions) != 1 ||
		details.ToolExecutions[0].ToolName != "write_to_file" {
		t.Fatalf("job details = %#v", details)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
