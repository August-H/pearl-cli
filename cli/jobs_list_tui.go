package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"golang.org/x/term"
)

type jobsListScreen struct {
	Frame      string
	BodyHeight int
}

const (
	jobsListMouseEnable  = "\x1b[?1000h\x1b[?1006h"
	jobsListMouseDisable = "\x1b[?1000l\x1b[?1006l"
)

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
	// workspace limits top-level rows to one directory; empty shows every one.
	workspace     string
	folders       []store.Folder
	moveFolder    func(name, directory string) (store.Folder, error)
	pickDirectory func(string) (string, error)
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

// jobsListState is the jobs list with its folders, independent of the terminal.
type jobsListState struct {
	mode           jobsListMode
	jobs           []store.Job
	entries        []jobsListEntry
	openFolder     string
	selected       int
	scroll         int
	bodyHeight     int
	confirmArchive bool
	notice         string
	wheel          listWheelNavigation
}

func newJobsListState(jobs []store.Job, mode jobsListMode) *jobsListState {
	state := &jobsListState{mode: mode, jobs: jobs}
	state.refresh()
	return state
}

func (s *jobsListState) refresh() {
	s.entries = buildJobsListEntries(s.jobs, s.mode.folders, s.mode.workspace, s.openFolder)
	s.selected = min(max(0, s.selected), max(0, len(s.entries)-1))
}

func (s *jobsListState) current() (jobsListEntry, bool) {
	if len(s.entries) == 0 {
		return jobsListEntry{}, false
	}
	return s.entries[min(max(0, s.selected), len(s.entries)-1)], true
}

func (s *jobsListState) folder(name string) (store.Folder, bool) {
	for _, folder := range s.mode.folders {
		if folder.Name == name {
			return folder, true
		}
	}
	return store.Folder{}, false
}

func (s *jobsListState) screen(width, height int, color bool) jobsListScreen {
	height = max(8, height)
	bodyHeight := height - 5
	if len(s.entries) > 0 {
		s.selected = min(max(0, s.selected), len(s.entries)-1)
		if s.selected < s.scroll {
			s.scroll = s.selected
		} else if s.selected >= s.scroll+bodyHeight {
			s.scroll = s.selected - bodyHeight + 1
		}
		s.scroll = min(max(0, s.scroll), max(0, len(s.entries)-bodyHeight))
	} else {
		s.selected, s.scroll = 0, 0
	}
	footer := jobsListEntriesFooter(
		s.entries, s.selected, "Esc exit", s.mode.allowActions, s.openFolder,
	)
	if s.openFolder != "" {
		footer = jobsListEntriesFooter(
			s.entries, s.selected, "Esc back", s.mode.allowActions, s.openFolder,
		)
	}
	if entry, ok := s.current(); s.confirmArchive && ok {
		footer = fmt.Sprintf("Archive %s? y confirm · n cancel", jobViewText(entry.job.ID))
	} else if s.notice != "" {
		footer = s.notice + " · " + footer
	}
	title := s.mode.title
	empty := ""
	if s.openFolder != "" {
		title += " · " + s.openFolder
		empty = "No jobs in this folder yet. Press n to create one."
	}
	screen := renderJobsListEntriesScreen(s.entries, width, height, s.selected, s.scroll, color, jobsListView{
		title: title, footer: footer, empty: empty, showWorkspace: s.mode.showWorkspace,
	})
	s.bodyHeight = screen.BodyHeight
	return screen
}

