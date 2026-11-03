package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
)

var testRepository = os.TempDir()

func testAutonomousForm() *autonomousForm {
	form := newAutonomousForm("", testRepository)
	form.setModels([]store.AutonomousModel{
		{Provider: "claude", ID: "opus", Name: "Opus 5.5"},
		{Provider: "claude", ID: "haiku", Name: "Haiku 5.5"},
		{Provider: "chatgpt", ID: "gpt-6", Name: "GPT-6"},
		{Provider: "antigravity", ID: "gemini-3", Name: "Gemini 3"},
	}, 1, "", nil)
	return form
}

func typeInto(form *autonomousForm, text string) {
	for _, key := range text {
		form.handleKey(key)
	}
}

func TestAutonomousFormShowsTheFourFieldsWithDefaults(t *testing.T) {
	output := testAutonomousForm().render(80, 30, false)
	for _, want := range []string{
		"Autonomous", "Goal", "Repository", "Orchestrator Model", "Haiku 5.5 · Anthropic",
		"Subagent Models Allowed", "All models (4)", "Max Subagents", "3", "[ Start ]",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q:\n%s", want, output)
		}
	}
	if lines := strings.Count(output, "\n"); lines != 30 {
		t.Errorf("%d lines, want 30", lines)
	}
}

func TestAutonomousFormSubmitsAllModelsByDefault(t *testing.T) {
	form := testAutonomousForm()
	typeInto(form, "ship it")
	if result := form.handleKey(19); result != autonomousFormSubmitted {
		t.Fatalf("result = %v", result)
	}
	config, err := form.config()
	if err != nil {
		t.Fatal(err)
	}
	if config.Goal != "ship it" || config.WorkspaceRoot != resolveWorkspacePath(testRepository) || config.Orchestrator.ID != "haiku" ||
		!config.AllModels || len(config.AllowedModels) != 4 || config.MaxSubagents != 3 {
		t.Fatalf("config = %+v", config)
	}
}

func TestAutonomousFormWalksFieldsWithEnterAndTab(t *testing.T) {
	form := testAutonomousForm()
	typeInto(form, "goal\r")
	if form.focus != autonomousFieldRepository {
		t.Fatalf("focus = %d", form.focus)
	}
	form.handleKey('\r')
	if form.focus != autonomousFieldOrchestrator {
		t.Fatalf("focus = %d", form.focus)
	}
	form.handleKey('\t')
	form.handleKey('\t')
	if form.focus != autonomousFieldMax {
		t.Fatalf("focus = %d", form.focus)
	}
	form.handleKey('\r')
	if form.focus != autonomousFieldStart {
		t.Fatalf("focus = %d", form.focus)
	}
	if result := form.handleKey('\r'); result != autonomousFormSubmitted {
		t.Fatalf("Enter on Start = %v", result)
	}
	form.applyAction("next")
	if form.focus != autonomousFieldGoal {
		t.Fatalf("focus didn't wrap: %d", form.focus)
	}
	form.applyAction("previous_field")
	if form.focus != autonomousFieldStart {
		t.Fatalf("focus didn't wrap back: %d", form.focus)
	}
}

func TestAutonomousFormChoosesTheOrchestrator(t *testing.T) {
	form := testAutonomousForm()
	form.focus = autonomousFieldOrchestrator
	form.handleKey('\r')
	if form.picker == nil || form.picker.selected != 1 {
		t.Fatalf("picker = %+v", form.picker)
	}
	typeInto(form, "gem")
	output := form.render(80, 30, false)
	if !strings.Contains(output, "Gemini 3 · Google") || strings.Contains(output, "Opus") {
		t.Errorf("search didn't filter:\n%s", output)
	}
	form.handleKey('\r')
	if form.picker != nil || form.models[form.orchestrator].ID != "gemini-3" {
		t.Fatalf("orchestrator = %+v", form.models[form.orchestrator])
	}
}

