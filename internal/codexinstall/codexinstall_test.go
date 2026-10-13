package codexinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type archiveFile struct {
	name string
	body string
	mode int64
}

func buildArchive(t *testing.T, files []archiveFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, file := range files {
		header := &tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(file.name, "/") {
			header.Typeflag, header.Size = tar.TypeDir, 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// serveArchive makes Install download contents and expect checksum.
func serveArchive(t *testing.T, contents []byte, checksum string) *int {
	t.Helper()
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	platform := runtime.GOOS + "/" + runtime.GOARCH
	target := "test-target"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/codex-package-"+target+".tar.gz" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(contents)
	}))
	t.Cleanup(server.Close)
	t.Setenv(downloadBaseEnvironment, server.URL)
	previous, hadPrevious := archives[platform]
	archives[platform] = archive{target: target, sha256: checksum}
	t.Cleanup(func() {
		if hadPrevious {
			archives[platform] = previous
		} else {
			delete(archives, platform)
		}
	})
	return &requests
}

func checksum(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func TestInstallUnpacksTheVerifiedPackageAndSkipsVoiceFiles(t *testing.T) {
	contents := buildArchive(t, []archiveFile{
		{name: "bin/", mode: 0o755},
		{name: "bin/" + executableName(), body: "#!/bin/sh\necho codex\n", mode: 0o755},
		{name: "codex-path/rg", body: "rg", mode: 0o755},
		{name: "codex-package.json", body: "{}", mode: 0o644},
		{name: "codex-resources/voice/bin/codex-voice-host", body: "voice", mode: 0o755},
	})
	requests := serveArchive(t, contents, checksum(contents))
	directory, _ := Directory()
	if err := os.MkdirAll(filepath.Join(directory, "0.1.0", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if Path() != "" {
		t.Fatal("Path() found a copy before installing")
	}

	var reported int64
	path, err := Install(context.Background(), false, func(done, total int64) { reported = done })
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, Version, "bin", executableName()) || Path() != path {
		t.Fatalf("installed path = %q, Path() = %q", path, Path())
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatalf("codex binary is missing or not executable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, Version, "codex-path", "rg")); err != nil {
		t.Fatalf("helper files were not unpacked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, Version, "codex-resources", "voice")); !os.IsNotExist(err) {
		t.Fatalf("voice files were unpacked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "0.1.0")); !os.IsNotExist(err) {
		t.Fatal("the old Codex version was not removed")
	}
	if reported != int64(len(contents)) || !Managed() {
		t.Fatalf("progress = %d of %d, Managed() = %v", reported, len(contents), Managed())
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 {
		t.Fatalf("leftover files in the Codex folder: %v", entries)
	}

	if _, err := Install(context.Background(), false, nil); err != nil || *requests != 1 {
		t.Fatalf("second install downloaded again: requests = %d, err = %v", *requests, err)
	}
	if _, err := Install(context.Background(), true, nil); err != nil || *requests != 2 {
		t.Fatalf("forced install: requests = %d, err = %v", *requests, err)
	}
}

func TestInstallRefusesABadChecksumOrUnsafePaths(t *testing.T) {
	contents := buildArchive(t, []archiveFile{{name: "bin/" + executableName(), body: "x", mode: 0o755}})
	serveArchive(t, contents, strings.Repeat("0", 64))
	if _, err := Install(context.Background(), false, nil); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("bad checksum error = %v", err)
	}
	if Path() != "" || Managed() {
		t.Fatal("a rejected download left a Codex copy behind")
	}

	unsafe := buildArchive(t, []archiveFile{
		{name: "bin/" + executableName(), body: "x", mode: 0o755},
		{name: "../escape", body: "x", mode: 0o644},
	})
	serveArchive(t, unsafe, checksum(unsafe))
	if _, err := Install(context.Background(), false, nil); err == nil ||
		!strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("unsafe archive error = %v", err)
	}
	directory, _ := Directory()
	if _, err := os.Stat(filepath.Join(filepath.Dir(directory), "escape")); !os.IsNotExist(err) {
		t.Fatal("the archive wrote outside the Codex folder")
	}

	missing := buildArchive(t, []archiveFile{{name: "README.md", body: "no binary", mode: 0o644}})
	serveArchive(t, missing, checksum(missing))
	if _, err := Install(context.Background(), false, nil); err == nil ||
		!strings.Contains(err.Error(), "has no bin/") {
		t.Fatalf("archive without codex error = %v", err)
	}
}

func TestEveryReleasePlatformHasAPinnedChecksum(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64", "windows/amd64"} {
		release, ok := archives[platform]
		if !ok || len(release.sha256) != 64 || release.target == "" {
			t.Errorf("%s has no pinned Codex archive: %+v", platform, release)
		}
	}
}
