package cli

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

type modelRowKind int

const (
	modelRowHeader modelRowKind = iota
	modelRowOption
	modelRowLab
)

type modelRow struct {
	kind     modelRowKind
	provider string
	id       string
	name     string
	context  string
	current  bool
	label    string
	starred  bool
}

var starredModelLabs = []struct{ provider, label string }{
	{"anthropic", "Anthropic"},
	{"openai", "OpenAI"},
	{"google", "Google"},
	{"x-ai", "Grok"},
	{"deepseek", "DeepSeek"},
	{"meta", "Meta"},
	{"moonshotai", "Kimi"},
	{"z-ai", "GLM"},
}

func modelLabRank(provider string) int {
	for rank, lab := range starredModelLabs {
		if lab.provider == provider {
			return rank
		}
	}
	return len(starredModelLabs)
}

func modelLabDisplayName(provider string) string {
	if rank := modelLabRank(provider); rank < len(starredModelLabs) {
		return starredModelLabs[rank].label
	}
	return provider
}

func modelLabTitle(row modelRow) string {
	label := modelLabDisplayName(row.provider)
	if row.starred {
		label = "★ " + label
	}
	return label
}

// buildModelRows groups models added within the past calendar year by
// provider, newest first. Undated and future-dated models, media models, batch
// variants, and GPT Pro variants are hidden. The Free Tier router always leads.
func buildModelRows(
	models []openrouter_request.OpenRouterModel,
	currentID string,
) []modelRow {
	return buildModelRowsAt(models, currentID, time.Now())
}

