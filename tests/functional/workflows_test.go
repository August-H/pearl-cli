package functional_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunUsesRealToolsAndSavesReviewableHistory(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.script(
		func(r completionRequest) completion {
			if r.Model != "fixture/model" {
				t.Errorf("selected model = %q", r.Model)
			}
			return completion{calls: []toolCall{
				call("read", "read_file_contents", `{"relative_path":"source.txt"}`),
				call("patch", "apply_patch", `{"relative_path":"source.txt","old_text":"before","new_text":"after"}`),
				call("mkdir", "create_directory", `{"relative_path":"docs"}`),
				call("create", "create_file", `{"relative_path":"docs/result.txt"}`),
				call("write", "write_to_file", `{"relative_path":"docs/result.txt","content":"written by the agent\n"}`),
				call("list", "list_files", `{"relative_path":"."}`),
				call("search", "search_files", `{"relative_path":".","query":"written by the agent"}`),
			}}
		},
		func(r completionRequest) completion {
			for _, id := range []string{"read", "patch", "mkdir", "create", "write", "list", "search"} {
				requireToolResult(t, r, id, `"success":true`)
			}
			requireToolResult(t, r, "read", "before")
			requireToolResult(t, r, "search", "docs/result.txt")
			return completion{text: "Created the report and updated the source."}
		},
	)
	f.start()
	f.ok("job", "edit files and report", "-n", "edit job")
	requireContains(t, f.ok("run", "edit job"), "Created the report and updated the source.")
	d := f.waitStatus("edit job", "completed")
	if d.Job.Result != "Created the report and updated the source." || len(d.ToolExecutions) != 7 {
		t.Fatalf("job report missing result/tools: %+v", d)
	}
	for path, want := range map[string]string{"source.txt": "after\n", "docs/result.txt": "written by the agent\n"} {
		data, err := os.ReadFile(filepath.Join(f.workspace, path))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, err=%v", path, data, err)
		}
	}
	if len(d.ChangeRuns) != 1 {
		t.Fatalf("expected a change report, got %+v", d.ChangeRuns)
	}
	changes := map[string]string{}
	for _, c := range d.ChangeRuns[0].Report.Changes {
		changes[c.Path] = c.Diff
	}
	requireContains(t, changes["source.txt"], "-before", "+after")
	requireContains(t, changes["docs/result.txt"], "+written by the agent")
	transcript, _ := json.Marshal(d.Transcript)
	requireContains(t, string(transcript), "edit files and report", "Created the report", "apply_patch", "written by the agent")
	if len(d.StatusEvents) == 0 {
		t.Fatal("job details omitted status history")
	}
	requireContains(t, f.ok("attach", "edit job"), "Created the report")
	f.stop()
	f.start()
	after := f.view("edit job")
	if len(after.ToolExecutions) != 7 || len(after.ChangeRuns) != 1 || len(after.Transcript) != len(d.Transcript) || after.Job.Result != d.Job.Result {
		t.Fatalf("report lost on restart: %+v", after)
	}
}

func TestWaitingJobYieldsQueueAndResumesAfterRestart(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.workspace, "once.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.script(
		func(completionRequest) completion {
			return completion{calls: []toolCall{call("edit-once", "apply_patch", `{"relative_path":"once.txt","old_text":"original","new_text":"changed"}`)}}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "edit-once", `"success":true`)
			return completion{calls: []toolCall{call("question", "request_user_input", `{"question":"Which color should I use?"}`)}}
		},
		func(completionRequest) completion { return completion{text: "Other queued work completed."} },
		func(r completionRequest) completion {
			requireToolResult(t, r, "edit-once", `"success":true`)
			requireToolResult(t, r, "question", "blue")
			return completion{text: "Continued with blue."}
		},
	)
	f.start()
	f.ok("job", "edit then ask", "-n", "waiting")
	f.ok("run", "--detach", "waiting")
	d := f.waitStatus("waiting", "waiting_input")
	if d.Job.Question != "Which color should I use?" {
		t.Fatalf("question not exposed: %+v", d.Job)
	}
	f.ok("job", "other work", "-n", "other")
	f.ok("run", "other")
	f.waitStatus("other", "completed")
	f.stop()
	f.start()
	if d = f.view("waiting"); d.Job.Status != "waiting_input" || d.Job.Question != "Which color should I use?" {
		t.Fatalf("pause lost on restart: %+v", d.Job)
	}
	requireContains(t, f.ok("respond", "waiting", "blue"), "Continued with blue.")
	d = f.waitStatus("waiting", "completed")
	if len(d.ToolExecutions) != 2 {
		t.Fatalf("completed tools replayed or response not saved: %+v", d.ToolExecutions)
	}
	data, err := os.ReadFile(filepath.Join(f.workspace, "once.txt"))
	if err != nil || string(data) != "changed\n" {
		t.Fatalf("edit lost/repeated: %q, %v", data, err)
	}
}

