package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/openrouter_request"
)

func testModels() []openrouter_request.OpenRouterModel {
	return recentTestModels([]openrouter_request.OpenRouterModel{
		{ID: "anthropic/claude-fable-5.1", Name: "Anthropic: Claude Fable 5.1", ContextLength: 200000},
		{ID: "openai/gpt-5.5", Name: "OpenAI: GPT-5.5", ContextLength: 400000},
	})
}

func recentTestModels(models []openrouter_request.OpenRouterModel) []openrouter_request.OpenRouterModel {
	created := time.Now().Add(-time.Hour).Unix()
	for index := range models {
		if models[index].Created == 0 {
			models[index].Created = created
		}
	}
	return models
}

func TestModelPickerGroupsTheFullCatalogueIntoLabs(t *testing.T) {
	models := recentTestModels([]openrouter_request.OpenRouterModel{
		{ID: "openai/gpt-5.5"},
		{ID: "anthropic/claude-fable-5.1"},
		{ID: "openai/gpt-5"},
		{ID: "openai/gpt-5:free"},
		{ID: "openai/gpt-5"},
		{ID: openrouter_request.FreeTierModel},
	})
	state := newModelPickerState(buildModelRows(models, "openai/gpt-5"), "openai/gpt-5", "")
	if len(state.visible) != 3 {
		t.Fatalf("labs = %#v, want Pearl, anthropic, openai", state.visible)
	}
	if row := state.visible[state.selected]; row.provider != "openai" || row.name != "3 models" || !row.current {
		t.Fatalf("selected lab = %#v", row)
	}
	if id, done := state.handleKey('\r'); id != "" || done {
		t.Fatalf("opening a lab returned %q, %v", id, done)
	}
	if state.lab != "openai" || len(state.visible) != 3 || state.visible[state.selected].id != "openai/gpt-5" {
		t.Fatalf("lab state = %#v", state)
	}
	state.applyAction("end")
	if id, done := state.handleKey('\r'); id != "openai/gpt-5:free" || !done {
		t.Fatalf("model selection = %q, %v", id, done)
	}
	if models[0].ID != "openai/gpt-5.5" {
		t.Fatal("building the picker reordered the caller's catalogue")
	}
}

func TestModelPickerLabsSearchMatchesModelNamesAcrossLabs(t *testing.T) {
	state := newModelPickerState(buildModelRows(testModels(), ""), "", "")
	for _, character := range "/openai" {
		state.handleKey(character)
	}
	if len(state.visible) != 0 {
		t.Fatalf("lab name matched models: %#v", state.visible)
	}
	state.handleKey(21)
	for _, character := range "/gpt" {
		state.handleKey(character)
	}
	if len(state.visible) != 2 || state.visible[0].kind != modelRowHeader ||
		state.visible[0].provider != "openai" || state.visible[1].id != "openai/gpt-5.5" {
		t.Fatalf("model search = %#v", state.visible)
	}
	frame := state.screen(80, 12, false).Frame
	for _, want := range []string{"Pearl · model · all labs", "─ OpenAI ─", "GPT-5.5"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("search frame missing %q:\n%s", want, frame)
		}
	}
	if id, done := state.handleKey('\r'); id != "openai/gpt-5.5" || !done {
		t.Fatalf("search selection = %q, %v", id, done)
	}
}

func TestModelPickerBackspaceNeverClosesThePicker(t *testing.T) {
	state := newModelPickerState(buildModelRows(testModels(), ""), "", "")
	state.handleKey('\r')
	for _, character := range "/ab" {
		state.handleKey(character)
	}
	for press := range 6 {
		if id, done := state.handleKey(127); id != "" || done {
			t.Fatalf("backspace %d closed the picker: %q, %v", press+1, id, done)
		}
	}
	if state.lab != "" || state.filter != "" || state.filtering {
		t.Fatalf("backspace did not return to the labs screen: %#v", state)
	}
	if _, done := state.handleKey(8); done {
		t.Fatal("Ctrl-H closed the picker")
	}
}

