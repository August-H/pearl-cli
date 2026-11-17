package functional_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestHelpAndVersionWorkWithoutStartingDaemon(t *testing.T) {
	f := newFixture(t)
	for _, command := range []string{
		"", "configure", "job", "run", "folder", "skills", "auth", "provider", "model",
		"usage", "accounts", "codex", "claude", "antigravity", "jobs", "jobs view",
		"archive", "dashboard", "autonomous", "status", "version", "attach", "respond",
		"cancel", "retry", "schedule", "schedule add", "schedule list", "schedule remove",
		"daemon", "daemon run", "daemon start", "daemon stop", "daemon restart",
		"daemon status", "daemon install", "daemon uninstall", "update",
	} {
		t.Run(command, func(t *testing.T) {
			args := append(strings.Fields(command), "--help")
			requireContains(t, f.ok(args...), "Usage")
		})
	}
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		requireContains(t, f.ok(args...), "pearl version")
	}
	v := decode[map[string]string](t, f.ok("version", "--json"))
	for _, key := range []string{"version", "commit", "date"} {
		if v[key] == "" {
			t.Errorf("version JSON missing %s: %v", key, v)
		}
	}
	assertNoDaemonArtifacts(t, f)
}

func TestUsageErrorsHaveNonzeroExitAndDoNotStartDaemon(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"unknown-command"}, {"version", "extra"}, {"--workspace"},
		{"job"}, {"run"}, {"cancel"}, {"retry"}, {"respond", "id"}, {"attach"},
		{"jobs", "view"}, {"archive", "extra"}, {"configure", "extra"},
		{"folder", "create"}, {"folder", "remove"}, {"skills", "unknown"},
		{"model", "--bogus"}, {"model", "--free", "--set", "x"},
		{"auth", "use"}, {"provider", "unknown"}, {"usage", "unknown"}, {"accounts", "extra"},
		{"schedule"}, {"schedule", "add", "--every", "0s", "task"},
		{"schedule", "add", "--every", "invalid", "task"}, {"schedule", "remove"},
		{"daemon", "unknown"}, {"update", "--bogus"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := f.command("", args...)
			if r.code == 0 || r.stderr == "" {
				t.Fatalf("invalid args accepted: %v: %+v", args, r)
			}
		})
	}
	assertNoDaemonArtifacts(t, f)
}

func TestOfflineJSONQueriesReturnOneErrorOnStderr(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"status"}, {"daemon", "status"}, {"jobs"}, {"jobs", "view", "missing"},
		{"archive"}, {"schedule", "list"}, {"jobs", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := f.command("", append(args, "--json")...)
			if r.code == 0 || r.stdout != "" {
				t.Fatalf("offline JSON result: %+v", r)
			}
			failure := decode[map[string]string](t, r.stderr)
			if failure["error"] == "" {
				t.Fatalf("missing JSON error: %v", failure)
			}
		})
	}
	assertNoDaemonArtifacts(t, f)
}

func assertNoDaemonArtifacts(t *testing.T, f *fixture) {
	t.Helper()
	for _, path := range []string{filepath.Join(f.config, "pearl.db"), filepath.Join(f.config, "pearl.log"), filepath.Join(f.root, "p.sock")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("read-only/invalid command created %s: %v", path, err)
		}
	}
}

func TestConfigureAndModelSelectionPreserveUserPolicy(t *testing.T) {
	f := newFixture(t)
	f.writeSettings(`{"provider":"openrouter","model":"before/model","max_file_bytes":12345,"outside_access":"deny","allowed_commands":["go"],"approved_workspace_roots":["/example"]}`)
	r := f.command("functional-key\n1\n", "configure")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("configure: %+v", r)
	}
	assertSettings := func(wantModel string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(f.config, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		s := decode[map[string]any](t, string(data))
		if s["model"] != wantModel || s["provider"] != "openrouter" || s["outside_access"] != "deny" || s["max_file_bytes"] != float64(12345) {
			t.Fatalf("model/configure changed policy: %s", data)
		}
		if !reflect.DeepEqual(s["allowed_commands"], []any{"go"}) || !reflect.DeepEqual(s["approved_workspace_roots"], []any{"/example"}) {
			t.Fatalf("lost policy: %s", data)
		}
	}
	assertSettings("openrouter/free")
	key, err := os.ReadFile(filepath.Join(f.config, ".env"))
	if err != nil || !strings.Contains(string(key), "functional-key") {
		t.Fatalf("key not saved: %v", err)
	}
	f.ok("model", "--set", "custom/refactor-model")
	assertSettings("custom/refactor-model")
	f.ok("model", "--free")
	assertSettings("openrouter/free")
	assertNoDaemonArtifacts(t, f)
}

