//go:build !darwin && !linux && !windows

package cli

import (
	"fmt"
	"os"
)

func preloadTerminalInput(_ *os.File, _ string) error {
	return fmt.Errorf("preloading the terminal prompt is not supported on this platform")
}
