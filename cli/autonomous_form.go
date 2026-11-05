package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const (
	autonomousFieldGoal = iota
	autonomousFieldRepository
	autonomousFieldOrchestrator
	autonomousFieldSubagents
	autonomousFieldMax
	autonomousFieldStart
	autonomousFieldCount

	autonomousDefaultSubagents = 3
	autonomousGoalRows         = 5
)

type autonomousFormResult int

const (
	autonomousFormEditing autonomousFormResult = iota
	autonomousFormCancelled
	autonomousFormSubmitted
	autonomousFormOpenLatest
	// autonomousFormBrowse asks the caller to open the system's folder picker.
	autonomousFormBrowse
)

// chooseAutonomousRepository opens the system's folder picker. Tests replace it.
var chooseAutonomousRepository = chooseJobDirectory

// autonomousForm is the box for starting a session.
type autonomousForm struct {
	focus        int
	goal         string
	repository   string
	models       []store.AutonomousModel
	loaded       bool
	orchestrator int
	allModels    bool
	allowed      map[string]bool
	maxText      string
	notice       string
	picker       *autonomousModelPicker
}

// autonomousModelPicker chooses from the models, one for the orchestrator or any
// number for the subagents.
type autonomousModelPicker struct {
	multiple bool
	filter   string
	selected int
	scroll   int
}

// newAutonomousForm starts with the goal, if any, and the folder to work in, which
// is the current directory unless the user named another.
func newAutonomousForm(goal, repository string) *autonomousForm {
	return &autonomousForm{
		goal: strings.TrimSpace(goal), repository: displayJobDirectory(repository),
		allModels: true, allowed: map[string]bool{},
		maxText: strconv.Itoa(autonomousDefaultSubagents),
	}
}

// expandHome turns a leading ~ into the home folder, as a shell would.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// repositoryPath resolves what's typed in Repository.
func (form *autonomousForm) repositoryPath() (string, error) {
	typed := strings.TrimSpace(form.repository)
	if typed == "" {
		return "", errors.New("Enter a repository folder.")
	}
	path, err := explicitWorkspace(expandHome(typed))
	if err != nil {
		return "", fmt.Errorf("Repository: %s isn't a folder Pearl can open.", typed)
	}
	return path, nil
}

// browseStart is where the folder picker opens: the typed folder if it exists.
func (form *autonomousForm) browseStart() string {
	if path, err := form.repositoryPath(); err == nil {
		return path
	}
	return currentWorkspace()
}

// finishBrowse takes the folder picker's answer.
func (form *autonomousForm) finishBrowse(path string, err error) {
	switch {
	case errors.Is(err, errDirectoryPickerCancelled):
	case err != nil:
		form.notice = "Repository: " + dashboardText(err.Error()) + ". Type the path instead."
	default:
		form.repository = displayJobDirectory(resolveWorkspacePath(path))
		form.notice = ""
	}
}

func autonomousModelKey(model store.AutonomousModel) string {
	return model.Provider + "/" + model.ID
}

func autonomousModelName(model store.AutonomousModel) string {
	name := strings.TrimSpace(model.Name)
	if name == "" {
		name = modelLabel(model.ID)
	}
	return dashboardText(name)
}

func autonomousProviderName(provider string) string {
	switch provider {
	case openrouter_request.ProviderClaude:
		return "Anthropic"
	case openrouter_request.ProviderChatGPT:
		return "OpenAI"
	case openrouter_request.ProviderAntigravity:
		return "Google"
	case openrouter_request.ProviderOpenRouter:
		return "OpenRouter"
	}
	return provider
}

// loadAutonomousModels lists what can be assigned, the same models as `pearl model`.
// It starts on the selected model.
func loadAutonomousModels(ctx context.Context) ([]store.AutonomousModel, int, string, error) {
	rows, notice, err := modelChoices(ctx, currentModelID(), false)
	if err != nil {
		return nil, 0, "", err
	}
	var models []store.AutonomousModel
	current := 0
	for _, row := range rows {
		if row.kind != modelRowOption {
			continue
		}
		provider := row.connection
		if provider == "" {
			provider = openrouter_request.ProviderOpenRouter
		}
		if row.current {
			current = len(models)
		}
		models = append(models, store.AutonomousModel{Provider: provider, ID: row.id, Name: row.name})
	}
	if len(models) == 0 {
		return nil, 0, "", errors.New("no models are available; sign in with `pearl auth login`")
	}
	return models, current, notice, nil
}

