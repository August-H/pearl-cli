package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/August-H/pearl-cli/internal/store"
	"os"
	"time"
)

// JSON is available on inspection commands. It always bypasses terminal UI and
// never auto-starts a daemon, so stdout contains exactly one JSON value.
func runJSONCommand(args []string) int {
	write := func(value any, err error) int {
		if err != nil {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			return printError("JSON", err)
		}
		return 0
	}
	if len(args) == 1 && args[0] == "version" {
		return write(map[string]string{"version": version, "commit": commit, "date": date}, nil)
	}
	valid := false
	if len(args) > 0 {
		switch args[0] {
		case "status":
			valid = len(args) == 1
		case "daemon":
			valid = len(args) == 2 && args[1] == "status"
		case "jobs":
			_, rest := parseShowAllFlag(args[1:])
			valid = len(rest) == 0 || (len(rest) == 2 && rest[0] == "view")
		case "archive":
			_, rest := parseShowAllFlag(args[1:])
			valid = len(rest) == 0
		case "schedule":
			if len(args) >= 2 && args[1] == "list" {
				_, rest := parseShowAllFlag(args[2:])
				valid = len(rest) == 0
			}
		}
	}
	if !valid {
		return write(nil, fmt.Errorf("--json supports version, status, daemon status, jobs [view <id>], archive, and schedule list"))
	}
	client, err := newDaemonClient()
	if err != nil {
		return write(nil, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch args[0] {
	case "status", "daemon":
		value, err := client.status(ctx)
		return write(value, err)
	case "jobs", "archive":
		all, rest := parseShowAllFlag(args[1:])
		if len(rest) == 2 {
			details, err := client.jobDetails(ctx, rest[1])
			if err != nil {
				return write(nil, err)
			}
			transcript := json.RawMessage(details.Transcript)
			if len(transcript) == 0 {
				transcript = json.RawMessage("[]")
			}
			return write(struct {
				Job        store.Job             `json:"job"`
				Transcript json.RawMessage       `json:"transcript"`
				Tools      []store.ToolExecution `json:"tool_executions"`
				Events     []store.Event         `json:"status_events"`
			}{details.Job, transcript, details.ToolExecutions, details.StatusEvents}, nil)
		}
		var jobs []store.Job
		if args[0] == "archive" {
			jobs, err = client.archivedJobs(ctx)
		} else {
			jobs, err = client.jobs(ctx)
		}
		if err != nil {
			return write(nil, err)
		}
		if !all {
			jobs = filterJobsForWorkspace(jobs, currentWorkspace())
		}
		if jobs == nil {
			jobs = []store.Job{}
		}
		return write(jobs, nil)
	case "schedule":
		all, _ := parseShowAllFlag(args[2:])
		schedules, err := client.schedules(ctx)
		if err != nil {
			return write(nil, err)
		}
		if !all {
			schedules = filterSchedulesForWorkspace(schedules, currentWorkspace())
		}
		if schedules == nil {
			schedules = []store.Schedule{}
		}
		return write(schedules, nil)
	}
	return 2
}

// Inspection commands accept --json before or after their subcommand. For job
// IDs and other positional data, -- ends flag recognition.
func extractJSONFlag(args []string) (bool, []string) {
	if len(args) == 0 {
		return false, args
	}
	flag := false
	var rest []string
	for index, arg := range args {
		if arg == "--" {
			rest = append(rest, args[index+1:]...)
			break
		}
		if arg == "--json" {
			flag = true
		} else {
			rest = append(rest, arg)
		}
	}
	return flag, rest
}

func commandHelp(args []string) bool {
	key := ""
	switch {
	case len(args) == 2 && (args[1] == "--help" || args[1] == "-h"):
		key = args[0]
	case len(args) == 3 && (args[2] == "--help" || args[2] == "-h") && (args[0] == "daemon" || args[0] == "schedule" || args[0] == "jobs"):
		key = args[0] + " " + args[1]
	default:
		return false
	}
	help := map[string]string{
		"configure":       "pearl configure",
		"dashboard":       "pearl dashboard",
		"daemon":          "pearl daemon <run|start|stop|restart|status|install|uninstall>",
		"jobs":            "pearl jobs [--all] [--json] [view <job-id>]",
		"jobs view":       "pearl jobs view <job-id> [--json]",
		"archive":         "pearl archive [--all] [--json]",
		"schedule":        "pearl schedule <add|list|remove>",
		"schedule add":    "pearl schedule add --every <duration> [--name name] [--workspace path] \"prompt\"",
		"schedule list":   "pearl schedule list [--all] [--json]",
		"schedule remove": "pearl schedule remove <schedule-id>",
		"status":          "pearl status [--json]", "version": "pearl version [--json]",
		"attach": "pearl attach <job-id>", "cancel": "pearl cancel <job-id>",
		"retry": "pearl retry <job-id>", "respond": "pearl respond <job-id> \"answer\"",
	}
	for _, action := range []string{"run", "start", "stop", "restart", "status", "install", "uninstall"} {
		help["daemon "+action] = "pearl daemon " + action
	}
	value, ok := help[key]
	if ok {
		fmt.Println("Usage:", value)
	}
	return ok
}
