package agent_functions

import "context"

// View_fileTree returns the first page for callers of the legacy API.
// ListFiles exposes the continuation offset for larger projects.
func View_fileTree(path string) ([]string, error) {
	page, err := ListFiles(context.Background(), path, 0, 1000)
	return page.Files, err
}
