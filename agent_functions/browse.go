package agent_functions

import (
	"bufio"
	"context"
	"errors"
	"github.com/August-H/pearl-cli/internal/workspacepath"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type FilePage struct {
	Files      []string `json:"files"`
	NextOffset int      `json:"next_offset,omitempty"`
	Truncated  bool     `json:"truncated"`
}
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type SearchPage struct {
	Matches    []SearchMatch `json:"matches"`
	NextOffset int           `json:"next_offset,omitempty"`
	Truncated  bool          `json:"truncated"`
}

var browseDone = errors.New("page complete")

// ignoredPath reports whether a walked entry is skipped. rel is the slash
// path relative to the directory being listed.
func ignoredPath(rel string, entry fs.DirEntry, patterns []string) bool {
	name := entry.Name()
	if workspacepath.Protected(name) {
		return true
	}
	if entry.IsDir() {
		switch name {
		case "node_modules", "vendor", ".venv", "venv", "__pycache__", "dist", "build", "target", ".next", "coverage":
			return true
		}
	}
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "/") && !entry.IsDir() {
			continue
		}
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
		target := rel
		if !strings.Contains(pattern, "/") {
			target = name
		}
		if match, _ := path.Match(pattern, target); match {
			return true
		}
	}
	return false
}

// Ignore files support simple globs and directory patterns. Negation and **
// semantics are deliberately not interpreted as full gitignore syntax.
// visit receives slash paths relative to start.
func walkProject(ctx context.Context, root *os.Root, start string, visit func(string, string, fs.DirEntry) error) error {
	tree := root.FS()
	start = filepath.ToSlash(filepath.Clean(start))
	var patterns []string
	for _, name := range []string{".gitignore", ".pearlignore"} {
		file, err := tree.Open(path.Join(start, name))
		if err != nil {
			continue
		}
		if info, err := file.Stat(); err == nil && info.Mode().IsRegular() {
			scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
			for scanner.Scan() {
				pattern := strings.TrimSpace(scanner.Text())
				if pattern != "" && !strings.HasPrefix(pattern, "#") && !strings.HasPrefix(pattern, "!") {
					patterns = append(patterns, pattern)
				}
			}
		}
		file.Close()
	}
	err := fs.WalkDir(tree, start, func(name string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if name == start {
			return nil
		}
		rel := name
		if start != "." {
			rel = strings.TrimPrefix(name, start+"/")
		}
		if entry.Type()&fs.ModeSymlink != 0 || ignoredPath(rel, entry, patterns) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if info, err := entry.Info(); err != nil || workspacepath.IsLink(info) {
			return nil
		}
		return visit(name, rel, entry)
	})
	if errors.Is(err, browseDone) {
		return nil
	}
	return err
}

func pageBounds(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	return offset, limit
}

// ListFiles lists a directory by path. Agent tools use ListFilesIn.
func ListFiles(ctx context.Context, directory string, offset, limit int) (FilePage, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return FilePage{Files: []string{}}, err
	}
	defer root.Close()
	return ListFilesIn(ctx, root, ".", offset, limit)
}

func ListFilesIn(ctx context.Context, root *os.Root, start string, offset, limit int) (FilePage, error) {
	offset, limit = pageBounds(offset, limit)
	page := FilePage{Files: []string{}}
	index := 0
	err := walkProject(ctx, root, start, func(_, rel string, _ fs.DirEntry) error {
		index++
		if index <= offset {
			return nil
		}
		if len(page.Files) == limit {
			page.Truncated, page.NextOffset = true, offset+limit
			return browseDone
		}
		page.Files = append(page.Files, rel)
		return nil
	})
	return page, err
}

// SearchFiles searches a directory by path. Agent tools use SearchFilesIn.
func SearchFiles(ctx context.Context, directory, query string, offset, limit int, maxBytes int64) (SearchPage, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return SearchPage{Matches: []SearchMatch{}}, err
	}
	defer root.Close()
	return SearchFilesIn(ctx, root, ".", query, offset, limit, maxBytes)
}

func SearchFilesIn(ctx context.Context, root *os.Root, start, query string, offset, limit int, maxBytes int64) (SearchPage, error) {
	offset, limit = pageBounds(offset, limit)
	page := SearchPage{Matches: []SearchMatch{}}
	if query == "" {
		return page, errors.New("query cannot be empty")
	}
	tree := root.FS()
	index := 0
	err := walkProject(ctx, root, start, func(name, rel string, entry fs.DirEntry) error {
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxBytes || workspacepath.LinkCount(info) > 1 {
			return nil
		}
		file, err := tree.Open(name)
		if err != nil {
			return nil
		}
		contents, err := readOpenFile(file, maxBytes)
		file.Close()
		if err != nil {
			return nil
		}
		if strings.ContainsRune(contents, 0) {
			return nil
		}
		for line, text := range strings.Split(contents, "\n") {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !strings.Contains(text, query) {
				continue
			}
			index++
			if index <= offset {
				continue
			}
			if len(page.Matches) == limit {
				page.Truncated, page.NextOffset = true, offset+limit
				return browseDone
			}
			if len(text) > 500 {
				text = text[:500] + "…"
			}
			page.Matches = append(page.Matches, SearchMatch{rel, line + 1, text})
		}
		return nil
	})
	return page, err
}

func ReadLimitedFile(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", errors.New("file exceeds the readable size limit")
	}
	return string(data), nil
}
