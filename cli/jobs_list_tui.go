package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/August-H/pearl-cli/internal/store"
	"golang.org/x/term"
)

type jobsListScreen struct {
	Frame      string
	BodyHeight int
}

type jobsListLayout struct {
	IDWidth        int
	StatusWidth    int
	CreatedWidth   int
	WorkspaceWidth int
	PromptWidth    int
}

type jobsListResult struct {
	JobID   string
	Preload string
}

type jobsListMode struct {
	title         string
	allowActions  bool
	showWorkspace bool
	archiveJob    func(string) error
}

func runJobsListTUI(
	input, output *os.File,
	jobs []store.Job,
	archiveJob func(string) error,
) (jobsListResult, error) {
	return runJobsListTUIWithMode(input, output, jobs, jobsListMode{
		title: "Pearl", allowActions: true, archiveJob: archiveJob,
	})
}

func runJobsListTUIShowingWorkspace(
	input, output *os.File,
	jobs []store.Job,
	archiveJob func(string) error,
) (jobsListResult, error) {
	return runJobsListTUIWithMode(input, output, jobs, jobsListMode{
		title: "Pearl · all workspaces", allowActions: true,
		showWorkspace: true, archiveJob: archiveJob,
	})
}

func runArchivedJobsListTUI(
	input, output *os.File,
	jobs []store.Job,
) (jobsListResult, error) {
	return runJobsListTUIWithMode(input, output, jobs, jobsListMode{
		title: "Pearl archive",
	})
}

func runArchivedJobsListTUIShowingWorkspace(
	input, output *os.File,
	jobs []store.Job,
) (jobsListResult, error) {
	return runJobsListTUIWithMode(input, output, jobs, jobsListMode{
		title: "Pearl archive · all workspaces", showWorkspace: true,
	})
}

func runJobsListTUIWithMode(
	input, output *os.File,
	jobs []store.Job,
	mode jobsListMode,
) (jobsListResult, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return jobsListResult{}, fmt.Errorf("enable jobs input: %w", err)
	}
	defer term.Restore(int(input.Fd()), terminalState)

	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")

	selected := 0
	scroll := 0
	confirmArchive := false
	notice := ""
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	draw := func() jobsListScreen {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		height = max(8, height)
		bodyHeight := height - 5
		if len(jobs) > 0 {
			selected = min(max(0, selected), len(jobs)-1)
			if selected < scroll {
				scroll = selected
			} else if selected >= scroll+bodyHeight {
				scroll = selected - bodyHeight + 1
			}
			scroll = min(max(0, scroll), max(0, len(jobs)-bodyHeight))
		} else {
			selected, scroll = 0, 0
		}
		footer := ""
		if confirmArchive && len(jobs) > 0 {
			footer = fmt.Sprintf(
				"Archive %s? y confirm · n cancel", jobViewText(jobs[selected].ID),
			)
		} else if notice != "" {
			footer = notice + " · " + jobsListModeFooter(mode, jobs, selected, "q exit")
		}
		if footer == "" {
			footer = jobsListModeFooter(mode, jobs, selected, "q exit")
		}
		screen := renderJobsListScreenCore(
			jobs, width, height, selected, scroll, color, "q exit", footer,
			mode.title, mode.showWorkspace,
		)
		fmt.Fprint(output, "\x1b[H\x1b[2J", dashboardTerminalFrame(screen.Frame))
		return screen
	}
	screen := draw()

	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		character, _, err := reader.ReadRune()
		if err != nil {
			return jobsListResult{}, err
		}
		if confirmArchive {
			switch character {
			case '\r', '\n', 'y', 'Y':
				jobID := jobs[selected].ID
				if mode.archiveJob == nil {
					notice = "Archive failed: unavailable"
				} else if err := mode.archiveJob(jobID); err != nil {
					notice = "Archive failed: " + dashboardText(err.Error())
				} else {
					jobs = append(jobs[:selected], jobs[selected+1:]...)
					selected = min(selected, len(jobs)-1)
					notice = "Archived " + jobViewText(jobID)
				}
				confirmArchive = false
			case 3, 4, 'n', 'N', 'q', 'Q':
				confirmArchive = false
				notice = ""
			default:
				continue
			}
			screen = draw()
			continue
		}
		action := ""
		if escapeSequence != "" {
			escapeSequence += string(character)
			var complete bool
			action, complete = jobViewEscapeAction(escapeSequence)
			if !complete {
				continue
			}
			escapeSequence = ""
		} else if character == '\x1b' {
			escapeSequence = "\x1b"
			continue
		} else {
			switch character {
			case 3, 4, 'q', 'Q':
				return jobsListResult{}, nil
			case '\r', '\n':
				action = "open"
			case ' ':
				action = "preload"
			case 'd', 'D':
				action = "delete"
			case '\t', 'j':
				action = "next"
			case 'k':
				action = "previous"
			case 'g':
				action = "home"
			case 'G':
				action = "end"
			}
		}

		switch action {
		case "open":
			if len(jobs) > 0 {
				return jobsListResult{JobID: jobs[selected].ID}, nil
			}
			continue
		case "preload":
			if mode.allowActions && len(jobs) > 0 {
				if command := jobsListPreloadCommand(jobs[selected]); command != "" {
					return jobsListResult{Preload: command}, nil
				}
			}
			continue
		case "delete":
			if mode.archiveJob != nil && len(jobs) > 0 {
				confirmArchive = true
				notice = ""
			}
		case "previous":
			notice = ""
			selected--
		case "next":
			notice = ""
			selected++
		case "page_up":
			notice = ""
			selected -= max(1, screen.BodyHeight-1)
		case "page_down":
			notice = ""
			selected += max(1, screen.BodyHeight-1)
		case "home":
			notice = ""
			selected = 0
		case "end":
			notice = ""
			selected = len(jobs) - 1
		default:
			continue
		}
		screen = draw()
	}
}

