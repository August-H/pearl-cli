package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
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

// autonomousScreen is what both the dashboard and `pearl autonomous` show.
type autonomousScreen struct {
	details       store.AutonomousDetails
	loadErr       error
	confirmStop   bool
	showReasoning bool
	scroll        dashboardBodyScroll
}

type autonomousScreenResult int

const (
	autonomousScreenStay autonomousScreenResult = iota
	autonomousScreenLeave
	autonomousScreenRefresh
	autonomousScreenStop
)

// handleKey takes one key press and says what the caller should do.
func (screen *autonomousScreen) handleKey(key rune) autonomousScreenResult {
	if screen.showReasoning {
		switch key {
		case 3, 4:
			return autonomousScreenLeave
		case escapeKey, 't', 'T':
			screen.showReasoning = false
			return autonomousScreenRefresh
		case 'j':
			screen.applyAction("next")
		case 'k':
			screen.applyAction("previous")
		case 'g':
			screen.applyAction("home")
		case 'G':
			screen.applyAction("end")
		default:
			return autonomousScreenStay
		}
		return autonomousScreenRefresh
	}
	switch key {
	case 3, 4, escapeKey:
		return autonomousScreenLeave
	case 'r', 'R':
		return autonomousScreenRefresh
	case 't', 'T':
		screen.confirmStop = false
		screen.showReasoning = true
		screen.scroll = dashboardBodyScroll{}
		return autonomousScreenRefresh
	case 'x', 'X':
		if screen.details.Session.Terminal() {
			return autonomousScreenStay
		}
		stopping := screen.confirmStop
		screen.confirmStop = !stopping
		if stopping {
			return autonomousScreenStop
		}
		return autonomousScreenRefresh
	}
	if screen.confirmStop {
		screen.confirmStop = false
		return autonomousScreenRefresh
	}
	return autonomousScreenStay
}

// applyAction takes an arrow key or the wheel. It returns whether to redraw.
func (screen *autonomousScreen) applyAction(action string) bool {
	if !screen.showReasoning {
		return false
	}
	switch action {
	case "previous":
		screen.scroll.offset++
		action = "clamp"
	case "next":
		screen.scroll.offset--
		action = "clamp"
	}
	if action == "clamp" {
		screen.scroll.offset = min(max(0, screen.scroll.offset), max(0, screen.scroll.total-screen.scroll.visible))
		return true
	}
	return screen.scroll.apply(action)
}

// frame draws the whole screen.
func (screen *autonomousScreen) frame(leave string, width, height int, color bool, now time.Time) string {
	if screen.showReasoning {
		return renderAutonomousReasoningAt(screen.details, &screen.scroll, width, height, color, now)
	}
	return renderAutonomousScreenAt(
		screen.details, screen.loadErr, width, height, color,
		autonomousScreenHint(leave, screen.details.Session, screen.confirmStop), now,
	)
}

