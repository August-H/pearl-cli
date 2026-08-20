//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package cli

import "os/exec"

func configureDetachedProcess(_ *exec.Cmd) {}