func renderJobsListScreen(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
) jobsListScreen {
	return renderJobsListScreenWithFooter(
		jobs, width, height, selected, scroll, color, "q exit", "",
	)
}

func renderJobsListScreenWithQuitLabel(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
	quitLabel string,
) jobsListScreen {
	return renderJobsListScreenWithFooter(
		jobs, width, height, selected, scroll, color, quitLabel, "",
	)
}

func renderJobsListScreenWithFooter(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
	quitLabel string,
	footer string,
) jobsListScreen {
	return renderJobsListScreenWithTitleAndFooter(
		jobs, width, height, selected, scroll, color, quitLabel, footer, "Pearl",
	)
}

func renderJobsListScreenWithTitleAndFooter(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
	quitLabel string,
	footer string,
	title string,
) jobsListScreen {
	return renderJobsListScreenCore(
		jobs, width, height, selected, scroll, color, quitLabel, footer, title, false,
	)
}

func renderJobsListScreenShowingWorkspace(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
	quitLabel string,
	footer string,
	title string,
) jobsListScreen {
	return renderJobsListScreenCore(
		jobs, width, height, selected, scroll, color, quitLabel, footer, title, true,
	)
}

func renderJobsListScreenCore(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
	quitLabel string,
	footer string,
	title string,
	showWorkspace bool,
) jobsListScreen {
	width = max(20, width)
	height = max(8, height)
	bodyHeight := height - 5
	layout := calculateJobsListLayout(width)
	if showWorkspace {
		layout = calculateJobsListLayoutWithWorkspace(width)
	}
	if len(jobs) == 0 {
		selected, scroll = 0, 0
	} else {
		selected = min(max(0, selected), len(jobs)-1)
		scroll = min(max(0, scroll), max(0, len(jobs)-bodyHeight))
	}
	end := min(len(jobs), scroll+bodyHeight)

	var frame strings.Builder
	if title == "" {
		title = "Pearl"
	}
	count := jobViewCountSummary(len(jobs), "job", "jobs")
	if len([]rune(title))+len([]rune(count))+1 <= width {
		title += strings.Repeat(" ", width-len([]rune(title))-len([]rune(count))) + count
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate(title, width)))
	divider := strings.Repeat("─", width)
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	fmt.Fprintln(&frame, renderJobsListHeader(layout, color))
	if len(jobs) == 0 {
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, "  No jobs recorded."))
	}
	for index := scroll; index < end; index++ {
		fmt.Fprintln(&frame, renderJobsListRow(jobs[index], layout, index == selected, color))
	}
	usedRows := end - scroll
	if len(jobs) == 0 {
		usedRows = 1
	}
	for row := usedRows; row < bodyHeight; row++ {
		fmt.Fprintln(&frame)
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	hint := footer
	if hint == "" {
		hint = jobsListFooter(jobs, selected, quitLabel)
	}
	position := "0/0"
	if len(jobs) > 0 {
		position = fmt.Sprintf("%d/%d", selected+1, len(jobs))
	}
	if len([]rune(hint))+len([]rune(position))+1 <= width {
		hint += strings.Repeat(" ", width-len([]rune(hint))-len([]rune(position))) + position
	}
	footerColor := ansiDim
	if strings.HasPrefix(hint, "Archive ") || strings.HasPrefix(hint, "Archive failed:") {
		footerColor = ansiRed
	} else if strings.HasPrefix(hint, "Archived ") {
		footerColor = ansiGreen
	}
	fmt.Fprintln(&frame, dashboardPaint(color, footerColor, dashboardTruncate(hint, width)))
	return jobsListScreen{Frame: frame.String(), BodyHeight: bodyHeight}
}

