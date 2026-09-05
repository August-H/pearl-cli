package openrouter_request

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workspaceCall(name string, args any) agentToolCall {
	var call agentToolCall
	call.Function.Name = name
	encoded, _ := json.Marshal(args)
	call.Function.Arguments = string(encoded)
	return call
}

func TestPatchRejectsAmbiguousTextAndCreatesDirectories(t *testing.T) {
	root := t.TempDir()
	result := runAgentTool(workspaceCall("create_directory", map[string]any{"relative_path": "src/nested"}), root, 4096)
	if !result.Success {
		t.Fatal(result.Error)
	}
	path := filepath.Join(root, "src/nested/test.txt")
	if err := os.WriteFile(path, []byte("same same"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"relative_path": "src/nested/test.txt", "old_text": "same", "new_text": "updated"}
	if result = runAgentTool(workspaceCall("apply_patch", args), root, 4096); result.Success {
		t.Fatal("ambiguous patch accepted")
	}
	args["old_text"] = "same same"
	if result = runAgentTool(workspaceCall("apply_patch", args), root, 4096); !result.Success {
		t.Fatal(result.Error)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "updated" {
		t.Fatalf("file=%s", data)
	}
}

func TestCommandHelper(t *testing.T) {
	if os.Getenv("PEARL_COMMAND_TEST_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "output":
		dir, _ := os.Getwd()
		os.Stdout.WriteString(dir + "\n" + strings.Repeat("x", commandOutputLimit+1))
		os.Exit(0)
	case "fail":
		os.Exit(17)
	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "child":
		executable, _ := os.Executable()
		child := exec.Command(executable, "-test.run=^TestCommandHelper$", "--", "marker")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile("child-started.txt", []byte("ready"), 0o600)
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "marker":
		time.Sleep(2 * time.Second)
		_ = os.WriteFile("child-survived.txt", []byte("child was not cancelled"), 0o600)
		os.Exit(0)
	}
}

func TestCommandCancellationStopsChildProcesses(t *testing.T) {
	t.Setenv("PEARL_COMMAND_TEST_HELPER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type outcome struct {
		result commandResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := executeWorkspaceCommand(ctx, executable, []string{"-test.run=^TestCommandHelper$", "--", "child"}, root)
		finished <- outcome{result, err}
	}()
	for {
		if _, err := os.Stat(filepath.Join(root, "child-started.txt")); err == nil {
			break
		}
		select {
		case ended := <-finished:
			t.Fatalf("parent exited before child started: %+v", ended)
		case <-ctx.Done():
			t.Fatal("child did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	ended := <-finished
	result, err := ended.result, ended.err
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled result=%+v err=%v", result, err)
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(filepath.Join(root, "child-survived.txt")); !os.IsNotExist(err) {
		t.Fatal("child process survived cancellation")
	}
}

func TestCommandsEnforcePolicyAndBoundExecution(t *testing.T) {
	root := t.TempDir()
	config := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", config)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"model": "dummy", "allowed_commands": []string{executable}})
	if err := os.WriteFile(filepath.Join(config, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEARL_COMMAND_TEST_HELPER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	call := workspaceCall("run_command", map[string]any{"command": "unapproved", "args": []string{}})
	if result := runWorkspaceCommand(context.Background(), call, root); result.Success || !strings.Contains(result.Error, "not enabled") {
		t.Fatalf("policy result=%+v", result)
	}
	for _, mode := range []string{"output", "fail", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			call = workspaceCall("run_command", map[string]any{"command": executable, "args": []string{"-test.run=^TestCommandHelper$", "--", mode}, "timeout_seconds": 1})
			start := time.Now()
			result := runWorkspaceCommand(context.Background(), call, root)
			if result.Error != "" {
				t.Fatal(result.Error)
			}
			command := result.Result.(commandResult)
			switch mode {
			case "output":
				if !result.Success || !command.Truncated || len(command.Output) > commandOutputLimit {
					t.Fatalf("output result=%+v", command)
				}
			case "fail":
				if result.Success || command.ExitCode != 17 {
					t.Fatalf("exit=%+v", command)
				}
			case "sleep":
				if result.Success || !command.TimedOut || time.Since(start) > 5*time.Second {
					t.Fatalf("timeout=%+v", command)
				}
			}
		})
	}
	call = workspaceCall("run_command", map[string]any{"command": executable, "relative_path": "../outside", "args": []string{}})
	if result := runWorkspaceCommand(context.Background(), call, root); result.Error == "" {
		t.Fatal("workspace escape accepted")
	}
}