func TestModelPickerBackRestoresLabSelection(t *testing.T) {
	state := newModelPickerState(buildModelRows(testModels(), ""), "", "")
	state.applyAction("end")
	selected := state.selected
	state.handleKey('\r')
	state.applyAction("collapse")
	if state.lab != "" || state.selected != selected || len(state.visible) != 3 {
		t.Fatalf("back did not restore labs: %#v", state)
	}
	state.handleKey('\r')
	if _, done := state.handleKey(escapeKey); done || state.lab != "" || state.selected != selected {
		t.Fatalf("Esc did not return to the selected lab: %#v", state)
	}
	if id, done := state.handleKey(escapeKey); id != "" || !done {
		t.Fatalf("Esc at labs = %q, %v", id, done)
	}
}

func TestModelPickerEscClearsSearchBeforeGoingBackAndQTypes(t *testing.T) {
	state := newModelPickerState(buildModelRows(testModels(), ""), "", "")
	state.handleKey('\r')
	for _, character := range "/q" {
		if _, done := state.handleKey(character); done {
			t.Fatal("q closed the picker instead of typing")
		}
	}
	if state.filter != "q" || state.lab == "" {
		t.Fatalf("q did not type into the search: %#v", state)
	}
	if _, done := state.handleKey(escapeKey); done || state.filter != "" || state.filtering || state.lab == "" {
		t.Fatalf("Esc did not clear the search in place: %#v", state)
	}
	if _, done := state.handleKey(escapeKey); done || state.lab != "" {
		t.Fatalf("second Esc did not return to labs: %#v", state)
	}
}

func TestModelPickerSearchAcceptsNavigationLettersAndRecoversFromNoMatches(t *testing.T) {
	models := recentTestModels([]openrouter_request.OpenRouterModel{{ID: "qwen/qwen-model"}, {ID: "qwen/other"}})
	state := newModelPickerState(buildModelRows(models, ""), "", "")
	for _, character := range "/qwen" {
		if _, done := state.handleKey(character); done {
			t.Fatal("searching for qwen closed the picker")
		}
	}
	if len(state.visible) != 2 || state.visible[1].id != "qwen/qwen-model" {
		t.Fatalf("labs search = %#v", state.visible)
	}
	state.handleKey(21)
	state.applyAction("end")
	state.handleKey('\r')
	for _, character := range "/qwen-model" {
		state.handleKey(character)
	}
	if len(state.visible) != 1 || state.visible[0].id != "qwen/qwen-model" {
		t.Fatalf("model search = %#v", state.visible)
	}
	state.handleKey('x')
	state.applyAction("scroll_down")
	if id, done := state.handleKey('\r'); id != "" || done {
		t.Fatalf("empty search selected a model: %q, %v", id, done)
	}
	if screen := state.screen(80, 10, false); !strings.Contains(screen.Frame, "No models match.") {
		t.Fatalf("missing no-match notice:\n%s", screen.Frame)
	}
	state.handleKey(127)
	if len(state.visible) != 1 {
		t.Fatalf("backspace did not restore match: %#v", state.visible)
	}
	state.handleKey(21)
	if len(state.visible) != 2 || state.filtering {
		t.Fatalf("clear filter = %#v", state)
	}
}

