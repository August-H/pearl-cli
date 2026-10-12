package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

const (
	dashboardSuggestionLimit    = 12
	dashboardCommandOutputLimit = 100
	dashboardCommandBufferLimit = 64 * 1024
	dashboardTitle              = "Pearl"
	dashboardThinkingMessage    = "Thinking..."
)

type dashboardCommandSuggestion struct {
	usage       string
	completion  string
	description string
}

type dashboardConfigureStep string

const (
	dashboardConfigureInactive    dashboardConfigureStep = ""
	dashboardConfigureAPIKey      dashboardConfigureStep = "api_key"
	dashboardConfigureModel       dashboardConfigureStep = "model"
	dashboardConfigureCustomModel dashboardConfigureStep = "custom_model"
)

type dashboardCommandView struct {
	command       string
	output        string
	running       bool
	exitCode      int
	configureStep dashboardConfigureStep
	recalling     bool
	model         string
}

const dashboardHistoryLimit = 100

// dashboardHistory recalls sent commands with ↑/↓, newest first.
type dashboardHistory struct {
	entries  []string
	index    int
	browsing bool
}

func (history *dashboardHistory) add(command string) {
	history.browsing = false
	command = strings.TrimSpace(command)
	if command == "" || len(history.entries) > 0 && history.entries[len(history.entries)-1] == command {
		return
	}
	history.entries = append(history.entries, command)
	if len(history.entries) > dashboardHistoryLimit {
		history.entries = history.entries[len(history.entries)-dashboardHistoryLimit:]
	}
}

// previous returns the next older command, staying on the oldest.
func (history *dashboardHistory) previous() (string, bool) {
	if len(history.entries) == 0 {
		return "", false
	}
	if !history.browsing {
		history.browsing, history.index = true, len(history.entries)-1
	} else if history.index > 0 {
		history.index--
	}
	return history.entries[history.index], true
}

// next returns the next newer command, or an empty prompt past the newest.
func (history *dashboardHistory) next() (string, bool) {
	if !history.browsing {
		return "", false
	}
	if history.index < len(history.entries)-1 {
		history.index++
		return history.entries[history.index], true
	}
	history.browsing = false
	return "", true
}

type dashboardJobsView struct {
	open           bool
	archived       bool
	showAll        bool
	selected       int
	scroll         int
	bodyHeight     int
	confirmArchive bool
	notice         string
	wheel          listWheelNavigation
}

type dashboardJobDetailsView struct {
	job             store.Job
	sections        []jobViewSection
	selected        int
	scroll          int
	bodyHeight      int
	totalLines      int
	headerPositions []int
	returnToJobs    bool
	keepSelected    bool
}

type dashboardAutonomousView struct {
	sessionID string
	details   store.AutonomousDetails
	state     autonomousTUIState
	loadErr   error
}

type dashboardAutonomousCommand struct {
	goal     string
	resumeID string
	latest   bool
}

type dashboardCommandWriter struct {
	write func(string)
}

func (writer dashboardCommandWriter) Write(value []byte) (int, error) {
	writer.write(string(value))
	return len(value), nil
}

var dashboardCommandOptions = []dashboardCommandSuggestion{
	{usage: "run <job-id>", completion: "run ", description: "Run or retry a job"},
	{usage: "run --detach <job-id>", completion: "run --detach ", description: "Run without attaching"},
	{usage: "configure", completion: "configure", description: "Set the API key and model"},
	{usage: "model", completion: "model", description: "Browse and set the model"},
	{usage: "model --list", completion: "model --list", description: "List available models"},
	{usage: "model --free", completion: "model --free", description: "Use the Free Tier"},
	{usage: "jobs", completion: "jobs", description: "List the job board"},
	{usage: "jobs view <job-id>", completion: "jobs view ", description: "Show job details and transcript"},
	{usage: "archive", completion: "archive", description: "List archived jobs"},
	{usage: "autonomous <goal>", completion: "autonomous ", description: "Start an autonomous session"},
	{usage: "autonomous --resume <session-id>", completion: "autonomous --resume ", description: "Resume an autonomous session"},
	{usage: "job <prompt>", completion: "job ", description: "Create a job in the current directory"},
	{usage: "job <prompt> -n <name>", completion: "job ", description: "Give the job a custom ID"},
	{usage: "job <prompt> [-n name] --directory", completion: "job ", description: "Choose the job directory"},
	{usage: "job <prompt> --harness codex", completion: "job ", description: "Run this job with Codex"},
	{usage: "harness", completion: "harness", description: "Show the default agent harness"},
	{usage: "harness codex", completion: "harness codex", description: "Run new jobs with Codex"},
	{usage: "harness pearl", completion: "harness pearl", description: "Run new jobs with Pearl's harness"},
	{usage: "status", completion: "status", description: "Show daemon and queue status"},
	{usage: "update", completion: "update", description: "Update Pearl to the latest release"},
	{usage: "attach <job-id>", completion: "attach ", description: "Stream a job's output"},
	{usage: "respond <job-id> \"<answer>\"", completion: "respond ", description: "Answer a paused job"},
	{usage: "cancel <job-id>", completion: "cancel ", description: "Cancel a job"},
	{usage: "retry <job-id>", completion: "retry ", description: "Retry a finished job"},
	{usage: "schedule list", completion: "schedule list", description: "List recurring jobs"},
	{usage: "schedule add --every <duration> [--name <name>] \"<prompt>\"", completion: "schedule add --every ", description: "Add a recurring job"},
	{usage: "schedule remove <schedule-id>", completion: "schedule remove ", description: "Remove a recurring job"},
	{usage: "daemon status", completion: "daemon status", description: "Show daemon status"},
	{usage: "daemon start", completion: "daemon start", description: "Start the daemon"},
	{usage: "daemon stop", completion: "daemon stop", description: "Stop the daemon"},
	{usage: "daemon restart", completion: "daemon restart", description: "Restart the daemon"},
	{usage: "daemon install", completion: "daemon install", description: "Start Pearl at login"},
	{usage: "daemon uninstall", completion: "daemon uninstall", description: "Remove the login service"},
	{usage: "help", completion: "help", description: "Show command help"},
	{usage: "exit", completion: "exit", description: "Close the dashboard"},
}

var dashboardConfigureModelOptions = []dashboardCommandSuggestion{
	{usage: "free", completion: "free", description: "Use openrouter/free"},
	{usage: "custom", completion: "custom", description: "Enter a model ID"},
}

var dashboardCommandDescriptions = map[string]string{
	"run":        "Run or retry a job",
	"configure":  "Set the API key and model",
	"model":      "Browse and set the model",
	"harness":    "Show or set the default agent harness",
	"jobs":       "List the job board",
	"archive":    "List archived jobs",
	"autonomous": "Open or start autonomous mode",
	"job":        "Create a job",
	"status":     "Show daemon and queue status",
	"update":     "Update Pearl to the latest release",
	"attach":     "Stream a job's output",
	"respond":    "Answer a paused job",
	"cancel":     "Cancel a job",
	"retry":      "Retry a finished job",
	"schedule":   "Manage recurring jobs",
	"daemon":     "Manage the background process",
	"help":       "Show command help",
	"exit":       "Close the dashboard",
}

func runDashboard(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "Usage: pearl dashboard")
		return 2
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return printError("Dashboard", fmt.Errorf("requires an interactive terminal"))
	}
	if err := ensureDaemonRunning(); err != nil {
		return printError("Dashboard", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Dashboard", err)
	}

	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exit, err := dashboardLoop(
		ctx,
		os.Stdin,
		os.Stdout,
		client,
		func() int { return dashboardWidth(os.Stdout) },
		color,
		time.Second,
	)
	canceled := ctx.Err() != nil
	stop()
	if canceled || exit {
		return 0
	}
	if err != nil {
		return printError("Dashboard", err)
	}
	return 0
}

