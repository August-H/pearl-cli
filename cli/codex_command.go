package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/August-H/pearl-cli/internal/daemon"
)

const (
	codexUsage       = "Usage: pearl codex"
	codexInstallHint = "npm install -g @openai/codex"
)

func runCodex(args []string) int {
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "status":
		return printCodexStatus()
	case len(args) == 1 && args[0] == "install":
		// Earlier versions run this while updating, so it has to succeed.
		fmt.Println("Pearl doesn't install Codex. It uses the Codex CLI on this machine.")
		fmt.Println("To install Codex yourself: " + codexInstallHint)
		return 0
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Println(codexUsage)
		fmt.Println()
		fmt.Println("  Pearl runs your installed Codex CLI for GPT models. Pearl doesn't install it.")
		fmt.Println("  Shows which Codex Pearl uses. Set PEARL_CODEX_PATH to choose a specific one.")
		return 0
	}
	fmt.Fprintln(os.Stderr, codexUsage)
	return 2
}

func printCodexStatus() int {
	path, err := daemon.FindCodex()
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("Codex:", path)
	if output, err := exec.Command(path, "--version").Output(); err == nil {
		fmt.Println("Version:", strings.TrimSpace(string(output)))
	}
	return 0
}
