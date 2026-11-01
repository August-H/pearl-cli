package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
)

const (
	claudeUsage      = "Usage: pearl claude [login|logout]"
	antigravityUsage = "Usage: pearl antigravity [login|logout]"
)

// checkClaudeSignIn is replaced in tests, which must not depend on this machine.
var checkClaudeSignIn = daemon.ClaudeSignedIn

func runClaude(args []string) int {
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "status":
		return printClaudeStatus()
	case len(args) == 1 && args[0] == "login":
		return claudeAuthCommand("login")
	case len(args) == 1 && args[0] == "logout":
		return claudeAuthCommand("logout")
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Println(claudeUsage)
		fmt.Println()
		fmt.Println("  Pearl runs your installed Claude Code CLI with its own sign-in. Pearl")
		fmt.Println("  doesn't install it. Choose a Claude model with: pearl model")
		fmt.Println()
		fmt.Println("  pearl claude         Show which Claude Code Pearl uses")
		fmt.Println("  pearl claude login   Sign in to Claude Code")
		fmt.Println("  pearl claude logout  Sign out of Claude Code")
		return 0
	}
	fmt.Fprintln(os.Stderr, claudeUsage)
	return 2
}

func printClaudeStatus() int {
	path, err := daemon.FindClaude()
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("Claude Code:", path)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, path, "--version").Output(); err == nil {
		fmt.Println("Version:", strings.TrimSpace(string(output)))
	}
	if !checkClaudeSignIn(ctx) {
		fmt.Println("Signed in: no. Run \"pearl auth login claude\" to sign in.")
		return 1
	}
	fmt.Println("Signed in: yes")
	return 0
}

// claudeAuthCommand runs the installed CLI's own sign-in, in this terminal.
func claudeAuthCommand(action string) int {
	path, err := daemon.FindClaude()
	if err != nil {
		return printError("Claude Code", err)
	}
	command := exec.Command(path, "auth", action)
	command.Env = daemon.ClaudeEnvironment()
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return printError("Claude Code", err)
	}
	forgetModelCatalogs()
	if action == "login" {
		fmt.Println("Choose a Claude model with: pearl model")
	}
	return 0
}

func runAntigravity(args []string) int {
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "status":
		return printAntigravityStatus()
	case len(args) == 1 && args[0] == "login":
		return antigravityLogin()
	case len(args) == 1 && args[0] == "logout":
		if err := daemon.AntigravityLogout(); err != nil {
			return printError("Antigravity", err)
		}
		forgetModelCatalogs()
		fmt.Println("Signed out of Antigravity in Pearl.")
		return 0
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Println(antigravityUsage)
		fmt.Println()
		fmt.Println("  Pearl connects to an Antigravity ACP server already on this machine. Pearl")
		fmt.Println("  doesn't download it. Choose a Google model with: pearl model")
		fmt.Println()
		fmt.Println("  pearl antigravity         Show which server Pearl uses")
		fmt.Println("  pearl antigravity login   Sign in with your Google account")
		fmt.Println("  pearl antigravity logout  Remove Pearl's saved sign-in")
		return 0
	}
	fmt.Fprintln(os.Stderr, antigravityUsage)
	return 2
}

func printAntigravityStatus() int {
	install, err := daemon.FindAntigravity()
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("Antigravity server:", install.Server)
	signedIn := daemon.AntigravitySignedIn()
	if signedIn {
		fmt.Println("Signed in: yes")
	} else {
		fmt.Println("Signed in: no. Run \"pearl auth login antigravity\" to sign in.")
	}
	if !signedIn {
		return 1
	}
	return 0
}

func antigravityLogin() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println("Waiting for you to sign in. This expires after 5 minutes.")
	if err := daemon.AntigravityLogin(ctx, os.Stdout); err != nil {
		return printError("Antigravity", err)
	}
	forgetModelCatalogs()
	fmt.Println("Signed in to Antigravity. Choose a Google model with: pearl model")
	return 0
}
