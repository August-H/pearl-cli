package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSubscriptionChecksWithoutInferenceAndClassifiesFailures(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, test := range []struct {
		name, response   string
		invalid, failure bool
	}{
		{"valid", `{"subtype":"success","response":{"rate_limits_available":true}}`, false, false},
		{"expired", `{"subtype":"error","error":"OAuth token has expired"}`, true, true},
		{"outage", `{"subtype":"error","error":"network unavailable"}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := writeModelScript(t, "claude", `if [ "$1" = auth ]; then echo '{"loggedIn":true}'; exit 0; fi
IFS= read -r request
printf '%s' "$request" > "$0.request"
echo '{"type":"control_response","response":{"request_id":"pearl-subscription",`+strings.TrimPrefix(test.response, "{")+`}'
sleep 30
`)
			t.Setenv(claudePathEnvironment, script)
			connected, err := CheckClaudeSubscription(context.Background())
			if !connected || (err != nil) != test.failure || errors.Is(err, ErrSubscriptionInvalid) != test.invalid {
				t.Fatalf("connected=%v err=%v", connected, err)
			}
			request, err := os.ReadFile(script + ".request")
			if err != nil || !strings.Contains(string(request), "get_usage") || strings.Contains(string(request), `"type":"user"`) {
				t.Fatalf("unexpected probe: %s, %v", request, err)
			}
		})
	}
}

func TestClaudeSubscriptionDoesNotTreatBrokenStatusAsSignedOut(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", `echo '{}'`))
	if _, err := CheckClaudeSubscription(context.Background()); err == nil || errors.Is(err, ErrClaudeSignedOut) {
		t.Fatalf("broken status = %v", err)
	}
}

func TestAntigravitySubscriptionUsesOnlyTokenRefresh(t *testing.T) {
	profile := t.TempDir()
	t.Setenv(antigravityHomeEnvironment, profile)
	if err := os.MkdirAll(filepath.Join(profile, "antigravity-acp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "antigravity-acp", antigravityTokenFile), []byte(`{"client_id":"client","client_secret":"secret","refresh_token":"saved"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name             string
		status           int
		body             string
		invalid, failure bool
	}{
		{"valid", 200, `{"access_token":"new-access"}`, false, false},
		{"revoked", 400, `{"error":"invalid_grant","error_description":"saved-secret"}`, true, true},
		{"denied", 401, `{}`, true, true},
		{"outage", 503, `{}`, false, true},
		{"limited", 429, `{}`, false, true},
		{"bad-response", 200, `{}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "saved" {
					t.Error("not a refresh request")
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			previous := antigravityTokenEndpoint
			antigravityTokenEndpoint = server.URL
			defer func() { antigravityTokenEndpoint = previous }()
			connected, err := CheckAntigravitySubscription(context.Background())
			if !connected || (err != nil) != test.failure || errors.Is(err, ErrSubscriptionInvalid) != test.invalid {
				t.Fatalf("connected=%v err=%v", connected, err)
			}
			if err != nil && strings.Contains(err.Error(), "saved-secret") {
				t.Fatal("provider error body leaked")
			}
		})
	}
}