func buildModelRowsAt(
	models []openrouter_request.OpenRouterModel,
	currentID string,
	now time.Time,
) []modelRow {
	currentID = strings.TrimSpace(currentID)
	cutoff := now.AddDate(-1, 0, 0).Unix()
	latest := now.Unix()
	models = append([]openrouter_request.OpenRouterModel(nil), models...)
	sort.Slice(models, func(left, right int) bool {
		lProvider := openrouter_request.ModelProvider(models[left].ID)
		rProvider := openrouter_request.ModelProvider(models[right].ID)
		if lProvider != rProvider {
			return lProvider < rProvider
		}
		if models[left].Created != models[right].Created {
			return models[left].Created > models[right].Created
		}
		return models[left].ID < models[right].ID
	})
	var rows []modelRow
	rows = append(rows, modelRow{kind: modelRowHeader, provider: "Pearl"})
	rows = append(rows, modelRow{
		kind: modelRowOption, provider: "Pearl",
		id:      openrouter_request.FreeTierModel,
		name:    "Free Tier",
		current: currentID == openrouter_request.FreeTierModel,
	})
	lastProvider := ""
	seen := map[string]bool{openrouter_request.FreeTierModel: true}
	for _, model := range models {
		if model.Created <= 0 || model.Created < cutoff || model.Created > latest {
			continue
		}
		if model.ID == "" || seen[model.ID] || isHiddenCatalogueModel(model) {
			continue
		}
		seen[model.ID] = true
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

func isHiddenCatalogueModel(model openrouter_request.OpenRouterModel) bool {
	for _, output := range model.OutputModalities {
		if !strings.EqualFold(strings.TrimSpace(output), "text") {
			return true
		}
	}
	if len(model.InputModalities) > 0 {
		textInput := false
		for _, input := range model.InputModalities {
			textInput = textInput || strings.EqualFold(strings.TrimSpace(input), "text")
		}
		if !textInput {
			return true
		}
	}
	// Older caches lack modality metadata. Hide explicitly named media models
	// there too, while retaining ordinary chat and custom model IDs.
	if len(model.OutputModalities) == 0 {
		for _, token := range strings.FieldsFunc(strings.ToLower(model.ID+" "+model.Name), func(character rune) bool {
			return !unicode.IsLetter(character) && !unicode.IsDigit(character)
		}) {
			switch token {
			case "image", "video", "audio", "speech", "tts", "transcription", "realtime", "whisper", "lyria":
				return true
			}
		}
		if strings.Contains(strings.ToLower(model.ID), "nano-banana") {
			return true
		}
	}
	id := strings.ToLower(strings.TrimSpace(model.ID))
	if strings.HasSuffix(id, ":batch") {
		return true
	}
	base, _, _ := strings.Cut(id, ":")
	if strings.HasPrefix(base, "openai/gpt-") {
		for _, token := range strings.Split(base, "-") {
			if token == "pro" {
				return true
			}
		}
	}
	return false
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
		haystack := strings.ToLower(row.provider + " " + row.id + " " + row.name + " " + row.label)
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
		if row.kind == modelRowOption || row.kind == modelRowLab {
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
	Scroll     int
}

func renderModelScreen(
	visible []modelRow,
	selected, scroll int,
	filter, currentID, notice string,
	width, height int,
	color bool,
) modelScreen {
	return renderModelScreenCore(visible, selected, scroll, filter, currentID, notice, width, height, color, nil)
}

func renderModelPickerScreen(state *modelPickerState, notice string, width, height int, color bool) modelScreen {
	return renderModelScreenCore(state.visible, state.selected, state.scroll, state.filter,
		state.currentID, notice, width, height, color, state)
}

func renderModelScreenCore(
	visible []modelRow,
	selected, scroll int,
	filter, currentID, notice string,
	width, height int,
	color bool,
	picker *modelPickerState,
) modelScreen {
	width = max(1, width)
	height = max(1, height)
	footerHeight := min(2, height-1)
	headerHeight := min(5, height-footerHeight-1)
	bodyHeight := height - headerHeight - footerHeight
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
	labs := len(visible) > 0 && visible[0].kind == modelRowLab
	if picker != nil {
		labs = picker.lab == ""
	}
	if labs {
		title += " · labs"
		count = fmt.Sprintf("%d labs", len(selectable))
	} else if picker != nil {
		title += " · " + modelLabDisplayName(picker.lab)
	} else if len(visible) > 0 && visible[0].kind == modelRowOption {
		title += " · " + visible[0].provider
	}
	if len([]rune(title))+len([]rune(count))+1 <= width {
		title += strings.Repeat(" ", width-len([]rune(title))-len([]rune(count))) + count
	}
	divider := strings.Repeat("─", width)
	status := "Current: " + currentID
	if currentID == "" {
		status = "Current: (unset)"
	}
	filterLine := "Filter: " + filter + "▏"
	if picker != nil && !picker.filtering {
		filterLine = "Filter: / to search"
	}
	headers := []string{
		dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate(title, width)),
		dashboardPaint(color, ansiDim, divider),
		dashboardPaint(color, ansiDim, dashboardTruncate(status, width)),
		dashboardPaint(color, ansiDim, dashboardTruncate(filterLine, width)),
	}
	if notice != "" {
		headers = append(headers, dashboardPaint(color, ansiYellow, dashboardTruncate(notice, width)))
	} else {
		headers = append(headers, dashboardPaint(color, ansiDim, divider))
	}
	for _, header := range headers[:headerHeight] {
		fmt.Fprintln(&frame, header)
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
		if row.kind == modelRowLab {
			row.id, row.context = modelLabTitle(row), ""
		}
		fmt.Fprintln(&frame, renderModelOption(row, isSelected, width, color))
		shown++
	}
	for row := shown; row < bodyHeight; row++ {
		fmt.Fprintln(&frame)
	}
	if footerHeight > 1 {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	}
	position := "0/0"
	if len(selectable) > 0 {
		position = fmt.Sprintf("%d/%d", selected+1, len(selectable))
	}
	hint := "↑/↓/scroll · Enter select · ← labs · / filter · q back"
	if labs {
		hint = "↑/↓/scroll · Enter open lab · / filter · q back"
	}
	if picker != nil && picker.filtering {
		hint = "↑/↓/scroll · Enter select · ← labs · Ctrl-U clear · Ctrl-C quit"
		if labs {
			hint = "↑/↓/scroll · Enter open lab · Ctrl-U clear · Ctrl-C quit"
		}
	}
	if len([]rune(hint))+len([]rune(position))+1 <= width {
		hint += strings.Repeat(" ", width-len([]rune(hint))-len([]rune(position))) + position
	}
	if footerHeight > 0 {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(hint, width)))
	}
	return modelScreen{Frame: strings.TrimSuffix(frame.String(), "\n"), BodyHeight: bodyHeight, Scroll: scroll}
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
	if row.kind == modelRowLab {
		label += "  " + row.name
	} else if name := strings.TrimSpace(row.name); name != "" {
		label = name
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

// modelPickerState is shared by the standalone picker and the dashboard.
type modelPickerState struct {
	rows         []modelRow
	visible      []modelRow
	lab          string
	filter       string
	filtering    bool
	selected     int
	scroll       int
	bodyHeight   int
	labSelected  int
	labScroll    int
	labFilter    string
	labFiltering bool
	currentID    string
	notice       string
	wheel        listWheelNavigation
}

func buildModelLabRows(rows []modelRow) []modelRow {
	var labs []modelRow
	indexes := make(map[string]int)
	counts := make(map[string]int)
	for _, row := range rows {
		if row.kind != modelRowOption {
			continue
		}
		index, found := indexes[row.provider]
		if !found {
			index = len(labs)
			indexes[row.provider] = index
			labs = append(labs, modelRow{kind: modelRowLab, provider: row.provider, id: row.provider})
		}
		counts[row.provider]++
		labs[index].current = labs[index].current || row.current
	}
	for index := range labs {
		labs[index].label = modelLabDisplayName(labs[index].provider)
		labs[index].starred = modelLabRank(labs[index].provider) < len(starredModelLabs)
		count := counts[labs[index].provider]
		labs[index].name = fmt.Sprintf("%d models", count)
		if count == 1 {
			labs[index].name = "1 model"
		}
	}
	sort.SliceStable(labs, func(left, right int) bool {
		return modelLabRank(labs[left].provider) < modelLabRank(labs[right].provider)
	})
	return labs
}

func newModelPickerState(rows []modelRow, currentID, notice string) *modelPickerState {
	state := &modelPickerState{rows: rows, currentID: currentID, notice: notice}
	state.visible = buildModelLabRows(rows)
	state.selected = initialModelSelection(state.visible)
	return state
}

func (state *modelPickerState) scope() []modelRow {
	if state.lab == "" {
		return buildModelLabRows(state.rows)
	}
	var rows []modelRow
	for _, row := range state.rows {
		if row.kind == modelRowOption && row.provider == state.lab {
			rows = append(rows, row)
		}
	}
	return rows
}

func (state *modelPickerState) refilter() {
	previous := ""
	if indexes := selectableModelIndexes(state.visible); state.selected >= 0 && state.selected < len(indexes) {
		previous = state.visible[indexes[state.selected]].id
	}
	state.visible = filterModelRows(state.scope(), state.filter)
	state.selected, state.scroll = 0, 0
	for position, index := range selectableModelIndexes(state.visible) {
		if state.visible[index].id == previous {
			state.selected = position
			break
		}
	}
}

func (state *modelPickerState) back() bool {
	if state.lab == "" {
		return true
	}
	state.lab = ""
	state.filter, state.filtering = state.labFilter, state.labFiltering
	state.visible = filterModelRows(state.scope(), state.filter)
	state.selected, state.scroll = state.labSelected, state.labScroll
	return false
}

// activate opens a lab or returns the selected model ID.
func (state *modelPickerState) activate() string {
	indexes := selectableModelIndexes(state.visible)
	if state.selected < 0 || state.selected >= len(indexes) {
		return ""
	}
	row := state.visible[indexes[state.selected]]
	if row.kind == modelRowOption {
		return row.id
	}
	state.labSelected, state.labScroll = state.selected, state.scroll
	state.labFilter, state.labFiltering = state.filter, state.filtering
	state.lab, state.filter, state.filtering = row.provider, "", false
	state.visible = state.scope()
	state.selected, state.scroll = initialModelSelection(state.visible), 0
	return ""
}

func (state *modelPickerState) applyAction(action string) bool {
	return state.applyActionAt(action, time.Now())
}

func (state *modelPickerState) applyActionAt(action string, now time.Time) bool {
	action = state.wheel.filter(action, now)
	count := len(selectableModelIndexes(state.visible))
	switch action {
	case "previous", "scroll_up":
		state.selected--
	case "next", "scroll_down":
		state.selected++
	case "page_up":
		state.selected -= max(1, state.bodyHeight-1)
	case "page_down":
		state.selected += max(1, state.bodyHeight-1)
	case "home":
		state.selected = 0
	case "end":
		state.selected = count - 1
	case "expand":
		if state.lab == "" {
			state.activate()
		}
	case "collapse":
		if state.lab != "" {
			state.back()
		}
	default:
		return false
	}
	state.selected = min(max(0, state.selected), max(0, len(selectableModelIndexes(state.visible))-1))
	return true
}

// handleKey returns a model ID or a request to close the picker.
func (state *modelPickerState) handleKey(character rune) (string, bool) {
	state.wheel = listWheelNavigation{}
	switch character {
	case 3, 4:
		return "", true
	case 'q', 'Q':
		if !state.filtering {
			return "", state.back()
		}
	case '\r', '\n':
		id := state.activate()
		return id, id != ""
	case '\t':
		state.applyAction("next")
		return "", false
	case 127, 8:
		if state.filter != "" {
			runes := []rune(state.filter)
			state.filter = string(runes[:len(runes)-1])
			state.refilter()
		} else if state.filtering {
			state.filtering = false
		} else {
			return "", state.back()
		}
		return "", false
	case 21:
		state.filter, state.filtering = "", false
		state.refilter()
		return "", false
	case '/':
		if !state.filtering {
			state.filtering = true
			return "", false
		}
	case 'j', 'k':
		if !state.filtering {
			action := "next"
			if character == 'k' {
				action = "previous"
			}
			state.applyAction(action)
			return "", false
		}
	}
	if !unicode.IsControl(character) {
		state.filtering = true
		state.filter += string(character)
		state.refilter()
	}
	return "", false
}

func (state *modelPickerState) screen(width, height int, color bool) modelScreen {
	notice := state.notice
	if len(state.visible) == 0 {
		notice = "No models match."
		if state.lab == "" {
			notice = "No labs match."
		}
	}
	screen := renderModelPickerScreen(state, notice, width, height, color)
	state.bodyHeight, state.scroll = screen.BodyHeight, screen.Scroll
	return screen
}

// runModelTUI returns the chosen model ID, or "" when the user leaves.
func runModelTUI(input, output *os.File, rows []modelRow, currentID, notice string) (string, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return "", fmt.Errorf("enable model input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)
	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l", jobsListMouseEnable)
	defer fmt.Fprint(output, jobsListMouseDisable, "\x1b[?25h\x1b[?1049l")
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	state := newModelPickerState(rows, currentID, notice)
	draw := func() {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		screen := state.screen(width, height, color)
		fmt.Fprint(output, "\x1b[H", listTerminalFrame(screen.Frame))
	}
	draw()
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
			if !state.applyAction(action) {
				continue
			}
		} else if character == '\x1b' {
			escapeSequence = "\x1b"
			continue
		} else if id, done := state.handleKey(character); done {
			return id, nil
		}
		draw()
	}
}
