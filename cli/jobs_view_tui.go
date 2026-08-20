package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/August-H/pearl-cli/internal/store"
	"golang.org/x/term"
)

type jobViewScreen struct {
	Frame           string
	TotalLines      int
	BodyHeight      int
	HeaderPositions []int
}

func runJobDetailsTUI(
	input, output *os.File,
	job store.Job,
	sections []jobViewSection,
) error {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return fmt.Errorf("enable job view input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)

	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")

	selected := 0
	scroll := 0
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	draw := func(keepSelectionVisible bool) jobViewScreen {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		screen := renderJobViewScreen(
			job, sections, width, height, selected, scroll, color,
		)
		if keepSelectionVisible && selected >= 0 && selected < len(screen.HeaderPositions) {
			header := screen.HeaderPositions[selected]
			if header < scroll {
				scroll = header
			} else if header >= scroll+screen.BodyHeight {
				scroll = header - screen.BodyHeight + 1
			}
			screen = renderJobViewScreen(
				job, sections, width, height, selected, scroll, color,
			)
		}
		maxScroll := max(0, screen.TotalLines-screen.BodyHeight)
		scroll = min(max(0, scroll), maxScroll)
		fmt.Fprint(output, "\x1b[H\x1b[2J", dashboardTerminalFrame(screen.Frame))
		return screen
	}
	screen := draw(true)

	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		character, _, err := reader.ReadRune()
		if err != nil {
			return err
		}
		action := ""
		if escapeSequence != "" {
			escapeSequence += string(character)
			var complete bool
			action, complete = jobViewEscapeAction(escapeSequence)
			if !complete {
				continue
			}
			escapeSequence = ""
		} else if character == '\x1b' {
			escapeSequence = "\x1b"
			continue
		} else {
			switch character {
			case 3, 4, 'q', 'Q':
				return nil
			case '\r', '\n', ' ':
				action = "toggle"
			case '\t':
				action = "next"
			case 'j':
				action = "scroll_down"
			case 'k':
				action = "scroll_up"
			case 'a', 'A':
				action = "expand_all"
			case 'c', 'C':
				action = "collapse_all"
			case 'g':
				action = "home"
			case 'G':
				action = "end"
			}
		}

		keepSelectionVisible := false
		switch action {
		case "previous":
			selected = max(0, selected-1)
			keepSelectionVisible = true
		case "next":
			selected = min(len(sections)-1, selected+1)
			keepSelectionVisible = true
		case "toggle":
			sections[selected].Expanded = !sections[selected].Expanded
			keepSelectionVisible = true
		case "expand":
			sections[selected].Expanded = true
			keepSelectionVisible = true
		case "collapse":
			sections[selected].Expanded = false
			keepSelectionVisible = true
		case "expand_all":
			for index := range sections {
				sections[index].Expanded = true
			}
			keepSelectionVisible = true
		case "collapse_all":
			for index := range sections {
				sections[index].Expanded = false
			}
			keepSelectionVisible = true
		case "scroll_up":
			scroll--
		case "scroll_down":
			scroll++
		case "page_up":
			scroll -= max(1, screen.BodyHeight-1)
		case "page_down":
			scroll += max(1, screen.BodyHeight-1)
		case "home":
			scroll = 0
		case "end":
			scroll = max(0, screen.TotalLines-screen.BodyHeight)
		default:
			continue
		}
		screen = draw(keepSelectionVisible)
	}
}

func jobViewEscapeAction(sequence string) (string, bool) {
	actions := map[string]string{
		"\x1b[A":  "previous",
		"\x1bOA":  "previous",
		"\x1b[B":  "next",
		"\x1bOB":  "next",
		"\x1b[C":  "expand",
		"\x1bOC":  "expand",
		"\x1b[D":  "collapse",
		"\x1bOD":  "collapse",
		"\x1b[5~": "page_up",
		"\x1b[6~": "page_down",
		"\x1b[H":  "home",
		"\x1bOH":  "home",
		"\x1b[1~": "home",
		"\x1b[7~": "home",
		"\x1b[F":  "end",
		"\x1bOF":  "end",
		"\x1b[4~": "end",
		"\x1b[8~": "end",
	}
	if action, found := actions[sequence]; found {
		return action, true
	}
	for candidate := range actions {
		if strings.HasPrefix(candidate, sequence) {
			return "", false
		}
	}
	return "", true
}

