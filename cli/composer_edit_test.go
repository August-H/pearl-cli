package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestComposerEditsAtCursor(t *testing.T) {
	var buffer composerBuffer
	buffer.set("job café!")
	buffer.move("collapse", 80)
	buffer.move("collapse", 80)
	buffer.insert("X")
	if buffer.text != "job cafXé!" {
		t.Fatalf("inserted at wrong position: %q", buffer.text)
	}
	buffer.delete()
	buffer.backspace()
	if buffer.text != "job caf!" || buffer.cursor != len("job caf") {
		t.Fatalf("delete/backspace: %#v", buffer)
	}
	buffer.move("home", 80)
	buffer.backspace()
	buffer.insert("日")
	buffer.move("expand", 80)
	buffer.backspace()
	if buffer.text != "日ob caf!" || !utf8.ValidString(buffer.text) {
		t.Fatalf("Unicode editing corrupted text: %#v", buffer)
	}
	buffer.move("end", 80)
	buffer.move("expand", 80)
	buffer.delete()
	if buffer.cursor != len(buffer.text) {
		t.Fatalf("cursor escaped draft: %#v", buffer)
	}
}

func TestComposerMovesBetweenLinesAndWrappedRows(t *testing.T) {
	var buffer composerBuffer
	buffer.set("abc\ndéf\nlast")
	buffer.move("home", 80)
	buffer.move("expand", 80)
	buffer.move("expand", 80)
	buffer.move("previous", 80)
	buffer.insert("X")
	if buffer.text != "abc\ndéXf\nlast" {
		t.Fatalf("up moved to wrong column: %#v", buffer)
	}
	buffer.move("next", 80)
	buffer.insert("Y")
	if buffer.text != "abc\ndéXf\nlasYt" {
		t.Fatalf("down moved to wrong column: %#v", buffer)
	}
	buffer.set("abcdefgh")
	buffer.move("previous", 4)
	buffer.insert("X")
	if buffer.text != "abcXdefgh" {
		t.Fatalf("up across soft wrap: %#v", buffer)
	}
	buffer.set("short")
	if buffer.move("previous", 80) || buffer.move("next", 80) {
		t.Fatal("single-line arrows must remain available for history/suggestions")
	}
	buffer.set("abc\n")
	buffer.move("previous", 80)
	buffer.move("end", 80)
	if buffer.cursor != 3 {
		t.Fatalf("End should stop before the newline: %#v", buffer)
	}
}

func TestComposerPastesAndAddsNewlinesAtCursor(t *testing.T) {
	var buffer composerBuffer
	buffer.set("firstlast")
	buffer.cursor = len("first")
	buffer.insert("\n日\n")
	buffer.insert("middle ")
	if buffer.text != "first\n日\nmiddle last" {
		t.Fatalf("paste/newline appended instead of inserting: %#v", buffer)
	}
	buffer.set(strings.Repeat("x", dashboardDraftLimit-1))
	if buffer.insert("é") || len(buffer.text) != dashboardDraftLimit-1 {
		t.Fatal("multibyte insertion exceeded the draft limit")
	}
}

func TestComposerKeepsCursorVisible(t *testing.T) {
	for _, test := range []struct {
		text   string
		cursor int
		width  int
		want   string
	}{
		{"abc", 1, 4, "a█bc"},
		{"abcd", 4, 4, "abcd█"},
		{"abcde", 4, 4, "abcd\n█e"},
		{"日éx", len("日"), 4, "日█éx"},
		{"a\nb\nc\nd\ne", 0, 4, "█a\nb"},
		{"a\nb\nc\nd\ne", len("a\nb\nc\nd\ne"), 4, "d\ne█"},
	} {
		got := strings.Join(composerRowsAtCursor(test.text, test.cursor, test.width, 2), "\n")
		if got != test.want {
			t.Errorf("text=%q cursor=%d: %q, want %q", test.text, test.cursor, got, test.want)
		}
	}
}

func TestDashboardComposerMasksKeyAndShowsSelectionMode(t *testing.T) {
	cursor := len("sé")
	for _, mouse := range []bool{false, true} {
		output := renderDashboardComposerAtCursor(&cursor, mouse, nil, "sécret", "", 80, false,
			0, false, dashboardConfigureAPIKey, false, "", "", 0, 0, 0, true)
		if !strings.Contains(output, "••█••••") || strings.Contains(output, "sécret") {
			t.Fatalf("key masking or cursor position is wrong: %s", output)
		}
		if mouse && !strings.Contains(output, "Shift+drag") {
			t.Fatalf("missing mouse selection hint: %s", output)
		}
		if !mouse && (strings.Contains(output, "Drag to select") || strings.Contains(output, "F2 mouse scroll")) {
			t.Fatalf("unexpected selection hint: %s", output)
		}
	}
}

func TestDashboardRecognizesEditingKeys(t *testing.T) {
	for sequence, want := range map[string]string{
		"\x1b[D": "collapse", "\x1bOD": "collapse",
		"\x1b[C": "expand", "\x1bOC": "expand",
		"\x1b[H": "home", "\x1b[F": "end",
		"\x1b[3~": "delete", "\x1bOQ": "mouse_toggle", "\x1b[12~": "mouse_toggle",
		"\x1b[57350u": "collapse", "\x1b[57365u": "mouse_toggle",
	} {
		if action, complete := dashboardEscapeAction(sequence); !complete || action != want {
			t.Errorf("%q: %q/%v, want %q/true", sequence, action, complete, want)
		}
	}
}

func TestDashboardRefreshPreservesUnchangedRows(t *testing.T) {
	var renderer dashboardFrameRenderer
	first := renderer.render("selected text\nage 1\ninput", 80, 24)
	if first == "" || renderer.render("selected text\nage 1\ninput", 80, 24) != "" {
		t.Fatal("an unchanged refresh should write nothing")
	}
	update := renderer.render("selected text\nage 2\ninput", 80, 24)
	if update != "\x1b[2;1H\x1b[2Kage 2" {
		t.Fatalf("refresh touched selected text or input: %q", update)
	}
	update = renderer.render("selected text\nage 2", 80, 24)
	if update != "\x1b[3;1H\x1b[J" {
		t.Fatalf("did not clear removed rows: %q", update)
	}
	for _, size := range [][2]int{{40, 24}, {40, 12}} {
		if update := renderer.render("selected text\nage 2", size[0], size[1]); !strings.HasPrefix(update, "\x1b[H") {
			t.Fatalf("resize did not redraw the screen: %q", update)
		}
	}
}
