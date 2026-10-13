package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/codexinstall"
	"github.com/August-H/pearl-cli/internal/daemon"
	"golang.org/x/term"
)

const codexUsage = "Usage: pearl codex [install [--force]]"

var installCodex = codexinstall.Install

func runCodex(args []string) int {
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "status":
		return printCodexStatus()
	case args[0] == "install" && len(args) == 1:
		return installCodexCommand(false)
	case args[0] == "install" && len(args) == 2 && args[1] == "--force":
		return installCodexCommand(true)
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Println(codexUsage)
		fmt.Println()
		fmt.Println("  pearl codex                  Show which Codex the Codex harness uses")
		fmt.Println("  pearl codex install          Install Pearl's copy of Codex " + codexinstall.Version)
		fmt.Println("  pearl codex install --force  Download and reinstall it")
		return 0
	}
	fmt.Fprintln(os.Stderr, codexUsage)
	return 2
}

func printCodexStatus() int {
	path, err := daemon.FindCodex()
	if err != nil {
		fmt.Println("Codex is not installed. Run \"pearl codex install\" to add it.")
		return 1
	}
	fmt.Println("Codex:", path)
	if output, err := exec.Command(path, "--version").Output(); err == nil {
		fmt.Println("Version:", strings.TrimSpace(string(output)))
	}
	if path == codexinstall.Path() {
		fmt.Println("This is Pearl's own copy.")
	} else if os.Getenv("PEARL_CODEX_PATH") == "" {
		fmt.Println("Pearl's own copy is not installed; run \"pearl codex install\" to use it instead.")
	}
	return 0
}

func installCodexCommand(force bool) int {
	if path := codexinstall.Path(); path != "" && !force {
		fmt.Println("Codex", codexinstall.Version, "is already installed:", path)
		return 0
	}
	live := term.IsTerminal(int(os.Stdout.Fd()))
	fmt.Printf("Downloading Codex %s for the Codex harness...\n", codexinstall.Version)
	lastPercent := -1
	progress := func(done, total int64) {
		if !live || total <= 0 {
			return
		}
		percent := int(done * 100 / total)
		if percent == lastPercent {
			return
		}
		lastPercent = percent
		fmt.Printf("\r\x1b[K  %3d%%  %d of %d MB", percent, done>>20, total>>20)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	path, err := installCodex(ctx, force, progress)
	if live && lastPercent >= 0 {
		fmt.Print("\r\x1b[K")
	}
	if err != nil {
		return printError("Codex", err)
	}
	fmt.Println("Installed Codex", codexinstall.Version+":", path)
	return 0
}
