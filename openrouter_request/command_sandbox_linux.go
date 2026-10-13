package openrouter_request

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

var bubblewrapWorks = sync.OnceValue(func() string {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return ""
	}
	// User namespaces can be turned off, so check that bwrap really runs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, path, "--ro-bind", "/", "/", "--unshare-net", "true").Run() != nil {
		return ""
	}
	return path
})

func sandboxCommand(s commandSandbox, executable string, args []string, directory string) (string, []string, error) {
	bwrap := bubblewrapWorks()
	if bwrap == "" {
		return "", nil, errNoSandbox
	}
	return bwrap, bubblewrapArguments(s, executable, args, directory), nil
}