func (form *autonomousForm) setModels(models []store.AutonomousModel, current int, notice string, err error) {
	form.loaded = true
	form.models, form.orchestrator = models, current
	if err != nil {
		notice = "Models: " + err.Error()
	}
	form.notice = dashboardText(notice)
}

func (form *autonomousForm) subagentsSummary() string {
	switch {
	case !form.loaded:
		return "Loading models..."
	case len(form.models) == 0:
		return "No models available"
	case form.allModels:
		return fmt.Sprintf("All models (%d)", len(form.models))
	}
	var names []string
	for _, model := range form.models {
		if form.allowed[autonomousModelKey(model)] {
			names = append(names, autonomousModelName(model))
		}
	}
	if len(names) == 0 {
		return "None chosen"
	}
	return strings.Join(names, ", ")
}

func (form *autonomousForm) orchestratorSummary() string {
	switch {
	case !form.loaded:
		return "Loading models..."
	case len(form.models) == 0:
		return "No models available"
	}
	model := form.models[form.orchestrator]
	return autonomousModelName(model) + " · " + autonomousProviderName(model.Provider)
}

// config checks the form and returns what to start.
func (form *autonomousForm) config() (store.AutonomousConfig, error) {
	if strings.TrimSpace(form.goal) == "" {
		form.focus = autonomousFieldGoal
		return store.AutonomousConfig{}, errors.New("Enter a goal.")
	}
	workspace, err := form.repositoryPath()
	if err != nil {
		form.focus = autonomousFieldRepository
		return store.AutonomousConfig{}, err
	}
	if !form.loaded {
		return store.AutonomousConfig{}, errors.New("The models are still loading.")
	}
	if len(form.models) == 0 {
		return store.AutonomousConfig{}, errors.New("No models are available. Sign in with `pearl auth login`.")
	}
	maxSubagents, err := strconv.Atoi(form.maxText)
	if err != nil || maxSubagents < 1 || maxSubagents > daemon.AutonomousMaxSubagents {
		form.focus = autonomousFieldMax
		return store.AutonomousConfig{}, fmt.Errorf("Max Subagents must be from 1 to %d.", daemon.AutonomousMaxSubagents)
	}
	allowed := form.models
	if !form.allModels {
		allowed = nil
		for _, model := range form.models {
			if form.allowed[autonomousModelKey(model)] {
				allowed = append(allowed, model)
			}
		}
		if len(allowed) == 0 {
			form.focus = autonomousFieldSubagents
			return store.AutonomousConfig{}, errors.New("Choose at least one subagent model.")
		}
	}
	return store.AutonomousConfig{
		Goal: strings.TrimSpace(form.goal), WorkspaceRoot: workspace,
		Orchestrator:  form.models[form.orchestrator],
		AllowedModels: allowed, AllModels: form.allModels, MaxSubagents: maxSubagents,
	}, nil
}

func (form *autonomousForm) moveFocus(delta int) {
	form.focus = (form.focus + delta + autonomousFieldCount) % autonomousFieldCount
}

// addText adds pasted text or a new line to the goal, or a pasted path to Repository.
func (form *autonomousForm) addText(text string) {
	if form.picker != nil {
		return
	}
	if form.focus == autonomousFieldRepository {
		form.repository += strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, text))
		return
	}
	if form.focus != autonomousFieldGoal {
		form.focus = autonomousFieldGoal
	}
	if len(form.goal)+len(text) > dashboardDraftLimit {
		form.notice = "Goal exceeds 1 MB; paste was not added."
		return
	}
	form.goal += text
}

