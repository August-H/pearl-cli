package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

type modelRowKind int

const (
	modelRowHeader modelRowKind = iota
	modelRowOption
)

type modelRow struct {
	kind     modelRowKind
	provider string
	id       string
	name     string
	context  string
	current  bool
}

// buildModelRows groups winners by provider for the picker. The Free Tier
// option always leads. A configured model missing from the catalogue (custom
// IDs, stale cache) is shown under its own Current group so it stays visible
// and selectable.
func buildModelRows(
	models []openrouter_request.OpenRouterModel,
	currentID string,
) []modelRow {
	currentID = strings.TrimSpace(currentID)
	known := make(map[string]bool, len(models)+1)
	known[openrouter_request.FreeTierModel] = true
	for _, model := range models {
		known[model.ID] = true
	}

	var rows []modelRow
	rows = append(rows, modelRow{kind: modelRowHeader, provider: "Pearl"})
	rows = append(rows, modelRow{
		kind: modelRowOption, provider: "Pearl",
		id:      openrouter_request.FreeTierModel,
		name:    "Free Tier",
		current: currentID == openrouter_request.FreeTierModel,
	})
	if currentID != "" && !known[currentID] {
		rows = append(rows, modelRow{kind: modelRowHeader, provider: "Current"})
		rows = append(rows, modelRow{
			kind: modelRowOption, provider: "Current",
			id: currentID, name: "Custom model", current: true,
		})
	}

	lastProvider := ""
	for _, model := range models {
		provider := openrouter_request.ModelProvider(model.ID)
		if provider != lastProvider {
			rows = append(rows, modelRow{kind: modelRowHeader, provider: provider})
			lastProvider = provider
		}
		name := strings.TrimSpace(model.Name)
		rows = append(rows, modelRow{
			kind: modelRowOption, provider: provider,
			id:      model.ID,
			name:    name,
			context: openrouter_request.FormatContextLength(model.ContextLength),
			current: model.ID == currentID,
		})
	}
	return rows
}

// filterModelRows keeps options matching the query (case-insensitive substring
// over provider, ID and name) plus the headers above surviving options.
func filterModelRows(rows []modelRow, query string) []modelRow {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return rows
	}
	var filtered []modelRow
	var pendingHeader *modelRow
	flushHeader := func() {
		if pendingHeader != nil {
			filtered = append(filtered, *pendingHeader)
			pendingHeader = nil
		}
	}
	for _, row := range rows {
		if row.kind == modelRowHeader {
			header := row
			pendingHeader = &header
			continue
		}
		haystack := strings.ToLower(row.provider + " " + row.id + " " + row.name)
		if strings.Contains(haystack, query) {
			flushHeader()
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// selectableModelIndexes returns visible indexes holding options.
func selectableModelIndexes(rows []modelRow) []int {
	var indexes []int
	for index, row := range rows {
		if row.kind == modelRowOption {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// initialModelSelection prefers the current model, else the first option.
func initialModelSelection(rows []modelRow) int {
	selectable := selectableModelIndexes(rows)
	if len(selectable) == 0 {
		return 0
	}
	for position, index := range selectable {
		if rows[index].current {
			return position
		}
	}
	return 0
}

type modelScreen struct {
	Frame      string
	BodyHeight int
}

func renderModelScreen(
	visible []modelRow,
	selected, scroll int,
	filter, currentID, notice string,
	width, height int,
	color bool,
) modelScreen {
	width = max(20, width)
	height = max(10, height)
	bodyHeight := height - 7
	selectable := selectableModelIndexes(visible)
	if len(selectable) > 0 {
		selected = min(max(0, selected), len(selectable)-1)
		anchor := selectable[selected]
		if anchor < scroll {
			scroll = anchor
		} else if anchor >= scroll+bodyHeight {
			scroll = anchor - bodyHeight + 1
		}
		scroll = min(max(0, scroll), max(0, len(visible)-bodyHeight))
	} else {
		selected, scroll = 0, 0
	}

	var frame strings.Builder
	title := "Pearl · model"
	count := fmt.Sprintf("%d models", len(selectable))
	if len([]rune(title))+len([]rune(count))+1 <= width {
		title += strings.Repeat(" ", width-len([]rune(title))-len([]rune(count))) + count
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate(title, width)))
	divider := strings.Repeat("─", width)
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	status := "Current: " + currentID
	if currentID == "" {
		status = "Current: (unset)"
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(status, width)))
	filterLine := "Filter: " + filter + "▏"
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(filterLine, width)))
	if notice != "" {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiYellow, dashboardTruncate(notice, width)))
	} else {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	}

	end := min(len(visible), scroll+bodyHeight)
	shown := 0
	for index := scroll; index < end; index++ {
		row := visible[index]
		if row.kind == modelRowHeader {
			label := "─ " + row.provider + " ─"
			fmt.Fprintln(&frame, dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate(label, width)))
			shown++
			continue
		}
		isSelected := len(selectable) > 0 && selectable[selected] == index
		fmt.Fprintln(&frame, renderModelOption(row, isSelected, width, color))
		shown++
	}
	for row := shown; row < bodyHeight; row++ {
		fmt.Fprintln(&frame)
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	position := "0/0"
	if len(selectable) > 0 {
		position = fmt.Sprintf("%d/%d", selected+1, len(selectable))
	}
	hint := "↑/↓ navigate · Enter select · type to filter · q quit"
	if len([]rune(hint))+len([]rune(position))+1 <= width {
		hint += strings.Repeat(" ", width-len([]rune(hint))-len([]rune(position))) + position
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(hint, width)))
	return modelScreen{Frame: frame.String(), BodyHeight: bodyHeight}
}