func dashboardLoop(
	ctx context.Context,
	input *os.File,
	output io.Writer,
	client *daemonClient,
	readWidth func() int,
	color bool,
	refresh time.Duration,
) (bool, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return false, fmt.Errorf("enable command input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)

	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, jobsListMouseDisable, "\x1b[?25h\x1b[?1049l")

	sessionContext, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	var inputLock sync.Mutex
	var renderLock sync.Mutex
	mouseEnabled := false
	commandText := ""
	notice := ""
	selectedSuggestion := 0
	var history dashboardHistory
	currentModel := currentModelID()
	commandView := dashboardCommandView{}
	configureAPIKey := ""
	var commandCancel context.CancelFunc
	var commandWait sync.WaitGroup
	jobsView := dashboardJobsView{}
	var jobDetailsView *dashboardJobDetailsView
	var autonomousView *dashboardAutonomousView
	var modelView *modelPickerState
	dashboardWorkspace := currentWorkspace()
	var jobsLock sync.RWMutex
	var jobs []store.Job
	var archivedJobs []store.Job
	var loadErr error
	var archiveLoadErr error
	loadJobs := func(archived bool) ([]store.Job, error) {
		jobsLock.RLock()
		defer jobsLock.RUnlock()
		if archived {
			return append([]store.Job(nil), archivedJobs...), archiveLoadErr
		}
		return append([]store.Job(nil), jobs...), loadErr
	}
	jobSnapshot := func(archived, showAll bool) ([]store.Job, error) {
		loaded, err := loadJobs(archived)
		if !showAll && err == nil {
			loaded = filterJobsForWorkspace(loaded, dashboardWorkspace)
		}
		return loaded, err
	}
	draw := func(refreshJobs bool) {
		renderLock.Lock()
		defer renderLock.Unlock()
		inputLock.Lock()
		archiveOpen := jobsView.open && jobsView.archived
		jobsShowAll := jobsView.showAll
		autonomousID := ""
		if autonomousView != nil {
			autonomousID = autonomousView.sessionID
		}
		inputLock.Unlock()
		if refreshJobs {
			model := currentModelID()
			inputLock.Lock()
			currentModel = model
			inputLock.Unlock()
			requestContext, cancel := context.WithTimeout(sessionContext, 2*time.Second)
			if autonomousID != "" {
				loaded, loadedErr := client.autonomousDetails(requestContext, autonomousID)
				cancel()
				inputLock.Lock()
				if autonomousView != nil && autonomousView.sessionID == autonomousID {
					autonomousView.loadErr = loadedErr
					if loadedErr == nil {
						autonomousView.details = loaded
						updateAutonomousActivity(
							&autonomousView.state, loaded, time.Now(),
						)
					}
				}
				inputLock.Unlock()
			} else {
				var loadedJobs []store.Job
				var loadedErr error
				if archiveOpen {
					loadedJobs, loadedErr = client.archivedJobs(requestContext)
				} else {
					loadedJobs, loadedErr = client.jobs(requestContext)
				}
				cancel()
				jobsLock.Lock()
				if archiveOpen {
					if loadedErr == nil {
						archivedJobs = loadedJobs
					}
					archiveLoadErr = loadedErr
				} else {
					if loadedErr == nil {
						jobs = loadedJobs
					}
					loadErr = loadedErr
				}
				jobsLock.Unlock()
			}
		}
		boardJobs, visibleLoadErr := loadJobs(archiveOpen)
		visibleJobs := boardJobs
		if jobsView.open && !jobsShowAll {
			visibleJobs = filterJobsForWorkspace(boardJobs, dashboardWorkspace)
		}
		width := readWidth()
		height := dashboardHeight(input)
		inputLock.Lock()
		wantMouse := modelView != nil || jobsView.open && jobDetailsView == nil && autonomousView == nil
		if wantMouse != mouseEnabled {
			if wantMouse {
				fmt.Fprint(output, jobsListMouseEnable)
			} else {
				fmt.Fprint(output, jobsListMouseDisable)
			}
			mouseEnabled = wantMouse
		}
		if modelView != nil {
			screen := modelView.screen(width, height, color)
			inputLock.Unlock()
			fmt.Fprint(output, "\x1b[H", listTerminalFrame(screen.Frame))
			return
		}
		if autonomousView != nil {
			frame := renderAutonomousScreenWithHint(
				autonomousView.details,
				autonomousView.state.activity,
				autonomousView.loadErr,
				width,
				height,
				color,
				"Esc back · r refresh · work continues in daemon",
			)
			inputLock.Unlock()
			fmt.Fprint(output, "\x1b[H\x1b[2J", dashboardTerminalFrame(frame))
			return
		}
		if jobDetailsView != nil {
			view := *jobDetailsView
			screen := renderJobViewScreenWithQuitLabel(
				view.job, view.sections, width, height,
				view.selected, view.scroll, color, "Esc back",
			)
			if view.keepSelected && view.selected >= 0 &&
				view.selected < len(screen.HeaderPositions) {
				header := screen.HeaderPositions[view.selected]
				if header < view.scroll {
					view.scroll = header
				} else if header >= view.scroll+screen.BodyHeight {
					view.scroll = header - screen.BodyHeight + 1
				}
				screen = renderJobViewScreenWithQuitLabel(
					view.job, view.sections, width, height,
					view.selected, view.scroll, color, "Esc back",
				)
			}
			view.scroll = min(
				max(0, view.scroll), max(0, screen.TotalLines-screen.BodyHeight),
			)
			view.bodyHeight = screen.BodyHeight
			view.totalLines = screen.TotalLines
			view.headerPositions = screen.HeaderPositions
			view.keepSelected = false
			*jobDetailsView = view
			inputLock.Unlock()
			fmt.Fprint(output, "\x1b[H\x1b[2J", dashboardTerminalFrame(screen.Frame))
			return
		}
		if jobsView.open {
			bodyHeight := max(3, height-5)
			if len(visibleJobs) == 0 {
				jobsView.selected, jobsView.scroll = 0, 0
			} else {
				jobsView.selected = min(max(0, jobsView.selected), len(visibleJobs)-1)
				if jobsView.selected < jobsView.scroll {
					jobsView.scroll = jobsView.selected
				} else if jobsView.selected >= jobsView.scroll+bodyHeight {
					jobsView.scroll = jobsView.selected - bodyHeight + 1
				}
				jobsView.scroll = min(
					max(0, jobsView.scroll), max(0, len(visibleJobs)-bodyHeight),
				)
			}
			footer := ""
			if jobsView.confirmArchive && len(visibleJobs) > 0 {
				footer = fmt.Sprintf(
					"Archive %s? y confirm · n cancel",
					jobViewText(visibleJobs[jobsView.selected].ID),
				)
			} else if jobsView.notice != "" {
				mode := jobsListMode{allowActions: !jobsView.archived}
				footer = jobsView.notice + " · " + jobsListModeFooter(
					mode, visibleJobs, jobsView.selected, "Esc back",
				)
			}
			mode := jobsListMode{allowActions: !jobsView.archived}
			if footer == "" {
				footer = jobsListModeFooter(mode, visibleJobs, jobsView.selected, "Esc back")
			}
			title := "Pearl"
			if jobsView.archived {
				title = "Pearl archive"
			}
			if jobsShowAll {
				title += " · all workspaces"
			}
			renderList := renderJobsListScreenWithTitleAndFooter
			if jobsShowAll {
				renderList = renderJobsListScreenShowingWorkspace
			}
			screen := renderList(
				visibleJobs, width, height, jobsView.selected, jobsView.scroll,
				color, "Esc back", footer, title,
			)
			jobsView.bodyHeight = screen.BodyHeight
			inputLock.Unlock()
			fmt.Fprint(output, "\x1b[H", listTerminalFrame(screen.Frame))
			return
		}
		visibleCommand := commandText
		visibleNotice := notice
		visibleSuggestion := selectedSuggestion
		visibleCommandView := commandView
		visibleCommandView.recalling = history.browsing
		visibleCommandView.model = displayCurrentModel(currentModel)
		inputLock.Unlock()
		fmt.Fprint(output, "\x1b[H\x1b[2J")
		frame := renderDashboardState(
			visibleJobs, time.Now(), width, color, visibleLoadErr, visibleCommand, visibleNotice,
			visibleSuggestion, visibleCommandView,
		)
		fmt.Fprint(output, dashboardTerminalFrame(frame))
	}
	finishDashboardConfigure := func(apiKey, model string) {
		inputLock.Lock()
		configureAPIKey = ""
		commandText = ""
		selectedSuggestion = 0
		commandView = dashboardCommandView{command: "configure", running: true}
		inputLock.Unlock()
		draw(false)

		_, configureErr := saveOpenRouterConfiguration(apiKey, model)
		commandOutput := "Configuration saved.\nModel: " + strings.TrimSpace(model) + "\n"
		exitCode := 0
		if configureErr != nil {
			commandOutput = "Configure: " + configureErr.Error() + "\n"
			exitCode = 1
		}
		inputLock.Lock()
		commandView.running = false
		commandView.exitCode = exitCode
		commandView.output = commandOutput
		inputLock.Unlock()
		draw(true)
	}
	draw(true)

	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	resize, stopResize := watchDashboardResize()
	defer stopResize()
	var refreshWait sync.WaitGroup
	refreshWait.Add(1)
	go func() {
		defer refreshWait.Done()
		for {
			select {
			case <-sessionContext.Done():
				return
			case <-ticker.C:
				draw(true)
			case <-resize:
				draw(false)
			}
		}
	}()
	defer func() {
		cancelSession()
		commandWait.Wait()
		refreshWait.Wait()
	}()

	type readResult struct {
		character rune
		err       error
	}
	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		resultReady := make(chan readResult, 1)
		go func() {
			character, err := readTerminalKey(reader)
			resultReady <- readResult{character: character, err: err}
		}()
		var result readResult
		select {
		case <-sessionContext.Done():
			return true, nil
		case result = <-resultReady:
		}
		if result.err != nil {
			return false, result.err
		}
		character := result.character
		if escapeSequence != "" {
			escapeSequence += string(character)
			action, complete := jobViewEscapeAction(escapeSequence)
			if !complete {
				continue
			}
			escapeSequence = ""
			inputLock.Lock()
			snapshotArchived := jobsView.open && jobsView.archived
			snapshotShowAll := jobsView.showAll
			inputLock.Unlock()
			suggestionJobs, _ := jobSnapshot(snapshotArchived, snapshotShowAll)
			inputLock.Lock()
			switch {
			case modelView != nil:
				if !modelView.applyAction(action) {
					inputLock.Unlock()
					continue
				}
			case autonomousView != nil:
			case jobDetailsView != nil:
				dashboardApplyJobDetailsAction(jobDetailsView, action)
			case jobsView.open:
				if !dashboardApplyJobsViewAction(&jobsView, action, len(suggestionJobs)) {
					inputLock.Unlock()
					continue
				}
			case (action == "previous" || action == "next") &&
				commandView.configureStep == dashboardConfigureInactive &&
				(history.browsing || commandText == ""):
				recall := history.next
				if action == "previous" {
					recall = history.previous
				}
				if recalled, ok := recall(); ok {
					commandText, selectedSuggestion = recalled, 0
				}
			case action == "previous" || action == "next":
				direction := 1
				if action == "previous" {
					direction = -1
				}
				selectedSuggestion = dashboardMoveSuggestionForStepWithJobs(
					commandText, commandView.configureStep,
					selectedSuggestion, direction, suggestionJobs,
				)
			}
			inputLock.Unlock()
			draw(false)
			continue
		}
		if character == '\x1b' {
			escapeSequence = "\x1b"
			continue
		}

		inputLock.Lock()
		snapshotArchived := jobsView.open && jobsView.archived
		snapshotShowAll := jobsView.showAll
		inputLock.Unlock()
		navigationJobs, _ := jobSnapshot(snapshotArchived, snapshotShowAll)
		inputLock.Lock()
		if modelView != nil {
			chosen, done := modelView.handleKey(character)
			if done && chosen == "" {
				notice = "Model unchanged: " + displayCurrentModel(modelView.currentID)
				modelView = nil
			}
			inputLock.Unlock()
			if chosen != "" {
				_, saveErr := openrouter_request.ConfigureAgentModel(chosen)
				inputLock.Lock()
				if saveErr != nil {
					modelView.notice = "Model: " + dashboardText(saveErr.Error())
				} else {
					modelView = nil
					currentModel = chosen
					notice = "Model: " + chosen
				}
				inputLock.Unlock()
			}
			draw(false)
			continue
		}
		if autonomousView != nil {
			refreshView := false
			switch character {
			case 3, 4, escapeKey:
				autonomousView = nil
				refreshView = true
			case 'r', 'R':
				refreshView = true
			}
			inputLock.Unlock()
			if refreshView {
				draw(true)
			}
			continue
		}
		if jobDetailsView != nil {
			back := false
			action := ""
			switch character {
			case 3, 4, escapeKey:
				back = true
			case '\r', '\n', ' ':
				action = "toggle"
			case '\t':
				action = "next"
			case 'j':
				action = "scroll_down"
			case 'k':
				action = "scroll_up"
			case 'a', 'A':
				action = "expand_all"
			case 'c', 'C':
				action = "collapse_all"
			case 'g':
				action = "home"
			case 'G':
				action = "end"
			}
			if back {
				returnToJobs := jobDetailsView.returnToJobs
				jobDetailsView = nil
				if !returnToJobs {
					jobsView.open = false
				}
			} else {
				dashboardApplyJobDetailsAction(jobDetailsView, action)
			}
			inputLock.Unlock()
			draw(false)
			continue
		}
		if jobsView.open {
			if jobsView.confirmArchive {
				archiveJobID := ""
				switch character {
				case '\r', '\n', 'y', 'Y':
					if len(navigationJobs) > 0 {
						jobsView.selected = min(max(0, jobsView.selected), len(navigationJobs)-1)
						archiveJobID = navigationJobs[jobsView.selected].ID
					}
				case 3, 4, 'n', 'N', escapeKey:
					jobsView.confirmArchive = false
					jobsView.notice = ""
				}
				inputLock.Unlock()
				refreshJobs := false
				if archiveJobID != "" {
					requestContext, cancel := context.WithTimeout(sessionContext, 5*time.Second)
					archiveErr := client.archiveJob(requestContext, archiveJobID)
					cancel()
					inputLock.Lock()
					jobsView.confirmArchive = false
					if archiveErr != nil {
						jobsView.notice = "Archive failed: " + dashboardText(archiveErr.Error())
					} else {
						jobsView.notice = "Archived " + jobViewText(archiveJobID)
						refreshJobs = true
					}
					inputLock.Unlock()
				}
				draw(refreshJobs)
				continue
			}
			openJobID := ""
			refreshAfterToggle := false
			switch character {
			case 3, 4, escapeKey:
				jobsView.open = false
			case 'a', 'A':
				jobsView.showAll = !jobsView.showAll
				jobsView.selected, jobsView.scroll = 0, 0
				jobsView.notice = ""
				refreshAfterToggle = true
			case 'd', 'D':
				if !jobsView.archived && len(navigationJobs) > 0 {
					jobsView.confirmArchive = true
					jobsView.notice = ""
				}
			case ' ':
				if !jobsView.archived && len(navigationJobs) > 0 {
					jobsView.selected = min(max(0, jobsView.selected), len(navigationJobs)-1)
					if command := jobsListPreloadCommand(navigationJobs[jobsView.selected]); command != "" {
						commandText = command
						selectedSuggestion = 0
						notice = ""
						jobsView.open = false
					}
				}
			case '\r', '\n':
				if len(navigationJobs) > 0 {
					jobsView.selected = min(max(0, jobsView.selected), len(navigationJobs)-1)
					openJobID = navigationJobs[jobsView.selected].ID
				}
			case '\t', 'j':
				dashboardApplyJobsViewAction(&jobsView, "next", len(navigationJobs))
			case 'k':
				dashboardApplyJobsViewAction(&jobsView, "previous", len(navigationJobs))
			case 'g':
				dashboardApplyJobsViewAction(&jobsView, "home", len(navigationJobs))
			case 'G':
				dashboardApplyJobsViewAction(&jobsView, "end", len(navigationJobs))
			}
			inputLock.Unlock()
			if openJobID != "" {
				requestContext, cancel := context.WithTimeout(sessionContext, 5*time.Second)
				details, detailsErr := client.jobDetails(requestContext, openJobID)
				cancel()
				var sections []jobViewSection
				if detailsErr == nil {
					sections, detailsErr = buildJobViewSections(details)
				}
				inputLock.Lock()
				if detailsErr != nil {
					jobsView.open = false
					notice = "Jobs: " + detailsErr.Error()
				} else {
					jobDetailsView = &dashboardJobDetailsView{
						job: details.Job, sections: sections,
						returnToJobs: true, keepSelected: true,
					}
				}
				inputLock.Unlock()
			}
			draw(refreshAfterToggle)
			continue
		}
		notice = ""
		history.browsing = false
		switch character {
		case escapeKey:
			if commandView.configureStep != dashboardConfigureInactive {
				configureAPIKey = ""
				commandText = ""
				selectedSuggestion = 0
				commandView = dashboardCommandView{}
				notice = "Configuration canceled."
			}
		case 3:
			if commandView.configureStep != dashboardConfigureInactive {
				configureAPIKey = ""
				commandText = ""
				selectedSuggestion = 0
				commandView = dashboardCommandView{}
				notice = "Configuration canceled."
				inputLock.Unlock()
				draw(false)
				continue
			}
			if commandView.running {
				cancel := commandCancel
				if cancel != nil {
					commandCancel = nil
					notice = "Canceling the running command."
				} else {
					notice = "Waiting for the command to stop."
				}
				inputLock.Unlock()
				if cancel != nil {
					cancel()
				}
				draw(false)
				continue
			}
			inputLock.Unlock()
			return true, nil
		case 4:
			if commandText == "" {
				inputLock.Unlock()
				return true, nil
			}
		case 8, 127:
			characters := []rune(commandText)
			if len(characters) > 0 {
				commandText = string(characters[:len(characters)-1])
			}
			selectedSuggestion = 0
		case '\t':
			commandText = dashboardFillSuggestionForStepWithJobs(
				commandText, commandView.configureStep, selectedSuggestion, navigationJobs,
			)
			selectedSuggestion = 0
		case 21:
			commandText = ""
			selectedSuggestion = 0
		case '\r', '\n':
			if commandView.configureStep != dashboardConfigureInactive {
				switch commandView.configureStep {
				case dashboardConfigureAPIKey:
					apiKey := strings.TrimSpace(commandText)
					if apiKey == "" {
						notice = "API key cannot be empty."
						inputLock.Unlock()
						draw(false)
						continue
					}
					configureAPIKey = apiKey
					commandText = ""
					selectedSuggestion = 0
					commandView.configureStep = dashboardConfigureModel
					inputLock.Unlock()
					draw(false)
					continue
				case dashboardConfigureModel:
					choice := strings.ToLower(strings.TrimSpace(commandText))
					if choice == "" {
						options := dashboardComposerSuggestions(
							commandText, commandView.configureStep,
						)
						if len(options) > 0 {
							selectedSuggestion %= len(options)
							if selectedSuggestion < 0 {
								selectedSuggestion += len(options)
							}
							choice = options[selectedSuggestion].completion
						}
					}
					switch choice {
					case "1", "free":
						apiKey := configureAPIKey
						inputLock.Unlock()
						finishDashboardConfigure(apiKey, "openrouter/free")
						continue
					case "2", "custom":
						commandText = ""
						selectedSuggestion = 0
						commandView.configureStep = dashboardConfigureCustomModel
						inputLock.Unlock()
						draw(false)
						continue
					default:
						notice = "Choose free or custom."
						commandText, selectedSuggestion = "", 0
						inputLock.Unlock()
						draw(false)
						continue
					}
				case dashboardConfigureCustomModel:
					model := strings.TrimSpace(commandText)
					if model == "" {
						notice = "Model ID cannot be empty."
						inputLock.Unlock()
						draw(false)
						continue
					}
					apiKey := configureAPIKey
					inputLock.Unlock()
					finishDashboardConfigure(apiKey, model)
					continue
				}
			}
			history.add(commandText)
			if commandView.running {
				notice = "Wait for the running command or press Ctrl-C to cancel it."
				commandText, selectedSuggestion = "", 0
				inputLock.Unlock()
				draw(false)
				continue
			}
			parsed, parseErr := parseDashboardCommand(commandText)
			if parseErr != nil {
				notice = parseErr.Error()
				commandText, selectedSuggestion = "", 0
				inputLock.Unlock()
				draw(false)
				continue
			}
			if len(parsed) == 0 {
				inputLock.Unlock()
				draw(false)
				continue
			}
			switch parsed[0] {
			case "exit", "quit":
				inputLock.Unlock()
				return true, nil
			case "pearl":
				notice = "Omit the pearl prefix. Type the command directly."
			case "dashboard":
				notice = "The dashboard is already open."
			case "autonomous":
				autonomousCommand, commandErr := parseDashboardAutonomousCommand(parsed[1:])
				if commandErr != nil {
					notice = commandErr.Error()
					break
				}
				commandText = ""
				selectedSuggestion = 0
				commandView = dashboardCommandView{}
				inputLock.Unlock()

				requestContext, cancel := context.WithTimeout(sessionContext, 5*time.Second)
				var session store.AutonomousSession
				var autonomousErr error
				switch {
				case autonomousCommand.latest:
					session, autonomousErr = client.latestAutonomousSession(requestContext)
				case autonomousCommand.resumeID != "":
					var details store.AutonomousDetails
					details, autonomousErr = client.autonomousDetails(
						requestContext, autonomousCommand.resumeID,
					)
					session = details.Session
				default:
					workspace, workspaceErr := os.Getwd()
					if workspaceErr != nil {
						autonomousErr = workspaceErr
					} else {
						session, autonomousErr = client.createAutonomousSession(
							requestContext, autonomousCommand.goal, workspace,
						)
					}
				}
				cancel()

				inputLock.Lock()
				if autonomousErr != nil {
					notice = "Autonomous: " + autonomousErr.Error()
				} else {
					autonomousView = &dashboardAutonomousView{
						sessionID: session.ID,
						state: autonomousTUIState{
							jobStatuses: make(map[string]string),
						},
					}
				}
				inputLock.Unlock()
				draw(true)
				continue
			case "archive":
				showAll, rest := parseShowAllFlag(parsed[1:])
				if len(rest) != 0 {
					notice = "Usage: archive [--all]"
					break
				}
				commandText = ""
				selectedSuggestion = 0
				commandView = dashboardCommandView{}
				jobsView = dashboardJobsView{open: true, archived: true, showAll: showAll}
				inputLock.Unlock()
				draw(true)
				continue
			case "jobs":
				switch {
				case len(parsed) == 1:
					commandText = ""
					selectedSuggestion = 0
					commandView = dashboardCommandView{}
					jobsView = dashboardJobsView{open: true}
					inputLock.Unlock()
					draw(true)
					continue
				case len(parsed) == 2 && (parsed[1] == "--all" || parsed[1] == "-a"):
					commandText = ""
					selectedSuggestion = 0
					commandView = dashboardCommandView{}
					jobsView = dashboardJobsView{open: true, showAll: true}
					inputLock.Unlock()
					draw(true)
					continue
				case len(parsed) == 3 && parsed[1] == "view":
					jobID := parsed[2]
					commandText = ""
					selectedSuggestion = 0
					commandView = dashboardCommandView{}
					inputLock.Unlock()

					requestContext, cancel := context.WithTimeout(sessionContext, 5*time.Second)
					details, detailsErr := client.jobDetails(requestContext, jobID)
					cancel()
					var sections []jobViewSection
					if detailsErr == nil {
						sections, detailsErr = buildJobViewSections(details)
					}
					inputLock.Lock()
					if detailsErr != nil {
						notice = "Jobs: " + detailsErr.Error()
					} else {
						jobsView.open = false
						jobDetailsView = &dashboardJobDetailsView{
							job: details.Job, sections: sections, keepSelected: true,
						}
					}
					inputLock.Unlock()
					draw(false)
					continue
				default:
					notice = dashboardInvalidCommandNotice(strings.Join(parsed, " "))
					commandText = ""
					selectedSuggestion = 0
				}
			case "configure":
				if len(parsed) != 1 {
					notice = "Usage: configure"
					break
				}
				commandText = ""
				configureAPIKey = ""
				selectedSuggestion = 0
				commandView = dashboardCommandView{
					configureStep: dashboardConfigureAPIKey,
				}
				inputLock.Unlock()
				draw(true)
				continue
			case "model":
				if interactive, refresh := dashboardModelPickerCommand(parsed[1:]); interactive {
					commandContext, cancel := context.WithCancel(sessionContext)
					commandCancel = cancel
					commandView = dashboardCommandView{
						command: strings.Join(parsed, " "), running: true,
						output: "Loading models from OpenRouter...\n",
					}
					commandText, selectedSuggestion = "", 0
					inputLock.Unlock()
					draw(false)
					commandWait.Add(1)
					go func() {
						defer commandWait.Done()
						defer cancel()
						picker, loadErr := loadModelPicker(commandContext, currentModelID(), refresh)
						inputLock.Lock()
						commandView.running = false
						commandCancel = nil
						if commandContext.Err() != nil {
							commandView.output = "Model selection canceled.\n"
							commandView.exitCode = 1
						} else if loadErr != nil {
							commandView.output = "Model: " + loadErr.Error() + "\n"
							commandView.exitCode = 1
						} else {
							commandView = dashboardCommandView{}
							modelView = picker
						}
						inputLock.Unlock()
						if sessionContext.Err() == nil {
							draw(false)
						}
					}()
					continue
				}
				fallthrough
			default:
				if _, known := dashboardCommandDescriptions[parsed[0]]; !known {
					notice = dashboardInvalidCommandNotice(strings.Join(parsed, " "))
					commandText = ""
					selectedSuggestion = 0
					break
				}
				commandContext, cancel := context.WithCancel(sessionContext)
				commandCancel = cancel
				commandView = dashboardCommandView{
					command: strings.Join(parsed, " "),
					running: true,
				}
				commandText = ""
				selectedSuggestion = 0
				inputLock.Unlock()
				draw(false)
				commandWait.Add(1)
				go func(arguments []string) {
					defer commandWait.Done()
					exitCode := executeDashboardCommand(
						commandContext,
						arguments,
						func(value string) {
							inputLock.Lock()
							commandView.output = dashboardAppendCommandOutput(
								commandView.output, value,
							)
							inputLock.Unlock()
							draw(false)
						},
					)
					inputLock.Lock()
					commandView.running = false
					commandView.exitCode = exitCode
					commandCancel = nil
					inputLock.Unlock()
					if sessionContext.Err() == nil {
						draw(true)
					}
				}(append([]string(nil), parsed...))
				continue
			}
			// Accepted commands continue above; anything left was rejected.
			commandText, selectedSuggestion = "", 0
		default:
			if !unicode.IsControl(character) && len([]rune(commandText)) < 4096 {
				commandText += string(character)
				selectedSuggestion = 0
			}
		}
		inputLock.Unlock()
		draw(false)
	}
}

