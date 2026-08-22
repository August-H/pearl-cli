package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

const version = "0.1.0"

const usage = `Pearl CLI

Usage:
  pearl job [-d] [-n name] "prompt"                        Create a pending job
  pearl run [--detach] <job-id>                            Run or retry a job
  pearl configure                                          Set the API key and model
  pearl jobs [--all]                                       List jobs for this directory
    view <job-id>                                           Show job details and transcript
  pearl archive [--all]                                    List archived jobs
  pearl dashboard                                          Watch and control active jobs
  pearl attach <job-id>                                    Stream a job's output
  pearl respond <job-id> "answer"                          Answer a paused job and resume it
  pearl cancel <job-id>                                    Cancel a job
  pearl retry <job-id>                                     Retry a finished job and stream its output
  pearl status                                             Show daemon and queue status
  pearl version                                            Show the Pearl version
  pearl autonomous [--resume <session-id>] ["goal"]        Run or resume an autonomous session TUI

  pearl schedule                                           Manage recurring jobs
    add --every <duration> [--name name] "prompt"           Run a prompt on an interval in this workspace
    list                                                   Show schedules and their next run times
    remove <schedule-id>                                   Delete a schedule

  pearl daemon                                             Manage the local background process
    run                                                    Run it in the foreground
    start                                                  Start it in the background
    stop                                                   Stop it
    restart                                                Restart it
    status                                                 Show its status
    install                                                Start it automatically at login
    uninstall                                              Remove the login service

  pearl help                                               Show this help`

func Run(args []string) int {
	if len(args) == 0 {
		return runDashboard(nil)
	}
	if args[0] == "help" {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: pearl help")
			return 2
		}
		fmt.Println(usage)
		return 0
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Println(usage)
		return 0
	}
	if args[0] == "version" || args[0] == "-v" || args[0] == "--version" {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: pearl version")
			return 2
		}
		fmt.Println("pearl version " + version)
		return 0
	}

	switch args[0] {
	case "configure":
		return runConfigure(args[1:])
	case "job":
		return createJob(args[1:])
	case "run":
		return runJob(args[1:])
	case "daemon":
		return runDaemonCommand(args[1:])
	case "status":
		return printDaemonStatus()
	case "jobs":
		return runJobs(args[1:])
	case "archive":
		showAll, rest := parseShowAllFlag(args[1:])
		if len(rest) != 0 {
			fmt.Fprintln(os.Stderr, "Usage: pearl archive [--all]")
			return 2
		}
		return listArchivedJobs(showAll)
	case "dashboard":
		return runDashboard(args[1:])
	case "autonomous":
		return runAutonomous(args[1:])
	case "attach":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "Usage: pearl attach <job-id>")
			return 2
		}
		return attachJob(args[1])
	case "respond":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, `Usage: pearl respond <job-id> "answer"`)
			return 2
		}
		return respondToJob(args[1], strings.Join(args[2:], " "))
	case "cancel", "retry":
		if len(args) != 2 {
			fmt.Fprintf(os.Stderr, "Usage: pearl %s <job-id>\n", args[0])
			return 2
		}
		return changeJob(args[1], args[0])
	case "schedule":
		return runSchedule(args[1:])
	default:
		return printInvalidCommand(args[0])
	}
}

func runConfigure(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "Usage: pearl configure")
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	_, err := configureOpenRouterInteractively(
		os.Stdin,
		os.Stdout,
	)
	if err != nil {
		return printError("Configure", err)
	}
	return 0
}

type configureResult struct {
	environmentPath string
	settingsPath    string
	model           string
}

func configureOpenRouterInteractively(
	input io.Reader,
	output io.Writer,
) (configureResult, error) {
	reader := bufio.NewReader(input)
	apiKey, err := promptForOpenRouterAPIKey(reader, output)
	if err != nil {
		return configureResult{}, err
	}

	fmt.Fprintln(output, "Choose a model:")
	fmt.Fprintln(output, "  1) free    openrouter/free")
	fmt.Fprintln(output, "  2) custom  Enter a model ID")
	model, err := promptForOpenRouterModel(reader, output)
	if err != nil {
		return configureResult{}, err
	}

	result, err := saveOpenRouterConfiguration(apiKey, model)
	if err != nil {
		return configureResult{}, err
	}
	fmt.Fprintln(output, "Configuration saved.")
	fmt.Fprintln(output, "Model:", model)
	return result, nil
}

func promptForOpenRouterAPIKey(
	reader *bufio.Reader,
	output io.Writer,
) (string, error) {
	for {
		fmt.Fprint(output, "OpenRouter API key: ")
		apiKey, err := readConfigureLine(reader)
		if err != nil {
			return "", fmt.Errorf("read OpenRouter API key: %w", err)
		}
		if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
			return apiKey, nil
		}
		fmt.Fprintln(output, "API key cannot be empty.")
	}
}