func TestModelPickerScrollKeepsSelectionVisibleWithinTerminal(t *testing.T) {
	models := make([]openrouter_request.OpenRouterModel, 20)
	for index := range models {
		models[index].ID = fmt.Sprintf("lab/model-%02d", index)
		models[index].Created = time.Now().Add(-time.Hour).Unix()
	}
	state := newModelPickerState(buildModelRows(models, "lab/model-00"), "lab/model-00", "")
	state.handleKey('\r')
	state.screen(80, 10, false)
	state.applyAction("page_down")
	if state.selected != 2 {
		t.Fatalf("page down selected %d, want 2", state.selected)
	}
	state.applyAction("end")
	for _, size := range []struct{ width, height int }{{80, 24}, {40, 10}, {20, 8}, {12, 5}, {1, 1}} {
		screen := state.screen(size.width, size.height, false)
		lines := strings.Split(screen.Frame, "\n")
		if len(lines) != size.height || strings.HasSuffix(screen.Frame, "\n") {
			t.Fatalf("%dx%d frame has %d lines:\n%s", size.width, size.height, len(lines), screen.Frame)
		}
		for _, line := range lines {
			if len([]rune(line)) > size.width {
				t.Fatalf("line exceeds width %d: %q", size.width, line)
			}
		}
		if state.scroll > state.selected || state.scroll+screen.BodyHeight <= state.selected {
			t.Fatalf("selection is outside viewport: %#v", state)
		}
	}
	state.applyAction("scroll_down")
	if state.selected != 19 {
		t.Fatalf("wheel escaped list: %d", state.selected)
	}
	state.applyAction("home")
	state.applyAction("scroll_up")
	if state.selected != 0 {
		t.Fatalf("wheel escaped top: %d", state.selected)
	}
}

func TestModelPickerWheelSwipesAccelerateInLabsAndModels(t *testing.T) {
	models := make([]openrouter_request.OpenRouterModel, 0, 200)
	for lab := range 25 {
		for model := range 8 {
			models = append(models, openrouter_request.OpenRouterModel{ID: fmt.Sprintf("lab-%02d/model-%02d", lab, model)})
		}
	}
	state := newModelPickerState(buildModelRows(recentTestModels(models), ""), "", "")
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for elapsed := time.Duration(0); elapsed < 400*time.Millisecond; elapsed += 5 * time.Millisecond {
		state.applyActionAt("scroll_down", now.Add(elapsed))
	}
	if state.selected < 10 {
		t.Fatalf("long swipe only reached lab %d", state.selected)
	}
	// Keyboard navigation and changing menus reset the swipe so small wheel
	// gestures stay precise after fast scrolling.
	state.handleKey('j')
	state.handleKey('\r')
	if len(state.visible) != 8 || state.selected != 0 {
		t.Fatalf("lab did not open at the first model: %#v", state)
	}
	now = now.Add(time.Second)
	for _, delay := range []time.Duration{0, 3 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond} {
		redraw := state.applyActionAt("scroll_down", now.Add(delay))
		if state.selected != 1 || redraw != (delay == 0) {
			t.Fatalf("single wheel tick at %s selected %d, redraw %v", delay, state.selected, redraw)
		}
	}
	for elapsed := 30 * time.Millisecond; elapsed < 400*time.Millisecond; elapsed += 5 * time.Millisecond {
		state.applyActionAt("scroll_down", now.Add(elapsed))
	}
	if state.selected != 7 {
		t.Fatalf("long swipe stopped at model %d", state.selected)
	}
	state.applyActionAt("scroll_up", now.Add(401*time.Millisecond))
	if state.selected != 6 {
		t.Fatal("direction reversal did not respond immediately")
	}
	state.handleKey('k')
	state.applyActionAt("scroll_up", now.Add(402*time.Millisecond))
	if state.selected != 4 {
		t.Fatal("keyboard navigation delayed the next wheel step")
	}
}

func TestModelPickerFreeTierRemainsSelectableWithUnknownCurrent(t *testing.T) {
	state := newModelPickerState(buildModelRows(nil, "custom/model"), "custom/model", "")
	state.handleKey('\r')
	if id := state.activate(); id != openrouter_request.FreeTierModel {
		t.Fatalf("free selection = %q", id)
	}
}