func dashboardTerminalFrame(frame string) string {
	return strings.ReplaceAll(frame, "\n", "\r\n")
}

func dashboardInvalidCommandNotice(command string) string {
	return strings.ReplaceAll(invalidCommandMessage(command), "\n", " ")
}

func dashboardApplyJobsViewAction(
	view *dashboardJobsView,
	action string,
	jobCount int,
) bool {
	return dashboardApplyJobsViewActionAt(view, action, jobCount, time.Now())
}

func dashboardApplyJobsViewActionAt(
	view *dashboardJobsView,
	action string,
	jobCount int,
	now time.Time,
) bool {
	if view != nil && view.confirmArchive && (action == "scroll_up" || action == "scroll_down") {
		return false
	}
	if view == nil || jobCount <= 0 {
		if view != nil {
			view.selected, view.scroll = 0, 0
			view.wheel = listWheelNavigation{}
		}
		return false
	}
	action = view.wheel.filter(action, now)
	switch action {
	case "previous", "scroll_up":
		view.selected--
	case "next", "scroll_down":
		view.selected++
	case "page_up":
		view.selected -= max(1, view.bodyHeight-1)
	case "page_down":
		view.selected += max(1, view.bodyHeight-1)
	case "home":
		view.selected = 0
	case "end":
		view.selected = jobCount - 1
	default:
		return false
	}
	view.selected = min(max(0, view.selected), jobCount-1)
	view.confirmArchive = false
	view.notice = ""
	return true
}