func runAutonomous(args []string) int {
	flags := flag.NewFlagSet("autonomous", flag.ContinueOnError)
	flags.SetOutput(flagOutput(args))
	resume := flags.String("resume", "", "open an autonomous session by ID")
	latest := flags.Bool("latest", false, "open the latest autonomous session")
	workspacePath := flags.String("workspace", "", "workspace directory path for a new session")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), `Usage: pearl autonomous ["goal"] [--workspace <path>]`)
		fmt.Fprintln(flags.Output(), `       pearl autonomous --latest | --resume <session-id>`)
		fmt.Fprintln(flags.Output(), "  Opens the form to start a session, with the goal filled in if you give one.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	goal := strings.TrimSpace(strings.Join(flags.Args(), " "))
	sessionID := strings.TrimSpace(*resume)
	opening := *latest || sessionID != ""
	if *latest && sessionID != "" || opening && (goal != "" || *workspacePath != "") {
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

	if !opening {
		workspace, workspaceErr := os.Getwd()
		if *workspacePath != "" {
			workspace, workspaceErr = explicitWorkspace(*workspacePath)
		}
		if workspaceErr != nil {
			return printError("Autonomous", workspaceErr)
		}
		var result autonomousFormResult
		sessionID, result, err = runAutonomousForm(os.Stdin, os.Stdout, client, goal, workspace)
		if err != nil {
			return printError("Autonomous", err)
		}
		if result == autonomousFormCancelled {
			return 0
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *latest || !opening && sessionID == "" {
		requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		session, err := client.latestAutonomousSession(requestContext)
		cancel()
		if err != nil {
			return printError("Autonomous", err)
		}
		sessionID = session.ID
	}
	if err := autonomousTUILoop(ctx, os.Stdin, os.Stdout, client, sessionID, 500*time.Millisecond); err != nil &&
		!errors.Is(err, context.Canceled) {
		return printError("Autonomous", err)
	}
	return 0
}

// runAutonomousForm shows the form until it starts a session or the user leaves. It
// returns the new session's ID, or no ID to open the latest one. Keys are read on
// this goroutine so nothing is left reading them when the session screen opens.
func runAutonomousForm(
	input, output *os.File,
	client *daemonClient,
	goal, workspace string,
) (string, autonomousFormResult, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return "", autonomousFormCancelled, fmt.Errorf("enable autonomous input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)
	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l\x1b[?2004h", dashboardKeyboardEnable, jobsListMouseEnable)
	defer fmt.Fprint(output, jobsListMouseDisable, dashboardKeyboardDisable, "\x1b[?2004l\x1b[?25h\x1b[?1049l")

	form := newAutonomousForm(goal, workspace)
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	var lock sync.Mutex
	closed := false
	defer func() {
		lock.Lock()
		closed = true
		lock.Unlock()
	}()
	// draw needs the lock held.
	draw := func() {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		fmt.Fprint(output, "\x1b[H", listTerminalFrame(form.render(width, height, color)))
	}
	lock.Lock()
	draw()
	lock.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		models, current, notice, err := loadAutonomousModels(ctx)
		lock.Lock()
		defer lock.Unlock()
		if !closed {
			form.setModels(models, current, notice, err)
			draw()
		}
	}()

	reader := bufio.NewReader(input)
	sequence := ""
	for {
		key, err := readTerminalKey(reader)
		if err != nil {
			return "", autonomousFormCancelled, err
		}
		if sequence != "" {
			sequence += string(key)
			if isComposerEscapePrefix(sequence) {
				continue
			}
			if sequence == "\x1b[200~" {
				sequence = ""
				text, pasteErr := readComposerPaste(reader)
				lock.Lock()
				if pasteErr != nil {
					form.notice = pasteErr.Error()
				} else {
					form.addText(text)
				}
				draw()
				lock.Unlock()
				continue
			}
			decoded, report := composerKeyFromSequence(sequence)
			if !report {
				action, complete := jobViewEscapeAction(sequence)
				if !complete {
					continue
				}
				sequence = ""
				lock.Lock()
				if form.applyAction(action) {
					draw()
				}
				lock.Unlock()
				continue
			}
			sequence = ""
			key = decoded
		}
		if key == '\x1b' {
			sequence = "\x1b"
			continue
		}
		lock.Lock()
		if key == composerNewlineKey || key == '\n' {
			if form.picker == nil && form.focus == autonomousFieldGoal {
				form.addText("\n")
				draw()
				lock.Unlock()
				continue
			}
			key = '\r'
		}
		result := form.handleKey(key)
		switch result {
		case autonomousFormCancelled, autonomousFormOpenLatest:
			lock.Unlock()
			return "", result, nil
		case autonomousFormBrowse:
			start := form.browseStart()
			lock.Unlock()
			path, browseErr := chooseAutonomousRepository(start)
			lock.Lock()
			form.finishBrowse(path, browseErr)
		case autonomousFormSubmitted:
			config, err := form.config()
			if err != nil {
				form.notice = err.Error()
				break
			}
			requestContext, cancelRequest := context.WithTimeout(ctx, 5*time.Second)
			session, err := client.createAutonomousSession(requestContext, config)
			cancelRequest()
			if err != nil {
				form.notice = dashboardText(err.Error())
				break
			}
			lock.Unlock()
			return session.ID, result, nil
		}
		draw()
		lock.Unlock()
	}
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
	mouse := false
	defer func() {
		if mouse {
			fmt.Fprint(output, jobsListMouseDisable)
		}
		fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")
	}()

	screen := autonomousScreen{}
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	draw := func(load bool) {
		if load {
			requestContext, cancel := context.WithTimeout(ctx, 2*time.Second)
			loaded, err := client.autonomousDetails(requestContext, sessionID)
			cancel()
			screen.loadErr = err
			if err == nil {
				screen.details = loaded
			}
		}
		// Only the full reasoning view scrolls, and mouse reporting would stop the
		// main screen's text from being selected.
		if screen.showReasoning != mouse {
			mouse = screen.showReasoning
			if mouse {
				fmt.Fprint(output, jobsListMouseEnable)
			} else {
				fmt.Fprint(output, jobsListMouseDisable)
			}
		}
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		frame := screen.frame("Esc detach", width, height, color, time.Now())
		fmt.Fprint(output, "\x1b[H", listTerminalFrame(frame))
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
			character, err := readTerminalKey(reader)
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

	sequence := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-inputReady:
			if result.err != nil {
				return result.err
			}
			if sequence != "" {
				sequence += string(result.character)
				action, complete := jobViewEscapeAction(sequence)
				if !complete {
					continue
				}
				sequence = ""
				if screen.applyAction(action) {
					draw(false)
				}
				continue
			}
			if result.character == '\x1b' {
				sequence = "\x1b"
				continue
			}
			switch screen.handleKey(result.character) {
			case autonomousScreenLeave:
				return nil
			case autonomousScreenRefresh:
				draw(true)
			case autonomousScreenStop:
				requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := client.stopAutonomousSession(requestContext, sessionID); err != nil {
					screen.loadErr = err
				}
				cancel()
				draw(true)
			}
		case <-ticker.C:
			draw(true)
		}
	}
}

// buildAutonomousActivity lists what happened from what's saved, so it's the same
// whenever the screen is opened.
func buildAutonomousActivity(details store.AutonomousDetails) []autonomousActivity {
	session := details.Session
	activity := []autonomousActivity{{At: session.CreatedAt, Text: "session started", Status: store.AutonomousRunning}}
	for _, turn := range details.Turns {
		if turn.FinishedAt != nil && turn.Outcome != "" {
			activity = append(activity, autonomousActivity{
				At: *turn.FinishedAt, Status: store.AutonomousReviewing,
				Text: fmt.Sprintf("orchestrator · %s", dashboardText(turn.Outcome)),
			})
		}
	}
	assigned := make(map[string]string, len(details.Jobs))
	for _, job := range details.Jobs {
		if job.AssignedModel != "" {
			assigned[job.ID] = " · " + modelLabel(job.AssignedModel)
		}
	}
	statuses := make(map[string]string, len(details.Jobs))
	for _, event := range details.Events {
		previous, found := statuses[event.JobID]
		switch {
		case !found:
			activity = append(activity, autonomousActivity{
				At: event.CreatedAt, Status: event.Data,
				Text: "created " + event.JobID + " · " + event.Data + assigned[event.JobID],
			})
		case previous != event.Data:
			activity = append(activity, autonomousActivity{
				At: event.CreatedAt, Status: event.Data,
				Text: event.JobID + " · " + previous + " → " + event.Data,
			})
		}
		statuses[event.JobID] = event.Data
	}
	if session.FinishedAt != nil {
		activity = append(activity, autonomousActivity{At: *session.FinishedAt, Status: session.Status, Text: "session " + session.Status})
	}
	sort.SliceStable(activity, func(left, right int) bool { return activity[left].At.Before(activity[right].At) })
	return activity
}

func renderAutonomousScreen(
	details store.AutonomousDetails,
	loadErr error,
	width, height int,
	color bool,
	now time.Time,
) string {
	return renderAutonomousScreenAt(
		details, loadErr, width, height, color,
		autonomousScreenHint("Esc detach", details.Session, false), now,
	)
}

// autonomousScreenHint names the keys that work right now.
func autonomousScreenHint(leave string, session store.AutonomousSession, confirmStop bool) string {
	if confirmStop {
		return "Press x again to stop this session and its jobs · any other key keeps it going"
	}
	if session.Terminal() {
		return leave + " · t full reasoning"
	}
	return leave + " · t full reasoning · x stop · work continues in daemon"
}

// autonomousSessionSettings is the screen's line about what the user chose.
func autonomousSessionSettings(session store.AutonomousSession) string {
	parts := []string{filepath.Base(session.WorkspaceRoot)}
	if session.Orchestrator.ID != "" {
		parts = append(parts, "Orchestrator: "+autonomousModelName(session.Orchestrator))
	}
	switch {
	case session.AllModels:
		parts = append(parts, "Subagents: all models")
	case len(session.AllowedModels) > 0:
		names := make([]string, 0, len(session.AllowedModels))
		for _, model := range session.AllowedModels {
			names = append(names, autonomousModelName(model))
		}
		parts = append(parts, "Subagents: "+strings.Join(names, ", "))
	}
	if session.MaxSubagents > 0 {
		parts = append(parts, fmt.Sprintf("Max %d at once", session.MaxSubagents))
	}
	return strings.Join(parts, " · ")
}

// autonomousQuietNotice is how long a model can say nothing before the screen says so.
const autonomousQuietNotice = 45 * time.Second

// autonomousDuration is a short running time: 42s, 1m 04s, 2h 05m.
func autonomousDuration(elapsed time.Duration) string {
	seconds := int(max(0, elapsed).Seconds())
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %02dm", seconds/3600, seconds/60%60)
}

// autonomousStats is the line that shows how far along the session is.
func autonomousStats(details store.AutonomousDetails, now time.Time) string {
	session := details.Session
	end := now
	if session.FinishedAt != nil {
		end = *session.FinishedAt
	}
	counts := map[string]int{}
	passed, failed := 0, 0
	for _, job := range details.Jobs {
		counts[job.Status]++
		switch job.Verification {
		case "passed":
			passed++
		case "failed":
			failed++
		}
	}
	parts := []string{
		"Elapsed " + autonomousDuration(end.Sub(session.CreatedAt)),
		fmt.Sprintf("Jobs: %d created", len(details.Jobs)),
		fmt.Sprintf("%d completed", counts[store.JobCompleted]),
		fmt.Sprintf("%d running", counts[store.JobRunning]),
	}
	add := func(count int, label string) {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, label))
		}
	}
	add(counts[store.JobQueued], "queued")
	add(counts[store.JobWaitingInput], "need your answer")
	add(counts[store.JobFailed]+counts[store.JobInterrupted], "failed")
	add(counts[store.JobCancelled], "cancelled")
	add(passed, "tests passed")
	add(failed, "tests failed")
	if len(details.Turns) > 0 {
		parts = append(parts, fmt.Sprintf("Turn %d", details.Turns[len(details.Turns)-1].Turn))
	}
	return strings.Join(parts, " · ")
}

