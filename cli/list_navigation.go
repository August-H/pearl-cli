package cli

import (
	"bufio"
	"strings"
	"time"
)

// escapeKey is what readTerminalKey returns for a lone Esc press. It is
// negative so it can't collide with typed text or control characters.
const escapeKey rune = -1

// readTerminalKey reads one key. Terminals write escape sequences such as
// arrow keys and mouse reports in a single burst, so an ESC byte with nothing
// buffered behind it is the Esc key itself rather than a sequence prefix.
func readTerminalKey(reader *bufio.Reader) (rune, error) {
	character, _, err := reader.ReadRune()
	if err == nil && character == '\x1b' && reader.Buffered() == 0 {
		return escapeKey, nil
	}
	return character, err
}

const (
	listWheelBurstGap      = 50 * time.Millisecond
	listWheelInitialRepeat = 50 * time.Millisecond
	listWheelFastRepeat    = 16 * time.Millisecond
	listWheelAcceleration  = 250 * time.Millisecond
)

// Coalesce duplicate reports from individual ticks, then increase the repeat
// rate during a continuous swipe. Each accepted report still moves one row.
type listWheelNavigation struct {
	direction string
	started   time.Time
	lastEvent time.Time
	lastStep  time.Time
}

func (wheel *listWheelNavigation) filter(action string, now time.Time) string {
	if action != "scroll_up" && action != "scroll_down" {
		if action != "" {
			*wheel = listWheelNavigation{}
		}
		return action
	}
	newSwipe := action != wheel.direction || now.Sub(wheel.lastEvent) >= listWheelBurstGap
	if newSwipe {
		wheel.started = now
	}
	wheel.direction, wheel.lastEvent = action, now
	elapsed := min(max(0, now.Sub(wheel.started)), listWheelAcceleration)
	interval := listWheelInitialRepeat - (listWheelInitialRepeat-listWheelFastRepeat)*elapsed/listWheelAcceleration
	if !newSwipe && now.Sub(wheel.lastStep) < interval {
		return ""
	}
	wheel.lastStep = now
	return action
}

// Overwrite rows in place without blanking the screen between wheel reports.
// Avoid a final newline, which would scroll the frame off the terminal bottom.
// listTerminalFrame clears each row before drawing it. Clearing after a row
// that fills the terminal width would erase its last character, because the
// cursor waits on the final column.
func listTerminalFrame(frame string) string {
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	var output strings.Builder
	for index, line := range lines {
		if index == len(lines)-1 {
			output.WriteString("\x1b[J" + line)
			break
		}
		output.WriteString("\x1b[2K" + line + "\r\n")
	}
	return output.String()
}