func TestSkillsFilesAreDiscoverableWithoutDaemon(t *testing.T) {
	f := newFixture(t)
	path := strings.TrimSpace(f.ok("skills", "path"))
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, f.config+string(os.PathSeparator)) {
		t.Fatalf("skills path outside config: %s", path)
	}
	if err := os.WriteFile(filepath.Join(path, "review.md"), []byte("---\nname: code-review\ndescription: Review changes carefully\n---\nRead the diff."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "ship"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "ship", "SKILL.md"), []byte("Run checks before shipping.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireContains(t, f.ok("skills", "list"), "code-review", "Review changes carefully", "ship", "Run checks before shipping.")
	assertNoDaemonArtifacts(t, f)
}

func TestJobCreationWorkspaceScopingAndValidation(t *testing.T) {
	f := newFixture(t)
	f.start()
	nested, other := filepath.Join(f.workspace, "nested"), filepath.Join(f.root, "other")
	for _, path := range []string{nested, other} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prompt := "Keep quotes \"intact\" and Unicode café\nsecond line"
	f.ok("job", prompt, "-n", "root job")
	f.ok("job", "nested task", "-n", "nested", "--workspace", nested)
	f.ok("--workspace", other, "job", "other task", "-n", "other")
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--json")), "root job", "nested")
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--all", "--json")), "root job", "nested", "other")
	assertIDs(t, decode[[]job](t, f.ok("--workspace", other, "jobs", "--json")), "other")
	d := f.view("root job")
	if d.Job.Prompt != prompt || d.Job.Status != "pending" || len(d.Transcript) != 0 {
		t.Fatalf("created job: %+v", d)
	}
	for _, args := range [][]string{
		{"job", "duplicate", "-n", "root job"}, {"job", "bad", "-n", "-flag"},
		{"job", "bad", "-n", strings.Repeat("x", 21)}, {"job", "bad", "--workspace", filepath.Join(f.root, "missing")},
	} {
		if r := f.command("", args...); r.code == 0 {
			t.Fatalf("invalid submission accepted: %v", args)
		}
	}
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--all", "--json")), "root job", "nested", "other")
	f.ok("cancel", "root job")
	if d = f.view("root job"); d.Job.Status != "cancelled" || d.Job.Prompt != prompt {
		t.Fatalf("cancelled job: %+v", d.Job)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("creating, listing, or cancelling pending jobs called the model")
	}
}

func TestFoldersMoveJobsAndRemovalKeepsThem(t *testing.T) {
	f := newFixture(t)
	f.start()
	other := filepath.Join(f.root, "moved")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	f.ok("folder", "create", "my project")
	f.ok("job", "folder task", "-n", "folder job", "--folder", "my project")
	requireContains(t, f.ok("folder", "list"), "my project", "1")
	f.ok("folder", "edit", "my project", "--workspace", other)
	d := f.view("folder job")
	if !sameDirectory(d.Job.WorkspaceRoot, other) || d.Job.Folder != "my project" {
		t.Fatalf("folder didn't move job: %+v", d.Job)
	}
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--json")))
	assertIDs(t, decode[[]job](t, f.ok("--workspace", other, "jobs", "--json")), "folder job")
	f.ok("folder", "remove", "my project")
	d = f.view("folder job")
	if d.Job.Folder != "" || d.Job.Status != "pending" || !sameDirectory(d.Job.WorkspaceRoot, other) {
		t.Fatalf("folder removal lost/changed job: %+v", d.Job)
	}
}

func TestSchedulesAreScopedDurableAndRemovable(t *testing.T) {
	f := newFixture(t)
	f.start()
	other := filepath.Join(f.root, "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	f.ok("schedule", "add", "--every", "24h", "--name", "daily", "check the repo")
	f.ok("schedule", "add", "--every", "48h", "--workspace", other, "other task")
	schedules := decode[[]schedule](t, f.ok("schedule", "list", "--json"))
	if len(schedules) != 1 || schedules[0].Name != "daily" || schedules[0].Prompt != "check the repo" || schedules[0].IntervalSeconds != 86400 {
		t.Fatalf("schedules: %+v", schedules)
	}
	id := schedules[0].ID
	f.stop()
	f.start()
	schedules = decode[[]schedule](t, f.ok("schedule", "list", "--all", "--json"))
	if len(schedules) != 2 {
		t.Fatalf("schedules lost on restart: %+v", schedules)
	}
	f.ok("schedule", "remove", id)
	if left := decode[[]schedule](t, f.ok("schedule", "list", "--json")); len(left) != 0 {
		t.Fatalf("schedule not removed: %+v", left)
	}
	assertIDs(t, decode[[]job](t, f.ok("jobs", "--all", "--json")))
}

func TestDaemonRestartKeepsPendingAndCancelledJobs(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.ok("job", "keep this prompt", "-n", "pending")
	f.ok("job", "cancel this", "-n", "cancelled")
	f.ok("cancel", "cancelled")
	f.ok("daemon", "stop")
	f.stop()
	f.start()
	if d := f.view("pending"); d.Job.Status != "pending" || d.Job.Prompt != "keep this prompt" {
		t.Fatalf("pending job changed: %+v", d.Job)
	}
	if d := f.view("cancelled"); d.Job.Status != "cancelled" {
		t.Fatalf("cancelled job changed: %+v", d.Job)
	}
	for _, args := range [][]string{{"status", "--json"}, {"daemon", "status", "--json"}} {
		s := decode[map[string]json.RawMessage](t, f.ok(args...))
		if len(s) == 0 || s["resources"] == nil {
			t.Fatalf("missing status/resource information: %v", s)
		}
	}
}

func TestBackgroundDaemonStartsRestartsAndStopsThroughCLI(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(func() { f.command("", "daemon", "stop") })
	f.ok("daemon", "start")
	f.ok("daemon", "start")
	f.ok("job", "persist across a real process restart", "-n", "durable")
	f.ok("daemon", "restart")
	if d := f.view("durable"); d.Job.Status != "pending" || d.Job.Prompt != "persist across a real process restart" {
		t.Fatalf("restart lost job: %+v", d.Job)
	}
	f.ok("daemon", "stop")
	f.eventually(func() bool {
		r, err := f.client.Get("http://pearl/v1/status")
		if err != nil {
			return true
		}
		r.Body.Close()
		return false
	}, "background daemon to stop")
	f.ok("daemon", "stop")
}

func assertIDs(t *testing.T, jobs []job, want ...string) {
	t.Helper()
	got := make([]string, 0, len(jobs))
	for _, j := range jobs {
		got = append(got, j.ID)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("job IDs = %v, want %v", got, want)
	}
}

func sameDirectory(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && resolvedA == resolvedB
}
