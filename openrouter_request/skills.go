package openrouter_request

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	readSkillTool = "read_skill"
	// maxSkillBytes caps one skill file; a skill is instructions, not data.
	maxSkillBytes          = 256 << 10
	maxSkillDescriptionLen = 200
)

// Skill is a Markdown file of instructions in Pearl's skills folder. A skill
// is either skills/<name>.md or skills/<name>/SKILL.md.
type Skill struct {
	Name        string
	Description string
	Path        string
}

// SkillsDirectory returns the folder Pearl loads skills from.
func SkillsDirectory() (string, error) {
	paths := pearlConfigFiles("skills", false)
	if len(paths) == 0 {
		return "", errors.New("could not determine the user config directory; set PEARL_CONFIG_DIR")
	}
	return paths[0], nil
}

// EnsureSkillsDirectory creates the skills folder if needed and returns it.
func EnsureSkillsDirectory() (string, error) {
	directory, err := SkillsDirectory()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create skills folder: %w", err)
	}
	return directory, nil
}

// LoadSkills lists the skills in the skills folder, sorted by name. A missing
// folder means no skills.
func LoadSkills() ([]Skill, error) {
	directory, err := SkillsDirectory()
	if err != nil {
		return nil, err
	}
	return loadSkillsFrom(directory)
}

func loadSkillsFrom(directory string) ([]Skill, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills folder: %w", err)
	}
	var skills []Skill
	seen := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(directory, name)
		fallback := strings.TrimSuffix(name, filepath.Ext(name))
		switch {
		case entry.IsDir():
			path = skillFileInFolder(path)
			fallback = name
			if path == "" {
				continue
			}
		case !strings.EqualFold(filepath.Ext(name), ".md") || strings.EqualFold(name, "README.md"):
			continue
		}
		skill, err := readSkill(path, fallback)
		if err != nil || seen[strings.ToLower(skill.Name)] {
			continue
		}
		seen[strings.ToLower(skill.Name)] = true
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})
	return skills, nil
}

func skillFileInFolder(folder string) string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "SKILL.md") {
			return filepath.Join(folder, entry.Name())
		}
	}
	return ""
}

// readSkill reads a skill's name and description from its front matter, or
// falls back to the file name and the first line of text.
func readSkill(path, fallbackName string) (Skill, error) {
	contents, err := readSkillFile(path)
	if err != nil {
		return Skill{}, err
	}
	skill := Skill{Name: fallbackName, Path: path}
	body := contents
	if frontMatter, rest, found := splitFrontMatter(contents); found {
		body = rest
		for _, line := range strings.Split(frontMatter, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "name":
				if value != "" {
					skill.Name = value
				}
			case "description":
				skill.Description = value
			}
		}
	}
	if skill.Description == "" {
		scanner := bufio.NewScanner(strings.NewReader(body))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				skill.Description = line
				break
			}
		}
	}
	if runes := []rune(skill.Description); len(runes) > maxSkillDescriptionLen {
		skill.Description = string(runes[:maxSkillDescriptionLen-3]) + "..."
	}
	return skill, nil
}

func readSkillFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxSkillBytes {
		return "", fmt.Errorf("skill file is %d bytes; the limit is %d", info.Size(), maxSkillBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(bytes.ReplaceAll(contents, []byte("\r\n"), []byte("\n"))), nil
}

func splitFrontMatter(contents string) (frontMatter, body string, found bool) {
	if !strings.HasPrefix(contents, "---\n") {
		return "", contents, false
	}
	rest := contents[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", contents, false
	}
	body = rest[end+len("\n---"):]
	body = strings.TrimPrefix(strings.TrimPrefix(body, "\r"), "\n")
	return rest[:end], body, true
}

// skillsSystemPrompt tells the agent which skills exist. The agent loads a
// skill's full text with read_skill only when it is relevant.
func skillsSystemPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var prompt strings.Builder
	prompt.WriteString("The user saved these skills: instructions for specific kinds of tasks. ")
	prompt.WriteString("When a skill fits the task, call " + readSkillTool + " with its name before you start, then follow it.\n")
	for _, skill := range skills {
		prompt.WriteString("- " + skill.Name)
		if skill.Description != "" {
			prompt.WriteString(": " + skill.Description)
		}
		prompt.WriteString("\n")
	}
	return strings.TrimSuffix(prompt.String(), "\n")
}

func readSkillToolDefinition() map[string]any {
	return newAgentTool(
		readSkillTool,
		"Load the full instructions of one of the user's saved skills.",
		map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The skill name from the list of available skills",
			},
		},
		"name",
	)
}

func runReadSkillTool(_ context.Context, call agentToolCall) agentToolResult {
	var arguments struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return agentToolResult{Error: "decode arguments: " + err.Error()}
	}
	skills, err := LoadSkills()
	if err != nil {
		return agentToolResult{Error: err.Error()}
	}
	name := strings.TrimSpace(arguments.Name)
	for _, skill := range skills {
		if strings.EqualFold(skill.Name, name) {
			contents, err := readSkillFile(skill.Path)
			if err != nil {
				return agentToolResult{Error: err.Error()}
			}
			return agentToolResult{Success: true, Result: contents}
		}
	}
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	if len(names) == 0 {
		return agentToolResult{Error: "no skills are saved"}
	}
	return agentToolResult{Error: fmt.Sprintf("no skill named %q; available skills: %s", name, strings.Join(names, ", "))}
}
