package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func testFolderBoard() ([]store.Job, []store.Folder) {
	folders := []store.Folder{
		{Name: "docs", WorkspaceRoot: "/work/site"},
		{Name: "elsewhere", WorkspaceRoot: "/other"},
	}
	jobs := []store.Job{
		{ID: "loose-job", Prompt: "fix it", WorkspaceRoot: "/work/site", Status: store.JobPending},
		{ID: "docs-one", Prompt: "write", WorkspaceRoot: "/work/site", Folder: "docs", Status: store.JobPending},
		{ID: "docs-two", Prompt: "edit", WorkspaceRoot: "/work/site", Folder: "docs", Status: store.JobRunning},
		{ID: "far-job", Prompt: "far", WorkspaceRoot: "/other", Status: store.JobPending},
		{ID: "orphan", Prompt: "lost", WorkspaceRoot: "/work/site", Folder: "gone", Status: store.JobPending},
	}
	return jobs, folders
}

func entryNames(entries []jobsListEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		if entry.isFolder() {
			names[index] = entry.folder.Name + "/"
		} else {
			names[index] = entry.job.ID
		}
	}
	return names
}

func TestJobsListEntriesPutFoldersFirstAndHideTheirJobs(t *testing.T) {
	jobs, folders := testFolderBoard()
	top := buildJobsListEntries(jobs, folders, "/work/site", "")
	if got := strings.Join(entryNames(top), ","); got != "docs/,loose-job,orphan" {
		t.Fatalf("top level = %s", got)
	}
	if top[0].jobCount != 2 {
		t.Fatalf("docs count = %d", top[0].jobCount)
	}
	all := buildJobsListEntries(jobs, folders, "", "")
	if got := strings.Join(entryNames(all), ","); got != "docs/,elsewhere/,loose-job,far-job,orphan" {
		t.Fatalf("all workspaces = %s", got)
	}
	inside := buildJobsListEntries(jobs, folders, "/work/site", "docs")
	if got := strings.Join(entryNames(inside), ","); got != "docs-one,docs-two" {
		t.Fatalf("inside docs = %s", got)
	}
}

func TestJobsListStateOpensFoldersAndCreatesJobsInThem(t *testing.T) {
	jobs, folders := testFolderBoard()
	state := newJobsListState(jobs, jobsListMode{
		title: "Pearl", allowActions: true, workspace: "/work/site", folders: folders,
	})
	now := time.Now()
	if result, done, _ := state.handle(' ', "", now); !done ||
		result.Preload != "job --folder docs " {
		t.Fatalf("Space on a folder = %+v, %v", result, done)
	}
	if _, done, redraw := state.handle('\r', "", now); done || !redraw || state.openFolder != "docs" {
		t.Fatalf("Enter did not open the folder: %q", state.openFolder)
	}
	if result, done, _ := state.handle('\r', "", now); !done || result.JobID != "docs-one" {
		t.Fatalf("Enter on a folder job = %+v", result)
	}
	if result, done, _ := state.handle('n', "", now); !done || result.Preload != "job --folder docs " {
		t.Fatalf("n inside a folder = %+v", result)
	}
	state.handle(0, "next", now)
	if _, done, _ := state.handle(escapeKey, "", now); done || state.openFolder != "" || state.selected != 0 {
		t.Fatalf("Esc should return to the folder row: open=%q selected=%d", state.openFolder, state.selected)
	}
	if _, done, _ := state.handle(escapeKey, "", now); !done {
		t.Fatal("Esc at the top level should leave the list")
	}
	if result, done, _ := state.handle('f', "", now); !done || result.Preload != "folder create " {
		t.Fatalf("f = %+v", result)
	}
}

func TestJobsListStateMovesAFolderWithE(t *testing.T) {
	jobs, folders := testFolderBoard()
	var picked, moved string
	state := newJobsListState(jobs, jobsListMode{
		title: "Pearl", allowActions: true, folders: folders,
		pickDirectory: func(start string) (string, error) {
			picked = start
			return "/new/site", nil
		},
		moveFolder: func(name, directory string) (store.Folder, error) {
			moved = name + " " + directory
			return store.Folder{Name: name, WorkspaceRoot: directory}, nil
		},
	})
	now := time.Now()
	state.handle('e', "", now)
	if picked != "/work/site" || moved != "docs /new/site" {
		t.Fatalf("picker started at %q and moved %q", picked, moved)
	}
	if !strings.HasPrefix(state.notice, "Moved docs to /new/site") {
		t.Fatalf("notice = %q", state.notice)
	}
	for _, job := range state.jobs {
		want := map[string]string{"docs-one": "/new/site", "docs-two": "/work/site"}[job.ID]
		if want != "" && job.WorkspaceRoot != want {
			t.Fatalf("%s is in %q, want %q", job.ID, job.WorkspaceRoot, want)
		}
	}

	state.mode.pickDirectory = func(string) (string, error) { return "", errDirectoryPickerCancelled }
	state.handle('e', "", now)
	if state.notice != "Directory unchanged" {
		t.Fatalf("cancel notice = %q", state.notice)
	}
	state.mode.pickDirectory = func(string) (string, error) { return "", errors.New("no picker") }
	state.handle('e', "", now)
	if state.notice != "Folder failed: no picker" {
		t.Fatalf("error notice = %q", state.notice)
	}
	state.handle(0, "end", now)
	state.handle('e', "", now)
	if state.notice != "Select a folder to change its directory" {
		t.Fatalf("e on a job = %q", state.notice)
	}
}

func TestJobsListScreenShowsFolderRows(t *testing.T) {
	jobs, folders := testFolderBoard()
	state := newJobsListState(jobs, jobsListMode{
		title: "Pearl", allowActions: true, workspace: "/work/site", folders: folders,
	})
	screen := state.screen(100, 12, false).Frame
	for _, want := range []string{
		"1 folder · 2 jobs",
		"› docs/",
		"2 jobs",
		"/work/site",
		"Enter open · Space new job · e directory",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("screen missing %q:\n%s", want, screen)
		}
	}
	state.handle('\r', "", time.Now())
	if screen := state.screen(100, 12, false).Frame; !strings.Contains(screen, "Pearl · docs") ||
		!strings.Contains(screen, "Esc back") || !strings.Contains(screen, "n new job") {
		t.Fatalf("folder screen:\n%s", screen)
	}
}

func TestJobFolderFlagCanFollowThePrompt(t *testing.T) {
	for _, args := range [][]string{
		{"write docs", "--folder", "docs"},
		{"write docs", "-f", "docs"},
		{"write docs", "--folder=docs"},
	} {
		rest, trailing := splitTrailingJobFlags(args)
		if len(rest) != 1 || trailing.folder != "docs" {
			t.Fatalf("splitTrailingJobFlags(%q) = %q, %+v", args, rest, trailing)
		}
	}
}
