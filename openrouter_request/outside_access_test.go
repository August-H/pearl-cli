package openrouter_request

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/workspacepath"
)

func outsideCall(id, name string, args any) agentToolCall {
	call := workspaceCall(name, args)
	call.ID = id
	return call
}

func TestFileToolsRefuseHardLinksToOutsideFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link counts aren't read on Windows")
	}
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	result := runAgentTool(workspaceCall("read_file_contents", map[string]any{"relative_path": "linked.txt"}), root, 4096)
	if result.Success || !strings.Contains(result.Error, "hard links") {
		t.Fatalf("hard link read = %+v", result)
	}
	search := runAgentTool(workspaceCall("search_files", map[string]any{"relative_path": ".", "query": "hunter2"}), root, 4096)
	if !search.Success || strings.Contains(mustJSON(t, search), "linked.txt") {
		t.Fatalf("search read a hard link: %+v", search)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOutsideAccessNeedsAReviewedGrant(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()
	outside := workspacepath.CanonicalPath(t.TempDir())
	target := filepath.Join(outside, "notes.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	var reviewed []outsideRequest
	allow := true
	access := newOutsideAccess("Update the notes in the sibling project", workspacepath.CanonicalPath(workspace), nil,
		func(_ context.Context, task string, request outsideRequest) (outsideDecision, error) {
			reviewed = append(reviewed, request)
			return outsideDecision{Allow: allow, Reason: "fits the task"}, nil
		})
	var events []string
	options := RunOptions{Events: func(event AgentEvent) error {
		if event.Type == "review" {
			events = append(events, event.Data)
		}
		return nil
	}}
	write := func() agentToolResult {
		return runFileTool(context.Background(), workspaceCall("write_to_file", map[string]any{
			"relative_path": target, "content": "new",
		}), workspace, 4096, access)
	}

	if result := write(); result.Success || !strings.Contains(result.Error, requestOutsideAccessTool) {
		t.Fatalf("ungranted write = %+v", result)
	}
	request := func(id, path, level string) agentToolResult {
		result, err := access.handle(context.Background(), outsideCall(id, requestOutsideAccessTool, map[string]any{
			"path": path, "access": level, "reason": "the task asks to update these notes",
		}), options)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if result := request("read-1", outside, "read"); !result.Success {
		t.Fatalf("read grant = %+v", result)
	}
	if result := runFileTool(context.Background(), workspaceCall("read_file_contents", map[string]any{"relative_path": target}), workspace, 4096, access); result.Result != "old" {
		t.Fatalf("granted read = %+v", result)
	}
	if result := write(); result.Success || !strings.Contains(result.Error, "reading only") {
		t.Fatalf("write with a read grant = %+v", result)
	}

	allow = false
	if result := request("write-1", outside, "write"); result.Success || !strings.Contains(result.Error, "Denied") {
		t.Fatalf("denied write grant = %+v", result)
	}
	allow = true
	granted := request("write-2", outside, "write")
	if !granted.Success {
		t.Fatalf("write grant = %+v", granted)
	}
	if result := write(); !result.Success {
		t.Fatalf("granted write = %+v", result)
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatalf("file = %q", data)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		before := len(reviewed)
		if result := request("ssh", filepath.Join(home, ".ssh"), "read"); result.Success {
			t.Fatalf("credentials granted: %+v", result)
		}
		if len(reviewed) != before {
			t.Fatal("a denied path reached the reviewer")
		}
	}
	if len(events) < 4 || !strings.HasPrefix(events[0], "Allowed read access to "+outside) {
		t.Fatalf("review events = %q", events)
	}

	content, _ := json.Marshal(granted)
	resumed := newOutsideAccess("", workspacepath.CanonicalPath(workspace), []agentMessage{
		{Role: "tool", Name: requestOutsideAccessTool, Content: string(content)},
	}, nil)
	if result := runFileTool(context.Background(), workspaceCall("write_to_file", map[string]any{
		"relative_path": target, "content": "resumed",
	}), workspace, 4096, resumed); !result.Success {
		t.Fatalf("grant was not restored from the transcript: %+v", result)
	}
}

func TestAbsolutePathsInsideTheWorkspaceStillWork(t *testing.T) {
	workspace := workspacepath.CanonicalPath(t.TempDir())
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := runFileTool(context.Background(), workspaceCall("read_file_contents", map[string]any{
		"relative_path": filepath.Join(workspace, "a.txt"),
	}), workspace, 4096, nil)
	if result.Result != "hello" {
		t.Fatalf("absolute workspace path = %+v", result)
	}
	escape := runFileTool(context.Background(), workspaceCall("read_file_contents", map[string]any{
		"relative_path": filepath.Join(workspace, "..", "elsewhere.txt"),
	}), workspace, 4096, nil)
	if escape.Success {
		t.Fatalf("escape accepted: %+v", escape)
	}
}

func TestOutsideAccessPolicyComesFromUserSettings(t *testing.T) {
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)
	if policy := outsideAccessPolicy(); policy != OutsideAccessReview {
		t.Fatalf("default policy = %q", policy)
	}
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"outside_access":"deny"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	access := newOutsideAccess("task", t.TempDir(), nil, func(context.Context, string, outsideRequest) (outsideDecision, error) {
		t.Fatal("reviewer called while outside access is off")
		return outsideDecision{}, nil
	})
	result, err := access.handle(context.Background(), outsideCall("1", requestOutsideAccessTool, map[string]any{
		"path": t.TempDir(), "access": "read", "reason": "need to read the other project",
	}), RunOptions{})
	if err != nil || result.Success || !strings.Contains(result.Error, "turned off") {
		t.Fatalf("deny policy result = %+v, %v", result, err)
	}
}

func TestReviewerRepliesMustStartWithAVerdict(t *testing.T) {
	for reply, allow := range map[string]bool{
		"ALLOW\nThe task names this folder.": true,
		"**ALLOW** fits":                     true,
		"DENY\nUnrelated to the task.":       false,
		"Sure, that seems fine":              false,
		"":                                   false,
	} {
		if decision := parseOutsideDecision(reply); decision.Allow != allow {
			t.Errorf("parseOutsideDecision(%q) = %+v", reply, decision)
		}
	}
}
