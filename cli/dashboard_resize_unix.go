//go:build darwin || linux || freebsd || openbsd || netbsd

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

func watchDashboardResize() (<-chan os.Signal, func()) {
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	return resize, func() {
		signal.Stop(resize)
	}
}