func dashboardApplyJobDetailsAction(
	view *dashboardJobDetailsView,
	action string,
) {
	if view == nil || len(view.sections) == 0 {
		return
	}
	switch action {
	case "previous":
		view.selected = max(0, view.selected-1)
		view.keepSelected = true
	case "next":
		view.selected = min(len(view.sections)-1, view.selected+1)
		view.keepSelected = true
	case "toggle":
		view.sections[view.selected].Expanded = !view.sections[view.selected].Expanded
		view.keepSelected = true
	case "expand":
		view.sections[view.selected].Expanded = true
		view.keepSelected = true
	case "collapse":
		view.sections[view.selected].Expanded = false
		view.keepSelected = true
	case "expand_all":
		for index := range view.sections {
			view.sections[index].Expanded = true
		}
		view.keepSelected = true
	case "collapse_all":
		for index := range view.sections {
			view.sections[index].Expanded = false
		}
		view.keepSelected = true
	case "scroll_up":
		view.scroll--
	case "scroll_down":
		view.scroll++
	case "page_up":
		view.scroll -= max(1, view.bodyHeight-1)
	case "page_down":
		view.scroll += max(1, view.bodyHeight-1)
	case "home":
		view.scroll = 0
	case "end":
		view.scroll = max(0, view.totalLines-view.bodyHeight)
	default:
		return
	}
	view.scroll = min(
		max(0, view.scroll), max(0, view.totalLines-view.bodyHeight),
	)
}

