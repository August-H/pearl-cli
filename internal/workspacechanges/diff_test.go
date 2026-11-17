package workspacechanges

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedDiffAppliesToOriginalContents(t *testing.T) {
	long := ""
	for i := range 40 {
		long += fmt.Sprintf("line %d\n", i)
	}
	for _, test := range []struct {
		name, before, after string
		had, has            bool
	}{
		{"modify", "first\nold\nlast\n", "first\nnew\nlast\n", true, true},
		{"create", "", "new\n", false, true},
		{"delete", "old\n", "", true, false},
		{"no-newline", "before", "after", true, true},
		{"add-newline", "line", "line\n", true, true},
		{"truncate", "old\n", "", true, true},
		{"separate-hunks", long, strings.ReplaceAll(strings.ReplaceAll(long, "line 4\n", "changed 4\n"), "line 35\n", "changed 35\n"), true, true},
		{"fallback", strings.Repeat("old\n", 1100), strings.Repeat("new\n", 1100), true, true},
		{"path with spaces", "old\n", "new\n", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			git(t, directory, "init", "-q")
			if test.had {
				writeFile(t, directory, test.name, test.before)
			}
			diff := unifiedDiff(test.name, test.before, test.after, test.had, test.has)
			command := exec.Command("git", "apply", "--whitespace=nowarn", "-")
			command.Dir = directory
			command.Stdin = strings.NewReader(diff)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("apply diff: %s %v\n%s", output, err, diff)
			}
			actual, err := os.ReadFile(filepath.Join(directory, test.name))
			if test.has {
				if err != nil || string(actual) != test.after {
					t.Fatalf("applied content = %q, %v", actual, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("deleted file exists: %v", err)
			}
		})
	}
}

func TestUnifiedDiffRandomEditsApply(t *testing.T) {
	directory := t.TempDir()
	git(t, directory, "init", "-q")
	random := rand.New(rand.NewSource(42))
	for iteration := range 40 {
		var a, b strings.Builder
		for i := range 50 {
			line := fmt.Sprintf("line %d\n", i)
			a.WriteString(line)
			switch random.Intn(8) {
			case 0:
				b.WriteString("inserted\n" + line)
			case 1:
			case 2:
				b.WriteString("replaced\n")
			default:
				b.WriteString(line)
			}
		}
		writeFile(t, directory, "file.txt", a.String())
		command := exec.Command("git", "apply", "--whitespace=nowarn", "-")
		command.Dir = directory
		command.Stdin = strings.NewReader(unifiedDiff("file.txt", a.String(), b.String(), true, true))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("iteration %d: %s, %v", iteration, output, err)
		}
		actual, err := os.ReadFile(filepath.Join(directory, "file.txt"))
		if err != nil || string(actual) != b.String() {
			t.Fatalf("iteration %d: applied content differs: %v", iteration, err)
		}
	}
}
