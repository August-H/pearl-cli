package cli

import (
	"strings"
	"time"
)

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
func listTerminalFrame(frame string) string {
	frame = strings.TrimSuffix(frame, "\n")
	return strings.ReplaceAll(frame, "\n", "\x1b[K\r\n") + "\x1b[K\x1b[J"
}