func renderJobViewScreen(
	job store.Job,
	sections []jobViewSection,
	width, height, selected, scroll int,
	color bool,
) jobViewScreen {
	return renderJobViewScreenWithQuitLabel(
		job, sections, width, height, selected, scroll, color, "q exit",
	)
}

func renderJobViewScreenWithQuitLabel(
	job store.Job,
	sections []jobViewSection,
	width, height, selected, scroll int,
	color bool,
	quitLabel string,
) jobViewScreen {
	width = max(20, width)
	height = max(8, height)
	bodyHeight := height - 4
	bodyLines, headerPositions := jobViewDisplayLines(sections, width, selected, color)
	maxScroll := max(0, len(bodyLines)-bodyHeight)
	scroll = min(max(0, scroll), maxScroll)
	end := min(len(bodyLines), scroll+bodyHeight)

	var frame strings.Builder
	title := "Pearl job: " + jobViewText(job.ID)
	status := job.Status
	plainTitle := title
	if len([]rune(title))+len([]rune(status))+1 <= width {
		plainTitle += strings.Repeat(" ", width-len([]rune(title))-len([]rune(status))) + status
	}
	plainTitle = dashboardTruncate(plainTitle, width)
	if color && strings.HasSuffix(plainTitle, status) {
		prefix := strings.TrimSuffix(plainTitle, status)
		fmt.Fprintln(&frame, dashboardPaint(true, ansiBold+ansiCyan, prefix)+
			dashboardPaint(true, jobStatusColor(status), status))
	} else {
		fmt.Fprintln(&frame, plainTitle)
	}
	divider := strings.Repeat("─", width)
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	for _, line := range bodyLines[scroll:end] {
		fmt.Fprintln(&frame, line)
	}
	for row := end - scroll; row < bodyHeight; row++ {
		fmt.Fprintln(&frame)
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	hint := quitLabel + " · ↑/↓ section · Enter toggle · j/k scroll · PgUp/PgDn · a all · c close"
	position := fmt.Sprintf("%d-%d/%d", min(scroll+1, len(bodyLines)), end, len(bodyLines))
	if len([]rune(hint))+len([]rune(position))+1 <= width {
		hint += strings.Repeat(" ", width-len([]rune(hint))-len([]rune(position))) + position
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(hint, width)))
	return jobViewScreen{
		Frame: frame.String(), TotalLines: len(bodyLines), BodyHeight: bodyHeight,
		HeaderPositions: headerPositions,
	}
}

func jobViewDisplayLines(
	sections []jobViewSection,
	width, selected int,
	color bool,
) ([]string, []int) {
	var lines []string
	headerPositions := make([]int, 0, len(sections))
	for index, section := range sections {
		headerPositions = append(headerPositions, len(lines))
		marker := "▸"
		if section.Expanded {
			marker = "▾"
		}
		header := marker + " " + section.Title
		if section.Summary != "" {
			header += "  " + section.Summary
		}
		header = dashboardTruncate(header, width)
		style := ansiBold
		if index == selected {
			style = ansiBold + ansiCyan
		}
		lines = append(lines, dashboardPaint(color, style, header))
		if !section.Expanded {
			continue
		}
		content := section.Lines
		if len(content) == 0 {
			content = []string{"None recorded"}
		}
		for _, line := range content {
			wrapped := jobViewWrapLine(line, max(1, width-2))
			for _, wrappedLine := range wrapped {
				displayLine := "  " + wrappedLine
				if color && section.Title == "Overview" &&
					strings.HasPrefix(wrappedLine, "Status: ") {
					status := strings.TrimPrefix(wrappedLine, "Status: ")
					displayLine = "  Status: " +
						dashboardPaint(true, jobStatusColor(status), status)
				}
				lines = append(lines, displayLine)
			}
		}
	}
	return lines, headerPositions
}

func jobViewWrapLine(value string, width int) []string {
	characters := []rune(value)
	if len(characters) == 0 {
		return []string{""}
	}
	var lines []string
	for len(characters) > width {
		cut := width
		for index := width; index > 0; index-- {
			if characters[index-1] == ' ' || characters[index-1] == '\t' {
				cut = index - 1
				break
			}
		}
		if cut == 0 {
			cut = width
		}
		lines = append(lines, strings.TrimRight(string(characters[:cut]), " \t"))
		characters = characters[cut:]
		for len(characters) > 0 && (characters[0] == ' ' || characters[0] == '\t') {
			characters = characters[1:]
		}
	}
	lines = append(lines, string(characters))
	return lines
}
