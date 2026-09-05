package agent_functions

import (
	"bufio"
	"context"
	"errors"
	"github.com/August-H/pearl-cli/internal/workspacepath"
	"io"
	"io/fs"
	"os"
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

func ignoredPath(root, path string, entry fs.DirEntry, patterns []string) bool {
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
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "/") && !entry.IsDir() {
			continue
		}
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
		target := rel
		if !strings.Contains(pattern, "/") {
			target = name
		}
		if match, _ := filepath.Match(pattern, target); match {
			return true
		}
	}
	return false
}

// Ignore files support simple globs and directory patterns. Negation and **
// semantics are deliberately not interpreted as full gitignore syntax.
func walkProject(ctx context.Context, root string, visit func(string, fs.DirEntry) error) error {
	var patterns []string
	for _, name := range []string{".gitignore", ".pearlignore"} {
		path, err := workspacepath.Resolve(root, name)
		if err != nil {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
		for scanner.Scan() {
			pattern := strings.TrimSpace(scanner.Text())
			if pattern != "" && !strings.HasPrefix(pattern, "#") && !strings.HasPrefix(pattern, "!") {
				patterns = append(patterns, pattern)
			}
		}
		file.Close()
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if workspacepath.IsLink(info) || ignoredPath(root, path, entry, patterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return visit(path, entry)
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

func ListFiles(ctx context.Context, root string, offset, limit int) (FilePage, error) {
	offset, limit = pageBounds(offset, limit)
	page := FilePage{Files: []string{}}
	index := 0
	err := walkProject(ctx, root, func(path string, entry fs.DirEntry) error {
		index++
		if index <= offset {
			return nil
		}
		if len(page.Files) == limit {
			page.Truncated, page.NextOffset = true, offset+limit
			return browseDone
		}
		rel, _ := filepath.Rel(root, path)
		page.Files = append(page.Files, filepath.ToSlash(rel))
		return nil
	})
	return page, err
}

func SearchFiles(ctx context.Context, root, query string, offset, limit int, maxBytes int64) (SearchPage, error) {
	offset, limit = pageBounds(offset, limit)
	page := SearchPage{Matches: []SearchMatch{}}
	if query == "" {
		return page, errors.New("query cannot be empty")
	}
	index := 0
	err := walkProject(ctx, root, func(path string, entry fs.DirEntry) error {
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxBytes {
			return nil
		}
		contents, err := ReadLimitedFile(path, maxBytes)
		if err != nil {
			return err
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
			rel, _ := filepath.Rel(root, path)
			page.Matches = append(page.Matches, SearchMatch{filepath.ToSlash(rel), line + 1, text})
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
