package openrouter_request

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/August-H/pearl-cli/agent_functions"
	"strings"
)

const agentPathDescription = "Path relative to the workspace (use . for its root), or an absolute path outside it that request_outside_access granted"

func workspaceToolDefinitions() []map[string]any {
	path := map[string]any{"type": "string", "description": agentPathDescription}
	text := map[string]any{"type": "string"}
	paging := func() map[string]any {
		return map[string]any{
			"relative_path": path, "offset": map[string]any{"type": "integer", "minimum": 0},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
		}
	}
	search := paging()
	search["query"] = text
	return []map[string]any{
		newAgentTool("list_files", "List a page of project paths, excluding dependencies, build output, protected files, links and ignore patterns. Use next_offset to continue.", paging(), "relative_path"),
		newAgentTool("search_files", "Search project text for a literal case-sensitive query. Returns paths, line numbers and snippets; use next_offset to continue.", search, "relative_path", "query"),
		newAgentTool("create_directory", "Create a project directory and missing parents.", map[string]any{"relative_path": path}, "relative_path"),
		newAgentTool("apply_patch", "Replace exactly one occurrence of old_text in a file. Read the file first; ambiguous or stale text is rejected.", map[string]any{"relative_path": path, "old_text": text, "new_text": text}, "relative_path", "old_text", "new_text"),
		newAgentTool("run_command", "Run an executable from allowed_commands in Pearl settings, without a shell. It runs in a sandbox that can write only to the workspace, temp folders, and granted outside paths, and has no network unless the user allows it. Returns bounded combined output and exit code. Maximum timeout is 120 seconds.", map[string]any{
			"command": text, "args": map[string]any{"type": "array", "items": text}, "relative_path": path,
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 120},
		}, "command", "args"),
	}
}

// runFileTool runs a file tool. Paths are relative to the workspace, or
// absolute paths covered by an outside access grant.
func runFileTool(ctx context.Context, call agentToolCall, workspaceRoot string, maxBytes int64, access *outsideAccess) agentToolResult {
	var input struct {
		RelativePath string `json:"relative_path"`
		Content      string `json:"content"`
		Offset       int    `json:"offset"`
		Limit        int    `json:"limit"`
		Query        string `json:"query"`
		OldText      string `json:"old_text"`
		NewText      string `json:"new_text"`
	}
	fail := func(err error) agentToolResult { return agentToolResult{Error: err.Error()} }
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return fail(fmt.Errorf("decode arguments: %w", err))
	}
	write := false
	switch call.Function.Name {
	case "view_file_tree", "list_files", "search_files", "read_file_contents":
	case "create_file", "write_to_file", "create_directory", "apply_patch":
		write = true
	default:
		return fail(fmt.Errorf("tool %q is not allowed", call.Function.Name))
	}
	root, name, err := openAgentPath(access, workspaceRoot, input.RelativePath, write)
	if err != nil {
		return fail(err)
	}
	defer root.Close()

	var result any
	switch call.Function.Name {
	case "view_file_tree", "list_files":
		result, err = agent_functions.ListFilesIn(ctx, root, name, input.Offset, input.Limit)
	case "search_files":
		result, err = agent_functions.SearchFilesIn(ctx, root, name, input.Query, input.Offset, input.Limit, maxBytes)
	case "read_file_contents":
		result, err = agent_functions.ReadFileIn(root, name, maxBytes)
	case "create_file":
		var existed bool
		existed, err = agent_functions.CreateFileIn(root, name)
		result = "Successfully created file at: " + input.RelativePath
		if existed {
			result = "File already exists at: " + input.RelativePath
		}
	case "write_to_file":
		if int64(len(input.Content)) > maxBytes {
			return fail(fmt.Errorf("content is %d bytes; maximum writable size is %d", len(input.Content), maxBytes))
		}
		err = agent_functions.WriteFileIn(root, name, input.Content)
		result = "Successfully wrote file at: " + input.RelativePath
	case "create_directory":
		err = root.MkdirAll(name, 0o755)
		result = "Created directory: " + input.RelativePath
	case "apply_patch":
		var contents string
		contents, err = agent_functions.ReadFileIn(root, name, maxBytes)
		if err == nil {
			if input.OldText == "" || strings.Count(contents, input.OldText) != 1 {
				err = errors.New("old_text must match exactly once; read the file again before patching")
			} else {
				updated := strings.Replace(contents, input.OldText, input.NewText, 1)
				if int64(len(updated)) > maxBytes {
					err = errors.New("patched file exceeds the writable size limit")
				} else {
					err = agent_functions.WriteFileIn(root, name, updated)
				}
			}
		}
		result = "Patched file: " + input.RelativePath
	}
	if err != nil {
		return fail(err)
	}
	return agentToolResult{Success: true, Result: result}
}