func TestAutonomousFormBuildsAWhitelist(t *testing.T) {
	form := testAutonomousForm()
	typeInto(form, "g")
	form.focus = autonomousFieldSubagents
	form.handleKey('\r')

	// Unchecking "All models" starts from nothing.
	form.handleKey(' ')
	if form.allModels {
		t.Fatal("All models stayed on")
	}
	if _, err := form.config(); err == nil {
		t.Fatal("an empty whitelist was accepted")
	}
	form.openPicker(true)
	form.applyAction("next")
	form.handleKey(' ') // Opus
	form.applyAction("next")
	form.applyAction("next")
	form.handleKey(' ') // GPT-6
	output := form.render(80, 30, false)
	if !strings.Contains(output, "[x] Opus 5.5") || !strings.Contains(output, "[ ] Haiku 5.5") || !strings.Contains(output, "[x] GPT-6") {
		t.Errorf("checkboxes are wrong:\n%s", output)
	}
	form.handleKey('\r')
	if got := form.subagentsSummary(); got != "Opus 5.5, GPT-6" {
		t.Errorf("summary = %q", got)
	}
	config, err := form.config()
	if err != nil {
		t.Fatal(err)
	}
	if config.AllModels || len(config.AllowedModels) != 2 || config.AllowedModels[1].ID != "gpt-6" {
		t.Fatalf("config = %+v", config)
	}
}

func TestAutonomousFormUncheckingOneKeepsTheOthersAndAllCheckedMeansAll(t *testing.T) {
	form := testAutonomousForm()
	form.openPicker(true)
	form.applyAction("next")
	form.handleKey(' ')
	if form.allModels || form.allowed["claude/opus"] || !form.allowed["claude/haiku"] || !form.allowed["chatgpt/gpt-6"] {
		t.Fatalf("all=%v allowed=%v", form.allModels, form.allowed)
	}
	form.handleKey(' ')
	if !form.allModels {
		t.Fatal("checking every model didn't return to All models")
	}
}

func TestAutonomousFormValidates(t *testing.T) {
	form := testAutonomousForm()
	if _, err := form.config(); err == nil || !strings.Contains(err.Error(), "goal") {
		t.Fatalf("err = %v", err)
	}
	typeInto(form, "g")
	for _, text := range []string{"", "0", "17", "999"} {
		form.maxText = text
		if _, err := form.config(); err == nil {
			t.Errorf("max %q was accepted", text)
		}
	}
	if form.focus != autonomousFieldMax {
		t.Errorf("focus = %d", form.focus)
	}
	loading := newAutonomousForm("goal", testRepository)
	if _, err := loading.config(); err == nil || !strings.Contains(err.Error(), "loading") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(loading.render(80, 30, false), "Loading models") {
		t.Error("the form doesn't say it's loading")
	}
}

func TestAutonomousFormEditsMaxSubagents(t *testing.T) {
	form := testAutonomousForm()
	form.focus = autonomousFieldMax
	form.applyAction("expand")
	if form.maxText != "4" {
		t.Fatalf("after right = %q", form.maxText)
	}
	form.handleKey(127)
	typeInto(form, "16")
	form.applyAction("expand")
	if form.maxText != "16" {
		t.Fatalf("right past the limit = %q", form.maxText)
	}
	form.maxText = "1"
	form.applyAction("collapse")
	if form.maxText != "1" {
		t.Fatalf("left past the limit = %q", form.maxText)
	}
	typeInto(form, "x")
	if form.maxText != "1" {
		t.Fatalf("a letter was typed: %q", form.maxText)
	}
}

func TestAutonomousFormGoalKeepsPastesAndLines(t *testing.T) {
	form := testAutonomousForm()
	form.addText("first\nsecond")
	form.addText("\n")
	typeInto(form, "third")
	if form.goal != "first\nsecond\nthird" {
		t.Fatalf("goal = %q", form.goal)
	}
	form.handleKey(127)
	if !strings.HasSuffix(form.goal, "thir") {
		t.Fatalf("goal = %q", form.goal)
	}
	form.handleKey(21)
	if form.goal != "" {
		t.Fatalf("Ctrl+U left %q", form.goal)
	}
}

func TestAutonomousFormCancelsAndClosesPickerFirst(t *testing.T) {
	form := testAutonomousForm()
	form.openPicker(false)
	typeInto(form, "gem")
	form.handleKey(escapeKey)
	if form.picker == nil || form.picker.filter != "" {
		t.Fatalf("Esc should clear the search first: %+v", form.picker)
	}
	form.handleKey(escapeKey)
	if form.picker != nil {
		t.Fatal("Esc didn't close the picker")
	}
	if result := form.handleKey(escapeKey); result != autonomousFormCancelled {
		t.Fatalf("result = %v", result)
	}
	if result := form.handleKey(15); result != autonomousFormOpenLatest {
		t.Fatalf("Ctrl+O = %v", result)
	}
}