func saveOpenRouterConfiguration(apiKey, model string) (configureResult, error) {
	environmentPath, err := openrouter_request.ConfigureOpenRouterAPIKey(apiKey)
	if err != nil {
		return configureResult{}, err
	}
	settingsPath, err := openrouter_request.ConfigureAgentModel(model)
	if err != nil {
		return configureResult{}, err
	}
	return configureResult{
		environmentPath: environmentPath,
		settingsPath:    settingsPath,
		model:           strings.TrimSpace(model),
	}, nil
}

func promptForOpenRouterModel(reader *bufio.Reader, output io.Writer) (string, error) {
	for {
		fmt.Fprint(output, "Selection [1-2]: ")
		choice, err := readConfigureLine(reader)
		if err != nil {
			return "", fmt.Errorf("read model selection: %w", err)
		}
		switch strings.ToLower(choice) {
		case "1", "free":
			return "openrouter/free", nil
		case "2", "custom":
			for {
				fmt.Fprint(output, "Model ID: ")
				model, err := readConfigureLine(reader)
				if err != nil {
					return "", fmt.Errorf("read model ID: %w", err)
				}
				if model = strings.TrimSpace(model); model != "" {
					return model, nil
				}
				fmt.Fprintln(output, "Model ID cannot be empty.")
			}
		default:
			fmt.Fprintln(output, "Enter 1 for free or 2 for custom.")
		}
	}
}

func readConfigureLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func createJob(args []string) int {
	return createJobWithDirectoryPicker(args, chooseJobDirectory)
}

func createJobWithDirectoryPicker(
	args []string,
	pickDirectory func(string) (string, error),
) int {
	flags := flag.NewFlagSet("job", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("n", "", "job ID, up to 20 characters")
	flags.StringVar(name, "name", "", "job ID, up to 20 characters")
	chooseDirectory := flags.Bool("d", false, "choose the job directory")
	flags.BoolVar(chooseDirectory, "directory", false, "choose the job directory")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), `Usage: pearl job [-d] [-n name] "prompt"`)
		fmt.Fprintln(flags.Output(), "  -d, --directory  Open a directory picker")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" {
		flags.Usage()
		return 2
	}
	if err := store.ValidateJobName(*name); err != nil {
		return printError("Job", err)
	}
	workspace, err := os.Getwd()
	if err != nil {
		return printError("Job", err)
	}
	if *chooseDirectory {
		workspace, err = pickDirectory(workspace)
		if errors.Is(err, errDirectoryPickerCancelled) {
			fmt.Fprintln(os.Stderr, "Directory selection cancelled")
			return 1
		}
		if err != nil {
			return printError("Directory", err)
		}
	}
	if err := ensureDaemonRunning(); err != nil {
		return printError("Job", err)
	}
	return createPendingJob(*name, prompt, workspace)
}

func runJob(args []string) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	detach := flags.Bool("detach", false, "run without attaching")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: pearl run [--detach] <job-id>")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	jobID := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if jobID == "" {
		flags.Usage()
		return 2
	}
	if err := ensureDaemonRunning(); err != nil {
		return printError("Run", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Run", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	job, eventSequence, err := client.startJob(ctx, jobID)
	cancel()
	if err != nil {
		return printError("Run", err)
	}
	fmt.Println("Job:", job.ID)
	if *detach {
		return 0
	}
	return attachWithClientAfter(client, job.ID, eventSequence)
}

func ensureDaemonRunning() error {
	client, err := newDaemonClient()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, statusErr := client.status(ctx)
		cancel()
		if statusErr == nil {
			return nil
		}
	}
	if exitCode := startDaemon(); exitCode != 0 {
		return errors.New("Pearl daemon could not be started")
	}
	return nil
}

func createPendingJob(name, prompt, workspace string) int {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "Prompt cannot be empty")
		return 2
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Pearl", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	job, err := client.submitNamed(ctx, strings.TrimSpace(name), prompt, workspace)
	cancel()
	if err != nil {
		return printError("Pearl", err)
	}
	fmt.Println("Job:", job.ID)
	fmt.Println("Directory:", displayJobDirectory(workspace))
	return 0
}

func attachJob(jobID string) int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Attach", err)
	}
	return attachWithClient(client, jobID)
}

func attachWithClient(client *daemonClient, jobID string) int {
	return attachWithClientAfter(client, jobID, 0)
}