// handleKey takes one key press.
func (form *autonomousForm) handleKey(key rune) autonomousFormResult {
	if form.picker != nil {
		form.pickerKey(key)
		return autonomousFormEditing
	}
	switch key {
	case 3, 4, escapeKey:
		return autonomousFormCancelled
	case 15: // Ctrl+O
		return autonomousFormOpenLatest
	case 19: // Ctrl+S
		return autonomousFormSubmitted
	case '\t':
		form.moveFocus(1)
		return autonomousFormEditing
	}
	form.notice = ""
	switch form.focus {
	case autonomousFieldGoal:
		switch {
		case key == '\r':
			var continued bool
			if form.goal, continued = composerContinueLine(form.goal); !continued {
				form.moveFocus(1)
			}
		case key == 127 || key == 8:
			if runes := []rune(form.goal); len(runes) > 0 {
				form.goal = string(runes[:len(runes)-1])
			}
		case key == 21: // Ctrl+U
			form.goal = ""
		case key >= 32 && key != 127 && len(form.goal) < dashboardDraftLimit:
			form.goal += string(key)
		}
	case autonomousFieldRepository:
		switch {
		case key == '\r':
			form.moveFocus(1)
		case key == 6: // Ctrl+F
			return autonomousFormBrowse
		case key == 127 || key == 8:
			if runes := []rune(form.repository); len(runes) > 0 {
				form.repository = string(runes[:len(runes)-1])
			}
		case key == 21: // Ctrl+U
			form.repository = ""
		case key >= 32 && key != 127 && len(form.repository) < 4096:
			form.repository += string(key)
		}
	case autonomousFieldOrchestrator, autonomousFieldSubagents:
		if (key == '\r' || key == ' ') && form.loaded && len(form.models) > 0 {
			form.openPicker(form.focus == autonomousFieldSubagents)
		}
	case autonomousFieldMax:
		switch {
		case key == '\r':
			form.moveFocus(1)
		case key == 127 || key == 8:
			if form.maxText != "" {
				form.maxText = form.maxText[:len(form.maxText)-1]
			}
		case key >= '0' && key <= '9' && len(form.maxText) < 3:
			form.maxText += string(key)
		}
	case autonomousFieldStart:
		if key == '\r' || key == ' ' {
			return autonomousFormSubmitted
		}
	}
	return autonomousFormEditing
}

// applyAction takes an arrow key or the mouse wheel. It returns whether to redraw.
func (form *autonomousForm) applyAction(action string) bool {
	if form.picker != nil {
		return form.pickerAction(action)
	}
	switch action {
	case "previous", "previous_field":
		form.moveFocus(-1)
	case "next":
		form.moveFocus(1)
	case "expand", "collapse":
		if form.focus != autonomousFieldMax {
			return false
		}
		delta := 1
		if action == "collapse" {
			delta = -1
		}
		value, err := strconv.Atoi(form.maxText)
		if err != nil {
			value = autonomousDefaultSubagents
			delta = 0
		}
		form.maxText = strconv.Itoa(min(max(1, value+delta), daemon.AutonomousMaxSubagents))
	default:
		return false
	}
	form.notice = ""
	return true
}

func (form *autonomousForm) openPicker(multiple bool) {
	form.notice = ""
	form.picker = &autonomousModelPicker{multiple: multiple}
	if !multiple {
		form.picker.selected = form.orchestrator
	}
}

// pickerRows returns the indexes of the models that match the search.
func (form *autonomousForm) pickerRows() []int {
	filter := strings.ToLower(form.picker.filter)
	rows := make([]int, 0, len(form.models))
	for index, model := range form.models {
		text := strings.ToLower(autonomousModelName(model) + " " + model.ID + " " + autonomousProviderName(model.Provider))
		if strings.Contains(text, filter) {
			rows = append(rows, index)
		}
	}
	return rows
}

// pickerLength counts the rows: the models, and for subagents "All models" on top.
func (form *autonomousForm) pickerLength() int {
	length := len(form.pickerRows())
	if form.picker.multiple {
		length++
	}
	return length
}

func (form *autonomousForm) pickerKey(key rune) {
	picker := form.picker
	rows := form.pickerRows()
	switch key {
	case 3, 4, escapeKey:
		if picker.filter != "" && key == escapeKey {
			picker.filter, picker.selected, picker.scroll = "", 0, 0
			return
		}
		form.picker = nil
	case '\r':
		if !picker.multiple {
			if picker.selected < len(rows) {
				form.orchestrator = rows[picker.selected]
				form.picker = nil
			}
			return
		}
		form.normalizeAllowed()
		form.picker = nil
	case ' ':
		if picker.multiple {
			form.toggleRow(rows)
		}
	case 127, 8:
		if runes := []rune(picker.filter); len(runes) > 0 {
			picker.filter = string(runes[:len(runes)-1])
			picker.selected, picker.scroll = 0, 0
		}
	default:
		if key > 32 {
			picker.filter += string(key)
			picker.selected, picker.scroll = 0, 0
		}
	}
}