func executeDashboardCommand(
	ctx context.Context,
	arguments []string,
	appendOutput func(string),
) int {
	executable, err := os.Executable()
	if err != nil {
		appendOutput(fmt.Sprintf("Could not locate the Pearl executable: %v\n", err))
		return 1
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = dashboardCommandEnvironment()
	writer := dashboardCommandWriter{write: appendOutput}
	command.Stdout = writer
	command.Stderr = writer
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			appendOutput("Command canceled.\n")
			return 130
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			return exitError.ExitCode()
		}
		appendOutput(fmt.Sprintf("Could not run command: %v\n", err))
		return 1
	}
	return 0
}

func dashboardCommandEnvironment() []string {
	environment := os.Environ()
	for index, value := range environment {
		if strings.HasPrefix(value, "NO_COLOR=") {
			environment[index] = "NO_COLOR=1"
			return environment
		}
	}
	return append(environment, "NO_COLOR=1")
}

func dashboardAppendCommandOutput(current, value string) string {
	current += value
	if len(current) > dashboardCommandBufferLimit {
		current = current[len(current)-dashboardCommandBufferLimit:]
	}
	return current
}

func dashboardCommandSuggestions(value string) []dashboardCommandSuggestion {
	return dashboardCommandSuggestionsInternal(value, nil, false)
}

func dashboardCommandSuggestionsWithJobs(
	value string,
	jobs []store.Job,
) []dashboardCommandSuggestion {
	return dashboardCommandSuggestionsInternal(value, jobs, true)
}

func dashboardCommandSuggestionsInternal(
	value string,
	jobs []store.Job,
	useJobIDs bool,
) []dashboardCommandSuggestion {
	input := strings.TrimLeftFunc(value, unicode.IsSpace)
	if input == "" {
		if value == "" {
			return nil
		}
		return dashboardTopLevelSuggestions("")
	}
	separator := strings.IndexFunc(input, unicode.IsSpace)
	if separator == -1 {
		return dashboardTopLevelSuggestions(strings.ToLower(input))
	}
	command := strings.ToLower(input[:separator])
	remainder := strings.TrimSpace(input[separator:])
	trailingSpace := strings.TrimRightFunc(input, unicode.IsSpace) != input
	if useJobIDs && command == "run" {
		return dashboardRunCommandSuggestions(remainder, trailingSpace, jobs)
	}
	if useJobIDs && command == "jobs" {
		return dashboardJobsCommandSuggestions(remainder, trailingSpace, jobs)
	}
	remainder = strings.ToLower(remainder)
	return dashboardCommandItemSuggestions(command, remainder)
}

func dashboardJobsCommandSuggestions(
	remainder string,
	trailingSpace bool,
	jobs []store.Job,
) []dashboardCommandSuggestion {
	const viewCommand = "view"
	lower := strings.ToLower(remainder)
	if lower == "" || (strings.HasPrefix(viewCommand, lower) && !trailingSpace) {
		return []dashboardCommandSuggestion{{
			usage:       "view <job-id>",
			completion:  "jobs view ",
			description: "Show job details and transcript",
		}}
	}
	if lower != viewCommand && !strings.HasPrefix(lower, viewCommand+" ") {
		return nil
	}

	query := ""
	if lower != viewCommand {
		query = strings.Trim(strings.TrimSpace(remainder[len(viewCommand):]), "\"'")
	}
	query = strings.ToLower(query)
	suggestions := make([]dashboardCommandSuggestion, 0, len(jobs))
	for _, job := range jobs {
		if !strings.HasPrefix(strings.ToLower(job.ID), query) {
			continue
		}
		suggestions = append(suggestions, dashboardCommandSuggestion{
			usage:       job.ID,
			completion:  "jobs view " + dashboardEscapeCommandArgument(job.ID),
			description: job.Status + "  " + dashboardRunSuggestionDescription(job),
		})
	}
	return suggestions
}

func dashboardRunCommandSuggestions(
	remainder string,
	trailingSpace bool,
	jobs []store.Job,
) []dashboardCommandSuggestion {
	const detachedOption = "--detach"
	lower := strings.ToLower(remainder)
	detached := false
	query := remainder
	switch {
	case lower == detachedOption && trailingSpace:
		detached = true
		query = ""
	case lower == detachedOption:
		return []dashboardCommandSuggestion{{
			usage:       "--detach <job-id>",
			completion:  "run --detach ",
			description: "Run without attaching",
		}}
	case strings.HasPrefix(lower, detachedOption+" "):
		detached = true
		query = strings.TrimSpace(remainder[len(detachedOption):])
	case strings.HasPrefix(detachedOption, lower) && strings.HasPrefix(lower, "-"):
		return []dashboardCommandSuggestion{{
			usage:       "--detach <job-id>",
			completion:  "run --detach ",
			description: "Run without attaching",
		}}
	case strings.HasPrefix(lower, "-"):
		return nil
	}

	query = strings.Trim(strings.ToLower(query), "\"'")
	prefix := "run "
	if detached {
		prefix = "run --detach "
	}
	suggestions := make([]dashboardCommandSuggestion, 0, len(jobs)+1)
	for _, job := range jobs {
		if !jobCanRun(job) ||
			!strings.HasPrefix(strings.ToLower(job.ID), query) {
			continue
		}
		suggestions = append(suggestions, dashboardCommandSuggestion{
			usage:       job.ID,
			completion:  prefix + dashboardEscapeCommandArgument(job.ID),
			description: dashboardRunSuggestionDescription(job),
		})
	}
	if !detached && query == "" {
		suggestions = append(suggestions, dashboardCommandSuggestion{
			usage:       "--detach <job-id>",
			completion:  "run --detach ",
			description: "Run without attaching",
		})
	}
	return suggestions
}