func TestModelPickerHidesBatchModelsAndEmptyLabs(t *testing.T) {
	created := time.Now().Add(-time.Hour).Unix()
	models := []openrouter_request.OpenRouterModel{
		{ID: "anthropic/claude-fable-5:batch", Name: "Anthropic: Claude Fable 5 (batch)", Created: created + 300},
		{ID: "anthropic/claude-fable-5", Created: created + 100},
		{ID: "anthropic/claude-fable-5:free", Created: created + 200},
		{ID: "openai/gpt-5:BATCH", Created: created + 400},
	}
	for _, currentID := range []string{"", "anthropic/claude-fable-5:batch", "custom/model:batch"} {
		state := newModelPickerState(buildModelRows(models, currentID), currentID, "")
		if len(state.visible) != 2 || state.visible[0].provider != "anthropic" || state.visible[0].name != "2 models" {
			t.Fatalf("current %q: labs include batch entries or empty lab: %#v", currentID, state.visible)
		}
		state.selected = 0
		state.activate()
		if len(state.visible) != 2 || state.visible[0].id != "anthropic/claude-fable-5:free" || state.visible[1].id != "anthropic/claude-fable-5" {
			t.Fatalf("current %q: wrong expanded models: %#v", currentID, state.visible)
		}
	}
}

func TestModelPickerStarsMetaAndKeepsLlamaUnstarred(t *testing.T) {
	models := recentTestModels([]openrouter_request.OpenRouterModel{
		{ID: "meta-llama/llama-4"}, {ID: "meta/muse-spark", Name: "Meta: Muse Spark"},
		{ID: "z-ai/glm-5"}, {ID: "moonshotai/kimi-k2"}, {ID: "deepseek/deepseek-v4"},
		{ID: "x-ai/grok-4"}, {ID: "google/gemini-3"}, {ID: "openai/gpt-5"},
		{ID: "anthropic/claude-fable-5"},
	})
	state := newModelPickerState(buildModelRows(models, "meta-llama/llama-4"), "meta-llama/llama-4", "")
	want := []string{"anthropic", "openai", "google", "x-ai", "deepseek", "meta", "moonshotai", "z-ai", "Pearl", "meta-llama"}
	if len(state.visible) != len(want) {
		t.Fatalf("labs = %#v", state.visible)
	}
	for index, provider := range want {
		if lab := state.visible[index]; lab.provider != provider || lab.starred != (index < 8) {
			t.Fatalf("lab %d = %#v, want %s", index, lab, provider)
		}
	}
	if title := modelLabTitle(state.visible[5]); title != "★ Meta" {
		t.Fatalf("Meta title = %q", title)
	}
	state.selected = 5
	state.activate()
	if id := state.activate(); id != "meta/muse-spark" {
		t.Fatalf("Meta model selection = %q", id)
	}
}

func TestModelPickerHidesGPTProModelsOnly(t *testing.T) {
	models := recentTestModels([]openrouter_request.OpenRouterModel{
		{ID: "openai/gpt-5-pro"}, {ID: "openai/gpt-6.1-sol-pro:free"},
		{ID: "openai/gpt-5-pro-2026-01-01"}, {ID: "openai/GPT-6-LUNA-PRO"},
		{ID: "openai/gpt-5"}, {ID: "openai/gpt-5:free"},
		{ID: "openai/o3-pro"}, {ID: "openai/gpt-project"},
		{ID: "google/gemini-pro"}, {ID: "deepseek/deepseek-v4-pro"},
	})
	for _, currentID := range []string{"", "openai/gpt-5-pro", "openai/gpt-missing-pro"} {
		rows := buildModelRows(models, currentID)
		var got []string
		for _, row := range rows {
			if row.kind == modelRowOption {
				got = append(got, row.id)
			}
		}
		want := []string{openrouter_request.FreeTierModel, "deepseek/deepseek-v4-pro", "google/gemini-pro", "openai/gpt-5", "openai/gpt-5:free", "openai/gpt-project", "openai/o3-pro"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("current %q: models = %v, want %v", currentID, got, want)
		}
	}
}

