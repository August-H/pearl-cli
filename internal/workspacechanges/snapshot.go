// Package workspacechanges records bounded filesystem snapshots and reviewable
// diffs independently of the tools an agent uses.
package workspacechanges

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/August-H/pearl-cli/agent_functions"
	"github.com/August-H/pearl-cli/internal/workspacepath"
)

const (
	maxFiles          = 10000
	maxFileBytes      = 1 << 20
	maxSnapshotBytes  = 32 << 20
	maxDiscoveryBytes = 2 << 20
)

var errGitUnavailable = errors.New("Git is unavailable or this folder is not a Git repository")

type File struct {
	Hash    string `json:"hash,omitempty"`
	Text    string `json:"text,omitempty"`
	Mode    uint32 `json:"mode"`
	Size    int64  `json:"size"`
	Binary  bool   `json:"binary,omitempty"`
	Omitted string `json:"omitted,omitempty"`
}

type Snapshot struct {
	Files map[string]File `json:"files"`
	Notes []string        `json:"notes,omitempty"`
}

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Diff   string `json:"diff,omitempty"`
	Note   string `json:"note,omitempty"`
}

type Report struct {
	Changes []Change `json:"changes"`
	Notes   []string `json:"notes,omitempty"`
}

// Capture includes tracked and nonignored untracked Git files. Without Git it
// uses the agent browser's project exclusions. Previously captured paths are
// also inspected, so changing ignore rules cannot invent deletions.
func Capture(ctx context.Context, directory string, previous *Snapshot) (Snapshot, error) {
	if err := workspacepath.CheckJobDirectory(directory); err != nil {
		return Snapshot{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Snapshot{}, err
	}
	defer root.Close()
	snapshot := Snapshot{Files: make(map[string]File)}
	paths, gitErr := gitFiles(ctx, directory)
	if gitErr != nil {
		if !errors.Is(gitErr, errGitUnavailable) {
			return Snapshot{}, gitErr
		}
		paths = nil
		err = agent_functions.WalkProjectFiles(ctx, root, func(name string, entry fs.DirEntry) error {
			if !entry.IsDir() {
				paths = append(paths, name)
				if len(paths) > maxFiles {
					return errors.New("workspace exceeds the 10000-file snapshot limit")
				}
			}
			return nil
		})
		if err != nil {
			return Snapshot{}, err
		}
	}
	if previous != nil {
		for name := range previous.Files {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	used := 0
	last := ""
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if name == last {
			continue
		}
		last = name
		if _, err := workspacepath.Resolve(directory, filepath.FromSlash(name)); err != nil {
			continue
		}
		info, err := root.Lstat(filepath.FromSlash(name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Snapshot{}, fmt.Errorf("inspect %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || workspacepath.IsLink(info) {
			continue
		}
		if len(snapshot.Files) == maxFiles {
			return Snapshot{}, errors.New("workspace exceeds the 10000-file snapshot limit")
		}
		file := File{Mode: uint32(info.Mode().Perm()), Size: info.Size()}
		switch {
		case workspacepath.LinkCount(info) > 1:
			file.Omitted = "hard-linked file; contents not read"
		case info.Size() > maxFileBytes:
			file.Omitted = "file exceeds 1 MiB; contents not captured"
		case info.Size() > int64(maxSnapshotBytes-used):
			file.Omitted = "32 MiB snapshot limit reached; contents not captured"
		default:
			content, err := agent_functions.ReadFileIn(root, filepath.FromSlash(name), min(maxFileBytes, int64(maxSnapshotBytes-used)))
			if errors.Is(err, agent_functions.ErrHardLinkedFile) {
				file.Omitted = "hard-linked file; contents not read"
				break
			}
			if err != nil {
				return Snapshot{}, fmt.Errorf("capture %s: %w", name, err)
			}
			used += len(content)
			hash := sha256.Sum256([]byte(content))
			file.Hash = hex.EncodeToString(hash[:])
			file.Binary = strings.ContainsRune(content, 0) || !utf8.ValidString(content)
			if !file.Binary {
				file.Text = content
			}
		}
		if file.Omitted != "" {
			snapshot.Notes = append(snapshot.Notes, name+": "+file.Omitted)
		}
		snapshot.Files[name] = file
	}
	return snapshot, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) String() string { return b.buffer.String() }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > maxDiscoveryBytes-b.buffer.Len() {
		b.exceeded = true
		return 0, errors.New("Git file list exceeds 2 MiB")
	}
	return b.buffer.Write(p)
}

func gitFiles(parent context.Context, directory string) ([]string, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "--no-optional-locks", "-c", "core.fsmonitor=false", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".")
	command.Dir = directory
	// An inherited GIT_DIR or GIT_WORK_TREE must not point at another project.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), "GIT_") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "LC_ALL=C")
	command.WaitDelay = time.Second
	var output boundedBuffer
	var stderr boundedBuffer
	command.Stdout = &output
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.exceeded || stderr.exceeded {
		return nil, errors.New("Git file discovery exceeds the 2 MiB limit")
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(stderr.String(), "not a git repository") {
			return nil, errGitUnavailable
		}
		return nil, fmt.Errorf("discover Git files: %w", err)
	}
	paths := strings.Split(strings.TrimSuffix(output.String(), "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return nil, nil
	}
	if len(paths) > maxFiles {
		return nil, errors.New("workspace exceeds the 10000-file snapshot limit")
	}
	return paths, nil
}

func Compare(before, after Snapshot) Report {
	report := Report{Changes: []Change{}}
	seenNotes := make(map[string]bool)
	for _, note := range append(append([]string(nil), before.Notes...), after.Notes...) {
		if !seenNotes[note] {
			report.Notes = append(report.Notes, note)
			seenNotes[note] = true
		}
	}
	paths := make(map[string]bool)
	for name := range before.Files {
		paths[name] = true
	}
	for name := range after.Files {
		paths[name] = true
	}
	var names []string
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	remaining := 4 << 20
	for _, name := range names {
		old, had := before.Files[name]
		current, has := after.Files[name]
		if had && has && old.Hash == current.Hash && old.Mode == current.Mode && old.Size == current.Size && old.Omitted == current.Omitted {
			continue
		}
		change := Change{Path: name, Action: "modified"}
		if !had {
			change.Action = "created"
		} else if !has {
			change.Action = "deleted"
		}
		switch {
		case old.Omitted != "" || current.Omitted != "":
			change.Note = "Contents unavailable; see capture notes."
		case old.Binary || current.Binary:
			change.Note = "Binary file changed; text diff unavailable."
		case remaining == 0:
			change.Note = "Diff omitted by the report size limit."
		default:
			change.Diff = unifiedDiff(name, old.Text, current.Text, had, has)
			limit := min(64<<10, remaining)
			if len(change.Diff) > limit {
				change.Diff = change.Diff[:limit]
				for !utf8.ValidString(change.Diff) {
					change.Diff = change.Diff[:len(change.Diff)-1]
				}
				change.Note = "Diff truncated by the report size limit."
			}
			remaining -= len(change.Diff)
		}
		if had && has && old.Mode != current.Mode {
			change.Note = strings.TrimSpace(fmt.Sprintf("%s Permissions: %04o -> %04o.", change.Note, old.Mode, current.Mode))
		}
		report.Changes = append(report.Changes, change)
	}
	return report
}