func dashboardRunSuggestionDescription(job store.Job) string {
	prompt := dashboardText(job.Prompt)
	directory := strings.TrimSpace(job.WorkspaceRoot)
	if directory == "" {
		return prompt
	}
	directory = dashboardText(displayJobDirectory(directory))
	if directory == "" {
		return prompt
	}
	return prompt + "  " + directory
}

func dashboardEscapeCommandArgument(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		if unicode.IsSpace(character) || character == '\\' || character == '\'' || character == '"' {
			escaped.WriteRune('\\')
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}

func dashboardTopLevelSuggestions(query string) []dashboardCommandSuggestion {
	suggestions := make([]dashboardCommandSuggestion, 0, len(dashboardCommandDescriptions))
	seen := make(map[string]bool, len(dashboardCommandDescriptions))
	for _, option := range dashboardCommandOptions {
		fields := strings.Fields(option.usage)
		if len(fields) == 0 {
			continue
		}
		command := fields[0]
		if seen[command] || !strings.HasPrefix(command, query) {
			continue
		}
		seen[command] = true
		suggestions = append(suggestions, dashboardCommandSuggestion{
			usage:       command,
			completion:  command,
			description: dashboardCommandDescriptions[command],
		})
	}
	return suggestions
}

func dashboardCommandItemSuggestions(
	command string,
	query string,
) []dashboardCommandSuggestion {
	if _, found := dashboardCommandDescriptions[command]; !found {
		return nil
	}
	prefixMatches := make([]dashboardCommandSuggestion, 0, dashboardSuggestionLimit)
	commandMatches := make([]dashboardCommandSuggestion, 0, dashboardSuggestionLimit)
	for _, option := range dashboardCommandOptions {
		fields := strings.Fields(option.usage)
		if len(fields) == 0 || fields[0] != command {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(option.usage, command))
		if item == "" {
			continue
		}
		suggestion := option
		suggestion.usage = item
		if query == "" || strings.HasPrefix(strings.ToLower(item), query) {
			prefixMatches = append(prefixMatches, suggestion)
		} else {
			commandMatches = append(commandMatches, suggestion)
		}
	}
	suggestions := prefixMatches
	if len(suggestions) == 0 {
		suggestions = commandMatches
	}
	return suggestions
}

func dashboardMoveSuggestion(value string, selected, direction int) int {
	return dashboardMoveSuggestionForStep(
		value, dashboardConfigureInactive, selected, direction,
	)
}

func dashboardMoveSuggestionForStep(
	value string,
	configureStep dashboardConfigureStep,
	selected, direction int,
) int {
	return dashboardMoveSuggestionForStepWithJobs(
		value, configureStep, selected, direction, nil,
	)
}

func dashboardMoveSuggestionForStepWithJobs(
	value string,
	configureStep dashboardConfigureStep,
	selected, direction int,
	jobs []store.Job,
) int {
	count := len(dashboardComposerSuggestionsWithJobs(value, configureStep, jobs))
	if count == 0 {
		return 0
	}
	selected = (selected + direction) % count
	if selected < 0 {
		selected += count
	}
	return selected
}

func dashboardFillSuggestion(value string, selected int) string {
	return dashboardFillSuggestionForStep(value, dashboardConfigureInactive, selected)
}

func dashboardFillSuggestionForStep(
	value string,
	configureStep dashboardConfigureStep,
	selected int,
) string {
	return dashboardFillSuggestionForStepWithJobs(value, configureStep, selected, nil)
}

func dashboardFillSuggestionForStepWithJobs(
	value string,
	configureStep dashboardConfigureStep,
	selected int,
	jobs []store.Job,
) string {
	suggestions := dashboardComposerSuggestionsWithJobs(value, configureStep, jobs)
	if len(suggestions) == 0 {
		return value
	}
	selected %= len(suggestions)
	if selected < 0 {
		selected += len(suggestions)
	}
	completion := suggestions[selected].completion
	if strings.TrimSpace(value) == "" {
		return completion
	}
	if !strings.HasPrefix(strings.ToLower(completion), strings.ToLower(value)) {
		return value
	}
	return completion
}

func dashboardComposerSuggestions(
	value string,
	configureStep dashboardConfigureStep,
) []dashboardCommandSuggestion {
	if configureStep == dashboardConfigureInactive {
		return dashboardCommandSuggestions(value)
	}
	return dashboardComposerSuggestionsWithJobs(value, configureStep, nil)
}

func dashboardComposerSuggestionsWithJobs(
	value string,
	configureStep dashboardConfigureStep,
	jobs []store.Job,
) []dashboardCommandSuggestion {
	if configureStep != dashboardConfigureModel {
		if configureStep != dashboardConfigureInactive {
			return nil
		}
		return dashboardCommandSuggestionsWithJobs(value, jobs)
	}
	query := strings.ToLower(strings.TrimSpace(value))
	var matches []dashboardCommandSuggestion
	for _, option := range dashboardConfigureModelOptions {
		if query == "" || strings.HasPrefix(option.usage, query) {
			matches = append(matches, option)
		}
	}
	if len(matches) == 0 {
		return dashboardConfigureModelOptions
	}
	return matches
}

func parseDashboardCommand(value string) ([]string, error) {
	var arguments []string
	var current strings.Builder
	var quote rune
	escaped := false
	tokenStarted := false

	flush := func() {
		if !tokenStarted {
			return
		}
		arguments = append(arguments, current.String())
		current.Reset()
		tokenStarted = false
	}

	for _, character := range value {
		if escaped {
			current.WriteRune(character)
			escaped = false
			tokenStarted = true
			continue
		}

		if quote != 0 {
			if character == quote {
				quote = 0
				tokenStarted = true
				continue
			}
			if quote == '"' && character == '\\' {
				escaped = true
				continue
			}
			current.WriteRune(character)
			tokenStarted = true
			continue
		}

		switch {
		case character == '\\':
			escaped = true
			tokenStarted = true
		case character == '\'' || character == '"':
			quote = character
			tokenStarted = true
		case unicode.IsSpace(character):
			flush()
		default:
			current.WriteRune(character)
			tokenStarted = true
		}
	}

	if escaped {
		return nil, fmt.Errorf("unfinished escape sequence")
	}
	if quote != 0 {
		return nil, fmt.Errorf("missing closing %c quote", quote)
	}
	flush()
	return arguments, nil
}

func parseDashboardAutonomousCommand(
	arguments []string,
) (dashboardAutonomousCommand, error) {
	if len(arguments) == 0 {
		return dashboardAutonomousCommand{latest: true}, nil
	}
	if arguments[0] == "--resume" {
		if len(arguments) != 2 || strings.TrimSpace(arguments[1]) == "" {
			return dashboardAutonomousCommand{}, errors.New(
				"Usage: autonomous --resume <session-id>",
			)
		}
		return dashboardAutonomousCommand{resumeID: strings.TrimSpace(arguments[1])}, nil
	}
	if strings.HasPrefix(arguments[0], "-") {
		return dashboardAutonomousCommand{}, errors.New(
			"Usage: autonomous [--resume <session-id>] [\"goal\"]",
		)
	}
	goal := strings.TrimSpace(strings.Join(arguments, " "))
	if goal == "" {
		return dashboardAutonomousCommand{}, errors.New("Autonomous goal cannot be empty.")
	}
	return dashboardAutonomousCommand{goal: goal}, nil
}

func renderDashboard(
	jobs []store.Job,
	now time.Time,
	width int,
	color bool,
	loadErr error,
) string {
	return renderDashboardWithInput(jobs, now, width, color, loadErr, "", "")
}

func renderDashboardWithInput(
	jobs []store.Job,
	now time.Time,
	width int,
	color bool,
	loadErr error,
	commandText string,
	notice string,
) string {
	return renderDashboardWithSelection(
		jobs, now, width, color, loadErr, commandText, notice, 0,
	)
}

func renderDashboardWithSelection(
	jobs []store.Job,
	now time.Time,
	width int,
	color bool,
	loadErr error,
	commandText string,
	notice string,
	selectedSuggestion int,
) string {
	return renderDashboardState(
		jobs, now, width, color, loadErr, commandText, notice,
		selectedSuggestion, dashboardCommandView{},
	)
}

func renderDashboardState(
	jobs []store.Job,
	now time.Time,
	width int,
	color bool,
	loadErr error,
	commandText string,
	notice string,
	selectedSuggestion int,
	commandView dashboardCommandView,
) string {
	if width < 1 {
		width = 1
	}
	active := make([]store.Job, 0, len(jobs))
	running, queued, waiting := 0, 0, 0
	for _, job := range jobs {
		switch job.Status {
		case store.JobRunning:
			running++
		case store.JobQueued:
			queued++
		case store.JobWaitingInput:
			waiting++
		default:
			continue
		}
		active = append(active, job)
	}

	var frame bytes.Buffer
	title := dashboardPaint(color, ansiBold+ansiCyan, dashboardTitle)
	clock := now.Local().Format("15:04:05")
	if width >= len(dashboardTitle)+len(clock)+1 {
		fmt.Fprintf(&frame, "%s%s%s\n", title,
			strings.Repeat(" ", width-len(dashboardTitle)-len(clock)),
			dashboardPaint(color, ansiDim, clock))
	} else {
		fmt.Fprintln(&frame, dashboardPaint(
			color, ansiBold+ansiCyan, dashboardTruncate(dashboardTitle, width),
		))
	}
	fmt.Fprintln(&frame, strings.Repeat("─", width))
	fmt.Fprintln(&frame)

	if loadErr != nil {
		fmt.Fprintln(&frame, dashboardPaint(
			color, ansiRed, dashboardTruncate("Daemon connection lost", width),
		))
		fmt.Fprintln(&frame, dashboardTruncate(dashboardText(loadErr.Error()), width))
		fmt.Fprintln(&frame)
		fmt.Fprintln(&frame, dashboardTruncate("Pearl will retry on the next refresh.", width))
	} else if len(active) == 0 {
		fmt.Fprintln(&frame, dashboardPaint(
			color,
			ansiDim,
			dashboardTruncate("No active jobs. Pearl is waiting for work.", width),
		))
	} else {
		for index, job := range active {
			if width >= 48 {
				status := dashboardJobStatus(job.Status, color)
				workspaceWidth := width - 47
				workspace := dashboardTruncate(filepath.Base(job.WorkspaceRoot), workspaceWidth)
				fmt.Fprintf(&frame, "%s  %-20s  %-7s  %s\n",
					status,
					dashboardTruncate(job.ID, 20),
					dashboardJobAge(job, now),
					workspace,
				)
			} else {
				status := dashboardJobStatusCompact(job.Status, color)
				statusWidth := len([]rune(dashboardJobStatusCompact(job.Status, false)))
				if width <= statusWidth {
					fmt.Fprintln(&frame, dashboardTruncate(
						dashboardJobStatusCompact(job.Status, false), width,
					))
				} else {
					fmt.Fprintf(&frame, "%s %s\n", status,
						dashboardTruncate(job.ID, width-statusWidth-1))
				}
			}
			if width > 2 {
				fmt.Fprintf(&frame, "  %s\n", dashboardTruncate(dashboardText(job.Prompt), width-2))
			}
			if job.Status == store.JobWaitingInput && job.Question != "" {
				question := "Question: " + dashboardText(job.Question)
				if width > 2 {
					fmt.Fprintf(&frame, "  %s\n", dashboardPaint(
						color, ansiMagenta, dashboardTruncate(question, width-2),
					))
				}
			}
			if index < len(active)-1 {
				fmt.Fprintln(&frame)
			}
		}
	}

	if commandView.command != "" {
		fmt.Fprintln(&frame)
		fmt.Fprint(&frame, renderDashboardCommandOutput(commandView, width, color))
	}

	fmt.Fprintln(&frame)
	fmt.Fprint(&frame, renderDashboardComposer(
		jobs, commandText, notice, width, color, selectedSuggestion, commandView.running,
		commandView.configureStep, commandView.recalling, commandView.model,
		running, queued, waiting, loadErr == nil,
	))
	return frame.String()
}

func renderDashboardCommandOutput(
	view dashboardCommandView,
	width int,
	color bool,
) string {
	var output bytes.Buffer
	command := "$ pearl " + dashboardOutputText(view.command)
	status := "running"
	statusColor := ansiYellow
	if !view.running {
		status = "done"
		statusColor = ansiGreen
		if view.exitCode != 0 {
			status = fmt.Sprintf("exit %d", view.exitCode)
			statusColor = ansiRed
		}
	}
	commandWidth := len([]rune(command))
	statusWidth := len([]rune(status))
	if commandWidth+statusWidth+1 <= width {
		fmt.Fprintf(&output, "%s%s%s\n",
			dashboardPaint(color, ansiCyan, command),
			strings.Repeat(" ", width-commandWidth-statusWidth),
			dashboardPaint(color, statusColor, status),
		)
	} else {
		fmt.Fprintln(&output, dashboardPaint(
			color, ansiCyan, dashboardTruncate(command, width),
		))
	}
	fmt.Fprintln(&output, strings.Repeat("─", width))
	lines := dashboardCommandOutputLines(view.output)
	thinking := view.running && dashboardCommandStreamsJob(view.command) &&
		!dashboardModelOutputStarted(lines)
	if len(lines) == 0 {
		message := "Waiting for output..."
		if thinking {
			message = dashboardThinkingMessage
		} else if !view.running {
			message = "Command completed without output."
		}
		fmt.Fprintln(&output, dashboardPaint(
			color, ansiDim, dashboardTruncate(message, width),
		))
		return output.String()
	}
	table := dashboardCommandPrintsTable(view.command)
	for _, line := range lines {
		line = dashboardOutputText(line)
		if table {
			line = dashboardTruncate(line, width)
			if strings.TrimSpace(view.command) == "jobs" {
				line = dashboardColorJobTableLine(line, color)
			}
			fmt.Fprintln(&output, line)
			continue
		}
		modelLine := strings.HasPrefix(line, "[model] ") || strings.HasPrefix(line, "[models] ")
		for _, wrapped := range jobViewWrapLine(line, max(1, width)) {
			if modelLine {
				wrapped = dashboardPaint(color, ansiBlue, wrapped)
			}
			fmt.Fprintln(&output, wrapped)
		}
	}
	if thinking {
		fmt.Fprintln(&output, dashboardPaint(
			color, ansiDim, dashboardTruncate(dashboardThinkingMessage, width),
		))
	}
	return output.String()
}

// dashboardCommandStreamsJob reports commands that attach to a job's live output.
func dashboardCommandStreamsJob(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "run":
		return !slices.Contains(fields[1:], "--detach")
	case "retry", "respond", "attach":
		return true
	}
	return false
}