func attachWithClientAfter(client *daemonClient, jobID string, after int64) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	currentSection := ""
	live := false
	err := client.streamEvents(ctx, jobID, after, func(event store.Event) error {
		switch event.Type {
		case "live":
			live = true
		case "reasoning", "answer":
			if currentSection != event.Type {
				if currentSection != "" {
					fmt.Println()
				}
				fmt.Printf("[%s]\n", event.Type)
				currentSection = event.Type
			}
			fmt.Print(event.Data)
		case "tool":
			if currentSection != "" {
				fmt.Println()
				currentSection = ""
			}
			fmt.Printf("[tool] %s\n", event.Data)
		case "tool_cached":
			fmt.Printf("[tool cached] %s\n", event.Data)
		case "input_required":
			if currentSection != "" {
				fmt.Println()
				currentSection = ""
			}
			fmt.Printf("[input required]\n%s\n", event.Data)
		case "user_input":
			if currentSection != "" {
				fmt.Println()
				currentSection = ""
			}
			fmt.Printf("[user input]\n%s\n", event.Data)
		case "error":
			if !live {
				return nil
			}
			if currentSection != "" {
				fmt.Println()
				currentSection = ""
			}
			fmt.Fprintln(os.Stderr, "Agent error:", formatAgentError(event.Data))
			return nil
		}
		return nil
	})
	if currentSection != "" {
		fmt.Println()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return printError("Attach", err)
	}
	job, err := client.job(context.Background(), jobID)
	if err != nil {
		return printError("Job", err)
	}
	if job.Status == store.JobCompleted {
		return 0
	}
	if job.Status == store.JobWaitingInput {
		fmt.Printf("Job is paused. Respond with:\n  pearl respond %s \"<answer>\"\n", job.ID)
		return 0
	}
	if job.Error != "" {
		fmt.Fprintln(os.Stderr, "Job", job.Status+":", formatAgentError(job.Error))
	} else {
		fmt.Fprintln(os.Stderr, "Job status:", job.Status)
	}
	return 1
}

func listJobs(showAll bool) int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Jobs", err)
	}
	jobs, err := client.jobs(context.Background())
	if err != nil {
		return printError("Jobs", err)
	}
	if !showAll {
		jobs = filterJobsForWorkspace(jobs, currentWorkspace())
	}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		listTUI := runJobsListTUI
		if showAll {
			listTUI = runJobsListTUIShowingWorkspace
		}
		selection, err := listTUI(
			os.Stdin, os.Stdout, jobs,
			func(jobID string) error {
				requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return client.archiveJob(requestContext, jobID)
			},
		)
		if err != nil {
			return printError("Jobs", err)
		}
		if selection.Preload != "" {
			if err := preloadTerminalInput(os.Stdin, selection.Preload); err != nil {
				return printError("Preload", err)
			}
			return 0
		}
		if selection.JobID != "" {
			return viewJob(selection.JobID)
		}
		return 0
	}
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" &&
		term.IsTerminal(int(os.Stdout.Fd()))
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', tabwriter.StripEscape)
	header := "ID\t%s\tCREATED\t"
	if showAll {
		header += "WORKSPACE\t"
	}
	fmt.Fprintf(writer, header+"PROMPT\n",
		jobBoardPaint(color, ansiBold+ansiCyan, "STATUS"))
	for _, job := range jobs {
		prompt := jobBoardText(job.Prompt)
		row := fmt.Sprintf("%s\t%s\t%s\t", job.ID,
			jobBoardStatus(job.Status, color),
			formatJobCreatedAt(job.CreatedAt))
		if showAll {
			row += workspaceBoardLabel(job.WorkspaceRoot) + "\t"
		}
		fmt.Fprintf(writer, row+"%s\n", prompt)
	}
	_ = writer.Flush()
	return 0
}

func listArchivedJobs(showAll bool) int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Archive", err)
	}
	jobs, err := client.archivedJobs(context.Background())
	if err != nil {
		return printError("Archive", err)
	}
	if !showAll {
		jobs = filterJobsForWorkspace(jobs, currentWorkspace())
	}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		listTUI := runArchivedJobsListTUI
		if showAll {
			listTUI = runArchivedJobsListTUIShowingWorkspace
		}
		selection, err := listTUI(os.Stdin, os.Stdout, jobs)
		if err != nil {
			return printError("Archive", err)
		}
		if selection.JobID != "" {
			return viewJob(selection.JobID)
		}
		return 0
	}
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" &&
		term.IsTerminal(int(os.Stdout.Fd()))
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', tabwriter.StripEscape)
	header := "ID\t%s\tCREATED\t"
	if showAll {
		header += "WORKSPACE\t"
	}
	fmt.Fprintf(writer, header+"PROMPT\n",
		jobBoardPaint(color, ansiBold+ansiCyan, "STATUS"))
	for _, job := range jobs {
		row := fmt.Sprintf("%s\t%s\t%s\t", job.ID,
			jobBoardStatus(job.Status, color),
			formatJobCreatedAt(job.CreatedAt))
		if showAll {
			row += workspaceBoardLabel(job.WorkspaceRoot) + "\t"
		}
		fmt.Fprintf(writer, row+"%s\n", jobBoardText(job.Prompt))
	}
	_ = writer.Flush()
	return 0
}

