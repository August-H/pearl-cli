//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package cli

import "os"

func watchDashboardResize() (<-chan os.Signal, func()) {
	return nil, func() {}
}