// autonomousOpenTurn is the turn in progress, if any.
func autonomousOpenTurn(details store.AutonomousDetails) (store.AutonomousTurn, bool) {
	if details.Session.Terminal() || len(details.Turns) == 0 {
		return store.AutonomousTurn{}, false
	}
	last := details.Turns[len(details.Turns)-1]
	return last, last.FinishedAt == nil
}

func autonomousBytes(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024)
}

// autonomousOrchestratorStatus says what the orchestrator is doing right now.
func autonomousOrchestratorStatus(details store.AutonomousDetails, now time.Time) string {
	session := details.Session
	name := "The orchestrator"
	if session.Orchestrator.ID != "" {
		name = autonomousModelName(session.Orchestrator)
	}
	if turn, open := autonomousOpenTurn(details); open {
		elapsed := autonomousDuration(now.Sub(turn.StartedAt))
		// A long quiet spell is worth saying out loud: it may be stuck, not thinking.
		heard := turn.StartedAt
		if turn.UpdatedAt != nil {
			heard = *turn.UpdatedAt
		}
		if quiet := now.Sub(heard); quiet >= autonomousQuietNotice {
			return fmt.Sprintf("%s has been quiet for %s · turn %d", name, autonomousDuration(quiet), turn.Turn)
		}
		if turn.DecisionBytes > 0 {
			return fmt.Sprintf("%s is writing jobs · %s · %s", name, autonomousBytes(turn.DecisionBytes), elapsed)
		}
		return fmt.Sprintf("%s is thinking · turn %d · %s", name, turn.Turn, elapsed)
	}
	switch session.Status {
	case store.AutonomousWaitingInput:
		return "Paused: a job needs your answer"
	case store.AutonomousCompleted, store.AutonomousFailed, store.AutonomousCancelled:
		return "Session " + session.Status
	}
	running := 0
	for _, job := range details.Jobs {
		if job.Status == store.JobRunning || job.Status == store.JobQueued {
			running++
		}
	}
	if running > 0 {
		return fmt.Sprintf("Waiting for a job to report back · %d still going", running)
	}
	if len(details.Turns) > 0 {
		return name + " is about to read the latest report"
	}
	return name + " is starting"
}