func formatJobCreatedAt(createdAt time.Time) string {
	return createdAt.Local().Format("January 2 3:04pm")
}

func jobBoardStatus(status string, color bool) string {
	return jobBoardPaint(color, jobStatusColor(status), status)
}

func jobStatusColor(status string) string {
	switch status {
	case store.JobRunning, store.JobCompleted:
		return ansiGreen
	case store.JobQueued:
		return ansiYellow
	case store.JobPending:
		return ansiYellow
	case store.JobWaitingInput:
		return ansiMagenta
	case store.JobFailed, store.JobCancelled, store.JobInterrupted:
		return ansiRed
	default:
		return ansiDim
	}
}

func jobBoardPaint(enabled bool, code, value string) string {
	if !enabled {
		return value
	}
	escape := string([]byte{tabwriter.Escape})
	return escape + code + escape + value + escape + ansiReset + escape
}

func jobBoardText(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	runes := []rune(value)
	if len(runes) > 60 {
		return string(runes[:57]) + "..."
	}
	return value
}

func respondToJob(jobID, response string) int {
	if err := ensureDaemonRunning(); err != nil {
		return printError("Respond", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Respond", err)
	}
	job, eventSequence, err := client.respondToJob(
		context.Background(), jobID, response,
	)
	if err != nil {
		return printError("Respond", err)
	}
	fmt.Printf("%s: %s\n", job.ID, job.Status)
	return attachWithClientAfter(client, job.ID, eventSequence)
}

func changeJob(jobID, action string) int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Job", err)
	}
	if action == "retry" {
		job, eventSequence, err := client.retryJob(context.Background(), jobID)
		if err != nil {
			return printError("Job", err)
		}
		fmt.Printf("%s: %s\n", job.ID, job.Status)
		return attachWithClientAfter(client, job.ID, eventSequence)
	}
	job, err := client.jobAction(context.Background(), jobID, action)
	if err != nil {
		return printError("Job", err)
	}
	fmt.Printf("%s: %s\n", job.ID, job.Status)
	return 0
}

func runSchedule(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: pearl schedule <add|list|remove>")
		return 2
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Schedule", err)
	}
	switch args[0] {
	case "list":
		schedules, err := client.schedules(context.Background())
		if err != nil {
			return printError("Schedule", err)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "ID\tNAME\tEVERY\tNEXT RUN\tPROMPT")
		for _, schedule := range schedules {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", schedule.ID,
				schedule.Name, time.Duration(schedule.IntervalSeconds)*time.Second,
				schedule.NextRunAt.Local().Format("2006-01-02 15:04:05"), schedule.Prompt)
		}
		_ = writer.Flush()
		return 0
	case "remove":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "Usage: pearl schedule remove <schedule-id>")
			return 2
		}
		if err := client.deleteSchedule(context.Background(), args[1]); err != nil {
			return printError("Schedule", err)
		}
		fmt.Println("Removed", args[1])
		return 0
	case "add":
		flags := flag.NewFlagSet("schedule add", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		every := flags.Duration("every", 0, "run interval, for example 30m or 24h")
		name := flags.String("name", "scheduled task", "schedule name")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
		if *every <= 0 || prompt == "" {
			fmt.Fprintln(os.Stderr, "Usage: pearl schedule add --every <duration> [--name name] \"prompt\"")
			return 2
		}
		workspace, err := os.Getwd()
		if err != nil {
			return printError("Schedule", err)
		}
		schedule, err := client.createSchedule(context.Background(), *name, prompt, workspace, *every)
		if err != nil {
			return printError("Schedule", err)
		}
		fmt.Printf("Created %s; next run %s\n", schedule.ID,
			schedule.NextRunAt.Local().Format(time.RFC3339))
		return 0
	default:
		return printInvalidCommand("schedule " + args[0])
	}
}

func printInvalidCommand(command string) int {
	fmt.Fprintln(os.Stderr, invalidCommandMessage(command))
	return 2
}

func invalidCommandMessage(command string) string {
	return fmt.Sprintf(
		"Invalid command %q. Use \"pearl help\" for more information.",
		strings.TrimSpace(command),
	)
}

func printError(prefix string, err error) int {
	fmt.Fprintf(os.Stderr, "%s error: %v\n", prefix, err)
	return 1
}
