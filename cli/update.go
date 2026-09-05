package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/fynelabs/selfupdate"
)

const updateRepository = "August-H/pearl-cli"

var updateReleaseURL = "https://api.github.com/repos/" + updateRepository + "/releases/latest"

var updateHTTPClient = &http.Client{Timeout: 60 * time.Second}

// applyUpdate is a package-level variable so tests can intercept the final
// executable swap.
var applyUpdate = selfupdate.Apply

type updateRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []updateAsset `json:"assets"`
}

type updateAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func runUpdate(args []string) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	checkOnly := flags.Bool("check", false, "report the latest release without installing it")
	force := flags.Bool("force", false, "install even when not newer or built from source")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: pearl update [--check] [--force]")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(flags.Args()) > 0 {
		flags.Usage()
		return 2
	}
	if *checkOnly {
		return reportLatestRelease()
	}
	return installLatestRelease(*force)
}

func reportLatestRelease() int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return printError("Update", err)
	}
	current := normalizeVersion(version)
	fmt.Println("Latest release:", release.TagName)
	if !hasComparableVersion(version) {
		fmt.Println("This is a development build; it has no version to compare against.")
		return 0
	}
	switch compareVersions(release.TagName, current) {
	case 1:
		fmt.Println("Run \"pearl update\" to install it.")
	case 0:
		fmt.Println("Pearl is up to date.")
	default:
		fmt.Println("This build is newer than the latest release.")
	}
	return 0
}

func installLatestRelease(force bool) int {
	if !force && !hasComparableVersion(version) {
		fmt.Fprintln(os.Stderr,
			"This build has no comparable version; Pearl refuses to replace it blindly. Use --force to install anyway.")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return printError("Update", err)
	}
	if !force && compareVersions(release.TagName, version) <= 0 {
		fmt.Printf("Pearl %s is up to date; latest release is %s.\n", version, release.TagName)
		return 0
	}
	executableName := "pearl-" + runtime.GOOS + "-" + runtime.GOARCH + exeSuffix()
	binary, checksumURL := selectReleaseAssets(release, executableName)
	if binary == nil {
		return printError("Update", fmt.Errorf(
			"release %s has no asset named %s; install manually from https://github.com/%s/releases",
			release.TagName, executableName, updateRepository))
	}
	if checksumURL == "" {
		return printError("Update", fmt.Errorf(
			"release %s has no %s.sha256 checksum to verify against", release.TagName, executableName))
	}
	expectedChecksum, err := downloadExpectedChecksum(ctx, checksumURL)
	if err != nil {
		return printError("Update", err)
	}
	fmt.Printf("Downloading pearl %s for %s/%s...\n", release.TagName, runtime.GOOS, runtime.GOARCH)
	body, length, err := downloadBinary(ctx, binary.URL)
	if err != nil {
		return printError("Update", err)
	}
	defer body.Close()
	oldPath, err := oldExecutablePath()
	if err != nil {
		return printError("Update", err)
	}
	if err := applyUpdate(body, selfupdate.Options{
		Checksum:    expectedChecksum,
		OldSavePath: oldPath,
	}); err != nil {
		if rollbackErr := selfupdate.RollbackError(err); rollbackErr != nil {
			err = fmt.Errorf("%w; rollback also failed: %v", err, rollbackErr)
		}
		return printError("Update", err)
	}
	if length >= 0 {
		fmt.Printf("Updated to %s (%.1f MiB); previous binary saved as %s\n",
			release.TagName, float64(length)/(1024*1024), filepath.Base(oldPath))
	} else {
		fmt.Printf("Updated to %s; previous binary saved as %s\n",
			release.TagName, filepath.Base(oldPath))
	}
	return restartDaemonAfterUpdate()
}

// restartDaemonAfterUpdate swaps the running daemon onto the new binary only
// when it is idle, so an in-flight job is never interrupted by an update.
func restartDaemonAfterUpdate() int {
	client, err := newDaemonClient()
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	status, err := client.status(ctx)
	cancel()
	if err != nil {
		return 0
	}
	if current, _ := status["current_job_id"].(string); current != "" {
		fmt.Printf("Job %s is still running on the old version; run \"pearl daemon restart\" after it finishes.\n", current)
		return 0
	}
	return restartDaemon()
}

func fetchLatestRelease(ctx context.Context) (*updateRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, updateReleaseURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach github.com: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no published releases found at https://github.com/%s/releases", updateRepository)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("github releases returned %s", response.Status)
	}
	var release updateRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release information: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, errors.New("latest release has no tag name")
	}
	return &release, nil
}

// selectReleaseAssets picks the platform binary and its .sha256 companion from
// the release asset list.
func selectReleaseAssets(release *updateRelease, executableName string) (*updateAsset, string) {
	checksumName := executableName + ".sha256"
	var binary *updateAsset
	var checksumURL string
	for index := range release.Assets {
		asset := &release.Assets[index]
		switch asset.Name {
		case executableName:
			binary = asset
		case checksumName:
			checksumURL = asset.URL
		}
	}
	return binary, checksumURL
}

func downloadExpectedChecksum(ctx context.Context, url string) ([]byte, error) {
	if url == "" {
		return nil, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download checksum: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download checksum: returned %s", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("read checksum: %w", err)
	}
	checksum, err := parseSHA256File(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse checksum: %w", err)
	}
	return checksum, nil
}

func downloadBinary(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("download release: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, 0, fmt.Errorf("download release: returned %s", response.Status)
	}
	return response.Body, response.ContentLength, nil
}

func oldExecutablePath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate current executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	return resolved + ".old", nil
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// parseSHA256File reads the "<checksum>  <filename>" format written by
// sha256sum as well as a bare hex digest.
func parseSHA256File(content string) ([]byte, error) {
	field := strings.Fields(content)
	digest := content
	if len(field) > 0 {
		digest = field[0]
	}
	return hex.DecodeString(strings.ToLower(digest))
}

// normalizeVersion strips a leading v and returns "dev" unchanged.
func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// hasComparableVersion reports whether the version is a dotted numeric
// release such as 0.2.1; development builds are not.
func hasComparableVersion(value string) bool {
	parts := splitVersion(normalizeVersion(value))
	return len(parts) > 0
}

// compareVersions orders dotted numeric versions such as v0.12.1. Missing
// components count as zero, so 0.2 equals 0.2.0.
func compareVersions(left, right string) int {
	leftParts := splitVersion(normalizeVersion(left))
	rightParts := splitVersion(normalizeVersion(right))
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		var leftValue, rightValue int
		if index < len(leftParts) {
			leftValue = leftParts[index]
		}
		if index < len(rightParts) {
			rightValue = rightParts[index]
		}
		if leftValue != rightValue {
			if leftValue > rightValue {
				return 1
			}
			return -1
		}
	}
	return 0
}

func splitVersion(value string) []int {
	chunks := strings.Split(value, ".")
	parts := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		number, err := strconv.Atoi(strings.TrimSpace(chunk))
		if err != nil {
			return nil
		}
		parts = append(parts, number)
	}
	return parts
}
