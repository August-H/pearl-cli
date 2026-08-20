//go:build windows

package cli

import (
	"fmt"
	"os"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type consoleKeyEventRecord struct {
	KeyDown        int32
	RepeatCount    uint16
	VirtualKeyCode uint16
	VirtualScan    uint16
	UnicodeChar    uint16
	ControlState   uint32
}

type consoleInputRecord struct {
	EventType uint16
	_         uint16
	KeyEvent  consoleKeyEventRecord
}

var writeConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")

func preloadTerminalInput(terminal *os.File, value string) error {
	if terminal == nil {
		return fmt.Errorf("terminal input is unavailable")
	}
	characters := utf16.Encode([]rune(value))
	records := make([]consoleInputRecord, 0, len(characters)*2)
	for _, character := range characters {
		records = append(records,
			consoleInputRecord{
				EventType: windows.KEY_EVENT,
				KeyEvent: consoleKeyEventRecord{
					KeyDown: 1, RepeatCount: 1, UnicodeChar: character,
				},
			},
			consoleInputRecord{
				EventType: windows.KEY_EVENT,
				KeyEvent: consoleKeyEventRecord{
					RepeatCount: 1, UnicodeChar: character,
				},
			},
		)
	}
	if len(records) == 0 {
		return nil
	}
	var written uint32
	result, _, callErr := writeConsoleInput.Call(
		terminal.Fd(),
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(uint32(len(records))),
		uintptr(unsafe.Pointer(&written)),
	)
	if result == 0 {
		return fmt.Errorf("write to the terminal prompt: %w", callErr)
	}
	if written != uint32(len(records)) {
		return fmt.Errorf("write to the terminal prompt: wrote %d of %d input events", written, len(records))
	}
	return nil
}
