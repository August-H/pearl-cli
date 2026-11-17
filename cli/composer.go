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
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const dashboardDraftLimit = 1 << 20

// Ask the terminal to report Shift+Enter as its own key. Terminals use one of two
// standards for this, and ones with neither ignore the request and send a plain
// Enter. Kitty's standard also changes how Esc and Ctrl keys arrive, which
// composerKeyFromSequence undoes.
const (
	dashboardKeyboardEnable  = "\x1b[>1u\x1b[>4;1m"
	dashboardKeyboardDisable = "\x1b[>4m\x1b[<u"
)

// composerNewlineKey is any key that adds a line to the draft.
const composerNewlineKey rune = -2

const composerSequenceLimit = 32

// composerBuffer keeps the insertion point at a UTF-8 boundary.
type composerBuffer struct {
	text   string
	cursor int
}

func (buffer *composerBuffer) set(text string) {
	buffer.text, buffer.cursor = text, len(text)
}

func (buffer *composerBuffer) insert(text string) bool {
	if len(buffer.text)+len(text) > dashboardDraftLimit {
		return false
	}
	buffer.text = buffer.text[:buffer.cursor] + text + buffer.text[buffer.cursor:]
	buffer.cursor += len(text)
	return true
}

func (buffer *composerBuffer) backspace() {
	if buffer.cursor > 0 {
		_, size := utf8.DecodeLastRuneInString(buffer.text[:buffer.cursor])
		buffer.text = buffer.text[:buffer.cursor-size] + buffer.text[buffer.cursor:]
		buffer.cursor -= size
	}
}

func (buffer *composerBuffer) delete() {
	if buffer.cursor < len(buffer.text) {
		_, size := utf8.DecodeRuneInString(buffer.text[buffer.cursor:])
		buffer.text = buffer.text[:buffer.cursor] + buffer.text[buffer.cursor+size:]
	}
}

func (buffer *composerBuffer) move(action string, width int) bool {
	switch action {
	case "collapse": // Left arrow.
		if buffer.cursor > 0 {
			_, size := utf8.DecodeLastRuneInString(buffer.text[:buffer.cursor])
			buffer.cursor -= size
		}
	case "expand": // Right arrow.
		if buffer.cursor < len(buffer.text) {
			_, size := utf8.DecodeRuneInString(buffer.text[buffer.cursor:])
			buffer.cursor += size
		}
	case "home":
		buffer.cursor = strings.LastIndex(buffer.text[:buffer.cursor], "\n") + 1
	case "end":
		if end := strings.IndexByte(buffer.text[buffer.cursor:], '\n'); end >= 0 {
			buffer.cursor += end
		} else {
			buffer.cursor = len(buffer.text)
		}
	case "delete":
		buffer.delete()
	case "previous", "next":
		// Single-line drafts keep Up/Down for history and suggestions.
		rows, row, column := composerLayout(buffer.text, buffer.cursor, width)
		if len(rows) < 2 {
			return false
		}
		target := row - 1
		if action == "next" {
			target = row + 1
		}
		if target >= 0 && target < len(rows) {
			buffer.cursor = rows[target].start
			lastColumn := len(rows[target].text)
			if target+1 < len(rows) && buffer.text[rows[target+1].start-1] != '\n' {
				// At a soft wrap, the position after the last character is
				// the start of the next row.
				lastColumn--
			}
			for count := 0; count < min(column, lastColumn); count++ {
				_, size := utf8.DecodeRuneInString(buffer.text[buffer.cursor:])
				buffer.cursor += size
			}
		}
	default:
		return false
	}
	return true
}

type composerRow struct {
	text  []rune
	start int
}

// composerLayout shares wrapping between cursor navigation and rendering.
func composerLayout(text string, cursor, width int) ([]composerRow, int, int) {
	width = max(1, width)
	rows := []composerRow{{}}
	cursorRow, cursorColumn := 0, 0
	for offset, character := range text {
		row := &rows[len(rows)-1]
		if character != '\n' && len(row.text) == width {
			rows = append(rows, composerRow{start: offset})
			row = &rows[len(rows)-1]
		}
		if offset == cursor {
			cursorRow, cursorColumn = len(rows)-1, len(row.text)
		}
		if character == '\n' {
			rows = append(rows, composerRow{start: offset + 1})
		} else {
			// Keep one display cell per input rune, as the existing composer does.
			if unicode.IsControl(character) {
				character = ' '
			}
			row.text = append(row.text, character)
		}
	}
	if cursor == len(text) {
		cursorRow, cursorColumn = len(rows)-1, len(rows[len(rows)-1].text)
	}
	return rows, cursorRow, cursorColumn
}

