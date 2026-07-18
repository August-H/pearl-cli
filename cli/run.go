package cli

import (
	"fmt"
	"os"
	"strings"

	agentloop "github.com/August-H/pearl-cli/agent_loop"
)

const usage = `Usage: pearl "your request"`

// Run executes the Pearl CLI and returns its process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Println(usage)
		return 0
	}

	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "Prompt cannot be empty")
		return 2
	}

	if _, err := agentloop.Init_agent(prompt); err != nil {
		fmt.Fprintln(os.Stderr, "Agent error:", err)
		return 1
	}

	return 0
}
