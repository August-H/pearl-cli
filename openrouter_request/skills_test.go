package openrouter_request

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkillsReadsFilesAndFoldersWithOrWithoutFrontMatter(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	directory := filepath.Join(configDirectory, "skills")
	writeSkillFile(t, filepath.Join(directory, "release.md"),
		"---\nname: ship-release\ndescription: \"Tag and publish a release\"\n---\n# Steps\nRun make.\n")
	writeSkillFile(t, filepath.Join(directory, "Commits.md"), "# Commit style\n\nUse short subjects.\n")
	writeSkillFile(t, filepath.Join(directory, "review", "SKILL.md"), "Review diffs for bugs first.\n")
	writeSkillFile(t, filepath.Join(directory, "README.md"), "not a skill")
	writeSkillFile(t, filepath.Join(directory, "notes.txt"), "not a skill")
	writeSkillFile(t, filepath.Join(directory, "empty", "other.md"), "no SKILL.md here")

	skills, err := LoadSkills()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, skill := range skills {
		got = append(got, skill.Name+"="+skill.Description)
	}
	want := []string{
		"Commits=Use short subjects.",
		"review=Review diffs for bugs first.",
		"ship-release=Tag and publish a release",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("skills = %q, want %q", got, want)
	}

	call := agentToolCall{}
	call.Function.Name = readSkillTool
	call.Function.Arguments = `{"name":"SHIP-RELEASE"}`
	result := runReadSkillTool(context.Background(), call)
	if !result.Success || !strings.Contains(result.Result.(string), "Run make.") {
		t.Fatalf("read_skill = %#v", result)
	}
	call.Function.Arguments = `{"name":"missing"}`
	if result := runReadSkillTool(context.Background(), call); result.Success ||
		!strings.Contains(result.Error, "available skills: Commits, review, ship-release") {
		t.Fatalf("missing skill = %#v", result)
	}

	prompt := skillsSystemPrompt(skills)
	if !strings.Contains(prompt, "call read_skill") || !strings.Contains(prompt, "- review: Review diffs") {
		t.Fatalf("skills prompt = %q", prompt)
	}
	tools := agentTools(RunOptions{})
	found := false
	for _, tool := range tools {
		if tool["function"].(map[string]any)["name"] == readSkillTool {
			found = true
		}
	}
	if !found {
		t.Fatal("read_skill tool is missing when skills exist")
	}
	if tools := agentTools(RunOptions{OnlyTools: true}); len(tools) != 0 {
		t.Fatalf("OnlyTools runs got %d tools", len(tools))
	}
}

func TestLoadSkillsWithoutAFolderHasNoSkillsOrTool(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	skills, err := LoadSkills()
	if err != nil || len(skills) != 0 {
		t.Fatalf("skills = %v, %v", skills, err)
	}
	for _, tool := range agentTools(RunOptions{}) {
		if tool["function"].(map[string]any)["name"] == readSkillTool {
			t.Fatal("read_skill tool offered with no skills saved")
		}
	}
}
