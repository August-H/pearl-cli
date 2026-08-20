package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var errDirectoryPickerCancelled = errors.New("directory selection cancelled")

type directoryPickerRunner func(name string, args ...string) ([]byte, error)

func chooseJobDirectory(initialDirectory string) (string, error) {
	return chooseJobDirectoryForOS(
		runtime.GOOS,
		initialDirectory,
		func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
	)
}

func chooseJobDirectoryForOS(
	goos string,
	initialDirectory string,
	run directoryPickerRunner,
) (string, error) {
	initialDirectory, err := validateJobDirectory(initialDirectory)
	if err != nil {
		return "", err
	}

	var output []byte
	switch goos {
	case "darwin":
		const script = `on run argv
set startFolder to POSIX file (item 1 of argv)
set selectedFolder to choose folder with prompt "Choose a directory for this Pearl job" default location startFolder
return POSIX path of selectedFolder
end run`
		output, err = run("osascript", "-e", script, initialDirectory)
	case "linux":
		output, err = run(
			"zenity", "--file-selection", "--directory",
			"--filename", initialDirectory+string(os.PathSeparator),
			"--title", "Choose a directory for this Pearl job",
		)
		if errors.Is(err, exec.ErrNotFound) {
			output, err = run(
				"kdialog", "--getexistingdirectory", initialDirectory,
				"--title", "Choose a directory for this Pearl job",
			)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("directory picker requires zenity or kdialog on Linux")
		}
	case "windows":
		const script = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Choose a directory for this Pearl job'
$dialog.SelectedPath = $args[0]
$dialog.ShowNewFolderButton = $true
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($dialog.SelectedPath)
    exit 0
}
exit 2`
		arguments := []string{
			"-NoProfile", "-NonInteractive", "-STA", "-Command", script, initialDirectory,
		}
		output, err = run("powershell.exe", arguments...)
		if errors.Is(err, exec.ErrNotFound) {
			output, err = run("pwsh.exe", arguments...)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("directory picker requires Windows PowerShell")
		}
	default:
		return "", fmt.Errorf("directory picker is not supported on %s", goos)
	}

	selected := strings.TrimSpace(string(output))
	if err != nil {
		if selected == "" || strings.Contains(strings.ToLower(selected), "user canceled") {
			return "", errDirectoryPickerCancelled
		}
		return "", fmt.Errorf("open directory picker: %w: %s", err, selected)
	}
	if selected == "" {
		return "", errDirectoryPickerCancelled
	}
	return validateJobDirectory(selected)
}

func validateJobDirectory(directory string) (string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return "", errors.New("job directory cannot be empty")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve job directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect job directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("job directory %q is not a directory", absolute)
	}
	return absolute, nil
}

func displayJobDirectory(directory string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return directory
	}
	relative, err := filepath.Rel(filepath.Clean(home), filepath.Clean(directory))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return directory
	}
	if relative == "." {
		return "~"
	}
	return filepath.Join("~", relative)
}
