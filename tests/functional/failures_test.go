package functional_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderFailuresReleaseQueueAndRemainRetryable(t *testing.T) {
	for _, failure := range []struct {
		name, body, diagnostic string
		status                 int
	}{
		{"authentication", `{"error":{"message":"fixture account denied"}}`, "fixture account denied", http.StatusUnauthorized},
		{"rate limit", `{"error":{"message":"fixture rate limit"}}`, "fixture rate limit", http.StatusTooManyRequests},
		{"outage", `{"error":{"message":"fixture unavailable"}}`, "fixture unavailable", http.StatusServiceUnavailable},
		{"stream error", "data: {\"error\":{\"message\":\"fixture stream failure\",\"code\":502}}\n\n", "fixture stream failure", http.StatusOK},
		{"truncated stream", "data: {\"choices\":[{\"delta\":{\"content\":\"unfinished answer\"}}]}\n\n", "completion", http.StatusOK},
		{"malformed stream", "data: {broken JSON}\n\n", "decode", http.StatusOK},
	} {
		t.Run(failure.name, func(t *testing.T) {
			f := newFixture(t)
			started, release := make(chan struct{}), make(chan struct{})
			var count atomic.Int32
			f.respond = func(w http.ResponseWriter, r *http.Request, _ completionRequest) {
				switch count.Add(1) {
				case 1:
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(failure.status)
					fmt.Fprint(w, failure.body)
				case 2:
					sendCompletion(w, completion{text: "Next job finished."})
				case 3:
					sendCompletion(w, completion{text: "Provider recovered."})
				default:
					t.Error("unexpected model request after failure")
					w.WriteHeader(http.StatusBadRequest)
				}
			}
			f.start()
			f.ok("job", "fail at provider boundary", "-n", "broken")
			f.ok("run", "--detach", "broken")
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("provider request did not start")
			}
			f.ok("job", "continue after provider failure", "-n", "next")
			f.ok("run", "--detach", "next")
			if d := f.view("next"); d.Job.Status != "queued" {
				t.Fatalf("next job did not queue: %+v", d.Job)
			}
			close(release)
			d := f.waitStatus("broken", "failed")
			requireContains(t, d.Job.Error, failure.diagnostic)
			if d.Job.Result != "" {
				t.Fatalf("provider failure saved a successful result: %+v", d.Job)
			}
			f.waitStatus("next", "completed")
			if r := f.command("", "attach", "broken"); r.code == 0 || r.stderr == "" {
				t.Fatalf("failed job inspection reported success: %+v", r)
			}
			f.stop()
			f.start()
			if d := f.view("broken"); d.Job.Status != "failed" || d.Job.Error == "" {
				t.Fatalf("provider failure lost after restart: %+v", d.Job)
			}
			requireContains(t, f.ok("retry", "broken"), "Provider recovered.")
			d = f.waitStatus("broken", "completed")
			if d.Job.Error != "" || d.Job.Result != "Provider recovered." || count.Load() != 3 {
				t.Fatalf("retry did not recover cleanly: %+v, requests=%d", d.Job, count.Load())
			}
		})
	}
}

func TestRejectedJobActionsPreserveHistoryAndQueuedCancellationPreventsDispatch(t *testing.T) {
	f := newFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	f.respond = func(w http.ResponseWriter, r *http.Request, _ completionRequest) {
		if count.Add(1) != 1 {
			t.Error("a rejected action or cancelled queued job dispatched work")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		close(started)
		select {
		case <-release:
			sendCompletion(w, completion{text: "Original run finished."})
		case <-r.Context().Done():
		}
	}
	f.start()
	f.ok("job", "original task", "-n", "active")
	f.ok("run", "--detach", "active")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("active request did not start")
	}
	f.ok("job", "never dispatch this task", "-n", "queued")
	f.ok("run", "--detach", "queued")
	for _, id := range []string{"active", "queued"} {
		before := f.view(id)
		for _, args := range [][]string{{"run", "--detach", id}, {"retry", id}, {"respond", id, "unsolicited response"}} {
			if r := f.command("", args...); r.code == 0 || r.stderr == "" {
				t.Fatalf("invalid action accepted: %v: %+v", args, r)
			}
		}
		if status, body := f.api(http.MethodDelete, "/v1/jobs/"+id, ""); status != http.StatusBadRequest {
			t.Fatalf("active/queued archive accepted: %d: %s", status, body)
		}
		if after := f.view(id); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected actions mutated %s\nbefore: %+v\nafter: %+v", id, before, after)
		}
	}
	f.ok("cancel", "queued")
	close(release)
	f.waitStatus("active", "completed")
	f.stop()
	f.start()
	if d := f.view("queued"); d.Job.Status != "cancelled" || len(d.Transcript) != 0 || len(d.ToolExecutions) != 0 {
		t.Fatalf("queued cancellation did not survive restart: %+v", d)
	}
	if status, body := f.api(http.MethodDelete, "/v1/jobs/active", ""); status != http.StatusNoContent {
		t.Fatalf("completed job archive failed: %d: %s", status, body)
	}
	before := f.view("active")
	for _, args := range [][]string{{"run", "--detach", "active"}, {"retry", "active"}, {"respond", "active", "follow-up"}} {
		if r := f.command("", args...); r.code == 0 || r.stderr == "" {
			t.Fatalf("archived job accepted action: %v: %+v", args, r)
		}
	}
	if after := f.view("active"); !reflect.DeepEqual(before, after) {
		t.Fatalf("archived report changed after rejected actions: %+v", after)
	}
	if count.Load() != 1 {
		t.Fatalf("unexpected dispatch: requests=%d", count.Load())
	}
}

func TestSavedSkillsReachAgentAndLoadInstructionsOnDemand(t *testing.T) {
	f := newFixture(t)
	directory := filepath.Join(f.config, "skills", "review")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	const body = "Before finishing, check compatibility with saved job history."
	skill := "---\nname: review-contracts\ndescription: Review CLI compatibility\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	f.script(
		func(r completionRequest) completion {
			var system string
			for _, m := range r.Messages {
				if m.Role == "system" {
					system += m.Content
				}
			}
			requireContains(t, system, "review-contracts", "Review CLI compatibility", "read_skill")
			if strings.Contains(system, body) {
				t.Error("full skill instructions were loaded before the agent requested them")
			}
			return completion{calls: []toolCall{call("missing-skill", "read_skill", `{"name":"missing"}`)}}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "missing-skill", `"success":false`)
			return completion{calls: []toolCall{call("read-skill", "read_skill", `{"name":"REVIEW-CONTRACTS"}`)}}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "read-skill", `"success":true`)
			requireToolResult(t, r, "read-skill", body)
			return completion{text: "Reviewed compatibility using the saved skill."}
		},
	)
	f.start()
	f.ok("job", "review this CLI", "-n", "skill review")
	requireContains(t, f.ok("run", "skill review"), "Reviewed compatibility using the saved skill.")
	f.waitStatus("skill review", "completed")
	f.stop()
	f.start()
	d := f.view("skill review")
	var transcript string
	for _, m := range d.Transcript {
		transcript += m.Content
	}
	requireContains(t, transcript, body, "Reviewed compatibility using the saved skill.")
	if len(d.ToolExecutions) != 2 {
		t.Fatalf("skill tool history lost after restart: %+v", d.ToolExecutions)
	}
}
