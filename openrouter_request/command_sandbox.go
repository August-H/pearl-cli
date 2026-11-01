package openrouter_request

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/August-H/pearl-cli/internal/workspacepath"
)

// commandPolicy comes from the user's own settings only. A repository's
// settings.json can't enable commands or loosen the sandbox.
type commandPolicy struct {
	AllowedCommands []string `json:"allowed_commands"`
	// AllowUnsandboxed runs commands without a sandbox where none is available.
	AllowUnsandboxed bool `json:"allow_unsandboxed_commands"`
	// AllowNetwork lets sandboxed commands reach the network.
	AllowNetwork bool `json:"allow_command_network"`
	// AllowedEnv names extra environment variables commands may see.
	AllowedEnv []string `json:"allowed_command_env"`
}

func loadCommandPolicy() (commandPolicy, error) {
	for _, path := range pearlConfigFiles("settings.json", false) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return commandPolicy{}, err
		}
		var policy commandPolicy
		if err := json.Unmarshal(data, &policy); err != nil {
			return commandPolicy{}, err
		}
		return policy, nil
	}
	return commandPolicy{}, nil
}

// commandSandbox describes what a command may touch.
type commandSandbox struct {
	Writable []string
	Denied   []string
	Network  bool
	// Terminals lets the process open pseudo-terminals. Linux's sandbox gives
	// each process private ones already.
	Terminals bool
}

var errNoSandbox = errors.New("Pearl can't sandbox commands on this system; install bubblewrap (Linux), or set allow_unsandboxed_commands to true in Pearl's user settings to run them unsandboxed")

func newCommandSandbox(workspace string, extraWritable []string, network bool) commandSandbox {
	writable := []string{workspacepath.CanonicalPath(workspace)}
	temporary := workspacepath.CanonicalPath(os.TempDir())
	writable = append(writable, temporary)
	// macOS keeps per-user temp in .../T next to the per-user cache in .../C.
	if filepath.Base(temporary) == "T" {
		writable = append(writable, filepath.Dir(temporary))
	}
	for _, shared := range []string{"/tmp", "/private/tmp", "/var/tmp"} {
		if info, err := os.Stat(shared); err == nil && info.IsDir() {
			writable = append(writable, workspacepath.CanonicalPath(shared))
		}
	}
	if cache, err := os.UserCacheDir(); err == nil {
		writable = append(writable, workspacepath.CanonicalPath(cache))
	}
	if home, err := os.UserHomeDir(); err == nil {
		writable = append(writable, workspacepath.CanonicalPath(filepath.Join(home, ".cache")))
	}
	for _, path := range extraWritable {
		writable = append(writable, workspacepath.CanonicalPath(path))
	}
	var kept []string
	seen := map[string]bool{}
	for _, path := range writable {
		if path == "" || seen[path] || workspacepath.Denied(path) {
			continue
		}
		seen[path] = true
		kept = append(kept, path)
	}
	return commandSandbox{Writable: kept, Denied: workspacepath.DeniedRoots(), Network: network}
}

// wrap returns the program and arguments that run executable inside the
// sandbox. It returns errNoSandbox when the system has none.
func (s commandSandbox) wrap(executable string, args []string, directory string) (string, []string, error) {
	return sandboxCommand(s, executable, args, directory)
}

// ErrNoSandbox means this system has no way to sandbox a process.
var ErrNoSandbox = errNoSandbox

// CommandPermissions are the user's settings for what agent commands may do.
type CommandPermissions struct {
	AllowUnsandboxed bool
	AllowNetwork     bool
}

// LoadCommandPermissions reads them from the user's own settings only.
func LoadCommandPermissions() (CommandPermissions, error) {
	policy, err := loadCommandPolicy()
	if err != nil {
		return CommandPermissions{}, err
	}
	return CommandPermissions{AllowUnsandboxed: policy.AllowUnsandboxed, AllowNetwork: policy.AllowNetwork}, nil
}

// SandboxedTerminalProcess returns the program and arguments that run executable
// in the same sandbox as agent commands, able to open a pseudo-terminal. It
// returns ErrNoSandbox when there is none.
func SandboxedTerminalProcess(workspace string, extraWritable []string, network bool, executable string, args []string, directory string) (string, []string, error) {
	sandbox := newCommandSandbox(workspace, extraWritable, network)
	sandbox.Terminals = true
	return sandbox.wrap(executable, args, directory)
}

// StartProcessGroup starts command so that cancelling it, or calling the
// returned function, also stops every process it started.
func StartProcessGroup(command *exec.Cmd) (func(), error) {
	return startCommandProcess(command)
}

func seatbeltString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

// seatbeltProfile allows everything except writing outside the writable
// paths, reading the denied paths, and, unless allowed, the network.
func seatbeltProfile(s commandSandbox) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n(allow default)\n")
	if !s.Network {
		// Seatbelt can't scope loopback to the sandbox the way a Linux network
		// namespace does, and test suites need it for their local servers. This
		// leaves services already listening on localhost reachable on macOS.
		profile.WriteString("(deny network-outbound (remote ip))\n(allow network-outbound (remote ip \"localhost:*\"))\n")
	}
	profile.WriteString("(deny file-write*)\n(allow file-write*\n")
	for _, path := range s.Writable {
		profile.WriteString("  (subpath " + seatbeltString(path) + ")\n")
	}
	profile.WriteString("  (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/tty\") (literal \"/dev/dtracehelper\") (regex #\"^/dev/fd/\")")
	if s.Terminals {
		profile.WriteString(" (literal \"/dev/ptmx\") (regex #\"^/dev/ttys[0-9]+$\")")
	}
	profile.WriteString(")\n")
	if len(s.Denied) > 0 {
		profile.WriteString("(deny file-read* file-write*\n")
		for _, path := range s.Denied {
			profile.WriteString("  (subpath " + seatbeltString(path) + ")\n")
		}
		profile.WriteString(")\n")
	}
	return profile.String()
}

func bubblewrapArguments(s commandSandbox, executable string, args []string, directory string) []string {
	arguments := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--unshare-pid", "--proc", "/proc", "--die-with-parent"}
	if !s.Network {
		arguments = append(arguments, "--unshare-net")
	}
	for _, path := range s.Writable {
		if _, err := os.Stat(path); err == nil {
			arguments = append(arguments, "--bind", path, path)
		}
	}
	for _, path := range s.Denied {
		info, err := os.Stat(path)
		switch {
		case err != nil:
		case info.IsDir():
			arguments = append(arguments, "--tmpfs", path)
		default:
			arguments = append(arguments, "--ro-bind", "/dev/null", path)
		}
	}
	arguments = append(arguments, "--chdir", directory, "--", executable)
	return append(arguments, args...)
}