func TestAutonomousFormFitsSmallScreensAndManyModels(t *testing.T) {
	form := newAutonomousForm("goal", testRepository)
	var models []store.AutonomousModel
	for range 200 {
		models = append(models, store.AutonomousModel{Provider: "openrouter", ID: "x/model", Name: "A very long model name that goes on and on and on and on and on and on"})
	}
	form.setModels(models, 0, "", nil)
	form.openPicker(true)
	for range 150 {
		form.applyAction("next")
	}
	for _, size := range [][2]int{{80, 24}, {40, 12}, {24, 10}, {200, 60}} {
		output := form.render(size[0], size[1], false)
		lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if width := len([]rune(line)); width > size[0] {
				t.Errorf("%v: line is %d wide: %q", size, width, line)
			}
		}
	}
	if !strings.Contains(form.render(80, 24, false), "› ") {
		t.Error("the selected row scrolled away")
	}
}

func TestAutonomousFormRepositoryStartsHereAndTakesATypedPath(t *testing.T) {
	here := currentWorkspace()
	form := newAutonomousForm("goal", here)
	form.setModels([]store.AutonomousModel{{Provider: "claude", ID: "opus"}}, 0, "", nil)
	if !strings.Contains(form.render(100, 30, false), "Repository") || form.repository != displayJobDirectory(here) {
		t.Fatalf("repository = %q", form.repository)
	}
	config, err := form.config()
	if err != nil || config.WorkspaceRoot != here {
		t.Fatalf("workspace = %q, err = %v", config.WorkspaceRoot, err)
	}

	other := t.TempDir()
	form.focus = autonomousFieldRepository
	form.handleKey(21)
	typeInto(form, other)
	if config, err = form.config(); err != nil || config.WorkspaceRoot != resolveWorkspacePath(other) {
		t.Fatalf("typed: workspace = %q, err = %v", config.WorkspaceRoot, err)
	}

	form.handleKey(21)
	form.addText(filepath.Join(other, "missing") + "\n")
	if _, err := form.config(); err == nil || form.focus != autonomousFieldRepository {
		t.Fatalf("a missing folder was accepted: err = %v, focus = %d", err, form.focus)
	}
	form.repository = ""
	if _, err := form.config(); err == nil {
		t.Fatal("an empty repository was accepted")
	}
}

func TestAutonomousFormRepositoryUnderstandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	form := newAutonomousForm("goal", home)
	form.setModels([]store.AutonomousModel{{Provider: "claude", ID: "opus"}}, 0, "", nil)
	if form.repository != "~" {
		t.Fatalf("repository = %q", form.repository)
	}
	config, err := form.config()
	if err != nil || config.WorkspaceRoot != resolveWorkspacePath(home) {
		t.Fatalf("workspace = %q, err = %v", config.WorkspaceRoot, err)
	}
}

func TestAutonomousFormBrowsesForTheRepository(t *testing.T) {
	form := testAutonomousForm()
	form.goal = "goal"
	form.focus = autonomousFieldRepository
	if result := form.handleKey(6); result != autonomousFormBrowse {
		t.Fatalf("Ctrl+F = %v", result)
	}
	if start := form.browseStart(); start != resolveWorkspacePath(testRepository) {
		t.Errorf("picker opens at %q", start)
	}
	chosen := t.TempDir()
	form.finishBrowse(chosen, nil)
	config, err := form.config()
	if err != nil || config.WorkspaceRoot != resolveWorkspacePath(chosen) {
		t.Fatalf("workspace = %q, err = %v", config.WorkspaceRoot, err)
	}
	form.finishBrowse("", errDirectoryPickerCancelled)
	if form.notice != "" || resolveWorkspacePath(expandHome(form.repository)) != resolveWorkspacePath(chosen) {
		t.Fatalf("cancelling changed it: %q, notice %q", form.repository, form.notice)
	}
	form.finishBrowse("", errors.New("no picker here"))
	if !strings.Contains(form.notice, "Type the path instead") {
		t.Fatalf("notice = %q", form.notice)
	}
	// Ctrl+F only browses from Repository.
	form.focus = autonomousFieldGoal
	if result := form.handleKey(6); result == autonomousFormBrowse {
		t.Fatal("Ctrl+F browsed from Goal")
	}
}

func TestAutonomousFormShowsTheEndOfALongPath(t *testing.T) {
	form := testAutonomousForm()
	form.repository = "/very/long/" + strings.Repeat("nested/", 20) + "the-repo"
	output := form.render(60, 30, false)
	if !strings.Contains(output, "the-repo") {
		t.Fatalf("the folder name was cut off:\n%s", output)
	}
}
