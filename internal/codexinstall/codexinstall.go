// Package codexinstall installs Pearl's own copy of the Codex CLI, so the
// Codex harness works without a separate install.
package codexinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
)

// Version is the Codex release Pearl installs and is tested against.
const Version = "0.159.0"

const downloadBaseEnvironment = "PEARL_CODEX_DOWNLOAD_BASE"

var downloadBase = "https://github.com/openai/codex/releases/download/rust-v" + Version

type archive struct {
	target string
	sha256 string
}

// archives maps GOOS/GOARCH to the matching codex-package release asset and
// the SHA-256 GitHub reports for it.
var archives = map[string]archive{
	"darwin/arm64":  {"aarch64-apple-darwin", "7748d07d7921a67b91d015e9d0f43177c675977e67f955b2f8fb96dc880dd5d7"},
	"darwin/amd64":  {"x86_64-apple-darwin", "ab64d30288454124c7c15abd81672a9dfda7e3e723d76c4cfa6c0aba4b439017"},
	"linux/arm64":   {"aarch64-unknown-linux-musl", "7399d2bf7618cc8d6845a8acdb2078106332861f7f30d36527e8f53208df89da"},
	"linux/amd64":   {"x86_64-unknown-linux-musl", "35da65d7e8644e28ea0a4d4e3d8c15b40c6b492356d4cf21986c7e341f83a24e"},
	"windows/arm64": {"aarch64-pc-windows-msvc", "035357e157dfd72a9ef1d57bccbe88497ca62e91557517dc462c2d505616d36c"},
	"windows/amd64": {"x86_64-pc-windows-msvc", "7bf0e9eca0b3b736df349964a1fef2267883f39f124ff467a23c80d227183d13"},
}

// skippedPrefixes are parts of the package that `codex exec` never uses.
var skippedPrefixes = []string{"codex-resources/voice/"}

// Directory is where Pearl keeps its copies of Codex, one folder per version.
func Directory() (string, error) {
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.Directory, "codex"), nil
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "codex.exe"
	}
	return "codex"
}

// Path returns Pearl's installed copy of Codex, or "" when there is none.
func Path() string {
	directory, err := Directory()
	if err != nil {
		return ""
	}
	path := filepath.Join(directory, Version, "bin", executableName())
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

// Managed reports whether Pearl has ever installed a copy of Codex, of any
// version. Updates use it to keep that copy current.
func Managed() bool {
	directory, err := Directory()
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			return true
		}
	}
	return false
}

// Progress receives the bytes downloaded so far and the total, or -1 when the
// size is unknown.
type Progress func(done, total int64)

// Install downloads Codex Version, verifies its checksum, and unpacks it. It
// returns the path of the codex executable. An existing copy is kept unless
// force is set.
func Install(ctx context.Context, force bool, progress Progress) (string, error) {
	if path := Path(); path != "" && !force {
		return path, nil
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	release, ok := archives[platform]
	if !ok {
		return "", fmt.Errorf("Pearl has no Codex build for %s; install Codex yourself (npm install -g @openai/codex)", platform)
	}
	directory, err := Directory()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create Codex folder: %w", err)
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(downloadBaseEnvironment)), "/")
	if base == "" {
		base = downloadBase
	}
	archivePath, err := download(ctx, base+"/codex-package-"+release.target+".tar.gz", directory, release.sha256, progress)
	if err != nil {
		return "", err
	}
	defer os.Remove(archivePath)

	staging, err := os.MkdirTemp(directory, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := extract(archivePath, staging); err != nil {
		return "", fmt.Errorf("unpack Codex: %w", err)
	}
	if info, err := os.Stat(filepath.Join(staging, "bin", executableName())); err != nil || info.IsDir() {
		return "", errors.New("the Codex download has no bin/" + executableName())
	}
	final := filepath.Join(directory, Version)
	if err := os.RemoveAll(final); err != nil {
		return "", fmt.Errorf("replace the old Codex copy: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		return "", fmt.Errorf("install Codex: %w", err)
	}
	removeOtherVersions(directory)
	return filepath.Join(final, "bin", executableName()), nil
}

func download(ctx context.Context, url, directory, expected string, progress Progress) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("download Codex: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("download Codex from %s: %s", url, response.Status)
	}
	file, err := os.CreateTemp(directory, ".download-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(file.Name())
		}
	}()
	hash := sha256.New()
	reader := io.Reader(response.Body)
	if progress != nil {
		reader = &progressReader{reader: reader, total: response.ContentLength, report: progress}
	}
	if _, err := io.Copy(io.MultiWriter(file, hash), reader); err != nil {
		return "", fmt.Errorf("download Codex: %w", err)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return "", fmt.Errorf("Codex download checksum mismatch; refusing to install (expected %s, got %s)", expected, actual)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return file.Name(), nil
}

type progressReader struct {
	reader io.Reader
	done   int64
	total  int64
	report Progress
}

func (r *progressReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.done += int64(count)
	r.report(r.done, r.total)
	return count, err
}

func extract(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()
	entries := tar.NewReader(compressed)
	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(header.Name, "./")))
		if name == "." || skipped(name) {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe path %q in archive", header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, entries)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func skipped(name string) bool {
	for _, prefix := range skippedPrefixes {
		if strings.HasPrefix(name+"/", prefix) {
			return true
		}
	}
	return false
}

func removeOtherVersions(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != Version && !strings.HasPrefix(entry.Name(), ".") {
			_ = os.RemoveAll(filepath.Join(directory, entry.Name()))
		}
	}
}