func TestBuildModelRowsLeadsWithFreeTier(t *testing.T) {
	rows := buildModelRows(testModels(), "openai/gpt-5.5")
	if len(rows) < 5 {
		t.Fatalf("rows = %#v, want header + free + 2 groups", rows)
	}
	if rows[0].kind != modelRowHeader || rows[0].provider != "Pearl" {
		t.Fatalf("first row = %#v, want Pearl header", rows[0])
	}
	if rows[1].id != openrouter_request.FreeTierModel || rows[1].current {
		t.Fatalf("free row = %#v, want non-current free tier", rows[1])
	}
	var current []modelRow
	for _, row := range rows {
		if row.kind == modelRowOption && row.current {
			current = append(current, row)
		}
	}
	if len(current) != 1 || current[0].id != "openai/gpt-5.5" {
		t.Fatalf("current = %#v, want gpt-5.5", current)
	}
}

func TestBuildModelRowsHidesMissingCurrentWithUnknownDate(t *testing.T) {
	rows := buildModelRows(testModels(), "custom/my-model")
	for _, row := range rows {
		if row.kind == modelRowOption && row.id == "custom/my-model" {
			t.Fatalf("undated custom model is selectable: %#v", row)
		}
	}
}

func TestModelPickerOrdersModelsNewestFirstWithinEachLab(t *testing.T) {
	created := time.Now().Add(-time.Hour).Unix()
	models := []openrouter_request.OpenRouterModel{
		{ID: "openai/a-old", Created: created + 100},
		{ID: "anthropic/a-undated"},
		{ID: "openai/z-new", Created: created + 300},
		{ID: "anthropic/z-new", Created: created + 200},
		{ID: "anthropic/a-old", Created: created + 100},
		{ID: "anthropic/b-tie", Created: created + 200},
		{ID: "anthropic/b-undated"},
	}
	state := newModelPickerState(buildModelRows(models, ""), "", "")
	if state.visible[0].provider != "anthropic" || state.visible[1].provider != "openai" {
		t.Fatalf("starred lab order changed: %#v", state.visible)
	}
	for _, test := range []struct {
		provider string
		want     []string
	}{
		{"anthropic", []string{"anthropic/b-tie", "anthropic/z-new", "anthropic/a-old"}},
		{"openai", []string{"openai/z-new", "openai/a-old"}},
	} {
		for index, lab := range state.visible {
			if lab.provider == test.provider {
				state.selected = index
				break
			}
		}
		state.activate()
		if len(state.visible) != len(test.want) {
			t.Fatalf("%s has %d models, want %d", test.provider, len(state.visible), len(test.want))
		}
		for index, want := range test.want {
			if got := state.visible[index].id; got != want {
				t.Fatalf("%s model %d = %q, want %q", test.provider, index, got, want)
			}
		}
		state.filter = "new"
		state.refilter()
		if id := state.activate(); id != test.provider+"/z-new" {
			t.Fatalf("filtered model selection = %q", id)
		}
		state.back()
	}
	if models[0].ID != "openai/a-old" || models[1].ID != "anthropic/a-undated" {
		t.Fatal("building the picker reordered the caller's catalogue")
	}
}

