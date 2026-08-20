package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/August-H/pearl-cli/internal/store"
)

func TestChooseJobDirectoryUsesNativeMacPicker(t *testing.T) {
	initial := t.TempDir()
	selected := t.TempDir()
	called := false
	got, err := chooseJobDirectoryForOS(
		"darwin",
		initial,
		func(name string, args ...string) ([]byte, error) {
			called = true
			if name != "osascript" || len(args) < 3 || args[len(args)-1] != initial {
				t.Fatalf("picker command = %q %#v", name, args)
			}
			return []byte(selected + "\n"), nil
		},
	)
	if err != nil || !called || got != selected {
		t.Fatalf("selected directory = %q, called=%v, err=%v", got, called, err)
	}
}

func TestChooseJobDirectoryFallsBackToKDialog(t *testing.T) {
	initial := t.TempDir()
	selected := t.TempDir()
	got, err := chooseJobDirectoryForOS(
		"linux",
		initial,
		func(name string, _ ...string) ([]byte, error) {
			if name == "zenity" {
				return nil, exec.ErrNotFound
			}
			if name != "kdialog" {
				t.Fatalf("unexpected picker %q", name)
			}
			return []byte(selected), nil
		},
	)
	if err != nil || got != selected {
		t.Fatalf("selected directory = %q, err=%v", got, err)
	}
}

func TestChooseJobDirectoryUsesWindowsFolderBrowser(t *testing.T) {
	initial := t.TempDir()
	selected := t.TempDir()
	got, err := chooseJobDirectoryForOS(
		"windows",
		initial,
		func(name string, args ...string) ([]byte, error) {
			if name != "powershell.exe" {
				t.Fatalf("picker executable = %q", name)
			}
			arguments := strings.Join(args, "\x00")
			if !strings.Contains(arguments, "System.Windows.Forms.FolderBrowserDialog") ||
				!strings.Contains(arguments, "\x00-STA\x00") || args[len(args)-1] != initial {
				t.Fatalf("PowerShell arguments = %#v", args)
			}
			return []byte(selected), nil
		},
	)
	if err != nil || got != selected {
		t.Fatalf("selected directory = %q, err=%v", got, err)
	}
}

func TestChooseJobDirectoryFallsBackToPowerShellSeven(t *testing.T) {
	initial := t.TempDir()
	selected := t.TempDir()
	got, err := chooseJobDirectoryForOS(
		"windows",
		initial,
		func(name string, _ ...string) ([]byte, error) {
			if name == "powershell.exe" {
				return nil, exec.ErrNotFound
			}
			if name != "pwsh.exe" {
				t.Fatalf("unexpected picker %q", name)
			}
			return []byte(selected), nil
		},
	)
	if err != nil || got != selected {
		t.Fatalf("selected directory = %q, err=%v", got, err)
	}
}

func TestChooseJobDirectoryHandlesCancellationAndRejectsFiles(t *testing.T) {
	initial := t.TempDir()
	_, err := chooseJobDirectoryForOS(
		"darwin",
		initial,
		func(string, ...string) ([]byte, error) {
			return []byte("execution error: User canceled. (-128)"), errors.New("exit status 1")
		},
	)
	if !errors.Is(err, errDirectoryPickerCancelled) {
		t.Fatalf("cancel error = %v", err)
	}

	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateJobDirectory(file); err == nil {
		t.Fatal("file was accepted as a job directory")
	}
}

func TestJobDirectoryPickerAssignsWorkspace(t *testing.T) {
	client := startTestDaemon(t, answerRunner{})
	selected := t.TempDir()
	pickerCalled := false
	output, exitCode := captureTestStdout(t, func() int {
		return createJobWithDirectoryPicker(
			[]string{"--directory", "-n", "docs", "update", "the", "docs"},
			func(initial string) (string, error) {
				pickerCalled = true
				if _, err := validateJobDirectory(initial); err != nil {
					t.Fatalf("initial directory = %q, err=%v", initial, err)
				}
				return selected, nil
			},
		)
	})
	if exitCode != 0 || !pickerCalled || !strings.Contains(output, "Directory:") {
		t.Fatalf("job exit=%d pickerCalled=%v output=%q", exitCode, pickerCalled, output)
	}
	job, err := client.job(context.Background(), "docs")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.JobPending || job.WorkspaceRoot != selected {
		t.Fatalf("created job = %#v", job)
	}
}