// autonomousReasoningRows returns the end of what the orchestrator last said.
func autonomousReasoningRows(details store.AutonomousDetails, rows, width int, color bool) []string {
	reasoning := ""
	for index := len(details.Turns) - 1; index >= 0 && reasoning == ""; index-- {
		reasoning = strings.TrimSpace(details.Turns[index].Reasoning)
	}
	if reasoning == "" {
		return []string{dashboardPaint(color, ansiDim, dashboardTruncate("  Waiting for the model's first words.", width))}
	}
	wrapped := wrapAutonomousText(reasoning, width-2)
	if len(wrapped) > rows {
		wrapped = wrapped[len(wrapped)-rows:]
	}
	lines := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		lines = append(lines, "  "+line)
	}
	return lines
}

// wrapAutonomousText wraps by word and keeps the model's line breaks, but not its
// blank lines, which waste rows on a small screen.
func wrapAutonomousText(text string, width int) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(dashboardText(line))
		if line != "" {
			lines = append(lines, jobViewWrapLine(line, max(8, width))...)
		}
	}
	return lines
}

// autonomousVisibleJobs picks what fits, with work in progress first, and keeps
// them in the order they were created.
func autonomousVisibleJobs(jobs []store.Job, rows int) []store.Job {
	if len(jobs) <= rows {
		return jobs
	}
	chosen := make(map[string]bool, rows)
	remaining := rows
	for _, wanted := range []func(store.Job) bool{
		func(job store.Job) bool { return job.Status == store.JobRunning || job.Status == store.JobWaitingInput },
		func(job store.Job) bool { return job.Status == store.JobQueued },
		func(store.Job) bool { return true },
	} {
		// Newest first within each group.
		for _, job := range slices.Backward(jobs) {
			if remaining > 0 && !chosen[job.ID] && wanted(job) {
				chosen[job.ID] = true
				remaining--
			}
		}
	}
	visible := make([]store.Job, 0, rows)
	for _, job := range jobs {
		if chosen[job.ID] {
			visible = append(visible, job)
		}
	}
	return visible
}

