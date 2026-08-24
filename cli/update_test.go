package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fynelabs/selfupdate"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left     string
		right    string
		expected int
	}{
		{"v0.2.0", "0.1.0", 1},
		{"0.2.0", "v0.10.0", -1},
		{"v1.0.0", "1.0.0", 0},
		{"0.2", "0.2.0", 0},
		{"0.2.1", "0.2", 1},
	}
	for _, test := range tests {
		got := compareVersions(test.left, test.right)
		if got != test.expected {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d",
				test.left, test.right, got, test.expected)
		}
	}
}

func TestParseSHA256File(t *testing.T) {
	sum := sha256.Sum256([]byte("pearl"))
	digest := hex.EncodeToString(sum[:])

	for _, content := range []string{
		digest + "  pearl-darwin-arm64\n",
		digest + " pearl-darwin-arm64",
		digest + "\n",
		strings.ToUpper(digest),
	} {
		got, err := parseSHA256File(content)
		if err != nil {
			t.Fatalf("parseSHA256File(%q) returned %v", content, err)
		}
		if hex.EncodeToString(got) != digest {
			t.Fatalf("parseSHA256File(%q) = %x, want %s", content, got, digest)
		}
	}
	if _, err := parseSHA256File("not a checksum"); err == nil {
		t.Fatal("parseSHA256File accepted invalid hex")
	}
}

func TestSelectReleaseAssets(t *testing.T) {
	name := "pearl-" + runtime.GOOS + "-" + runtime.GOARCH + exeSuffix()
	release := &updateRelease{
		TagName: "v0.2.0",
		Assets: []updateAsset{
			{Name: name, URL: "https://example.com/" + name},
			{Name: name + ".sha256", URL: "https://example.com/" + name + ".sha256"},
			{Name: "pearl-other-platform", URL: "https://example.com/other"},
		},
	}
	binary, checksumURL := selectReleaseAssets(release, name)
	if binary == nil || binary.URL != "https://example.com/"+name {
		t.Fatalf("selectReleaseAssets lost the platform binary: %+v", binary)
	}
	if checksumURL != "https://example.com/"+name+".sha256" {
		t.Fatalf("checksum companion = %q", checksumURL)
	}

	binary, checksumURL = selectReleaseAssets(release, name+"-missing")
	if binary != nil || checksumURL != "" {
		t.Fatal("selectReleaseAssets matched assets for an unknown platform")
	}
}

func TestUpdateCommandRejectsUnknownArguments(t *testing.T) {
	output, exitCode := captureTestStderr(t, func() int {
		return Run([]string{"update", "extra"})
	})
	if exitCode != 2 || !strings.Contains(output, "Usage: pearl update") {
		t.Fatalf("update extra exit=%d output=%q", exitCode, output)
	}
}

