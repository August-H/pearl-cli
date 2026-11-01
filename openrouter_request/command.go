package openrouter_request

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/August-H/pearl-cli/internal/verification"
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
	policy, err := loadCommandPolicy()
	return policy.AllowedCommands, err
}

func runWorkspaceCommand(ctx context.Context, call agentToolCall, root string, access *outsideAccess) agentToolResult {
	return runWorkspaceCommandWithEvents(ctx, call, root, access, nil)
}

func runWorkspaceCommandWithEvents(ctx context.Context, call agentToolCall, root string, access *outsideAccess, events EventSink) agentToolResult {
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
	policy, err := loadCommandPolicy()
	if err != nil {
		return fail(err)
	}
	found := false
	for _, command := range policy.AllowedCommands {
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
	program, arguments := executable, input.Args
	sandbox := newCommandSandbox(root, access.writableRoots(), policy.AllowNetwork)
	if wrapped, wrappedArguments, err := sandbox.wrap(executable, input.Args, directory); err == nil {
		program, arguments = wrapped, wrappedArguments
	} else if !errors.Is(err, errNoSandbox) || !policy.AllowUnsandboxed {
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
	if events != nil && verification.Command(input.Command, input.Args) {
		if err := events(AgentEvent{Type: "verification", Data: verification.Event("running", strings.Join(append([]string{input.Command}, input.Args...), " "))}); err != nil {
			return fail(err)
		}
	}
	result, err := executeWorkspaceCommand(ctx, program, arguments, directory, policy.AllowedEnv)
	if err != nil {
		return fail(err)
	}
	return agentToolResult{Success: result.ExitCode == 0 && !result.TimedOut, Result: result}
}

// commandEnvironmentNames lists what build and test tools need from the
// environment. Everything else is withheld, because credentials go by too many
// names to filter out reliably. Users add names with allowed_command_env.
var commandEnvironmentNames = map[string]bool{
	// System.
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TERM": true, "LANG": true, "LANGUAGE": true, "TZ": true, "NO_COLOR": true,
	"CI": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true,
	// Windows.
	"SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
	"USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true, "APPDATA": true, "LOCALAPPDATA": true,
	"PROGRAMDATA": true, "PROGRAMFILES": true, "PROGRAMFILES(X86)": true,
	"PROCESSOR_ARCHITECTURE": true, "NUMBER_OF_PROCESSORS": true, "OS": true,
	// C toolchains.
	"CC": true, "CXX": true, "CFLAGS": true, "CXXFLAGS": true, "LDFLAGS": true,
	"PKG_CONFIG_PATH": true, "SDKROOT": true, "DEVELOPER_DIR": true,
	// Go.
	"GOROOT": true, "GOPATH": true, "GOBIN": true, "GOCACHE": true, "GOMODCACHE": true,
	"GOFLAGS": true, "GOTOOLCHAIN": true, "CGO_ENABLED": true,
	// Node, Python, Ruby, Rust and Java.
	"NODE_PATH": true, "NVM_DIR": true, "PNPM_HOME": true,
	"PYTHONPATH": true, "VIRTUAL_ENV": true, "GEM_HOME": true, "GEM_PATH": true,
	"CARGO_HOME": true, "RUSTUP_HOME": true, "JAVA_HOME": true,
}

// commandEnvironment keeps the variables a command may see. Names compare
// without regard to case because Windows treats them that way.
func commandEnvironment(environment, extra []string) []string {
	allowed := make(map[string]bool, len(extra))
	for _, name := range extra {
		allowed[strings.ToUpper(strings.TrimSpace(name))] = true
	}
	var kept []string
	for _, value := range environment {
		name, _, _ := strings.Cut(value, "=")
		upper := strings.ToUpper(name)
		if commandEnvironmentNames[upper] || allowed[upper] || strings.HasPrefix(upper, "LC_") {
			kept = append(kept, value)
		}
	}
	return kept
}

func executeWorkspaceCommand(ctx context.Context, executable string, args []string, directory string, extraEnvironment []string) (commandResult, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.WaitDelay = 2 * time.Second
	command.Env = commandEnvironment(os.Environ(), extraEnvironment)
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
