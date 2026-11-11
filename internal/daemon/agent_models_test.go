package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeModelScript(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI needs a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeModelsComeFromTheCLI(t *testing.T) {
	script := writeModelScript(t, "claude", `IFS= read -r request
printf '%s' "$request" > "$0.request"
echo '{"type":"system","subtype":"init"}'
echo '{"type":"control_response","response":{"subtype":"success","request_id":"pearl-models","response":{"models":[{"value":"default","displayName":"Default"},{"value":"opus","displayName":"Opus 5.5"},{"value":"haiku","displayName":"Haiku 5.5"}]}}}'
sleep 30
`)
	t.Setenv(claudePathEnvironment, script)
	models, err := ClaudeModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "opus" || models[1].Name != "Haiku 5.5" {
		t.Fatalf("models = %+v", models)
	}
	request, err := os.ReadFile(script + ".request")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(request), `"subtype":"initialize"`) || strings.Contains(string(request), `"type":"user"`) {
		t.Fatalf("listing models must not send a prompt: %s", request)
	}
}

func TestClaudeModelsReportsFailure(t *testing.T) {
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", "echo 'not logged in' >&2\nexit 1\n"))
	if _, err := ClaudeModels(context.Background()); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeControlRequestReportsFailureWhileWriting(t *testing.T) {
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", "echo 'not logged in' >&2\nexit 1\n"))
	// Exceed the pipe buffer so the request cannot finish before the CLI exits.
	_, err := claudeControlRequest(context.Background(), "pearl-models", strings.Repeat("x", 1<<20))
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeSignedInReadsTheCLIStatus(t *testing.T) {
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", `echo '{"loggedIn":true}'`))
	if !ClaudeSignedIn(context.Background()) {
		t.Fatal("signed in reads as signed out")
	}
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", `echo '{"loggedIn":false}'`))
	if ClaudeSignedIn(context.Background()) {
		t.Fatal("signed out reads as signed in")
	}
}

func antigravityModelServer(t *testing.T, sessionNew string) {
	t.Helper()
	server := writeModelScript(t, "agy_acp_server.par", `while IFS= read -r line; do
  printf '%s\n' "$line" >> "$0.requests"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id" ;;
    *'"method":"authenticate"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id" ;;
    *'"method":"session/new"'*) `+sessionNew+` ;;
  esac
done
`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(server), "localharness_external"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(antigravityPathEnvironment, server)
}

func TestAntigravityModelsComeFromTheSession(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5}`, true)
	antigravityModelServer(t, `printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"s","configOptions":[{"id":"mode","category":"mode","currentValue":"default","options":[{"value":"yolo","name":"YOLO"}]},{"id":"model","category":"model","currentValue":"gemini-a","options":[{"value":"gemini-a","name":"Gemini A"},{"group":"more","name":"More","options":[{"value":"gemini-b","name":"Gemini B"}]}]}]}}\n' "$id"`)
	models, err := AntigravityModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gemini-a" || models[1].Name != "Gemini B" {
		t.Fatalf("models = %+v", models)
	}
	requests, err := os.ReadFile(os.Getenv(antigravityPathEnvironment) + ".requests")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(requests), "session/prompt") {
		t.Fatal("listing models must not send a prompt")
	}
}

func TestAntigravityModelsNeedASignIn(t *testing.T) {
	setUpAntigravity(t, `{"model":"m","max_depth":5}`, false)
	antigravityModelServer(t, `printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"`)
	if _, err := AntigravityModels(context.Background()); !errors.Is(err, ErrAntigravitySignIn) {
		t.Fatalf("err = %v", err)
	}
	setUpAntigravity(t, `{"model":"m","max_depth":5}`, true)
	antigravityModelServer(t, `echo '`+antigravityAuthLinkPrefix+`https://accounts.google.com/x' >&2; sleep 30`)
	if _, err := AntigravityModels(context.Background()); !errors.Is(err, ErrAntigravitySignIn) {
		t.Fatalf("expired sign-in err = %v", err)
	}
}