func jobsListFooter(jobs []store.Job, selected int, quitLabel string) string {
	parts := []string{quitLabel, "↑/↓ navigate"}
	if len(jobs) == 0 {
		return strings.Join(parts, " · ")
	}
	selected = min(max(0, selected), len(jobs)-1)
	parts = append(parts, "Enter view")
	switch jobsListAction(jobs[selected]) {
	case "respond":
		parts = append(parts, "Space answer")
	case "run":
		parts = append(parts, "Space run")
	case "retry":
		parts = append(parts, "Space retry")
	}
	parts = append(parts, "d archive")
	return strings.Join(parts, " · ")
}

func archivedJobsListFooter(jobs []store.Job, selected int, quitLabel string) string {
	parts := []string{quitLabel, "↑/↓ navigate"}
	if len(jobs) > 0 {
		parts = append(parts, "Enter view")
	}
	return strings.Join(parts, " · ")
}

func jobsListModeFooter(
	mode jobsListMode,
	jobs []store.Job,
	selected int,
	quitLabel string,
) string {
	if mode.allowActions {
		return jobsListFooter(jobs, selected, quitLabel)
	}
	return archivedJobsListFooter(jobs, selected, quitLabel)
}

func jobsListAction(job store.Job) string {
	switch job.Status {
	case store.JobPending:
		return "run"
	case store.JobCompleted, store.JobFailed, store.JobInterrupted, store.JobCancelled:
		return "retry"
	case store.JobWaitingInput:
		return "respond"
	default:
		return ""
	}
}

func jobsListPreloadCommand(job store.Job) string {
	action := jobsListAction(job)
	if action == "" {
		return ""
	}
	command := action + " " + dashboardEscapeCommandArgument(job.ID)
	if action == "respond" {
		command += " "
	}
	return command
}

func jobCanRun(job store.Job) bool {
	action := jobsListAction(job)
	return action == "run" || action == "retry"
}

func calculateJobsListLayout(width int) jobsListLayout {
	available := max(18, width) - 2
	if width < 40 {
		idWidth := max(6, available-12)
		return jobsListLayout{IDWidth: idWidth, StatusWidth: max(1, available-idWidth-2)}
	}
	idWidth := min(20, max(12, available/3))
	statusWidth := 13
	if width < 72 {
		return jobsListLayout{
			IDWidth: idWidth, StatusWidth: statusWidth,
			PromptWidth: max(1, available-idWidth-statusWidth-4),
		}
	}
	return jobsListLayout{
		IDWidth: idWidth, StatusWidth: statusWidth, CreatedWidth: 19,
		PromptWidth: max(1, available-idWidth-statusWidth-25),
	}
}

func calculateJobsListLayoutWithWorkspace(width int) jobsListLayout {
	layout := calculateJobsListLayout(width)
	if layout.CreatedWidth == 0 {
		return layout
	}
	const workspaceWidth = 24
	layout.WorkspaceWidth = workspaceWidth
	layout.PromptWidth = max(1, layout.PromptWidth-workspaceWidth-2)
	return layout
}

func renderJobsListHeader(layout jobsListLayout, color bool) string {
	line := "  " + jobsListCell("ID", layout.IDWidth) + "  " +
		jobsListCell("STATUS", layout.StatusWidth)
	if layout.CreatedWidth > 0 {
		line += "  " + jobsListCell("CREATED", layout.CreatedWidth)
	}
	if layout.WorkspaceWidth > 0 {
		line += "  " + jobsListCell("WORKSPACE", layout.WorkspaceWidth)
	}
	if layout.PromptWidth > 0 {
		line += "  " + jobsListCell("PROMPT", layout.PromptWidth)
	}
	return dashboardPaint(color, ansiBold+ansiCyan, line)
}

func renderJobsListRow(
	job store.Job,
	layout jobsListLayout,
	selected, color bool,
) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	id := jobsListCell(jobViewText(job.ID), layout.IDWidth)
	status := jobsListCell(job.Status, layout.StatusWidth)
	if color {
		if selected {
			prefix = dashboardPaint(true, ansiBold+ansiCyan, prefix)
			id = dashboardPaint(true, ansiBold+ansiCyan, id)
		}
		status = dashboardPaint(true, jobStatusColor(job.Status), status)
	}
	line := prefix + id + "  " + status
	if layout.CreatedWidth > 0 {
		line += "  " + jobsListCell(formatJobCreatedAt(job.CreatedAt), layout.CreatedWidth)
	}
	if layout.WorkspaceWidth > 0 {
		line += "  " + jobsListCell(workspaceBoardLabel(job.WorkspaceRoot), layout.WorkspaceWidth)
	}
	if layout.PromptWidth > 0 {
		line += "  " + jobsListCell(dashboardText(job.Prompt), layout.PromptWidth)
	}
	return line
}

func jobsListCell(value string, width int) string {
	value = dashboardTruncate(value, width)
	return value + strings.Repeat(" ", max(0, width-len([]rune(value))))
}
