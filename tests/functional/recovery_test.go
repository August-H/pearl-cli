package functional_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A viewer and a daemon must be separate OS processes to test losing either
// independently. The buffers allow observing streamed output without a race.
type processOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *processOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

type childProcess struct {
	cmd            *exec.Cmd
	stdout, stderr processOutput
	done           chan struct{}
	err            error
}

func (f *fixture) startChild(executable string, args []string, extraEnv ...string) *childProcess {
	f.t.Helper()
	p := &childProcess{cmd: exec.Command(executable, args...), done: make(chan struct{})}
	p.cmd.Dir = f.workspace
	p.cmd.Env = append(append([]string(nil), f.env...), extraEnv...)
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	f.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			p.wait(f.t)
		}
		// A deliberately killed process cannot report the race detector's exit
		// status, so retain any report it emitted before termination.
		if output := p.stderr.String(); strings.Contains(output, "WARNING: DATA RACE") {
			f.t.Errorf("child process reported a data race:\n%s", output)
		}
	})
	return p
}

func (p *childProcess) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(10 * time.Second):
		t.Fatalf("process did not exit\nstdout: %s\nstderr: %s", p.stdout.String(), p.stderr.String())
		return nil
	}
}

func TestKilledDaemonRecoversCheckpointDiffAndQueue(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.workspace, "once.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	var count atomic.Int32
	f.respond = func(w http.ResponseWriter, r *http.Request, request completionRequest) {
		switch count.Add(1) {
		case 1:
			sendCompletion(w, completion{calls: []toolCall{call("edit-once", "apply_patch", `{"relative_path":"once.txt","old_text":"before","new_text":"after"}`)}})
		case 2:
			requireToolResult(t, request, "edit-once", `"success":true`)
			close(blocked)
			select {
			case <-r.Context().Done():
			case <-time.After(15 * time.Second):
				t.Error("crashed daemon did not disconnect from provider")
			}
		case 3:
			sendCompletion(w, completion{text: "Queued work survived the crash."})
		case 4:
			requireToolResult(t, request, "edit-once", `"success":true`)
			sendCompletion(w, completion{text: "Interrupted work resumed."})
		default:
			t.Error("crash recovery made an unexpected model request")
			w.WriteHeader(http.StatusBadRequest)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process := f.startChild(executable, nil, "PEARL_FUNCTIONAL_DAEMON_ENDPOINT="+f.providerURL)
	f.eventually(func() bool {
		select {
		case <-process.done:
			t.Fatalf("daemon exited before startup: %v\n%s", process.err, process.stderr.String())
		default:
		}
		response, err := f.client.Get("http://pearl/v1/status")
		if err != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, "child daemon to start")
	f.ok("job", "edit once, then finish", "-n", "interrupted")
	f.ok("run", "--detach", "interrupted")
	select {
	case <-blocked:
	case <-time.After(8 * time.Second):
		t.Fatal("job did not reach its saved tool checkpoint")
	}
	f.ok("job", "work behind interrupted job", "-n", "queued")
	f.ok("run", "--detach", "queued")
	if d := f.view("queued"); d.Job.Status != "queued" {
		t.Fatalf("second job did not wait: %+v", d.Job)
	}
	// Kill skips daemon shutdown, leaving a real running job and open snapshot.
	if err := process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.wait(t); err == nil {
		t.Fatal("killed daemon unexpectedly exited successfully")
	}
	f.start()
	d := f.view("interrupted")
	if d.Job.Status != "interrupted" || d.Job.Error == "" || len(d.ToolExecutions) != 1 {
		t.Fatalf("crash lost checkpoint or did not mark interruption: %+v", d)
	}
	if len(d.ChangeRuns) != 1 || len(d.ChangeRuns[0].Report.Changes) != 1 {
		t.Fatalf("crash lost the open change report: %+v", d.ChangeRuns)
	}
	change := d.ChangeRuns[0].Report.Changes[0]
	if change.Path != "once.txt" {
		t.Fatalf("unexpected recovered change: %+v", change)
	}
	requireContains(t, change.Diff, "-before", "+after")
	f.waitStatus("queued", "completed")
	requireContains(t, f.ok("retry", "interrupted"), "Interrupted work resumed.")
	d = f.waitStatus("interrupted", "completed")
	if len(d.ToolExecutions) != 1 || len(d.ChangeRuns) != 2 || len(d.ChangeRuns[1].Report.Changes) != 0 {
		t.Fatalf("retry replayed completed work or lost reports: %+v", d)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("completed edit lost or replayed: %q, %v", data, err)
	}
	if count.Load() != 4 {
		t.Fatalf("model requests = %d, want 4", count.Load())
	}
}

func TestDisconnectingViewerKeepsJobRunningAndAllowsReattach(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	var count atomic.Int32
	f.respond = func(w http.ResponseWriter, r *http.Request, _ completionRequest) {
		if count.Add(1) != 1 {
			t.Error("reattaching started another model request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"First streamed fragment.\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			sendCompletion(w, completion{text: " Final streamed fragment."})
		case <-r.Context().Done():
		}
	}
	f.start()
	f.ok("job", "keep working after I leave", "-n", "detached")
	viewer := f.startChild(pearlBinary, []string{"run", "detached"})
	f.eventually(func() bool {
		return strings.Contains(viewer.stdout.String(), "First streamed fragment.")
	}, "viewer to receive live output")
	if err := viewer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	viewer.wait(t)
	if d := f.view("detached"); d.Job.Status != "running" {
		t.Fatalf("losing the viewer stopped the job: %+v", d.Job)
	}
	reattached := f.startChild(pearlBinary, []string{"attach", "detached"})
	f.eventually(func() bool {
		return strings.Contains(reattached.stdout.String(), "First streamed fragment.")
	}, "new viewer to replay persisted output")
	close(release)
	if err := reattached.wait(t); err != nil || reattached.stderr.String() != "" {
		t.Fatalf("reattached viewer failed: %v\n%s", err, reattached.stderr.String())
	}
	output := reattached.stdout.String()
	for _, fragment := range []string{"First streamed fragment.", "Final streamed fragment."} {
		if strings.Count(output, fragment) != 1 {
			t.Errorf("reattached viewer lost or repeated %q: %s", fragment, output)
		}
	}
	d := f.waitStatus("detached", "completed")
	if d.Job.Result != "First streamed fragment. Final streamed fragment." || count.Load() != 1 {
		t.Fatalf("disconnection changed the result or reran the job: %+v, requests=%d", d.Job, count.Load())
	}
}
