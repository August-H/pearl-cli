//go:build !windows

package openrouter_request

import (
	"os/exec"
	"syscall"
)

func startCommandProcess(command *exec.Cmd) (func(), error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	if err := command.Start(); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }, nil
}