// toggleRow changes one row. "All models" starts a whitelist from nothing, which is
// the quick way to allow just a few. Unchecking one model while all are allowed
// keeps the others.
func (form *autonomousForm) toggleRow(rows []int) {
	picker := form.picker
	if picker.selected == 0 {
		form.allModels = !form.allModels
		form.allowed = map[string]bool{}
		return
	}
	if picker.selected-1 >= len(rows) {
		return
	}
	if form.allModels {
		form.allModels = false
		for _, model := range form.models {
			form.allowed[autonomousModelKey(model)] = true
		}
	}
	key := autonomousModelKey(form.models[rows[picker.selected-1]])
	form.allowed[key] = !form.allowed[key]
	form.normalizeAllowed()
}

// normalizeAllowed treats every model being checked as "All models".
func (form *autonomousForm) normalizeAllowed() {
	if form.allModels {
		return
	}
	for _, model := range form.models {
		if !form.allowed[autonomousModelKey(model)] {
			return
		}
	}
	form.allModels = true
	form.allowed = map[string]bool{}
}

func (form *autonomousForm) pickerAction(action string) bool {
	picker := form.picker
	length := form.pickerLength()
	if length == 0 {
		return false
	}
	switch action {
	case "previous", "scroll_up":
		picker.selected = max(0, picker.selected-1)
	case "next", "scroll_down":
		picker.selected = min(length-1, picker.selected+1)
	case "page_up":
		picker.selected = max(0, picker.selected-10)
	case "page_down":
		picker.selected = min(length-1, picker.selected+10)
	case "home":
		picker.selected = 0
	case "end":
		picker.selected = length - 1
	default:
		return false
	}
	return true
}

func autonomousBoxLine(content string, contentWidth, inner int) string {
	return "│ " + content + strings.Repeat(" ", max(0, inner-contentWidth)) + " │"
}

// render draws the whole screen, exactly height lines tall.
func (form *autonomousForm) render(width, height int, color bool) string {
	width, height = max(24, width), max(10, height)
	inner := min(width, 84) - 4
	var lines []string
	add := func(text string) {
		text = dashboardTruncate(text, inner)
		lines = append(lines, autonomousBoxLine(text, len([]rune(text)), inner))
	}
	addStyled := func(code, text string) {
		text = dashboardTruncate(text, inner)
		lines = append(lines, autonomousBoxLine(dashboardPaint(color, code, text), len([]rune(text)), inner))
	}
	title := " Autonomous "
	lines = append(lines, "╭─"+dashboardPaint(color, ansiBold+ansiCyan, title)+strings.Repeat("─", max(0, inner-len(title)))+"─╮")
	var hint string
	if form.picker != nil {
		hint = form.renderPicker(add, addStyled, inner, height, color)
	} else {
		hint = form.renderFields(add, addStyled, inner, height, color)
	}
	lines = append(lines, "╰"+strings.Repeat("─", inner+2)+"╯")
	if form.notice != "" {
		lines = append(lines, dashboardPaint(color, ansiYellow, dashboardTruncate(form.notice, width)))
	}
	lines = append(lines, dashboardPaint(color, ansiDim, dashboardTruncate(hint, width)))
	return fitAutonomousFrame(strings.Join(lines, "\n")+"\n", height)
}