func TestModelPickerKeepsOnlyModelsFromPastCalendarYear(t *testing.T) {
	for _, test := range []struct {
		name        string
		now, cutoff time.Time
	}{
		{"ordinary date", time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC), time.Date(2025, 10, 10, 18, 0, 0, 0, time.UTC)},
		{"window includes leap day", time.Date(2025, 2, 28, 18, 0, 0, 0, time.UTC), time.Date(2024, 2, 28, 18, 0, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			models := []openrouter_request.OpenRouterModel{
				{ID: "qwen/undated"},
				{ID: "qwen/expired", Created: test.cutoff.Unix() - 1},
				{ID: "qwen/boundary", Created: test.cutoff.Unix()},
				{ID: "qwen/inside", Created: test.cutoff.Unix() + 1},
				{ID: "qwen/now", Created: test.now.Unix()},
				{ID: "qwen/future", Created: test.now.Unix() + 1},
				{ID: "oldlab/expired", Created: test.cutoff.Unix() - 1},
			}
			for _, currentID := range []string{"qwen/expired", "qwen/undated", "custom/unknown-date", "qwen/inside"} {
				state := newModelPickerState(buildModelRowsAt(models, currentID, test.now), currentID, "")
				if len(state.visible) != 2 || state.visible[1].provider != "qwen" || state.visible[1].name != "3 models" {
					t.Fatalf("current %q: incorrect labs/counts: %#v", currentID, state.visible)
				}
				state.selected = 1
				state.activate()
				want := []string{"qwen/now", "qwen/inside", "qwen/boundary"}
				for index, id := range want {
					if state.visible[index].id != id {
						t.Fatalf("model %d = %q, want %q", index, state.visible[index].id, id)
					}
				}
				state.applyAction("end")
				if id := state.activate(); id != "qwen/boundary" {
					t.Fatalf("cutoff model selection = %q", id)
				}
			}
		})
	}
}

