package cli

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// chunkedReader returns one chunk per Read, like separate terminal writes.
type chunkedReader struct{ chunks []string }

func (reader *chunkedReader) Read(buffer []byte) (int, error) {
	if len(reader.chunks) == 0 {
		return 0, io.EOF
	}
	count := copy(buffer, reader.chunks[0])
	reader.chunks = reader.chunks[1:]
	return count, nil
}

func TestReadTerminalKeySeparatesLoneEscFromSequences(t *testing.T) {
	reader := bufio.NewReader(&chunkedReader{chunks: []string{"\x1b", "\x1b[A", "q", "\x1b"}})
	want := []rune{escapeKey, '\x1b', '[', 'A', 'q', escapeKey}
	for index, expected := range want {
		got, err := readTerminalKey(reader)
		if err != nil || got != expected {
			t.Fatalf("key %d = %q, %v; want %q", index, got, err, expected)
		}
	}
	if _, err := readTerminalKey(reader); err != io.EOF {
		t.Fatalf("end of input = %v, want EOF", err)
	}
}

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