func (form *autonomousForm) renderFields(
	add func(string), addStyled func(string, string), inner, height int, color bool,
) string {
	label := func(field int, text string) {
		code := ansiBold
		if form.focus == field {
			code = ansiBold + ansiCyan
		}
		marker := "  "
		if form.focus == field {
			marker = "› "
		}
		addStyled(code, marker+text)
	}
	value := func(field int, text string) {
		if form.focus != field {
			text = dashboardTruncate(text, inner-2)
		} else {
			text = dashboardTruncate(text, inner-3)
		}
		add("  " + text)
	}
	label(autonomousFieldGoal, "Goal")
	goalRows := max(1, min(autonomousGoalRows, height-18))
	rows := composerRows(form.goal, inner-3, goalRows)
	for index, row := range rows {
		if form.focus == autonomousFieldGoal && index == len(rows)-1 {
			row += "█"
		}
		add("  " + row)
	}
	if strings.TrimSpace(form.goal) == "" && form.focus != autonomousFieldGoal {
		addStyled(ansiDim, "  What should the team get done?")
	}
	add("")
	label(autonomousFieldRepository, "Repository")
	repository := form.repository
	room := inner - 3
	if runes := []rune(repository); len(runes) > room && room > 3 {
		// The end of a path is the part that tells folders apart.
		repository = "..." + string(runes[len(runes)-room+3:])
	}
	if form.focus == autonomousFieldRepository {
		repository += "█"
	}
	add("  " + repository)
	add("")
	label(autonomousFieldOrchestrator, "Orchestrator Model")
	value(autonomousFieldOrchestrator, form.orchestratorSummary())
	add("")
	label(autonomousFieldSubagents, "Subagent Models Allowed")
	value(autonomousFieldSubagents, form.subagentsSummary())
	add("")
	label(autonomousFieldMax, "Max Subagents")
	maxValue := form.maxText
	if form.focus == autonomousFieldMax {
		maxValue += "█"
	}
	value(autonomousFieldMax, maxValue)
	add("")
	if form.focus == autonomousFieldStart {
		addStyled(ansiBold+ansiCyan, "› [ Start ]")
	} else {
		add("  [ Start ]")
	}
	switch form.focus {
	case autonomousFieldGoal:
		return "Enter next · " + composerNewlineHint() + " new line · Ctrl+S start · Ctrl+O open last session · Esc cancel"
	case autonomousFieldRepository:
		return "Type a path, or Ctrl+F to browse · Enter next · Ctrl+S start · Esc cancel"
	case autonomousFieldOrchestrator, autonomousFieldSubagents:
		return "Enter choose · ↑/↓ move · Ctrl+S start · Esc cancel"
	case autonomousFieldMax:
		return fmt.Sprintf("How many run at once, 1 to %d · ←/→ change · Enter next · Esc cancel", daemon.AutonomousMaxSubagents)
	}
	return "Enter start · ↑/↓ move · Esc cancel"
}

func (form *autonomousForm) renderPicker(
	add func(string), addStyled func(string, string), inner, height int, color bool,
) string {
	picker := form.picker
	rows := form.pickerRows()
	title := "Orchestrator Model"
	if picker.multiple {
		title = "Subagent Models Allowed"
	}
	addStyled(ansiBold, title)
	search := "Search: " + picker.filter
	addStyled(ansiDim, dashboardTruncate(search, inner))
	add("")

	length := form.pickerLength()
	if length == 0 {
		addStyled(ansiDim, "No models match.")
	}
	picker.selected = min(picker.selected, max(0, length-1))
	visible := max(1, height-8)
	if picker.selected < picker.scroll {
		picker.scroll = picker.selected
	} else if picker.selected >= picker.scroll+visible {
		picker.scroll = picker.selected - visible + 1
	}
	picker.scroll = min(picker.scroll, max(0, length-visible))
	row := func(position int, text string) {
		text = dashboardTruncate(text, inner-2)
		if position == picker.selected {
			addStyled(ansiBold+ansiCyan, "› "+text)
		} else {
			add("  " + text)
		}
	}
	for position := picker.scroll; position < min(length, picker.scroll+visible); position++ {
		switch {
		case picker.multiple && position == 0:
			row(position, autonomousCheckbox(form.allModels)+" All models")
		default:
			index := position
			if picker.multiple {
				index--
			}
			model := form.models[rows[index]]
			text := autonomousModelName(model) + " · " + autonomousProviderName(model.Provider)
			if picker.multiple {
				text = autonomousCheckbox(form.allModels || form.allowed[autonomousModelKey(model)]) + " " + text
			}
			row(position, text)
		}
	}
	if picker.multiple {
		return "Space check · Enter done · type to search · Esc back"
	}
	return "Enter choose · type to search · Esc back"
}

func autonomousCheckbox(checked bool) string {
	if checked {
		return "[x]"
	}
	return "[ ]"
}
