package openrouter_request

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestCommandEnvironmentKeepsOnlyAllowedNames(t *testing.T) {
	kept := commandEnvironment([]string{
		"PATH=/bin",
		"HOME=/home/me",
		"LC_ALL=C",
		"GOCACHE=/cache",
		"Path=C:\\Windows",
		"AWS_ACCESS_KEY_ID=hidden",
		"DATABASE_URL=postgres://user:hidden@db/app",
		"PGPASSFILE=/home/me/.pgpass",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"OPENROUTER_API_KEY=hidden",
		"MY_SETTING=visible",
		"OTHER_SETTING=hidden",
	}, []string{" my_setting "})
	want := []string{
		"PATH=/bin", "HOME=/home/me", "LC_ALL=C", "GOCACHE=/cache", "Path=C:\\Windows", "MY_SETTING=visible",
	}
	if !slices.Equal(kept, want) {
		t.Fatalf("environment = %v, want %v", kept, want)
	}
}

func TestWorkspaceCommandDoesNotSeeCredentials(t *testing.T) {
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env program")
	}
	t.Setenv("DATABASE_URL", "postgres://hidden")
	t.Setenv("PEARL_VISIBLE_SETTING", "shown")
	result, err := executeWorkspaceCommand(context.Background(), env, nil, t.TempDir(), []string{"PEARL_VISIBLE_SETTING"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "DATABASE_URL") {
		t.Fatalf("credential reached the command:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "PEARL_VISIBLE_SETTING=shown") {
		t.Fatalf("allowed variable is missing:\n%s", result.Output)
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Fatal("test environment was not set")
	}
}