func TestIncompleteRunFailsAndRetryKeepsCompletedTools(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.workspace, "once.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.script(
		func(completionRequest) completion {
			return completion{calls: []toolCall{call("edit-once", "apply_patch", `{"relative_path":"once.txt","old_text":"original","new_text":"changed"}`)}}
		},
		func(completionRequest) completion {
			return completion{text: "partial attempt output", incomplete: true}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "edit-once", `"success":true`)
			return completion{text: "Retry finished successfully."}
		},
	)
	f.start()
	f.ok("job", "edit and finish", "-n", "retry job")
	r := f.command("", "run", "retry job")
	if r.code == 0 {
		t.Fatalf("incomplete stream reported success: %+v", r)
	}
	d := f.waitStatus("retry job", "failed")
	if d.Job.Error == "" || d.Job.Result != "" {
		t.Fatalf("failure not recorded: %+v", d.Job)
	}
	f.stop()
	f.start()
	output := f.ok("retry", "retry job")
	requireContains(t, output, "Retry finished successfully.")
	if strings.Contains(output, "partial attempt output") {
		t.Fatalf("retry replayed old output: %s", output)
	}
	d = f.waitStatus("retry job", "completed")
	if len(d.ToolExecutions) != 1 || len(d.ChangeRuns) != 2 {
		t.Fatalf("retry lost history or replayed tool: %+v", d)
	}
	if len(d.ChangeRuns[1].Report.Changes) != 0 {
		t.Fatalf("retry attributed earlier edit to itself: %+v", d.ChangeRuns[1])
	}
}

func TestFollowUpContinuesSavedConversation(t *testing.T) {
	f := newFixture(t)
	f.script(
		func(completionRequest) completion { return completion{text: "First answer."} },
		func(r completionRequest) completion {
			var history []string
			for _, m := range r.Messages {
				history = append(history, m.Role+":"+m.Content)
			}
			requireContains(t, strings.Join(history, "\n"), "user:original task", "assistant:First answer.", "user:include examples")
			return completion{text: "Follow-up answer with examples."}
		},
	)
	f.start()
	f.ok("job", "original task", "-n", "follow up")
	f.ok("run", "follow up")
	f.waitStatus("follow up", "completed")
	f.stop()
	f.start()
	requireContains(t, f.ok("respond", "follow up", "include examples"), "Follow-up answer with examples.")
	d := f.waitStatus("follow up", "completed")
	transcript, _ := json.Marshal(d.Transcript)
	requireContains(t, string(transcript), "original task", "First answer.", "include examples", "Follow-up answer with examples.")
}

func TestCancellationStopsActiveRequestAndReleasesQueue(t *testing.T) {
	f := newFixture(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	f.respond = func(w http.ResponseWriter, r *http.Request, _ completionRequest) {
		if count.Add(1) == 1 {
			close(started)
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-time.After(12 * time.Second):
				t.Error("model request wasn't cancelled")
			}
			return
		}
		sendCompletion(w, completion{text: "Queue continued."})
	}
	f.start()
	f.ok("job", "wait for cancellation", "-n", "cancel me")
	f.ok("run", "--detach", "cancel me")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model request didn't start")
	}
	f.ok("job", "next queued job", "-n", "next")
	f.ok("run", "--detach", "next")
	if d := f.view("next"); d.Job.Status != "queued" {
		t.Fatalf("ordinary jobs overlapped: %+v", d.Job)
	}
	f.ok("cancel", "cancel me")
	f.waitStatus("cancel me", "cancelled")
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation didn't reach the provider")
	}
	d := f.waitStatus("next", "completed")
	if d.Job.Result != "Queue continued." {
		t.Fatalf("queue failed to continue: %+v", d.Job)
	}
}