func dashboardModelOutputStarted(lines []string) bool {
	for _, line := range lines {
		for _, marker := range []string{"[reasoning]", "[answer]", "[tool", "[input required]"} {
			if strings.HasPrefix(line, marker) {
				return true
			}
		}
	}
	return false
}

// dashboardCommandPrintsTable reports output whose columns would break if wrapped.
func dashboardCommandPrintsTable(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "jobs":
		return len(fields) < 2 || fields[1] != "view"
	case "archive":
		return true
	case "schedule":
		return len(fields) >= 2 && fields[1] == "list"
	case "model":
		return slices.Contains(fields[1:], "--list")
	}
	return false
}

func dashboardCommandOutputLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= dashboardCommandOutputLimit {
		return lines
	}
	hidden := len(lines) - dashboardCommandOutputLimit
	visible := append([]string(nil), lines[len(lines)-dashboardCommandOutputLimit:]...)
	return append([]string{fmt.Sprintf("... %d earlier lines", hidden)}, visible...)
}

func dashboardOutputText(value string) string {
	var output strings.Builder
	escapeSequence := false
	for _, character := range value {
		if escapeSequence {
			if unicode.IsLetter(character) || character == '~' {
				escapeSequence = false
			}
			continue
		}
		if character == '\x1b' {
			escapeSequence = true
			continue
		}
		if unicode.IsControl(character) {
			output.WriteRune(' ')
			continue
		}
		output.WriteRune(character)
	}
	return output.String()
}

func dashboardColorJobTableLine(value string, color bool) string {
	if !color {
		return value
	}
	start, end, found := dashboardSecondField(value)
	if !found {
		return value
	}
	field := value[start:end]
	code := ""
	switch field {
	case "STATUS":
		code = ansiBold + ansiCyan
	case store.JobQueued, store.JobPending, store.JobRunning, store.JobWaitingInput,
		store.JobCompleted, store.JobFailed, store.JobCancelled, store.JobInterrupted:
		code = jobStatusColor(field)
	default:
		return value
	}
	return value[:start] + dashboardPaint(true, code, field) + value[end:]
}

func dashboardSecondField(value string) (int, int, bool) {
	index := 0
	for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
		index++
	}
	for index < len(value) && value[index] != ' ' && value[index] != '\t' {
		index++
	}
	for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
		index++
	}
	start := index
	for index < len(value) && value[index] != ' ' && value[index] != '\t' {
		index++
	}
	return start, index, start < index
}

