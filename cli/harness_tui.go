package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

type harnessOption struct {
	name        string
	label       string
	description string
	unavailable string
}

type harnessPickerState struct {
	options  []harnessOption
	selected int
	current  string
	notice   string
}

func availableHarnesses() []harnessOption {
	codex := harnessOption{
		name: store.HarnessCodex, label: "Codex",
		description: "OpenAI's Codex CLI, using your OpenRouter model",
	}
	if _, err := daemon.FindCodex(); err != nil {
		codex.unavailable = "not installed: run pearl codex install"
	}
	return []harnessOption{{
		name: store.HarnessPearl, label: "Pearl",
		description: "Pearl's built-in agent loop",
	}, codex}
}

func newHarnessPicker(current string, options []harnessOption) *harnessPickerState {
	picker := &harnessPickerState{options: options, current: current}
	for index, option := range options {
		if option.name == current {
			picker.selected = index
		}
	}
	return picker
}

// handleKey returns the chosen harness, or done with "" when the user leaves.
func (p *harnessPickerState) handleKey(character rune) (string, bool) {
	switch character {
	case 3, 4, escapeKey:
		return "", true
	case '\r', '\n':
		option := p.options[p.selected]
		if option.unavailable != "" {
			p.notice = option.label + " is " + option.unavailable
			return "", false
		}
		return option.name, true
	case 'j':
		p.applyAction("next")
	case 'k':
		p.applyAction("previous")
	}
	return "", false
}

// applyAction moves the selection and reports whether the screen changed.
func (p *harnessPickerState) applyAction(action string) bool {
	previous := p.selected
	switch action {
	case "previous", "scroll_up":
		p.selected = max(0, p.selected-1)
	case "next", "scroll_down":
		p.selected = min(len(p.options)-1, p.selected+1)
	case "home":
		p.selected = 0
	case "end":
		p.selected = len(p.options) - 1
	default:
		return false
	}
	if p.selected != previous {
		p.notice = ""
	}
	return true
}

func (p *harnessPickerState) screen(width, height int, color bool) string {
	width = max(20, width)
	var frame strings.Builder
	divider := strings.Repeat("─", width)
	title := "Pearl harness"
	count := fmt.Sprintf("%d harnesses", len(p.options))
	if len([]rune(title))+len([]rune(count))+1 <= width {
		title += strings.Repeat(" ", width-len([]rune(title))-len([]rune(count))) + count
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate(title, width)))
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	fmt.Fprintln(&frame, dashboardTruncate("Current: "+p.current, width))
	if p.notice != "" {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiYellow, dashboardTruncate(p.notice, width)))
	} else {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	}

	labelWidth := 0
	for _, option := range p.options {
		labelWidth = max(labelWidth, len([]rune(option.label)))
	}
	for index, option := range p.options {
		prefix := "  "
		if index == p.selected {
			prefix = "› "
		}
		marker := "  "
		if option.name == p.current {
			marker = "● "
		}
		description := option.description
		if option.unavailable != "" {
			description = option.unavailable
		}
		label := option.label + strings.Repeat(" ", labelWidth-len([]rune(option.label)))
		line := dashboardTruncate(prefix+marker+label+"  "+description, width)
		switch {
		case !color:
		case index == p.selected:
			line = dashboardPaint(true, ansiBold+ansiCyan, line)
		case option.unavailable != "":
			line = dashboardPaint(true, ansiDim, line)
		case option.name == p.current:
			line = dashboardPaint(true, ansiGreen, line)
		}
		fmt.Fprintln(&frame, line)
	}
	for row := len(p.options); row < max(0, height-6); row++ {
		fmt.Fprintln(&frame)
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	hint := "↑/↓ · Enter select · Esc back"
	position := fmt.Sprintf("%d/%d", p.selected+1, len(p.options))
	if len([]rune(hint))+len([]rune(position))+1 <= width {
		hint += strings.Repeat(" ", width-len([]rune(hint))-len([]rune(position))) + position
	}
	fmt.Fprint(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(hint, width)))
	return frame.String()
}

// runHarnessTUI returns the chosen harness, or "" when the user leaves.
func runHarnessTUI(input, output *os.File, current string) (string, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return "", fmt.Errorf("enable harness input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)
	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	picker := newHarnessPicker(current, availableHarnesses())
	draw := func() {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		fmt.Fprint(output, "\x1b[H", listTerminalFrame(picker.screen(width, height, color)))
	}
	draw()
	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		character, err := readTerminalKey(reader)
		if err != nil {
			return "", err
		}
		if escapeSequence != "" {
			escapeSequence += string(character)
			action, complete := jobViewEscapeAction(escapeSequence)
			if !complete {
				continue
			}
			escapeSequence = ""
			if !picker.applyAction(action) {
				continue
			}
		} else if character == '\x1b' {
			escapeSequence = "\x1b"
			continue
		} else if chosen, done := picker.handleKey(character); done {
			return chosen, nil
		}
		draw()
	}
}

// saveDefaultHarness validates and stores the harness used by new jobs.
func saveDefaultHarness(name string) (string, error) {
	harness, err := store.NormalizeHarness(name)
	if err == nil && harness == "" {
		err = errors.New("harness cannot be empty")
	}
	if err != nil {
		return "", err
	}
	if harness == store.HarnessCodex {
		if _, err := daemon.FindCodex(); err != nil {
			return "", err
		}
	}
	if _, err := openrouter_request.ConfigureDefaultHarness(harness); err != nil {
		return "", err
	}
	return harness, nil
}
