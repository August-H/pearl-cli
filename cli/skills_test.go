package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsCommandCreatesAndOpensTheFolder(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	var opened []string
	original := openFolder
	openFolder = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { openFolder = original })
	directory := filepath.Join(configDirectory, "skills")

	output, code := captureTestStdout(t, func() int { return Run([]string{"skills"}) })
	if code != 0 || len(opened) != 1 || opened[0] != directory {
		t.Fatalf("code = %d, opened = %q", code, opened)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("skills folder was not created: %v", err)
	}
	if !strings.Contains(output, "No skills yet") {
		t.Fatalf("output = %q", output)
	}

	if err := os.WriteFile(filepath.Join(directory, "deploy.md"), []byte("Deploy with make ship.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, code = captureTestStdout(t, func() int { return Run([]string{"skills", "list"}) })
	if code != 0 || len(opened) != 1 || !strings.Contains(output, "deploy") ||
		!strings.Contains(output, "Deploy with make ship.") {
		t.Fatalf("list code = %d, opened = %q, output = %q", code, opened, output)
	}
	output, code = captureTestStdout(t, func() int { return Run([]string{"skills", "path"}) })
	if code != 0 || strings.TrimSpace(output) != directory {
		t.Fatalf("path output = %q", output)
	}
	if code := Run([]string{"skills", "bogus"}); code != 2 {
		t.Fatalf("bad argument exit code = %d", code)
	}
}
