package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"golang.org/x/term"
)

type autonomousActivity struct {
	At     time.Time
	Text   string
	Status string
}

type autonomousTUIState struct {
	jobStatuses map[string]string
	session     string
	lastEvent   int64
	activity    []autonomousActivity
}

func runAutonomous(args []string) int {
	flags := flag.NewFlagSet("autonomous", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	resume := flags.String("resume", "", "resume an autonomous session by ID")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), `Usage: pearl autonomous [--resume <session-id>] ["goal"]`)
		fmt.Fprintln(flags.Output(), "  With no goal, Pearl resumes the latest autonomous session.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	goal := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if strings.TrimSpace(*resume) != "" && goal != "" {
		flags.Usage()
		return 2
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return printError("Autonomous", errors.New("requires an interactive terminal"))
	}
	if err := ensureDaemonRunning(); err != nil {
		return printError("Autonomous", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Autonomous", err)
	}

	var session store.AutonomousSession
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	switch {
	case goal != "":
		workspace, workspaceErr := os.Getwd()
		if workspaceErr != nil {
			cancel()
			return printError("Autonomous", workspaceErr)
		}
		session, err = client.createAutonomousSession(requestContext, goal, workspace)
	case strings.TrimSpace(*resume) != "":
		var details store.AutonomousDetails
		details, err = client.autonomousDetails(requestContext, strings.TrimSpace(*resume))
		session = details.Session
	default:
		session, err = client.latestAutonomousSession(requestContext)
	}
	cancel()
	if err != nil {
		return printError("Autonomous", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := autonomousTUILoop(ctx, os.Stdin, os.Stdout, client, session.ID, 500*time.Millisecond); err != nil &&
		!errors.Is(err, context.Canceled) {
		return printError("Autonomous", err)
	}
	return 0
}

func autonomousTUILoop(
	ctx context.Context,
	input, output *os.File,
	client *daemonClient,
	sessionID string,
	refresh time.Duration,
) error {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return fmt.Errorf("enable autonomous input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)

	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")

	state := autonomousTUIState{jobStatuses: make(map[string]string)}
	var details store.AutonomousDetails
	var loadErr error
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	draw := func(load bool) {
		if load {
			requestContext, cancel := context.WithTimeout(ctx, 2*time.Second)
			loaded, err := client.autonomousDetails(requestContext, sessionID)
			cancel()
			if err != nil {
				loadErr = err
			} else {
				details = loaded
				loadErr = nil
				updateAutonomousActivity(&state, details, time.Now())
			}
		}
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		frame := renderAutonomousScreen(details, state.activity, loadErr, width, height, color)
		fmt.Fprint(output, "\x1b[H\x1b[2J", dashboardTerminalFrame(frame))
	}
	draw(true)

	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	type inputResult struct {
		character rune
		err       error
	}
	inputReady := make(chan inputResult, 1)
	go func() {
		reader := bufio.NewReader(input)
		for {
			character, _, err := reader.ReadRune()
			select {
			case inputReady <- inputResult{character: character, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-inputReady:
			if result.err != nil {
				return result.err
			}
			switch result.character {
			case 3, 4, 'q', 'Q':
				return nil
			case 'r', 'R':
				draw(true)
			}
		case <-ticker.C:
			draw(true)
		}
	}
}

func updateAutonomousActivity(
	state *autonomousTUIState,
	details store.AutonomousDetails,
	now time.Time,
) {
	if state.jobStatuses == nil {
		state.jobStatuses = make(map[string]string)
	}
	if state.session == "" {
		state.session = details.Session.Status
		state.activity = append(state.activity, autonomousActivity{
			At: now, Status: details.Session.Status,
			Text: "session started · " + details.Session.Status,
		})
	} else if state.session != details.Session.Status {
		state.activity = append(state.activity, autonomousActivity{
			At: now, Status: details.Session.Status,
			Text: "session · " + state.session + " → " + details.Session.Status,
		})
		state.session = details.Session.Status
	}
	for _, event := range details.Events {
		if event.Sequence <= state.lastEvent {
			continue
		}
		previous, found := state.jobStatuses[event.JobID]
		if !found {
			state.activity = append(state.activity, autonomousActivity{
				At: event.CreatedAt, Status: event.Data,
				Text: "created " + event.JobID + " · " + event.Data,
			})
		} else if previous != event.Data {
			state.activity = append(state.activity, autonomousActivity{
				At: event.CreatedAt, Status: event.Data,
				Text: event.JobID + " · " + previous + " → " + event.Data,
			})
		}
		state.jobStatuses[event.JobID] = event.Data
		state.lastEvent = event.Sequence
	}
	for _, job := range details.Jobs {
		previous, found := state.jobStatuses[job.ID]
		switch {
		case !found:
			state.activity = append(state.activity, autonomousActivity{
				At: now, Status: job.Status,
				Text: "created " + job.ID + " · " + job.Status,
			})
		case len(details.Events) == 0 && previous != job.Status:
			state.activity = append(state.activity, autonomousActivity{
				At: now, Status: job.Status,
				Text: job.ID + " · " + previous + " → " + job.Status,
			})
		}
		state.jobStatuses[job.ID] = job.Status
	}
	if len(state.activity) > 200 {
		state.activity = append([]autonomousActivity(nil), state.activity[len(state.activity)-200:]...)
	}
}

func renderAutonomousScreen(
	details store.AutonomousDetails,
	activity []autonomousActivity,
	loadErr error,
	width, height int,
	color bool,
) string {
	return renderAutonomousScreenWithHint(
		details, activity, loadErr, width, height, color,
		"q detach · r refresh · work continues in daemon",
	)
}

func renderAutonomousScreenWithHint(
	details store.AutonomousDetails,
	activity []autonomousActivity,
	loadErr error,
	width, height int,
	color bool,
	hint string,
) string {
	width = max(32, width)
	height = max(12, height)
	var frame bytes.Buffer
	title := "Pearl autonomous"
	status := details.Session.Status
	if status == "" {
		status = "connecting"
	}
	statusText := strings.ToUpper(status)
	titleWidth := len([]rune(title))
	statusWidth := len([]rune(statusText))
	if titleWidth+statusWidth+1 <= width {
		fmt.Fprintf(&frame, "%s%s%s\n",
			dashboardPaint(color, ansiBold+ansiCyan, title),
			strings.Repeat(" ", width-titleWidth-statusWidth),
			dashboardPaint(color, jobStatusColor(status), statusText),
		)
	} else {
		fmt.Fprintln(&frame, dashboardTruncate(title, width))
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, strings.Repeat("─", width)))
	if loadErr != nil {
		fmt.Fprintln(&frame, dashboardPaint(
			color, ansiRed, dashboardTruncate("Daemon: "+dashboardText(loadErr.Error()), width),
		))
	} else {
		goal := "Goal: " + dashboardText(details.Session.Goal)
		workspace := "Workspace: " + filepath.Base(details.Session.WorkspaceRoot)
		fmt.Fprintln(&frame, dashboardTruncate(goal, width))
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate(workspace, width)))
	}

	jobsHeight := max(3, min(len(details.Jobs)+2, (height-8)/2+1))
	fmt.Fprintln(&frame)
	fmt.Fprintln(&frame, dashboardPaint(color, ansiBold, "JOBS"))
	jobRows := 0
	if len(details.Jobs) == 0 {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, "  Waiting for the coordinator to create work."))
		jobRows++
	} else {
		start := max(0, len(details.Jobs)-jobsHeight)
		for _, job := range details.Jobs[start:] {
			statusWidth := 13
			idWidth := min(20, max(8, width-statusWidth-4))
			line := fmt.Sprintf("  %-*s  %s", idWidth,
				dashboardTruncate(job.ID, idWidth),
				dashboardPaint(color, jobStatusColor(job.Status), job.Status),
			)
			fmt.Fprintln(&frame, line)
			jobRows++
		}
	}
	for jobRows < jobsHeight {
		fmt.Fprintln(&frame)
		jobRows++
	}

	fmt.Fprintln(&frame, dashboardPaint(color, ansiBold, "ACTIVITY"))
	activityHeight := max(1, height-9-jobsHeight)
	if len(activity) == 0 {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, "  Waiting for status changes."))
		activityHeight--
	} else {
		start := max(0, len(activity)-activityHeight)
		for _, entry := range activity[start:] {
			line := entry.At.Local().Format("15:04:05") + "  " + entry.Text
			fmt.Fprintln(&frame, dashboardPaint(
				color, jobStatusColor(entry.Status), dashboardTruncate(line, width),
			))
		}
		activityHeight -= len(activity) - start
	}
	for activityHeight > 0 {
		fmt.Fprintln(&frame)
		activityHeight--
	}

	message := details.Session.Summary
	if details.Session.Error != "" {
		message = "Error: " + details.Session.Error
	}
	if hint != "" {
		if message == "" {
			message = hint
		} else {
			message = hint + " · " + message
		}
	}
	if message == "" {
		message = "Session active"
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, strings.Repeat("─", width)))
	fmt.Fprintln(&frame, dashboardTruncate(dashboardText(message), width))
	return fitAutonomousFrame(frame.String(), height)
}

func fitAutonomousFrame(frame string, height int) string {
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n"
}
