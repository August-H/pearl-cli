// Package verification recognizes test commands, never claims in model prose.
package verification

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

type Check struct {
	Status  string `json:"status"`
	Command string `json:"command"`
}

func Event(status, command string) string {
	data, _ := json.Marshal(Check{status, command})
	return string(data)
}

func AffectsTests(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".rst":
		return false
	}
	return path != ""
}

func Command(program string, args []string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(program)), ".exe")
	if pythonName.MatchString(name) {
		return len(args) >= 2 && args[0] == "-m" && (args[1] == "unittest" || args[1] == "pytest")
	}
	switch name {
	case "pytest", "pytest-3":
		return true
	case "go", "cargo", "dotnet", "make":
		return len(args) > 0 && args[0] == "test"
	case "npm", "pnpm", "yarn", "bun":
		return len(args) > 0 && (args[0] == "test" || len(args) > 1 && args[0] == "run" && args[1] == "test")
	}
	return false
}

var pythonName = regexp.MustCompile(`^python(?:[0-9]+(?:\.[0-9]+)*)?$`)

// Shell accepts simple test commands joined with &&, optionally preceded by cd.
// Pipelines, conditionals, heredocs and other shell programs cannot establish a
// test's exit status from the overall command result, so leave those unverified.
func Shell(command string) bool {
	var words []string
	var word strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	check := func(final bool) bool {
		flush()
		if len(words) == 0 {
			return false
		}
		if !final && words[0] == "cd" && len(words) == 2 {
			words = nil
			return true
		}
		if words[0] == "env" {
			words = words[1:]
		}
		for len(words) > 0 && assignment.MatchString(words[0]) {
			words = words[1:]
		}
		if len(words) == 0 || !Command(words[0], words[1:]) {
			return false
		}
		words = nil
		return true
	}
	runes := []rune(strings.TrimSpace(command))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				if quote == '"' && (r == '$' || r == '`') {
					return false
				}
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t':
			flush()
		case '&':
			if i+1 >= len(runes) || runes[i+1] != '&' || !check(false) {
				return false
			}
			i++
		case '|', ';', '<', '>', '$', '`', '\n', '(', ')', '#':
			return false
		default:
			word.WriteRune(r)
		}
	}
	return quote == 0 && !escaped && check(true)
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*=`)
