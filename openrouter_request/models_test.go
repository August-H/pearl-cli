package openrouter_request

import (
	"testing"
)

func modelIDs(models []OpenRouterModel) map[string]bool {
	ids := make(map[string]bool, len(models))
	for _, model := range models {
		ids[model.ID] = true
	}
	return ids
}

func TestSelectLatestModelsKeepsNewestFable(t *testing.T) {
	models := []OpenRouterModel{
		{ID: "anthropic/claude-fable-5", Name: "Fable 5", Created: 100},
		{ID: "anthropic/claude-fable-5.1", Name: "Fable 5.1", Created: 200},
	}
	winners := SelectLatestModels(models)
	ids := modelIDs(winners)
	if len(winners) != 1 || !ids["anthropic/claude-fable-5.1"] {
		t.Fatalf("winners = %#v, want only fable 5.1", winners)
	}
}

func TestSelectLatestModelsDropsVariantsAndHidden(t *testing.T) {
	models := []OpenRouterModel{
		{ID: "google/gemini-3.8-flash", Created: 3},
		{ID: "google/gemini-3.8-flash:batch", Created: 4},
		{ID: "google/gemini-3.7-flash", Created: 2},
		{ID: "~z-ai/glm-latest", Created: 9},
		{ID: "openai/gpt-chat-latest", Created: 9},
		{ID: "openai/gpt-5", Created: 5},
	}
	winners := SelectLatestModels(models)
	ids := modelIDs(winners)
	if !ids["google/gemini-3.8-flash"] || ids["google/gemini-3.7-flash"] {
		t.Fatalf("gemini winners = %#v, want only 3.8-flash", winners)
	}
	for id := range ids {
		if len(id) > 0 && (id[0] == '~' || containsVariant(id)) {
			t.Fatalf("noise survived: %#v", winners)
		}
		if id == "openai/gpt-chat-latest" {
			t.Fatalf("latest alias survived: %#v", winners)
		}
	}
}

func containsVariant(id string) bool {
	for index := 0; index < len(id); index++ {
		if id[index] == ':' {
			return true
		}
	}
	return false
}

func TestSelectLatestModelsKeepsFreeOnlyModels(t *testing.T) {
	models := []OpenRouterModel{
		{ID: "inclusionai/ling-3.0-flash-fin:free", Created: 1},
	}
	winners := SelectLatestModels(models)
	if len(winners) != 1 || winners[0].ID != "inclusionai/ling-3.0-flash-fin:free" {
		t.Fatalf("free-only winners = %#v", winners)
	}
}

func TestSelectLatestModelsPrefersBaseOverDatedSnapshots(t *testing.T) {
	models := []OpenRouterModel{
		{ID: "openai/gpt-4o-2024-05-13", Created: 300},
		{ID: "openai/gpt-4o", Created: 100},
		{ID: "openai/gpt-3.5-turbo-0613", Created: 300},
		{ID: "openai/gpt-3.5-turbo", Created: 100},
	}
	winners := SelectLatestModels(models)
	ids := modelIDs(winners)
	if !ids["openai/gpt-4o"] || ids["openai/gpt-4o-2024-05-13"] {
		t.Fatalf("gpt-4o winners = %#v", winners)
	}
	if !ids["openai/gpt-3.5-turbo"] || ids["openai/gpt-3.5-turbo-0613"] {
		t.Fatalf("turbo winners = %#v", winners)
	}
}

func TestSelectLatestModelsKeepsSizesDistinct(t *testing.T) {
	models := []OpenRouterModel{
		{ID: "openai/gpt-oss-120b", Created: 2},
		{ID: "openai/gpt-oss-20b", Created: 1},
		{ID: "openai/gpt-5-mini", Created: 1},
		{ID: "openai/gpt-5-nano", Created: 1},
	}
	winners := SelectLatestModels(models)
	if len(winners) != 4 {
		t.Fatalf("size winners = %#v, want all four", winners)
	}
}

func TestSelectLatestModelsGroupsCodexAndBreaksTiesByCreated(t *testing.T) {
	models := []OpenRouterModel{
		{ID: "openai/gpt-5.1-codex", Created: 1},
		{ID: "openai/gpt-5.2-codex", Created: 2},
		{ID: "openai/gpt-5.3-codex", Created: 3},
	}
	winners := SelectLatestModels(models)
	if len(winners) != 1 || winners[0].ID != "openai/gpt-5.3-codex" {
		t.Fatalf("codex winners = %#v", winners)
	}
}

func TestCompareModelVersion(t *testing.T) {
	cases := []struct {
		left, right []int
		want        int
	}{
		{[]int{5, 1}, []int{5}, 1},
		{[]int{5}, []int{5, 1}, -1},
		{[]int{4}, []int{4, 2024, 5, 13}, 1},
		{[]int{3, 5}, []int{3, 5, 613}, 1},
		{[]int{3, 8}, []int{3, 7}, 1},
		{nil, nil, 0},
	}
	for _, test := range cases {
		if got := compareModelVersion(test.left, test.right); got != test.want {
			t.Fatalf("compare(%v, %v) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestFormatContextLength(t *testing.T) {
	cases := map[int64]string{
		1048576: "1M",
		200000:  "200K",
		128000:  "128K",
		0:       "",
	}
	for length, want := range cases {
		if got := FormatContextLength(length); got != want {
			t.Fatalf("FormatContextLength(%d) = %q, want %q", length, got, want)
		}
	}
}