func TestArchiveHidesJobButPreservesItsReport(t *testing.T) {
	f := newFixture(t)
	f.start()
	id := "archive job"
	f.ok("job", "keep this history", "-n", id)
	f.ok("run", id)
	f.waitStatus(id, "completed")
	status, body := f.api(http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), "")
	if status != http.StatusNoContent {
		t.Fatalf("archive returned %d: %s", status, body)
	}
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--all", "--json")))
	assertIDs(t, decode[[]job](t, f.ok("archive", "--json")), id)
	f.stop()
	f.start()
	d := f.view(id)
	if d.Job.Result != "fixture finished" || len(d.Transcript) == 0 {
		t.Fatalf("archive lost history: %+v", d)
	}
	assertIDs(t, decode[[]job](t, f.ok("archive", "--all", "--json")), id)
}

func TestUnsafeToolRequestsCannotChangeOutsideFilesOrSecrets(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.root, "outside.txt")
	for _, path := range []string{outside, filepath.Join(f.workspace, ".env")} {
		if err := os.WriteFile(path, []byte("secret-canary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.script(
		func(completionRequest) completion {
			return completion{calls: []toolCall{
				call("escape", "write_to_file", `{"relative_path":"../outside.txt","content":"stolen"}`),
				call("secret", "read_file_contents", `{"relative_path":".env"}`),
				call("command", "run_command", `{"command":"go","args":["version"]}`),
			}}
		},
		func(r completionRequest) completion {
			for _, id := range []string{"escape", "secret", "command"} {
				requireToolResult(t, r, id, `"success":false`)
			}
			encoded, _ := json.Marshal(r.Messages)
			if strings.Contains(string(encoded), "secret-canary") {
				t.Error("secret reached the model")
			}
			return completion{text: "Access was refused."}
		},
	)
	f.start()
	f.ok("job", "try unsafe tools", "-n", "safety")
	f.ok("run", "safety")
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "secret-canary" {
		t.Fatalf("outside file changed: %q, %v", data, err)
	}
}

func TestAPIRejectsInvalidRequestsWithoutMutatingJobs(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.ok("job", "stay pending", "-n", "untouched")
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/v1/jobs", "{", 400},
		{"POST", "/v1/jobs", `{"prompt":"x","workspace_root":"missing"}`, 400},
		{"POST", "/v1/schedules", "{", 400},
		{"POST", "/v1/folders", "{", 400},
		{"POST", "/v1/autonomous", `{}`, 400},
		{"PUT", "/v1/jobs", "", 405},
		{"POST", "/v1/status", "", 405},
		{"GET", "/v1/jobs/untouched/run", "", 405},
		{"GET", "/v1/jobs/untouched/cancel", "", 405},
		{"POST", "/v1/jobs/untouched/respond", `{}`, 400},
		{"GET", "/v1/jobs/missing", "", 404},
		{"GET", "/v1/jobs/untouched/unknown", "", 404},
	} {
		t.Run(tc.method+" "+tc.path+" "+tc.body, func(t *testing.T) {
			status, body := f.api(tc.method, tc.path, tc.body)
			if status != tc.status {
				t.Fatalf("status = %d, want %d: %s", status, tc.status, body)
			}
			// Unknown routes use HTTP's plain-text 404; handled API errors use JSON.
			if status != http.StatusNotFound {
				failure := decode[map[string]string](t, body)
				if failure["error"] == "" {
					t.Fatalf("error missing: %s", body)
				}
			}
		})
	}
	if d := f.view("untouched"); d.Job.Status != "pending" {
		t.Fatalf("invalid request mutated job: %+v", d.Job)
	}
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--all", "--json")), "untouched")
}

func TestCommandExecutionTracksTestsAndInvalidatesAfterCodeEdits(t *testing.T) {
	f := newFixture(t)
	f.writeSettings(`{"provider":"openrouter","model":"fixture/model","harness":"pearl","max_depth":16,"max_job_seconds":60,"allowed_commands":["go"],"allow_unsandboxed_commands":true}`)
	t.Setenv("AWS_ACCESS_KEY_ID", "credential-canary")
	for path, content := range map[string]string{
		"go.mod":      "module example.test/fixture\n\ngo 1.26\n",
		"add.go":      "package fixture\nfunc add(a, b int) int { return a + b }\n",
		"add_test.go": "package fixture\nimport (\"testing\"; \"os\")\nfunc TestAdd(t *testing.T) { if add(1, 2) != 3 { t.Fatal(\"wrong sum\") }; if os.Getenv(\"AWS_ACCESS_KEY_ID\") != \"\" { t.Fatal(\"credential leaked\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(f.workspace, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.script(
		func(completionRequest) completion {
			return completion{calls: []toolCall{call("test-pass", "run_command", `{"command":"go","args":["test","./..."],"timeout_seconds":30}`)}}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "test-pass", `"exit_code":0`)
			if d := f.view("verify"); d.Job.Verification != "passed" {
				t.Errorf("successful test command not recorded: %+v", d.Job)
			}
			return completion{calls: []toolCall{call("break-code", "apply_patch", `{"relative_path":"add.go","old_text":"a + b","new_text":"a - b"}`)}}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "break-code", `"success":true`)
			if d := f.view("verify"); d.Job.Verification != "not_run" {
				t.Errorf("code edit kept stale verification: %+v", d.Job)
			}
			return completion{calls: []toolCall{call("test-fail", "run_command", `{"command":"go","args":["test","./..."],"timeout_seconds":30}`)}}
		},
		func(r completionRequest) completion {
			requireToolResult(t, r, "test-fail", `"exit_code":1`)
			return completion{text: "The model claims tests passed."}
		},
	)
	f.start()
	f.ok("job", "run tests, edit code, then retest", "-n", "verify")
	f.ok("run", "verify")
	d := f.waitStatus("verify", "completed")
	if d.Job.Verification != "failed" || d.Job.VerificationCommand != "go test ./..." {
		t.Fatalf("model prose overrode actual test result: %+v", d.Job)
	}
}