func renderModelOption(row modelRow, selected bool, width int, color bool) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	marker := "  "
	if row.current {
		marker = "● "
	}
	label := row.id
	if row.name != "" && !strings.EqualFold(row.name, row.id) {
		short := row.name
		if providerPrefix := row.provider + ": "; strings.HasPrefix(short, providerPrefix) {
			short = strings.TrimPrefix(short, providerPrefix)
		}
		label += "  " + short
	}
	if row.context != "" {
		label += "  (" + row.context + " ctx)"
	}
	line := prefix + marker + label
	line = dashboardTruncate(line, width)
	if !color {
		return line
	}
	if selected {
		return dashboardPaint(true, ansiBold+ansiCyan, line)
	}
	if row.current {
		return dashboardPaint(true, ansiGreen, line)
	}
	return line
}

// runModelTUI opens the interactive picker and returns the chosen model ID,
// or "" when the user quits without choosing.
func runModelTUI(
	input, output *os.File,
	rows []modelRow,
	currentID, notice string,
) (string, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return "", fmt.Errorf("enable model input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)

	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")

	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	var filter strings.Builder
	visible := rows
	selected := initialModelSelection(visible)
	scroll := 0

	draw := func() modelScreen {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		screen := renderModelScreen(
			visible, selected, scroll,
			filter.String(), currentID, notice, width, height, color,
		)
		fmt.Fprint(output, "\x1b[H\x1b[2J", screen.Frame)
		return screen
	}
	screen := draw()
	_ = screen

	refilter := func() {
		previous := ""
		if selectable := selectableModelIndexes(visible); len(selectable) > 0 &&
			selected < len(selectable) {
			previous = visible[selectable[selected]].id
		}
		visible = filterModelRows(rows, filter.String())
		selectable := selectableModelIndexes(visible)
		selected = 0
		for position, index := range selectable {
			if visible[index].id == previous {
				selected = position
				break
			}
		}
		scroll = 0
		if len(selectable) == 0 {
			notice = "No models match."
		} else {
			notice = ""
		}
	}

	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		character, _, err := reader.ReadRune()
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
			selectable := selectableModelIndexes(visible)
			switch action {
			case "previous":
				if len(selectable) > 0 {
					selected = (selected - 1 + len(selectable)) % len(selectable)
				}
			case "next":
				if len(selectable) > 0 {
					selected = (selected + 1) % len(selectable)
				}
			case "page_up":
				selected -= max(1, screen.BodyHeight-1)
			case "page_down":
				selected += max(1, screen.BodyHeight-1)
			default:
				continue
			}
			if selectableCount := len(selectableModelIndexes(visible)); selectableCount > 0 {
				selected = min(max(0, selected), selectableCount-1)
			}
			screen = draw()
			continue
		}
		switch character {
		case '\x1b':
			escapeSequence = "\x1b"
			continue
		case 3, 4:
			return "", nil
		case 'q', 'Q':
			return "", nil
		case '\r', '\n':
			if selectable := selectableModelIndexes(visible); len(selectable) > 0 {
				return visible[selectable[selected]].id, nil
			}
			continue
		case 127, 8:
			text := filter.String()
			if text != "" {
				runes := []rune(text)
				filter.Reset()
				filter.WriteString(string(runes[:len(runes)-1]))
				refilter()
				screen = draw()
			}
			continue
		case 21:
			filter.Reset()
			refilter()
			screen = draw()
			continue
		}
		if character == 'j' || character == 'k' {
			// Vim-style navigation when the filter is empty; typed text
			// filters once the user starts searching.
			if filter.Len() == 0 {
				selectable := selectableModelIndexes(visible)
				if len(selectable) > 0 {
					if character == 'j' {
						selected = (selected + 1) % len(selectable)
					} else {
						selected = (selected - 1 + len(selectable)) % len(selectable)
					}
					screen = draw()
				}
				continue
			}
		}
		if character < 32 || character == 127 {
			continue
		}
		filter.WriteRune(character)
		refilter()
		screen = draw()
	}
}
