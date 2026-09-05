package openrouter_request

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const commandOutputLimit = 64 << 10

type commandResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
	Cancelled bool   `json:"cancelled"`
}

type boundedCommandOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedCommandOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := commandOutputLimit - len(b.data)
	if n > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, data...)
	return n, nil
}

// Read command permissions only from durable user configuration. A repository's
// settings.json cannot opt itself into executing commands.
func allowedCommands() ([]string, error) {
	for _, path := range pearlConfigFiles("settings.json", false) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var settings struct {
			AllowedCommands []string `json:"allowed_commands"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, err
		}
		return settings.AllowedCommands, nil
	}
	return nil, nil
}

func runWorkspaceCommand(ctx context.Context, call agentToolCall, root string) agentToolResult {
	fail := func(err error) agentToolResult { return agentToolResult{Error: err.Error()} }
	var input struct {
		Command        string   `json:"command"`
		Args           []string `json:"args"`
		RelativePath   string   `json:"relative_path"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return fail(err)
	}
	allowed, err := allowedCommands()
	if err != nil {
		return fail(err)
	}
	found := false
	for _, command := range allowed {
		if command == input.Command && command != "" {
			found = true
			break
		}
	}
	if !found {
		return fail(fmt.Errorf("command %q is not enabled; add its executable name or absolute path to allowed_commands in Pearl's user settings for this trusted workspace", input.Command))
	}
	if input.RelativePath == "" {
		input.RelativePath = "."
	}
	directory, err := safeAgentPath(root, input.RelativePath)
	if err != nil {
		return fail(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() {
		return fail(errors.New("command directory is not a directory"))
	}
	// Resolve PATH before setting Dir. Never search a model-selected directory
	// for an executable or interpret shell syntax.
	if !filepath.IsAbs(input.Command) && filepath.Base(input.Command) != input.Command {
		return fail(errors.New("use an executable name or configured absolute path"))
	}
	executable, err := exec.LookPath(input.Command)
	if err != nil {
		return fail(err)
	}
	if input.TimeoutSeconds <= 0 {
		input.TimeoutSeconds = 60
	}
	if input.TimeoutSeconds > 120 {
		input.TimeoutSeconds = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutSeconds)*time.Second)
	defer cancel()
	result, err := executeWorkspaceCommand(ctx, executable, input.Args, directory)
	if err != nil {
		return fail(err)
	}
	return agentToolResult{Success: result.ExitCode == 0 && !result.TimedOut, Result: result}
}

func executeWorkspaceCommand(ctx context.Context, executable string, args []string, directory string) (commandResult, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.WaitDelay = 2 * time.Second
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") {
			continue
		}
		command.Env = append(command.Env, value)
	}
	output := &boundedCommandOutput{}
	command.Stdout, command.Stderr = output, output
	cleanup, err := startCommandProcess(command)
	if err != nil {
		if ctx.Err() != nil {
			return commandResult{ExitCode: -1, TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), Cancelled: errors.Is(ctx.Err(), context.Canceled)}, nil
		}
		return commandResult{}, err
	}
	defer cleanup()
	err = command.Wait()
	result := commandResult{ExitCode: command.ProcessState.ExitCode(), TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), Cancelled: errors.Is(ctx.Err(), context.Canceled)}
	output.mu.Lock()
	result.Output, result.Truncated = strings.ToValidUTF8(string(output.data), "?"), output.truncated
	output.mu.Unlock()
	if ctx.Err() != nil {
		result.ExitCode = -1
		return result, nil
	}
	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		return result, err
	}
	return result, nil
}