func composerRowsAtCursor(text string, cursor, width, limit int) []string {
	rows, cursorRow, cursorColumn := composerLayout(text, cursor, width)
	limit = max(1, limit)
	start := max(0, len(rows)-limit)
	if cursorRow < start {
		start = cursorRow
	}
	end := min(len(rows), start+limit)
	visible := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		row := rows[index].text
		value := string(row)
		if index == cursorRow {
			value = string(row[:cursorColumn]) + "█" + string(row[cursorColumn:])
		}
		visible = append(visible, value)
	}
	return visible
}

func dashboardEscapeAction(sequence string) (string, bool) {
	switch sequence {
	case "\x1b[3~", "\x1b[57349u":
		return "delete", true
	case "\x1bOQ", "\x1b[12~", "\x1b[57365u":
		return "mouse_toggle", true
	case "\x1b[57350u":
		return "collapse", true
	case "\x1b[57351u":
		return "expand", true
	case "\x1b[57352u":
		return "previous", true
	case "\x1b[57353u":
		return "next", true
	case "\x1b[57356u":
		return "home", true
	case "\x1b[57357u":
		return "end", true
	}
	return jobViewEscapeAction(sequence)
}

// composerNewlineHint names the newline key to suggest. macOS Terminal sends a plain
// Enter for Shift+Enter, so suggesting it there would submit the draft.
func composerNewlineHint() string {
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return "Ctrl+J"
	}
	return "Shift+Enter"
}

// composerContinueLine turns a trailing backslash into a new line, so typing \ then
// Enter adds a line in terminals that can't report Shift+Enter, such as macOS Terminal.
func composerContinueLine(text string) (string, bool) {
	if trimmed, found := strings.CutSuffix(text, "\\"); found {
		return trimmed + "\n", true
	}
	return text, false
}

func isComposerEscapePrefix(sequence string) bool {
	// Key reports with numbers can end in many ways, so wait for the last byte.
	if parameters, found := strings.CutPrefix(sequence, "\x1b["); found && len(sequence) <= composerSequenceLimit &&
		strings.Trim(parameters, "0123456789;:") == "" {
		return true
	}
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

// composerKeyFromSequence turns a key report into the key it stands for. It knows
// the form "ESC [ code ; modifiers u" and the older "ESC [ 27 ; modifiers ; code ~".
func composerKeyFromSequence(sequence string) (rune, bool) {
	if sequence == "\x1b\r" || sequence == "\x1b\n" {
		return composerNewlineKey, true
	}
	parameters, found := strings.CutPrefix(sequence, "\x1b[")
	if !found || len(parameters) < 2 {
		return 0, false
	}
	var codeText, modifierText string
	fields := strings.Split(parameters[:len(parameters)-1], ";")
	switch {
	case strings.HasSuffix(parameters, "u"):
		codeText = fields[0]
		if len(fields) > 1 {
			modifierText = fields[1]
		}
	case strings.HasSuffix(parameters, "~") && len(fields) == 3 && fields[0] == "27":
		modifierText, codeText = fields[1], fields[2]
	default:
		return 0, false
	}
	// A field can carry extra detail after a colon, such as the key's other forms.
	codeText, _, _ = strings.Cut(codeText, ":")
	modifierText, _, _ = strings.Cut(modifierText, ":")
	code, err := strconv.Atoi(codeText)
	if err != nil || code <= 0 {
		return 0, false
	}
	modifiers := 1
	if modifierText != "" {
		if modifiers, err = strconv.Atoi(modifierText); err != nil || modifiers < 1 {
			return 0, false
		}
	}
	// The value is one more than a bit set: shift 1, alt 2, ctrl 4.
	bits := modifiers - 1
	const shift, alt, ctrl = 1, 2, 4
	switch code {
	case 13:
		if bits&(shift|alt|ctrl) != 0 {
			return composerNewlineKey, true
		}
		return '\r', true
	case 27:
		return escapeKey, true
	case 9:
		return '\t', true
	case 127:
		return 127, true
	}
	if bits&ctrl != 0 && code >= 'a' && code <= 'z' {
		return rune(code - 'a' + 1), true
	}
	// Keys with no text of their own, such as arrows and F-keys, report codes from
	// the private-use range, which would otherwise land in the draft.
	if bits&^shift == 0 && code >= ' ' && code <= unicode.MaxRune && !unicode.Is(unicode.Co, rune(code)) {
		return rune(code), true
	}
	return 0, false
}
