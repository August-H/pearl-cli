//go:build darwin || linux

package cli

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

func preloadTerminalInput(terminal *os.File, value string) error {
	if terminal == nil {
		return fmt.Errorf("terminal input is unavailable")
	}
	characters := []byte(value)
	for index := range characters {
		_, _, errno := unix.Syscall(
			unix.SYS_IOCTL,
			terminal.Fd(),
			uintptr(unix.TIOCSTI),
			uintptr(unsafe.Pointer(&characters[index])),
		)
		if errno != 0 {
			return fmt.Errorf("write to the terminal prompt: %w", errno)
		}
	}
	runtime.KeepAlive(characters)
	return nil
}