func TestInstallRefusesUncomparableVersionsWithoutForce(t *testing.T) {
	previousVersion := version
	version = "6939fdf-dirty"
	defer func() { version = previousVersion }()

	stdout, stderr, code := captureUpdateOutput(t, func() int {
		return installLatestRelease(false)
	})
	if code != 1 || stdout != "" ||
		!strings.Contains(stderr, "--force") {
		t.Fatalf("uncomparable version code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestHasComparableVersion(t *testing.T) {
	for value, expected := range map[string]bool{
		"0.1.0": true, "v0.2": true, "1.0": true,
		"dev": false, "6939fdf-dirty": false, "": false,
	} {
		if got := hasComparableVersion(value); got != expected {
			t.Fatalf("hasComparableVersion(%q) = %v, want %v", value, got, expected)
		}
	}
}

func TestUpdateCheckReportsLatestRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[]}`)
	}))
	defer server.Close()
	restoreUpdateEndpoints(t, server.URL+"/latest", server.Client())

	stdout, _, code := captureUpdateOutput(t, func() int {
		return Run([]string{"update", "--check"})
	})
	if code != 0 || !strings.Contains(stdout, "Latest release: v9.9.9") {
		t.Fatalf("update --check code=%d stdout=%q", code, stdout)
	}
}

func TestUpdateCheckExplainsMissingReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	restoreUpdateEndpoints(t, server.URL, server.Client())

	stdout, stderr, code := captureUpdateOutput(t, func() int {
		return Run([]string{"update", "--check"})
	})
	if code != 1 || stdout != "" ||
		!strings.Contains(stderr, "no published releases found") {
		t.Fatalf("update --check with no releases code=%d stdout=%q stderr=%q",
			code, stdout, stderr)
	}
}

func TestInstallLatestReleaseDownloadsVerifiesAndApplies(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	newBinary := []byte("fake new pearl binary")
	sum := sha256.Sum256(newBinary)
	executableName := "pearl-" + runtime.GOOS + "-" + runtime.GOARCH + exeSuffix()

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "latest":
			fmt.Fprintf(w,
				`{"tag_name":"v99.0.0","assets":[`+
					`{"name":%[1]q,"browser_download_url":"%[2]s/binaries/%[1]s"},`+
					`{"name":%[3]q,"browser_download_url":"%[2]s/binaries/%[3]s"}]}`,
				executableName, serverURL, executableName+".sha256")
		default:
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				fmt.Fprint(w, hex.EncodeToString(sum[:])+"  "+executableName+"\n")
				return
			}
			w.Write(newBinary)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	restoreUpdateEndpoints(t, server.URL+"/latest", server.Client())

	var appliedBody []byte
	var appliedOptions selfupdate.Options
	previousApply := applyUpdate
	applyUpdate = func(update io.Reader, opts selfupdate.Options) error {
		content, err := io.ReadAll(update)
		if err != nil {
			return err
		}
		appliedBody = content
		appliedOptions = opts
		return nil
	}
	defer func() { applyUpdate = previousApply }()

	previousVersion := version
	version = "0.0.1"
	defer func() { version = previousVersion }()

	stdout, stderr, code := captureUpdateOutput(t, func() int {
		return installLatestRelease(false)
	})
	if code != 0 {
		t.Fatalf("installLatestRelease exited %d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Updated to v99.0.0") {
		t.Fatalf("stdout missing success line: %q", stdout)
	}
	if string(appliedBody) != string(newBinary) {
		t.Fatalf("applied body = %q, want the downloaded binary", appliedBody)
	}
	if string(appliedOptions.Checksum) != string(sum[:]) {
		t.Fatalf("applied checksum = %x, want %x", appliedOptions.Checksum, sum[:])
	}
	if appliedOptions.OldSavePath == "" {
		t.Fatal("applied options did not keep the old executable path")
	}
}

func TestOldExecutablePathKeepsRollbackCopyNextToBinary(t *testing.T) {
	oldPath, err := oldExecutablePath()
	if err != nil {
		t.Skipf("cannot locate the test executable: %v", err)
	}
	if filepath.Base(oldPath) != filepath.Base(os.Args[0])+".old" &&
		!strings.HasSuffix(oldPath, ".old") {
		t.Fatalf("oldExecutablePath = %q, want a .old sibling of the binary", oldPath)
	}
}

// restoreUpdateEndpoints points release discovery at a test server.
func restoreUpdateEndpoints(t *testing.T, url string, client *http.Client) {
	t.Helper()
	previousURL, previousClient := updateReleaseURL, updateHTTPClient
	updateReleaseURL, updateHTTPClient = url, client
	t.Cleanup(func() {
		updateReleaseURL, updateHTTPClient = previousURL, previousClient
	})
}

// captureUpdateOutput captures stdout and stderr separately.
func captureUpdateOutput(t *testing.T, run func() int) (string, string, int) {
	t.Helper()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	code := run()
	os.Stdout, os.Stderr = originalStdout, originalStderr
	stdoutWriter.Close()
	stderrWriter.Close()
	stdoutContent, _ := io.ReadAll(stdoutReader)
	stderrContent, _ := io.ReadAll(stderrReader)
	return string(stdoutContent), string(stderrContent), code
}