func renderDashboardComposer(
	jobs []store.Job,
	commandText, notice string,
	width int,
	color bool,
	selectedSuggestion int,
	commandRunning bool,
	configureStep dashboardConfigureStep,
	recalling bool,
	model string,
	running, queued, waiting int,
	daemonActive bool,
) string {
	var composer bytes.Buffer
	displayText := commandText
	if configureStep == dashboardConfigureAPIKey && displayText != "" {
		displayText = strings.Repeat("•", len([]rune(displayText)))
	}
	if notice != "" {
		fmt.Fprintln(&composer, dashboardPaint(
			color, ansiRed, dashboardTruncate(notice, width),
		))
	}
	fmt.Fprintln(&composer, renderDashboardJobCounts(
		running, queued, waiting, width, color, daemonActive,
	))
	if width < 8 {
		value := displayText
		if value == "" {
			value = "Type"
		}
		fmt.Fprintln(&composer, dashboardTruncate("› "+value, width))
		return composer.String()
	}

	border := strings.Repeat("─", width-2)
	fmt.Fprintln(&composer, dashboardPaint(color, ansiDim, "╭"+border+"╮"))
	innerWidth := width - 4
	indicator := "› "
	plainContent := indicator
	styledContent := dashboardPaint(color, ansiCyan, indicator)
	if commandText == "" {
		placeholder := "Type a Pearl command"
		switch configureStep {
		case dashboardConfigureAPIKey:
			placeholder = "OpenRouter API key"
		case dashboardConfigureModel:
			placeholder = "Choose free or custom"
		case dashboardConfigureCustomModel:
			placeholder = "OpenRouter model ID"
		}
		placeholder = dashboardTruncate(placeholder, max(0, innerWidth-2))
		plainContent += placeholder
		styledContent += dashboardPaint(color, ansiDim, placeholder)
	} else {
		value := dashboardPromptText(displayText, max(0, innerWidth-3))
		plainContent += value + "█"
		styledContent += value + dashboardPaint(color, ansiDim, "█")
	}
	padding := strings.Repeat(" ", max(0, innerWidth-len([]rune(plainContent))))
	fmt.Fprintf(&composer, "%s %s%s %s\n",
		dashboardPaint(color, ansiDim, "│"),
		styledContent,
		padding,
		dashboardPaint(color, ansiDim, "│"),
	)
	var suggestions []dashboardCommandSuggestion
	if !recalling {
		suggestions = dashboardComposerSuggestionsWithJobs(commandText, configureStep, jobs)
	}
	if len(suggestions) == 0 {
		fmt.Fprintln(&composer, dashboardPaint(color, ansiDim, "╰"+border+"╯"))
	} else {
		fmt.Fprintln(&composer, dashboardPaint(color, ansiDim, "├"+border+"┤"))
		selectedSuggestion %= len(suggestions)
		if selectedSuggestion < 0 {
			selectedSuggestion += len(suggestions)
		}
		start := 0
		if selectedSuggestion >= dashboardSuggestionLimit {
			start = selectedSuggestion - dashboardSuggestionLimit + 1
		}
		end := min(len(suggestions), start+dashboardSuggestionLimit)
		for offset, suggestion := range suggestions[start:end] {
			index := start + offset
			prefix := "  "
			style := ansiDim
			if index == selectedSuggestion {
				prefix = "› "
				style = ansiCyan
			}
			line := dashboardTruncate(
				prefix+suggestion.usage+"  "+suggestion.description,
				innerWidth,
			)
			padding := strings.Repeat(" ", max(0, innerWidth-len([]rune(line))))
			fmt.Fprintf(&composer, "%s %s%s %s\n",
				dashboardPaint(color, ansiDim, "│"),
				dashboardPaint(color, style, line),
				padding,
				dashboardPaint(color, ansiDim, "│"),
			)
		}
		fmt.Fprintln(&composer, dashboardPaint(color, ansiDim, "╰"+border+"╯"))
	}
	if model != "" {
		fmt.Fprintln(&composer, dashboardTruncate("  Current model: "+model, width))
	}

	leftHint := ""
	if configureStep != dashboardConfigureInactive {
		leftHint = "  Enter to continue · Ctrl-C to cancel"
		if configureStep == dashboardConfigureModel && len(suggestions) > 0 {
			leftHint = fmt.Sprintf(
				"  ↑/↓ choose %d/%d · Tab to fill · Enter to choose · Ctrl-C cancel",
				selectedSuggestion+1,
				len(suggestions),
			)
		}
	} else if commandRunning {
		leftHint = "  Ctrl-C to cancel the running command"
	} else if recalling {
		leftHint = "  ↑/↓ history · Enter to run · type to edit"
	} else if len(suggestions) > 0 {
		leftHint = fmt.Sprintf(
			"  ↑/↓ choose %d/%d · Tab to fill · Enter to run · Ctrl-C exit",
			selectedSuggestion+1,
			len(suggestions),
		)
	}
	if leftHint == "" {
		return composer.String()
	}
	rightHint := ""
	if configureStep != dashboardConfigureInactive {
		rightHint = "configure  "
	}
	if len([]rune(leftHint))+len([]rune(rightHint))+1 <= width {
		fmt.Fprintln(&composer, dashboardPaint(
			color,
			ansiDim,
			leftHint+strings.Repeat(
				" ", width-len([]rune(leftHint))-len([]rune(rightHint)),
			)+rightHint,
		))
	} else {
		fmt.Fprintln(&composer, dashboardPaint(
			color, ansiDim, dashboardTruncate(leftHint, width),
		))
	}
	return composer.String()
}

func renderDashboardJobCounts(
	running, queued, waiting, width int,
	color bool,
	daemonActive bool,
) string {
	counts := fmt.Sprintf("%d running, %d queued, %d waiting", running, queued, waiting)
	daemonLabel := "● Daemon active"
	daemonColor := ansiGreen
	if !daemonActive {
		daemonLabel = "● Daemon not active"
		daemonColor = ansiRed
	}
	plain := counts + "  " + daemonLabel
	if len([]rune(plain)) > width {
		return dashboardPaint(color, ansiDim, dashboardTruncate(plain, width))
	}
	return fmt.Sprintf("%s, %s, %s  %s",
		dashboardPaint(color, ansiGreen, fmt.Sprintf("%d running", running)),
		dashboardPaint(color, ansiYellow, fmt.Sprintf("%d queued", queued)),
		dashboardPaint(color, ansiMagenta, fmt.Sprintf("%d waiting", waiting)),
		dashboardPaint(color, daemonColor, daemonLabel),
	)
}

func dashboardJobStatus(status string, color bool) string {
	label, code := "  UNKNOWN     ", ansiRed
	switch status {
	case store.JobRunning:
		label, code = "● RUNNING     ", ansiGreen
	case store.JobQueued:
		label, code = "○ QUEUED      ", ansiYellow
	case store.JobWaitingInput:
		label, code = "? NEEDS INPUT ", ansiMagenta
	}
	return dashboardPaint(color, code, label)
}

func dashboardJobStatusCompact(status string, color bool) string {
	label, code := "UNKNOWN", ansiRed
	switch status {
	case store.JobRunning:
		label, code = "● RUNNING", ansiGreen
	case store.JobQueued:
		label, code = "○ QUEUED", ansiYellow
	case store.JobWaitingInput:
		label, code = "? INPUT", ansiMagenta
	}
	return dashboardPaint(color, code, label)
}

func dashboardJobAge(job store.Job, now time.Time) string {
	started := job.CreatedAt
	if job.Status == store.JobRunning && job.StartedAt != nil {
		started = *job.StartedAt
	}
	age := now.Sub(started)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func dashboardPaint(enabled bool, code, value string) string {
	if !enabled {
		return value
	}
	return code + value + ansiReset
}

func dashboardText(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func dashboardTruncate(value string, width int) string {
	characters := []rune(value)
	if width <= 0 {
		return ""
	}
	if len(characters) <= width {
		return value
	}
	if width <= 3 {
		return string(characters[:width])
	}
	return string(characters[:width-3]) + "..."
}

func dashboardPromptText(value string, width int) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	characters := []rune(value)
	if width <= 0 {
		return ""
	}
	if len(characters) <= width {
		return value
	}
	if width <= 3 {
		return string(characters[len(characters)-width:])
	}
	return "..." + string(characters[len(characters)-(width-3):])
}

func dashboardWidth(terminal *os.File) int {
	if width, _, err := term.GetSize(int(terminal.Fd())); err == nil && width > 0 {
		return width
	}
	if value, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && value > 0 {
		return value
	}
	return 110
}

func dashboardHeight(terminal *os.File) int {
	if _, height, err := term.GetSize(int(terminal.Fd())); err == nil && height > 0 {
		return height
	}
	return 24
}