func renderAutonomousScreenAt(
	details store.AutonomousDetails,
	loadErr error,
	width, height int,
	color bool,
	hint string,
	now time.Time,
) string {
	width = max(32, width)
	height = max(12, height)
	var head []string
	title := "Pearl autonomous"
	status := details.Session.Status
	if status == "" {
		status = "connecting"
	}
	statusText := strings.ToUpper(status)
	titleWidth := len([]rune(title))
	statusWidth := len([]rune(statusText))
	if titleWidth+statusWidth+1 <= width {
		head = append(head, dashboardPaint(color, ansiBold+ansiCyan, title)+
			strings.Repeat(" ", width-titleWidth-statusWidth)+
			dashboardPaint(color, jobStatusColor(status), statusText))
	} else {
		head = append(head, dashboardTruncate(title, width))
	}
	head = append(head, dashboardPaint(color, ansiDim, strings.Repeat("─", width)))
	if loadErr != nil {
		head = append(head, dashboardPaint(color, ansiRed, dashboardTruncate("Daemon: "+dashboardText(loadErr.Error()), width)))
	}
	if details.Session.ID != "" {
		head = append(head, dashboardTruncate("Goal: "+dashboardText(details.Session.Goal), width))
		head = append(head, dashboardPaint(color, ansiDim, dashboardTruncate(autonomousSessionSettings(details.Session), width)))
		for _, line := range wrapAutonomousText(autonomousStats(details, now), width) {
			head = append(head, dashboardPaint(color, ansiBold, line))
		}
		if plan := dashboardText(details.Session.Plan); plan != "" && height >= 30 {
			head = append(head, dashboardTruncate("Plan: "+plan, width))
		}
	}

	// Three section titles and the footer's two lines are fixed, and the rest is
	// shared. Reasoning gets whatever the other sections don't need.
	room := max(3, height-len(head)-5)
	jobRows := min(max(1, len(details.Jobs)), max(1, room/2))
	activity := buildAutonomousActivity(details)
	if details.Session.ID == "" {
		activity = nil
	}
	activityRows := min(max(1, len(activity)), 5, max(1, (room-jobRows)/2))
	reasoningRows := max(1, room-jobRows-activityRows)

	body := append([]string(nil), head...)
	body = append(body, dashboardPaint(color, ansiBold, dashboardTruncate(
		"ORCHESTRATOR · "+autonomousOrchestratorStatus(details, now), width)))
	reasoning := autonomousReasoningRows(details, reasoningRows, width, color)
	body = append(body, reasoning...)
	for used := len(reasoning); used < reasoningRows; used++ {
		body = append(body, "")
	}

	body = append(body, dashboardPaint(color, ansiBold, "JOBS"))
	if len(details.Jobs) == 0 {
		body = append(body, dashboardPaint(color, ansiDim, dashboardTruncate("  The orchestrator hasn't started any yet.", width)))
	} else {
		for _, job := range autonomousVisibleJobs(details.Jobs, jobRows) {
			body = append(body, renderAutonomousJob(job, details.JobActivity[job.ID], width, color, now))
		}
	}

	body = append(body, dashboardPaint(color, ansiBold, "ACTIVITY"))
	for _, entry := range activity[max(0, len(activity)-activityRows):] {
		line := entry.At.Local().Format("15:04:05") + "  " + entry.Text
		body = append(body, dashboardPaint(color, jobStatusColor(entry.Status), dashboardTruncate(line, width)))
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
	if len(body) > height-2 {
		body = body[:height-2]
	}
	for len(body) < height-2 {
		body = append(body, "")
	}
	body = append(body,
		dashboardPaint(color, ansiDim, strings.Repeat("─", width)),
		dashboardTruncate(dashboardText(message), width),
	)
	return strings.Join(body, "\n") + "\n"
}

// renderAutonomousReasoningAt is the full, scrollable account of the session.
func renderAutonomousReasoningAt(
	details store.AutonomousDetails,
	scroll *dashboardBodyScroll,
	width, height int,
	color bool,
	now time.Time,
) string {
	width = max(32, width)
	height = max(12, height)
	lines := []string{
		dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate("Orchestrator reasoning · "+autonomousStats(details, now), width)),
		dashboardPaint(color, ansiDim, strings.Repeat("─", width)),
	}
	if plan := strings.TrimSpace(details.Session.Plan); plan != "" {
		lines = append(lines, dashboardPaint(color, ansiBold, "Current plan"))
		for _, line := range wrapAutonomousText(plan, width-2) {
			lines = append(lines, "  "+line)
		}
		lines = append(lines, "")
	}
	if len(details.Turns) == 0 {
		lines = append(lines, dashboardPaint(color, ansiDim, "The orchestrator hasn't started yet."))
	}
	for _, turn := range details.Turns {
		end := now
		if turn.FinishedAt != nil {
			end = *turn.FinishedAt
		}
		heading := fmt.Sprintf("Turn %d · %s · %s", turn.Turn, turn.StartedAt.Local().Format("15:04:05"), autonomousDuration(end.Sub(turn.StartedAt)))
		if turn.Trigger != "" {
			heading += " · " + dashboardText(turn.Trigger)
		}
		lines = append(lines, dashboardPaint(color, ansiBold, dashboardTruncate(heading, width)))
		reasoning := wrapAutonomousText(turn.Reasoning, width-2)
		for _, line := range reasoning {
			lines = append(lines, "  "+line)
		}
		switch {
		case turn.Outcome != "":
			lines = append(lines, dashboardPaint(color, ansiCyan, dashboardTruncate("  → "+dashboardText(turn.Outcome), width)))
		case details.Session.Terminal() || turn.FinishedAt != nil:
		case turn.DecisionBytes > 0:
			lines = append(lines, dashboardPaint(color, ansiDim, "  Writing its plan and job assignments · "+autonomousBytes(turn.DecisionBytes)))
		case len(reasoning) == 0:
			lines = append(lines, dashboardPaint(color, ansiDim, "  Waiting for the model's first words."))
		}
		lines = append(lines, "")
	}
	footer := dashboardPaint(color, ansiDim, dashboardTruncate("t or Esc back · ↑/↓ PgUp/PgDn or scroll to read", width)) + "\n"
	return fitAutonomousFrame(scroll.fit(strings.Join(lines, "\n")+"\n", footer, width, height, color)+"\n", height)
}

