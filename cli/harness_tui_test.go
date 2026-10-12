package cli

import (
	"strings"
	"testing"
)

func testHarnessOptions(codexMissing bool) []harnessOption {
	options := []harnessOption{
		{name: "pearl", label: "Pearl", description: "Pearl's built-in agent loop"},
		{name: "codex", label: "Codex", description: "OpenAI's Codex CLI"},
	}
	if codexMissing {
		options[1].unavailable = "not installed"
	}
	return options
}

func TestHarnessPickerStartsOnCurrentAndChoosesWithEnter(t *testing.T) {
	picker := newHarnessPicker("codex", testHarnessOptions(false))
	if picker.selected != 1 {
		t.Fatalf("selected = %d, want current harness", picker.selected)
	}
	picker.handleKey('k')
	if chosen, done := picker.handleKey('\r'); !done || chosen != "pearl" {
		t.Fatalf("Enter = %q, %v; want pearl", chosen, done)
	}
	if !picker.applyAction("end") || picker.selected != 1 {
		t.Fatalf("end selected %d", picker.selected)
	}
	if chosen, done := picker.handleKey(escapeKey); !done || chosen != "" {
		t.Fatalf("Esc = %q, %v; want cancel", chosen, done)
	}
}

func TestHarnessPickerRefusesMissingCodex(t *testing.T) {
	picker := newHarnessPicker("pearl", testHarnessOptions(true))
	picker.applyAction("next")
	if chosen, done := picker.handleKey('\r'); done || chosen != "" {
		t.Fatalf("Enter on missing codex = %q, %v", chosen, done)
	}
	if !strings.Contains(picker.notice, "not installed") {
		t.Fatalf("notice = %q", picker.notice)
	}
	picker.applyAction("previous")
	if picker.notice != "" {
		t.Fatalf("notice stayed after moving: %q", picker.notice)
	}
}

func TestHarnessPickerScreenListsEveryHarness(t *testing.T) {
	picker := newHarnessPicker("codex", testHarnessOptions(false))
	screen := picker.screen(60, 12, false)
	lines := strings.Split(screen, "\n")
	if len(lines) != 12 {
		t.Fatalf("screen has %d lines, want 12:\n%s", len(lines), screen)
	}
	for _, want := range []string{
		"Current: codex",
		"  Pearl  Pearl's built-in agent loop",
		"› ● Codex  OpenAI's Codex CLI",
		"Esc back",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("screen missing %q:\n%s", want, screen)
		}
	}
}