// handle applies a key, or an escape-sequence action when action is set. It
// returns the list's result once done, and whether the screen changed.
func (s *jobsListState) handle(character rune, action string, now time.Time) (jobsListResult, bool, bool) {
	if s.confirmArchive {
		if action != "" {
			return jobsListResult{}, false, false
		}
		switch character {
		case '\r', '\n', 'y', 'Y':
			s.archiveSelected()
		case 3, 4, 'n', 'N', escapeKey:
			s.confirmArchive = false
			s.notice = ""
		default:
			return jobsListResult{}, false, false
		}
		return jobsListResult{}, false, true
	}
	if action == "" {
		switch character {
		case 3, 4:
			return jobsListResult{}, true, false
		case escapeKey:
			if s.openFolder == "" {
				return jobsListResult{}, true, false
			}
			s.closeFolder()
			return jobsListResult{}, false, true
		case '\r', '\n':
			action = "open"
		case ' ':
			action = "preload"
		case 'd', 'D':
			action = "delete"
		case 'e', 'E':
			action = "folder_directory"
		case 'n':
			action = "new_job"
		case 'f':
			action = "new_folder"
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
	action = s.wheel.filter(action, now)
	entry, hasEntry := s.current()
	switch action {
	case "open":
		if !hasEntry {
			return jobsListResult{}, false, false
		}
		if entry.isFolder() {
			s.openFolder, s.selected, s.scroll, s.notice = entry.folder.Name, 0, 0, ""
			s.refresh()
			return jobsListResult{}, false, true
		}
		return jobsListResult{JobID: entry.job.ID}, true, false
	case "preload":
		if !s.mode.allowActions || !hasEntry {
			return jobsListResult{}, false, false
		}
		if entry.isFolder() {
			return jobsListResult{Preload: jobsListFolderJobCommand(entry.folder.Name)}, true, false
		}
		if command := jobsListPreloadCommand(entry.job); command != "" {
			return jobsListResult{Preload: command}, true, false
		}
		return jobsListResult{}, false, false
	case "new_job":
		if s.mode.allowActions && s.openFolder != "" {
			return jobsListResult{Preload: jobsListFolderJobCommand(s.openFolder)}, true, false
		}
		return jobsListResult{}, false, false
	case "new_folder":
		if s.mode.allowActions && s.openFolder == "" {
			return jobsListResult{Preload: "folder create "}, true, false
		}
		return jobsListResult{}, false, false
	case "folder_directory":
		if !s.mode.allowActions {
			return jobsListResult{}, false, false
		}
		s.editFolderDirectory()
		return jobsListResult{}, false, true
	case "delete":
		if s.mode.archiveJob == nil || !hasEntry || entry.isFolder() {
			return jobsListResult{}, false, false
		}
		s.confirmArchive, s.notice = true, ""
		return jobsListResult{}, false, true
	case "previous", "scroll_up":
		s.selected--
	case "next", "scroll_down":
		s.selected++
	case "page_up":
		s.selected -= max(1, s.bodyHeight-1)
	case "page_down":
		s.selected += max(1, s.bodyHeight-1)
	case "home":
		s.selected = 0
	case "end":
		s.selected = len(s.entries) - 1
	default:
		return jobsListResult{}, false, false
	}
	s.notice = ""
	s.selected = min(max(0, s.selected), max(0, len(s.entries)-1))
	return jobsListResult{}, false, true
}

func (s *jobsListState) closeFolder() {
	name := s.openFolder
	s.openFolder, s.notice = "", ""
	s.refresh()
	for index, entry := range s.entries {
		if entry.isFolder() && entry.folder.Name == name {
			s.selected = index
		}
	}
}

func (s *jobsListState) archiveSelected() {
	s.confirmArchive = false
	entry, ok := s.current()
	if !ok || entry.isFolder() {
		return
	}
	jobID := entry.job.ID
	if s.mode.archiveJob == nil {
		s.notice = "Archive failed: unavailable"
		return
	}
	if err := s.mode.archiveJob(jobID); err != nil {
		s.notice = "Archive failed: " + dashboardText(err.Error())
		return
	}
	for index, job := range s.jobs {
		if job.ID == jobID {
			s.jobs = append(s.jobs[:index], s.jobs[index+1:]...)
			break
		}
	}
	s.refresh()
	s.notice = "Archived " + jobViewText(jobID)
}

// editFolderDirectory opens a directory picker for the open or selected folder.
func (s *jobsListState) editFolderDirectory() {
	name := s.openFolder
	if entry, ok := s.current(); name == "" && ok && entry.isFolder() {
		name = entry.folder.Name
	}
	folder, found := s.folder(name)
	if !found {
		s.notice = "Select a folder to change its directory"
		return
	}
	if s.mode.pickDirectory == nil || s.mode.moveFolder == nil {
		s.notice = "Folder failed: changing directories is unavailable"
		return
	}
	directory, err := s.mode.pickDirectory(folder.WorkspaceRoot)
	if errors.Is(err, errDirectoryPickerCancelled) {
		s.notice = "Directory unchanged"
		return
	}
	if err == nil {
		folder, err = s.mode.moveFolder(folder.Name, directory)
	}
	if err != nil {
		s.notice = "Folder failed: " + dashboardText(err.Error())
		return
	}
	for index := range s.mode.folders {
		if s.mode.folders[index].Name == folder.Name {
			s.mode.folders[index] = folder
		}
	}
	moveFolderJobs(s.jobs, folder)
	s.refresh()
	s.notice = "Moved " + folder.Name + " to " + displayJobDirectory(folder.WorkspaceRoot)
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

	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l", jobsListMouseEnable)
	defer fmt.Fprint(output, jobsListMouseDisable, "\x1b[?25h\x1b[?1049l")

	state := newJobsListState(jobs, mode)
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	draw := func() {
		width, height, sizeErr := term.GetSize(int(output.Fd()))
		if sizeErr != nil || width <= 0 {
			width = 80
		}
		if sizeErr != nil || height <= 0 {
			height = 24
		}
		screen := state.screen(width, height, color)
		fmt.Fprint(output, "\x1b[H", listTerminalFrame(screen.Frame))
	}
	draw()

	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		character, err := readTerminalKey(reader)
		if err != nil {
			return jobsListResult{}, err
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
			if action == "" {
				continue
			}
			character = 0
		} else if character == '\x1b' {
			escapeSequence = "\x1b"
			continue
		}
		result, done, redraw := state.handle(character, action, time.Now())
		if done {
			return result, nil
		}
		if redraw {
			draw()
		}
	}
}

func renderJobsListScreen(
	jobs []store.Job,
	width, height, selected, scroll int,
	color bool,
) jobsListScreen {
	return renderJobsListScreenWithFooter(
		jobs, width, height, selected, scroll, color, "Esc exit", "",
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
	entries := make([]jobsListEntry, len(jobs))
	for index, job := range jobs {
		entries[index] = jobsListEntry{job: job}
	}
	if footer == "" {
		footer = jobsListFooter(jobs, selected, quitLabel)
	}
	return renderJobsListEntriesScreen(entries, width, height, selected, scroll, color, jobsListView{
		title: title, footer: footer, showWorkspace: showWorkspace,
	})
}

type jobsListView struct {
	title         string
	footer        string
	empty         string
	showWorkspace bool
}

func renderJobsListEntriesScreen(
	entries []jobsListEntry,
	width, height, selected, scroll int,
	color bool,
	view jobsListView,
) jobsListScreen {
	width = max(20, width)
	height = max(8, height)
	bodyHeight := height - 5
	layout := calculateJobsListLayout(width)
	if view.showWorkspace {
		layout = calculateJobsListLayoutWithWorkspace(width)
	}
	if len(entries) == 0 {
		selected, scroll = 0, 0
	} else {
		selected = min(max(0, selected), len(entries)-1)
		scroll = min(max(0, scroll), max(0, len(entries)-bodyHeight))
	}
	end := min(len(entries), scroll+bodyHeight)

	var frame strings.Builder
	title := view.title
	if title == "" {
		title = "Pearl"
	}
	folders := 0
	for _, entry := range entries {
		if entry.isFolder() {
			folders++
		}
	}
	count := jobViewCountSummary(len(entries)-folders, "job", "jobs")
	if folders > 0 {
		count = jobViewCountSummary(folders, "folder", "folders") + " · " + count
	}
	if len([]rune(title))+len([]rune(count))+1 <= width {
		title += strings.Repeat(" ", width-len([]rune(title))-len([]rune(count))) + count
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiBold+ansiCyan, dashboardTruncate(title, width)))
	divider := strings.Repeat("─", width)
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	fmt.Fprintln(&frame, renderJobsListHeader(layout, color))
	if len(entries) == 0 {
		empty := view.empty
		if empty == "" {
			empty = "No jobs recorded."
		}
		fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, dashboardTruncate("  "+empty, width)))
	}
	for index := scroll; index < end; index++ {
		entry := entries[index]
		if entry.isFolder() {
			fmt.Fprintln(&frame, renderJobsListFolderRow(entry, layout, index == selected, color))
		} else {
			fmt.Fprintln(&frame, renderJobsListRow(entry.job, layout, index == selected, color))
		}
	}
	usedRows := end - scroll
	if len(entries) == 0 {
		usedRows = 1
	}
	for row := usedRows; row < bodyHeight; row++ {
		fmt.Fprintln(&frame)
	}
	fmt.Fprintln(&frame, dashboardPaint(color, ansiDim, divider))
	hint := view.footer
	position := "0/0"
	if len(entries) > 0 {
		position = fmt.Sprintf("%d/%d", selected+1, len(entries))
	}
	if len([]rune(hint))+len([]rune(position))+1 <= width {
		hint += strings.Repeat(" ", width-len([]rune(hint))-len([]rune(position))) + position
	}
	footerColor := ansiDim
	if strings.HasPrefix(hint, "Archive ") || strings.HasPrefix(hint, "Archive failed:") ||
		strings.HasPrefix(hint, "Folder failed:") {
		footerColor = ansiRed
	} else if strings.HasPrefix(hint, "Archived ") || strings.HasPrefix(hint, "Moved ") {
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

func renderJobsListFolderRow(
	entry jobsListEntry,
	layout jobsListLayout,
	selected, color bool,
) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}
	folder := entry.folder
	name := jobsListCell(dashboardText(folder.Name)+"/", layout.IDWidth)
	count := jobsListCell(jobViewCountSummary(entry.jobCount, "job", "jobs"), layout.StatusWidth)
	if color {
		if selected {
			prefix = dashboardPaint(true, ansiBold+ansiCyan, prefix)
			name = dashboardPaint(true, ansiBold+ansiCyan, name)
		} else {
			name = dashboardPaint(true, ansiBold+ansiYellow, name)
		}
		count = dashboardPaint(true, ansiDim, count)
	}
	line := prefix + name + "  " + count
	if layout.CreatedWidth > 0 {
		line += "  " + jobsListCell(formatJobCreatedAt(folder.CreatedAt), layout.CreatedWidth)
	}
	directory := workspaceBoardLabel(folder.WorkspaceRoot)
	if layout.WorkspaceWidth > 0 {
		line += "  " + jobsListCell(directory, layout.WorkspaceWidth)
		directory = ""
	}
	if layout.PromptWidth > 0 {
		line += "  " + jobsListCell(directory, layout.PromptWidth)
	}
	return line
}

func jobsListCell(value string, width int) string {
	value = dashboardTruncate(value, width)
	return value + strings.Repeat(" ", max(0, width-len([]rune(value))))
}