// renderAutonomousJob shows who is doing what, and for how long.
func renderAutonomousJob(job store.Job, activity string, width int, color bool, now time.Time) string {
	const statusWidth = 13
	const timeWidth = 7
	idWidth := min(20, max(8, width-statusWidth-4))
	line := fmt.Sprintf("  %-*s  %s", idWidth,
		dashboardTruncate(job.ID, idWidth),
		dashboardPaint(color, jobStatusColor(job.Status), fmt.Sprintf("%-*s", statusWidth, job.Status)),
	)
	used := 2 + idWidth + 2 + statusWidth
	if job.StartedAt != nil && width-used > timeWidth+2 {
		end := now
		if job.FinishedAt != nil {
			end = *job.FinishedAt
		}
		line += fmt.Sprintf("  %*s", timeWidth, autonomousDuration(end.Sub(*job.StartedAt)))
		used += 2 + timeWidth
	}
	var details []string
	if model := job.AssignedModel; model != "" {
		details = append(details, modelLabel(model))
	}
	if job.Verification == "passed" || job.Verification == "failed" {
		details = append(details, "tests "+job.Verification)
	}
	// While it runs, what it's doing says more than what it was asked.
	if job.Status == store.JobRunning && strings.TrimSpace(activity) != "" {
		details = append(details, strings.TrimSpace(activity))
	} else {
		details = append(details, strings.TrimSpace(strings.SplitN(job.Prompt, "\n", 2)[0]))
	}
	if room := width - used - 2; room > 3 {
		line += "  " + dashboardTruncate(dashboardText(strings.Join(details, " · ")), room)
	}
	return line
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
