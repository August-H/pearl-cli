package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/fynelabs/selfupdate"
)

func TestUpdatePreservesApplyError(t *testing.T) {
	asset := "pearl-" + runtime.GOOS + "-" + runtime.GOARCH + exeSuffix()
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v99.0.0","assets":[{"name":%q,"browser_download_url":%q},{"name":%q,"browser_download_url":%q}]}`, asset, endpoint+"/binary", asset+".sha256", endpoint+"/checksum")
		case "/checksum":
			fmt.Fprint(w, strings.Repeat("0", 64))
		default:
			fmt.Fprint(w, "synthetic binary")
		}
	}))
	defer server.Close()
	endpoint = server.URL
	restoreUpdateEndpoints(t, endpoint+"/latest", server.Client())
	previousApply := applyUpdate
	defer func() { applyUpdate = previousApply }()
	applyUpdate = func(io.Reader, selfupdate.Options) error { return errors.New("synthetic checksum mismatch") }
	_, stderr, code := captureUpdateOutput(t, func() int { return installLatestRelease(true) })
	if code != 1 || !strings.Contains(stderr, "synthetic checksum mismatch") {
		t.Fatalf("update failure loses original error: code=%d stderr=%q", code, stderr)
	}
}
