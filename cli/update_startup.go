package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"golang.org/x/term"
)

const (
	startupUpdateMaxAge  = 20 * time.Hour
	startupUpdateTimeout = 2 * time.Second
)

type savedUpdateCheck struct {
	Release          updateRelease `json:"release"`
	LastCheckedAt    time.Time     `json:"last_checked_at"`
	DismissedVersion string        `json:"dismissed_version,omitempty"`
}

type updateSelection int

const (
	updateNow updateSelection = iota
	updateLater
	updateSkipVersion
)

func runDashboardWithUpdate(args []string) int {
	if len(args) == 0 {
		if handled, code := runStartupUpdate(); handled {
			return code
		}
	}
	return runDashboard(args)
}

func runStartupUpdate() (bool, int) {
	if os.Getenv("PEARL_NO_UPDATE_CHECK") != "" || !hasComparableVersion(version) ||
		!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false, 0
	}
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return false, 0
	}
	path := filepath.Join(paths.Directory, "updates.json")
	ctx, cancel := context.WithTimeout(context.Background(), startupUpdateTimeout)
	release := startupUpdateRelease(ctx, path, version, time.Now())
	cancel()
	if release == nil {
		return false, 0
	}
	selection, err := runUpdatePrompt(os.Stdin, os.Stdout, version, release.TagName)
	if err != nil {
		return false, 0
	}
	switch selection {
	case updateNow:
		code := installLatestRelease(false)
		if code == 0 {
			fmt.Println("Run \"pearl\" again to use the updated version.")
		}
		return true, code
	case updateSkipVersion:
		_ = dismissStartupUpdate(path, release.TagName)
	}
	return false, 0
}

func startupUpdateRelease(ctx context.Context, path, current string, now time.Time) *updateRelease {
	if !hasComparableVersion(current) {
		return nil
	}
	saved := readUpdateCheck(path)
	if saved.LastCheckedAt.IsZero() || now.Before(saved.LastCheckedAt) ||
		now.Sub(saved.LastCheckedAt) >= startupUpdateMaxAge {
		if release, err := fetchLatestRelease(ctx); err == nil {
			if !startupReleaseInstallable(release) {
				return nil
			}
			saved.Release = *release
			saved.LastCheckedAt = now
			_ = saveUpdateCheck(path, saved)
		}
	}
	release := &saved.Release
	if !startupReleaseInstallable(release) ||
		compareVersions(release.TagName, current) <= 0 ||
		normalizeVersion(saved.DismissedVersion) == normalizeVersion(release.TagName) {
		return nil
	}
	return release
}

func startupReleaseInstallable(release *updateRelease) bool {
	if !hasComparableVersion(release.TagName) || release.Draft || release.Prerelease {
		return false
	}
	name := "pearl-" + runtime.GOOS + "-" + runtime.GOARCH + exeSuffix()
	binary, checksumURL := selectReleaseAssets(release, name)
	return binary != nil && binary.URL != "" && checksumURL != ""
}

func readUpdateCheck(path string) savedUpdateCheck {
	var saved savedUpdateCheck
	content, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(content, &saved); err != nil {
			return savedUpdateCheck{}
		}
	}
	return saved
}

func saveUpdateCheck(path string, saved savedUpdateCheck) error {
	content, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "updates-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(append(content, '\n'))
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func dismissStartupUpdate(path, latest string) error {
	saved := readUpdateCheck(path)
	saved.DismissedVersion = normalizeVersion(latest)
	return saveUpdateCheck(path, saved)
}

func runUpdatePrompt(input, output *os.File, current, latest string) (updateSelection, error) {
	terminalState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return updateLater, err
	}
	defer term.Restore(int(input.Fd()), terminalState)
	fmt.Fprint(output, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h\x1b[?1049l")
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return updatePromptLoop(input, output, current, latest, color)
}

func updatePromptLoop(input io.Reader, output io.Writer, current, latest string, color bool) (updateSelection, error) {
	selected := updateNow
	reader := bufio.NewReader(input)
	escapeSequence := ""
	for {
		fmt.Fprint(output, "\x1b[H", listTerminalFrame(renderUpdatePrompt(current, latest, selected, color)))
		character, err := readTerminalKey(reader)
		if err != nil {
			return updateLater, err
		}
		if escapeSequence != "" {
			escapeSequence += string(character)
			action, complete := jobViewEscapeAction(escapeSequence)
			if !complete {
				continue
			}
			escapeSequence = ""
			switch action {
			case "previous":
				selected = (selected + 2) % 3
			case "next":
				selected = (selected + 1) % 3
			}
			continue
		}
		switch character {
		case '\x1b':
			escapeSequence = "\x1b"
		case escapeKey, 3, 4:
			return updateLater, nil
		case '\r', '\n':
			return selected, nil
		case '1', '2', '3':
			return updateSelection(character - '1'), nil
		case 'k':
			selected = (selected + 2) % 3
		case 'j', '\t':
			selected = (selected + 1) % 3
		}
	}
}

func renderUpdatePrompt(current, latest string, selected updateSelection, color bool) string {
	var frame strings.Builder
	fmt.Fprintf(&frame, "\n  Update available · %s → %s\n\n", normalizeVersion(current), normalizeVersion(latest))
	fmt.Fprintf(&frame, "  Release notes: https://github.com/%s/releases/latest\n\n", updateRepository)
	options := []string{"Update now (runs `pearl update`)", "Install later", "Skip this version"}
	for index, option := range options {
		prefix := "  "
		if updateSelection(index) == selected {
			prefix = "> "
		}
		line := fmt.Sprintf("  %s%d. %s", prefix, index+1, option)
		if color && updateSelection(index) == selected {
			line = ansiBold + ansiCyan + line + ansiReset
		}
		frame.WriteString(line + "\n")
	}
	frame.WriteString("\n  ↑/↓ choose · Enter continue · Esc install later\n")
	return frame.String()
}
