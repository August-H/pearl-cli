package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode"
)

const dashboardDraftLimit = 1 << 20

func isComposerEscapePrefix(sequence string) bool {
	for _, candidate := range []string{"\x1b[200~", "\x1b\r", "\x1b\n"} {
		if sequence != candidate && strings.HasPrefix(candidate, sequence) {
			return true
		}
	}
	return false
}

func composerRows(text string, width, limit int) []string {
	width = max(1, width)
	limit = max(1, limit)
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, line))
		for len(runes) > width {
			rows = append(rows, string(runes[:width]))
			runes = runes[width:]
		}
		rows = append(rows, string(runes))
	}
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
		rows[0] = "↑ " + dashboardTruncate(rows[0], max(0, width-2))
	}
	return rows
}

func readComposerPaste(reader *bufio.Reader) (string, error) {
	const end = "\x1b[201~"
	var text strings.Builder
	matched, size := 0, 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		size++
		if text.Len() < dashboardDraftLimit+len(end) {
			text.WriteByte(b)
		}
		if b == end[matched] {
			matched++
		} else if b == end[0] {
			matched = 1
		} else {
			matched = 0
		}
		if matched == len(end) {
			if size-len(end) > dashboardDraftLimit {
				return "", errors.New("paste exceeds 1 MB; draft was kept")
			}
			return strings.ReplaceAll(strings.TrimSuffix(text.String(), end), "\r\n", "\n"), nil
		}
	}
}

// Edit the entire draft without invoking a shell. Editor flags and quoted paths
// are supported; shell expansions and pipelines are not executed.
func editDashboardDraft(ctx context.Context, text string, input *os.File, output io.Writer) (string, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad.exe"
		}
	}
	args, err := parseDashboardCommand(editor)
	if err != nil || len(args) == 0 {
		return text, errors.New("EDITOR must name an executable and optional arguments")
	}
	file, err := os.CreateTemp("", "pearl-draft-*.txt")
	if err != nil {
		return text, err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(text); err != nil {
		file.Close()
		return text, err
	}
	if err := file.Close(); err != nil {
		return text, err
	}
	command := exec.CommandContext(ctx, args[0], append(args[1:], file.Name())...)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return text, fmt.Errorf("editor failed; original draft kept: %w", err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return text, err
	}
	if len(data) > dashboardDraftLimit {
		return text, errors.New("draft exceeds 1 MB; original draft kept")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
