package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"text/tabwriter"

	"github.com/August-H/pearl-cli/openrouter_request"
)

const skillsUsage = "Usage: pearl skills [list | path]"

// openFolder shows a folder in the system file manager.
var openFolder = func(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", path)
	case "windows":
		command = exec.Command("explorer", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	return command.Start()
}

func runSkills(args []string) int {
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, skillsUsage)
		return 2
	}
	action := ""
	if len(args) == 1 {
		action = args[0]
	}
	switch action {
	case "-h", "--help", "help":
		fmt.Println(skillsUsage)
		fmt.Println()
		fmt.Println("Skills are Markdown files of instructions that every job can use.")
		fmt.Println("Save one as <name>.md or <name>/SKILL.md in the skills folder.")
		fmt.Println("Front matter with name: and description: lines is optional.")
		fmt.Println()
		fmt.Println("  pearl skills        Open the skills folder and list its skills")
		fmt.Println("  pearl skills list   List skills without opening the folder")
		fmt.Println("  pearl skills path   Print the skills folder path")
		return 0
	case "", "open", "list", "ls", "path":
	default:
		fmt.Fprintln(os.Stderr, skillsUsage)
		return 2
	}

	directory, err := openrouter_request.EnsureSkillsDirectory()
	if err != nil {
		return printError("Skills", err)
	}
	if action == "path" {
		fmt.Println(directory)
		return 0
	}
	fmt.Println("Skills folder:", displayJobDirectory(directory))
	if action == "" || action == "open" {
		if err := openFolder(directory); err != nil {
			fmt.Fprintln(os.Stderr, "Could not open the folder:", err)
		} else {
			fmt.Println("Opened the folder. Add skills as <name>.md or <name>/SKILL.md.")
		}
	}

	skills, err := openrouter_request.LoadSkills()
	if err != nil {
		return printError("Skills", err)
	}
	fmt.Println()
	if len(skills) == 0 {
		fmt.Println("No skills yet. Jobs use any skill you add here.")
		return 0
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SKILL\tDESCRIPTION")
	for _, skill := range skills {
		fmt.Fprintf(writer, "%s\t%s\n", skill.Name, skill.Description)
	}
	_ = writer.Flush()
	return 0
}