func TestAutonomousSessionCreatesJobReviewsReportAndFinishes(t *testing.T) {
	f := newFixture(t)
	f.script(
		func(completionRequest) completion {
			return completion{text: "I will assign the work.\nDECISION\n" + `{"plan":"inspect and report","jobs":[{"model":"fixture/model","prompt":"inspect the fixture"}]}`}
		},
		func(completionRequest) completion { return completion{text: "The fixture is ready."} },
		func(r completionRequest) completion {
			encoded, _ := json.Marshal(r.Messages)
			requireContains(t, string(encoded), "The fixture is ready.", "completed")
			return completion{text: `{"finish":"Reviewed the report. Goal met."}`}
		},
	)
	f.start()
	body, _ := json.Marshal(map[string]any{
		"goal": "inspect the fixture", "workspace_root": f.workspace,
		"orchestrator":   map[string]string{"provider": "openrouter", "id": "fixture/model"},
		"allowed_models": []any{map[string]string{"provider": "openrouter", "id": "fixture/model"}},
		"max_subagents":  1, "max_jobs": 2,
	})
	status, response := f.api("POST", "/v1/autonomous", string(body))
	if status != http.StatusAccepted {
		t.Fatalf("start session = %d: %s", status, response)
	}
	session := decode[struct {
		ID string `json:"id"`
	}](t, response)
	type sessionDetails struct {
		Session struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"session"`
		Jobs  []job             `json:"jobs"`
		Turns []json.RawMessage `json:"turns"`
	}
	var d sessionDetails
	f.eventually(func() bool {
		status, response := f.api("GET", "/v1/autonomous/"+session.ID, "")
		if status != http.StatusOK {
			return false
		}
		d = decode[sessionDetails](t, response)
		return d.Session.Status == "completed"
	}, "autonomous session to finish")
	if len(d.Jobs) != 1 || d.Jobs[0].Status != "completed" || len(d.Turns) != 2 {
		t.Fatalf("session report: %+v", d)
	}
	requireContains(t, d.Session.Summary, "Goal met")
	requireContains(t, f.ok("jobs", "view", d.Jobs[0].ID, "--json"), "The fixture is ready.")
}

func TestUsageCapsStopDispatchAndSurviveRestart(t *testing.T) {
	for _, limit := range []struct{ name, settings string }{
		{"tokens", `"max_tokens":10`}, {"cost", `"max_cost_usd":0.5`},
	} {
		t.Run(limit.name, func(t *testing.T) {
			f := newFixture(t)
			f.writeSettings(`{"provider":"openrouter","model":"fixture/model","harness":"pearl",` + limit.settings + `}`)
			f.script(func(completionRequest) completion { return completion{text: "budget spent", tokens: 10, cost: 0.5} })
			f.start()
			f.ok("job", "spend the budget", "-n", "budget")
			f.ok("run", "--detach", "budget")
			d := f.waitStatus("budget", "failed")
			requireContains(t, d.Job.Error, "limit")
			f.ok("job", "wait for more budget", "-n", "blocked")
			f.ok("run", "--detach", "blocked")
			f.stop()
			f.start()
			if d := f.view("blocked"); d.Job.Status != "queued" {
				t.Fatalf("exhausted budget dispatched queued work: %+v", d.Job)
			}
			status := decode[struct {
				Resources struct {
					Usage struct {
						Tokens int64   `json:"tokens"`
						Cost   float64 `json:"cost_usd"`
					} `json:"usage"`
					Exhausted string `json:"exhausted"`
				} `json:"resources"`
			}](t, f.ok("status", "--json"))
			if status.Resources.Usage.Tokens != 10 || status.Resources.Usage.Cost != 0.5 || status.Resources.Exhausted == "" {
				t.Fatalf("budget accounting lost on restart: %+v", status)
			}
		})
	}
}

func TestDueScheduleRunsAutomaticallyAndSkipsOverlappingFirings(t *testing.T) {
	f := newFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	f.respond = func(w http.ResponseWriter, r *http.Request, _ completionRequest) {
		if count.Add(1) == 1 {
			close(started)
		} else {
			t.Error("schedule dispatched an overlapping run")
		}
		select {
		case <-release:
			sendCompletion(w, completion{text: "Scheduled work finished."})
		case <-r.Context().Done():
		case <-time.After(12 * time.Second):
			t.Error("scheduled run never released")
		}
	}
	f.start()
	f.ok("schedule", "add", "--every", "1s", "--name", "recurring", "scheduled prompt")
	schedules := decode[[]schedule](t, f.ok("schedule", "list", "--json"))
	if len(schedules) != 1 {
		t.Fatalf("schedule missing: %+v", schedules)
	}
	first := schedules[0]
	select {
	case <-started:
	case <-time.After(6 * time.Second):
		t.Fatal("due schedule did not run automatically")
	}
	// Let a second interval become due while its first run is still active.
	f.eventually(func() bool {
		current := decode[[]schedule](t, f.ok("schedule", "list", "--json"))
		return len(current) == 1 && !current[0].NextRunAt.Before(first.NextRunAt.Add(2*time.Second))
	}, "schedule cadence to advance while its job runs")
	jobs := decode[[]job](t, f.ok("jobs", "--all", "--json"))
	if len(jobs) != 1 || jobs[0].Status != "running" || jobs[0].Prompt != "scheduled prompt" {
		t.Fatalf("schedule duplicated or lost its run: %+v", jobs)
	}
	f.ok("schedule", "remove", first.ID)
	close(release)
	f.waitStatus(jobs[0].ID, "completed")
	if count.Load() != 1 {
		t.Fatalf("schedule made %d provider requests, want one", count.Load())
	}
}
