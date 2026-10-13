package openrouter_request

import "os"

const sandboxExec = "/usr/bin/sandbox-exec"

func sandboxCommand(s commandSandbox, executable string, args []string, _ string) (string, []string, error) {
	if _, err := os.Stat(sandboxExec); err != nil {
		return "", nil, errNoSandbox
	}
	return sandboxExec, append([]string{"-p", seatbeltProfile(s), "--", executable}, args...), nil
}
