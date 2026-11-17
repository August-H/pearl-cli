package workspacechanges

import (
	"fmt"
	"strings"
)

type diffLine struct {
	kind byte
	text string
}

func textLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// unifiedDiff uses bounded LCS work. Very large replacements fall back to a
// single replacement block, keeping computation bounded without losing changes.
func unifiedDiff(name, before, after string, had, has bool) string {
	if before == after {
		return ""
	}
	a, b := textLines(before), textLines(after)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-suffix-1] == b[len(b)-suffix-1] {
		suffix++
	}
	var lines []diffLine
	start := max(0, prefix-3)
	for _, line := range a[start:prefix] {
		lines = append(lines, diffLine{' ', line})
	}
	x, y := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	if (len(x) + 1) <= 1000000/(len(y)+1) {
		width := len(y) + 1
		lcs := make([]uint32, (len(x)+1)*width)
		for i := len(x) - 1; i >= 0; i-- {
			for j := len(y) - 1; j >= 0; j-- {
				if x[i] == y[j] {
					lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
				} else {
					lcs[i*width+j] = max(lcs[(i+1)*width+j], lcs[i*width+j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(x) || j < len(y) {
			switch {
			case i < len(x) && j < len(y) && x[i] == y[j]:
				lines = append(lines, diffLine{' ', x[i]})
				i++
				j++
			case i < len(x) && (j == len(y) || lcs[(i+1)*width+j] >= lcs[i*width+j+1]):
				lines = append(lines, diffLine{'-', x[i]})
				i++
			default:
				lines = append(lines, diffLine{'+', y[j]})
				j++
			}
		}
	} else {
		for _, line := range x {
			lines = append(lines, diffLine{'-', line})
		}
		for _, line := range y {
			lines = append(lines, diffLine{'+', line})
		}
	}
	for _, line := range a[len(a)-suffix : len(a)-suffix+min(3, suffix)] {
		lines = append(lines, diffLine{' ', line})
	}
	var out strings.Builder
	oldName, newName := "a/"+name, "b/"+name
	if !had {
		oldName = "/dev/null"
	}
	if !has {
		newName = "/dev/null"
	}
	// Quote names so whitespace and control characters cannot forge diff headers.
	quoteName := func(name string) string {
		if name == "/dev/null" {
			return name
		}
		return fmt.Sprintf("%q", name)
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", quoteName(oldName), quoteName(newName))
	oldLine, newLine := start+1, start+1
	for cursor := 0; cursor < len(lines); {
		change := cursor
		for change < len(lines) && lines[change].kind == ' ' {
			change++
		}
		if change == len(lines) {
			break
		}
		begin := max(cursor, change-3)
		for _, line := range lines[cursor:begin] {
			if line.kind != '+' {
				oldLine++
			}
			if line.kind != '-' {
				newLine++
			}
		}
		end, lastChange := change+1, change
		for end < len(lines) {
			if lines[end].kind != ' ' {
				lastChange = end
			}
			if end-lastChange > 6 {
				break
			}
			end++
		}
		end = min(len(lines), lastChange+4)
		oldCount, newCount := 0, 0
		for _, line := range lines[begin:end] {
			if line.kind != '+' {
				oldCount++
			}
			if line.kind != '-' {
				newCount++
			}
		}
		oldStart, newStart := oldLine, newLine
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, line := range lines[begin:end] {
			out.WriteByte(line.kind)
			out.WriteString(line.text)
			if !strings.HasSuffix(line.text, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		oldLine += oldCount
		newLine += newCount
		cursor = end
	}
	return out.String()
}
