package cli

import (
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/openrouter_request"
)

func testModels() []openrouter_request.OpenRouterModel {
	return []openrouter_request.OpenRouterModel{
		{ID: "anthropic/claude-fable-5.1", Name: "Anthropic: Claude Fable 5.1", ContextLength: 200000},
		{ID: "openai/gpt-5.5", Name: "OpenAI: GPT-5.5", ContextLength: 400000},
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

func TestBuildModelRowsShowsMissingCurrent(t *testing.T) {
	rows := buildModelRows(testModels(), "custom/my-model")
	found := false
	for _, row := range rows {
		if row.kind == modelRowOption && row.id == "custom/my-model" && row.current {
			found = true
		}
	}
	if !found {
		t.Fatalf("rows = %#v, want custom current preserved", rows)
	}
}

func TestFilterModelRowsMatchesIDNameAndProvider(t *testing.T) {
	rows := buildModelRows(testModels(), "")
	filtered := filterModelRows(rows, "fable")
	selectable := selectableModelIndexes(filtered)
	if len(selectable) != 1 || filtered[selectable[0]].id != "anthropic/claude-fable-5.1" {
		t.Fatalf("filtered = %#v", filtered)
	}
	if filtered[0].kind != modelRowHeader {
		t.Fatalf("filtered[0] = %#v, want group header", filtered[0])
	}
	if got := filterModelRows(rows, "anthropic"); len(selectableModelIndexes(got)) != 1 {
		t.Fatalf("provider filter = %#v", got)
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
	if !strings.Contains(screen.Frame, "openrouter/free") {
		t.Fatalf("frame missing free tier:\n%s", screen.Frame)
	}
}
