package cli

import (
	"fmt"
	"strings"
)

// Redraw only changed rows so refreshes leave selected text alone elsewhere.
type dashboardFrameRenderer struct {
	lines  []string
	width  int
	height int
}

func (renderer *dashboardFrameRenderer) render(frame string, width, height int) string {
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	var output strings.Builder
	if renderer.lines == nil || renderer.width != width || renderer.height != height {
		output.WriteString("\x1b[H" + listTerminalFrame(frame))
	} else {
		for index, line := range lines {
			if index >= len(renderer.lines) || line != renderer.lines[index] {
				fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K%s", index+1, line)
			}
		}
		if len(lines) < len(renderer.lines) {
			fmt.Fprintf(&output, "\x1b[%d;1H\x1b[J", len(lines)+1)
		}
	}
	renderer.lines, renderer.width, renderer.height = lines, width, height
	return output.String()
}