func TestModelPickerKeepsOnlyStarredLabModelsFromPastSixMonths(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 4, 10, 18, 0, 0, 0, time.UTC).Unix()
	models := []openrouter_request.OpenRouterModel{
		{ID: "anthropic/expired", Created: cutoff - 1},
		{ID: "anthropic/boundary", Created: cutoff},
		{ID: "anthropic/now", Created: now.Unix()},
		{ID: "openai/expired", Created: cutoff - 1},
		{ID: "meta/expired", Created: cutoff - 1},
		{ID: "meta-llama/older", Created: cutoff - 1},
	}
	var got []string
	for _, row := range buildModelRowsAt(models, "openai/expired", now) {
		if row.kind == modelRowOption {
			got = append(got, row.id)
		}
	}
	want := []string{openrouter_request.FreeTierModel, "anthropic/now", "anthropic/boundary", "meta-llama/older"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

func TestFilterModelRowsMatchesModelNameNotLab(t *testing.T) {
	rows := buildModelRows(testModels(), "")
	filtered := filterModelRows(rows, "fable")
	selectable := selectableModelIndexes(filtered)
	if len(selectable) != 1 || filtered[selectable[0]].id != "anthropic/claude-fable-5.1" {
		t.Fatalf("filtered = %#v", filtered)
	}
	if filtered[0].kind != modelRowHeader {
		t.Fatalf("filtered[0] = %#v, want group header", filtered[0])
	}
	if got := filterModelRows(rows, "claude-fable-5.1"); len(selectableModelIndexes(got)) != 1 {
		t.Fatalf("ID slug filter = %#v", got)
	}
	for _, lab := range []string{"anthropic", "openai/", "Anthropic:"} {
		if got := filterModelRows(rows, lab); len(selectableModelIndexes(got)) != 0 {
			t.Fatalf("lab filter %q = %#v", lab, got)
		}
	}
	if got := filterModelRows(rows, "no-such-model"); len(selectableModelIndexes(got)) != 0 {
		t.Fatalf("empty filter = %#v", got)
	}
	if got := filterModelRows(rows, ""); len(got) != len(rows) {
		t.Fatalf("blank filter changed rows")
	}
}

func TestInitialModelSelectionPrefersCurrent(t *testing.T) {
	rows := buildModelRows(testModels(), "openai/gpt-5.5")
	visible := filterModelRows(rows, "")
	selectable := selectableModelIndexes(visible)
	if got := initialModelSelection(visible); visible[selectable[got]].id != "openai/gpt-5.5" {
		t.Fatalf("selection = %d, want gpt-5.5", got)
	}
	rows = buildModelRows(testModels(), "")
	if got := initialModelSelection(filterModelRows(rows, "")); got != 0 {
		t.Fatalf("default selection = %d, want 0 (free tier)", got)
	}
}

func TestModelPickerShowsAlignedPricesInsideALab(t *testing.T) {
	models := recentTestModels([]openrouter_request.OpenRouterModel{
		{ID: "anthropic/claude-fable-5.1", Name: "Anthropic: Claude Fable 5.1",
			InputPrice: "0.000003", OutputPrice: "0.000015", CacheReadPrice: "0.0000003"},
		{ID: "anthropic/claude-haiku-5.5", Name: "Anthropic: Claude Haiku 5.5",
			InputPrice: "0.0000001", OutputPrice: "0.0000005"},
		{ID: "anthropic/claude-auto", Name: "Anthropic: Auto", InputPrice: "-1", OutputPrice: "-1"},
	})
	state := newModelPickerState(buildModelRows(models, ""), "", "")
	if frame := state.screen(100, 20, false).Frame; strings.Contains(frame, "in $") {
		t.Fatalf("labs view shows prices:\n%s", frame)
	}
	state.handleKey('\r')
	frame := state.screen(100, 20, false).Frame
	var fable, haiku, auto string
	for _, line := range strings.Split(frame, "\n") {
		switch {
		case strings.Contains(line, "Fable"):
			fable = line
		case strings.Contains(line, "Haiku"):
			haiku = line
		case strings.Contains(line, "Auto"):
			auto = line
		}
	}
	if !strings.HasSuffix(fable, "in $3     out $15    cache $0.30") {
		t.Fatalf("fable row = %q", fable)
	}
	if !strings.HasSuffix(haiku, "in $0.10  out $0.50") {
		t.Fatalf("haiku row = %q", haiku)
	}
	if strings.Index(fable, "in $") != strings.Index(haiku, "in $") {
		t.Fatalf("price columns are not aligned:\n%s\n%s", fable, haiku)
	}
	if strings.Contains(auto, "$") || strings.HasSuffix(auto, " ") {
		t.Fatalf("variable-price row = %q", auto)
	}
	if !strings.Contains(frame, "$ per 1M tokens") {
		t.Fatalf("frame missing price unit:\n%s", frame)
	}
	if frame := state.screen(30, 20, false).Frame; strings.Contains(frame, "in $") {
		t.Fatalf("narrow terminal kept prices:\n%s", frame)
	}

	state.back()
	for index, lab := range state.visible {
		if lab.provider == "Pearl" {
			state.selected = index
		}
	}
	state.activate()
	if frame := state.screen(100, 20, false).Frame; !strings.Contains(frame, "Free Tier  free") {
		t.Fatalf("free tier row missing free label:\n%s", frame)
	}
}

func TestRenderModelScreenShowsPositionAndFilter(t *testing.T) {
	rows := buildModelRows(testModels(), "openai/gpt-5.5")
	visible := filterModelRows(rows, "")
	screen := renderModelScreen(visible, 0, 0, "gpt", "openai/gpt-5.5", "", 80, 24, false)
	if !strings.Contains(screen.Frame, "Pearl · model") {
		t.Fatalf("frame missing title:\n%s", screen.Frame)
	}
	if !strings.Contains(screen.Frame, "Filter: gpt") {
		t.Fatalf("frame missing filter:\n%s", screen.Frame)
	}
	if !strings.Contains(screen.Frame, "Free Tier") {
		t.Fatalf("frame missing free tier:\n%s", screen.Frame)
	}
	if !strings.Contains(screen.Frame, "Anthropic: Claude Fable 5.1") {
		t.Fatalf("frame missing readable model name:\n%s", screen.Frame)
	}
	for _, unwanted := range []string{"anthropic/claude-fable-5.1", "ctx)"} {
		if strings.Contains(screen.Frame, unwanted) {
			t.Fatalf("model row still includes %q:\n%s", unwanted, screen.Frame)
		}
	}
}
