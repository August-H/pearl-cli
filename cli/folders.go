package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"golang.org/x/term"
)

const folderUsage = `Usage:
  pearl folder [list]                                List job folders
  pearl folder create <name> [-d | --workspace path] Create a folder (default: this directory)
  pearl folder edit <name> [--workspace path]        Change a folder's directory (default: picker)
  pearl folder remove <name>                         Remove a folder and keep its jobs`

func runFolder(args []string) int {
	return runFolderWithPicker(args, chooseJobDirectory)
}

func runFolderWithPicker(args []string, pickDirectory func(string) (string, error)) int {
	if len(args) == 0 {
		return listFolders()
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Println(folderUsage)
		return 0
	case "list", "ls":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, folderUsage)
			return 2
		}
		return listFolders()
	case "create", "new", "edit":
		return saveFolder(args[0] == "edit", args[1:], pickDirectory)
	case "remove", "rm", "delete":
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if name == "" {
			fmt.Fprintln(os.Stderr, folderUsage)
			return 2
		}
		return removeFolder(name)
	default:
		fmt.Fprintln(os.Stderr, folderUsage)
		return 2
	}
}

// saveFolder creates a folder, or with edit set, moves an existing one.
func saveFolder(edit bool, args []string, pickDirectory func(string) (string, error)) int {
	chooseDirectory := false
	workspacePath := ""
	var nameParts []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "-d" || argument == "--directory":
			chooseDirectory = true
		case argument == "--workspace" && index+1 < len(args):
			index++
			workspacePath = args[index]
		case strings.HasPrefix(argument, "--workspace="):
			workspacePath = strings.TrimPrefix(argument, "--workspace=")
		case argument == "-h" || argument == "--help":
			fmt.Println(folderUsage)
			return 0
		default:
			nameParts = append(nameParts, argument)
		}
	}
	name := strings.TrimSpace(strings.Join(nameParts, " "))
	if name == "" {
		fmt.Fprintln(os.Stderr, folderUsage)
		return 2
	}
	if err := store.ValidateFolderName(name); err != nil {
		return printError("Folder", err)
	}
	if chooseDirectory && workspacePath != "" {
		return printError("Folder", errors.New("use either --directory or --workspace"))
	}
	if err := ensureDaemonRunning(); err != nil {
		return printError("Folder", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Folder", err)
	}

	start, err := os.Getwd()
	if err != nil {
		return printError("Folder", err)
	}
	if edit {
		folder, err := findFolder(client, name)
		if err != nil {
			return printError("Folder", err)
		}
		start = folder.WorkspaceRoot
		chooseDirectory = workspacePath == ""
	}
	workspace := start
	if workspacePath != "" {
		if workspace, err = explicitWorkspace(workspacePath); err != nil {
			return printError("Workspace", err)
		}
	}
	if chooseDirectory {
		workspace, err = pickDirectory(start)
		if errors.Is(err, errDirectoryPickerCancelled) {
			fmt.Fprintln(os.Stderr, "Directory selection cancelled")
			return 1
		}
		if err != nil {
			return printError("Directory", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var folder store.Folder
	if edit {
		folder, err = client.setFolderWorkspace(ctx, name, workspace)
	} else {
		folder, err = client.createFolder(ctx, name, workspace)
	}
	if err != nil {
		return printError("Folder", err)
	}
	fmt.Println("Folder:", folder.Name)
	fmt.Println("Directory:", displayJobDirectory(folder.WorkspaceRoot))
	return 0
}

func findFolder(client *daemonClient, name string) (store.Folder, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	folders, err := client.folders(ctx)
	if err != nil {
		return store.Folder{}, err
	}
	for _, folder := range folders {
		if folder.Name == name {
			return folder, nil
		}
	}
	return store.Folder{}, fmt.Errorf("folder %q not found", name)
}

func removeFolder(name string) int {
	if err := ensureDaemonRunning(); err != nil {
		return printError("Folder", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Folder", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.deleteFolder(ctx, name); err != nil {
		return printError("Folder", err)
	}
	fmt.Println("Removed folder", name+". Its jobs are still on the job board.")
	return 0
}

func listFolders() int {
	if err := ensureDaemonRunning(); err != nil {
		return printError("Folders", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Folders", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	folders, err := client.folders(ctx)
	if err != nil {
		return printError("Folders", err)
	}
	if len(folders) == 0 {
		fmt.Println("No folders. Create one with: pearl folder create <name>")
		return 0
	}
	jobs, err := client.jobs(ctx)
	if err != nil {
		return printError("Folders", err)
	}
	counts := folderJobCounts(jobs)
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" &&
		term.IsTerminal(int(os.Stdout.Fd()))
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', tabwriter.StripEscape)
	fmt.Fprintln(writer, jobBoardPaint(color, ansiBold+ansiCyan, "FOLDER\tJOBS\tDIRECTORY"))
	for _, folder := range folders {
		fmt.Fprintf(writer, "%s\t%d\t%s\n", singleLineTableText(folder.Name),
			counts[folder.Name], displayJobDirectory(folder.WorkspaceRoot))
	}
	_ = writer.Flush()
	return 0
}

func folderJobCounts(jobs []store.Job) map[string]int {
	counts := make(map[string]int)
	for _, job := range jobs {
		if job.Folder != "" {
			counts[job.Folder]++
		}
	}
	return counts
}

// jobsListEntry is one row of a jobs list: a folder or a job.
type jobsListEntry struct {
	folder   *store.Folder
	jobCount int
	job      store.Job
}

func (entry jobsListEntry) isFolder() bool { return entry.folder != nil }

// buildJobsListEntries lists folders first, then jobs outside any folder.
// Inside openFolder it lists only that folder's jobs. An empty workspace shows
// every directory.
func buildJobsListEntries(
	jobs []store.Job,
	folders []store.Folder,
	workspace, openFolder string,
) []jobsListEntry {
	entries := []jobsListEntry{}
	if openFolder != "" {
		for _, job := range jobs {
			if job.Folder == openFolder {
				entries = append(entries, jobsListEntry{job: job})
			}
		}
		return entries
	}
	counts := folderJobCounts(jobs)
	known := make(map[string]bool, len(folders))
	for index := range folders {
		folder := folders[index]
		known[folder.Name] = true
		if workspace != "" && !jobBelongsToWorkspace(folder.WorkspaceRoot, workspace) {
			continue
		}
		entries = append(entries, jobsListEntry{folder: &folder, jobCount: counts[folder.Name]})
	}
	for _, job := range jobs {
		if known[job.Folder] {
			continue
		}
		if workspace != "" && !jobBelongsToWorkspace(job.WorkspaceRoot, workspace) {
			continue
		}
		entries = append(entries, jobsListEntry{job: job})
	}
	return entries
}

func jobsListEntryJobs(entries []jobsListEntry) []store.Job {
	jobs := make([]store.Job, 0, len(entries))
	for _, entry := range entries {
		if !entry.isFolder() {
			jobs = append(jobs, entry.job)
		}
	}
	return jobs
}

func jobsListEntriesFooter(
	entries []jobsListEntry,
	selected int,
	quitLabel string,
	allowActions bool,
	openFolder string,
) string {
	parts := []string{quitLabel, "↑/↓ navigate"}
	if len(entries) > 0 {
		entry := entries[min(max(0, selected), len(entries)-1)]
		if entry.isFolder() {
			parts = append(parts, "Enter open", "Space new job", "e directory")
		} else {
			parts = append(parts, "Enter view")
			if allowActions {
				switch jobsListAction(entry.job) {
				case "respond":
					parts = append(parts, "Space answer")
				case "run":
					parts = append(parts, "Space run")
				case "retry":
					parts = append(parts, "Space retry")
				}
				parts = append(parts, "d archive")
			}
		}
	}
	if allowActions && openFolder != "" {
		parts = append(parts, "n new job", "e directory")
	} else if allowActions {
		parts = append(parts, "f new folder")
	}
	return strings.Join(parts, " · ")
}

func jobsListFolderJobCommand(folder string) string {
	return "job --folder " + dashboardEscapeCommandArgument(folder) + " "
}

// moveFolderJobs mirrors store.SetFolderWorkspace on a local copy of the board.
func moveFolderJobs(jobs []store.Job, folder store.Folder) {
	for index := range jobs {
		job := &jobs[index]
		if job.Folder == folder.Name && job.Status != store.JobRunning &&
			job.Status != store.JobWaitingInput {
			job.WorkspaceRoot = folder.WorkspaceRoot
		}
	}
}
