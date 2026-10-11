package cli

import (
	"strings"
	"testing"
)

func TestListTerminalFrameClearsStaleRowsWithoutScrollingBottom(t *testing.T) {
	for _, frame := range []string{"Title\nShort row\n\nFooter\n", "Title\nShort row\n\nFooter"} {
		got := listTerminalFrame(frame)
		if strings.Contains(got, "\x1b[2J") || strings.HasSuffix(got, "\n") {
			t.Fatalf("frame blanks the screen or scrolls at the bottom: %q", got)
		}
		if strings.Count(got, "\r\n") != 3 || !strings.Contains(got, "Short row\x1b[K\r\n\x1b[K\r\n") || !strings.HasSuffix(got, "Footer\x1b[K\x1b[J") {
			t.Fatalf("frame leaves stale row content: %q", got)
		}
	}
}
