package agent_functions

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBrowseAndSearchPagesIgnoreGeneratedAndProtectedFiles(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{
		"a.txt": "needle one\nneedle two\n", "b.txt": "needle three\n", ".env": "needle secret",
		"node_modules/package/index.js": "needle dependency", "ignored.log": "needle ignored", ".pearlignore": "*.log\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var files []string
	for offset := 0; ; {
		page, err := ListFiles(context.Background(), root, offset, 1)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, page.Files...)
		if !page.Truncated {
			break
		}
		offset = page.NextOffset
	}
	if !reflect.DeepEqual(files, []string{".pearlignore", "a.txt", "b.txt"}) {
		t.Fatalf("files=%v", files)
	}
	page, err := SearchFiles(context.Background(), root, "needle", 0, 2, 4096)
	if err != nil || len(page.Matches) != 2 || !page.Truncated || page.NextOffset != 2 {
		t.Fatalf("search=%+v err=%v", page, err)
	}
	next, err := SearchFiles(context.Background(), root, "needle", page.NextOffset, 2, 4096)
	if err != nil || len(next.Matches) != 1 || next.Matches[0].Path != "b.txt" || next.Truncated {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}
