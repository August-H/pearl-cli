package cli

import (
	"strings"
	"testing"
)

func TestPathContains(t *testing.T) {
	tests := []struct {
		root   string
		target string
		want   bool
	}{
		{root: "/repo", target: "/repo", want: true},
		{root: "/repo", target: "/repo/sub/dir", want: true},
		{root: "/repo", target: "/repository", want: false},
		{root: "/repo", target: "/other/repo", want: false},
		{root: "/repo", target: "/Repo", want: true},
	}
	for _, test := range tests {
		if got := pathContains(test.root, test.target); got != test.want {
			t.Fatalf("pathContains(%q, %q) = %v, want %v",
				test.root, test.target, got, test.want)
		}
	}
}

func TestWorkspaceBoardLabelKeepsTailWithinLimit(t *testing.T) {
	label := workspaceBoardLabel("/very/long/path/that/exceeds/the/board/limit/project")
	runes := []rune(label)
	if len(runes) > workspaceBoardLabelLimit {
		t.Fatalf("workspace label length = %d, want at most %d",
			len(runes), workspaceBoardLabelLimit)
	}
	if !strings.HasSuffix(label, "project") {
		t.Fatalf("workspace label = %q, want the directory name preserved", label)
	}
}
