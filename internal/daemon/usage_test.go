package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseClaudeUsageReadsPlanWindows(t *testing.T) {
	usage, err := parseClaudeUsage([]byte(`{"subscription_type":"pro","rate_limits_available":true,"rate_limits":{
		"five_hour":{"utilization":47,"resets_at":"2026-11-01T21:30:00.024417+00:00"},
		"seven_day":{"utilization":11,"resets_at":"2026-11-02T16:00:00+00:00"},
		"seven_day_opus":null,
		"model_scoped":[{"display_name":"Fable","utilization":130,"resets_at":"bad"},{"utilization":5},{"display_name":"Opus"}],
		"spend":{"enabled":true,"used":{"amount_minor":250,"currency":"USD","exponent":2},"limit":{"amount_minor":1000,"currency":"USD","exponent":2}},
		"extra_usage":{"is_enabled":false}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Plan != "pro" || len(usage.Windows) != 4 {
		t.Fatalf("usage = %+v", usage)
	}
	five := usage.Windows[0]
	if five.Label != "5-hour" || five.UsedPercent != 47 || five.ResetsAt.UTC().Hour() != 21 {
		t.Errorf("five hour = %+v", five)
	}
	scoped := usage.Windows[2]
	if scoped.Label != "Weekly · Fable" || scoped.UsedPercent != 100 || !scoped.ResetsAt.IsZero() {
		t.Errorf("scoped = %+v", scoped)
	}
	if spend := usage.Windows[3]; spend.Label != "Monthly spend" || spend.UsedPercent != 25 {
		t.Errorf("spend = %+v", spend)
	}
}

func TestParseClaudeUsageNullSpendAndExtraUsage(t *testing.T) {
	usage, err := parseClaudeUsage([]byte(`{"rate_limits_available":true,"rate_limits":{
		"spend":{"enabled":false,"used":{"amount_minor":0,"currency":"USD","exponent":2},"limit":null},
		"extra_usage":{"is_enabled":true,"monthly_limit":200,"used_credits":50}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Windows) != 1 || usage.Windows[0].UsedPercent != 25 {
		t.Fatalf("usage = %+v", usage)
	}
	if _, err := parseClaudeUsage([]byte(`{"rate_limits_available":false}`)); err == nil {
		t.Fatal("an account with no limits should say so")
	}
}

func claudeUsageScript(t *testing.T, answer string) string {
	return writeModelScript(t, "claude", `if [ "$1" = auth ]; then echo '{"loggedIn":true}'; exit 0; fi
IFS= read -r request
printf '%s' "$request" > "$0.request"
`+answer+`
sleep 30
`)
}

func TestClaudeUsageAsksTheCLIWithoutAPrompt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	script := claudeUsageScript(t, `echo '{"type":"control_response","response":{"subtype":"success","request_id":"pearl-usage","response":{"subscription_type":"max","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":3}}}}}'`)
	t.Setenv(claudePathEnvironment, script)
	usage, err := ClaudeUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.Plan != "max" || len(usage.Windows) != 1 {
		t.Fatalf("usage = %+v", usage)
	}
	request, err := os.ReadFile(script + ".request")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(request), `"subtype":"get_usage"`) || strings.Contains(string(request), `"type":"user"`) {
		t.Fatalf("reading usage must not send a prompt: %s", request)
	}
}

func TestClaudeUsageWithAnAPIKeyHasNoPlanLimits(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv(claudePathEnvironment, claudeUsageScript(t, ""))
	if _, err := ClaudeUsage(context.Background()); !errors.Is(err, ErrNoPlanLimits) {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeUsageNeedsASignIn(t *testing.T) {
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", `echo '{"loggedIn":false}'`))
	if _, err := ClaudeUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "pearl auth login claude") {
		t.Fatalf("err = %v", err)
	}
}

const codexChatGPTAccount = `{"account":{"type":"chatgpt","email":"me@example.com","planType":"plus"}}`

func TestParseCodexUsageReadsPlanWindows(t *testing.T) {
	usage, err := parseCodexUsage([]byte(codexChatGPTAccount), []byte(`{
		"rateLimits":{"limitId":"other","primary":{"usedPercent":99}},
		"rateLimitsByLimitId":{"codex":{"limitId":"codex",
			"primary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":1793570389},
			"secondary":{"usedPercent":17,"windowDurationMins":10080,"resetsAt":1793775234},
			"individualLimit":{"remainingPercent":60}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Plan != "plus" || usage.Account != "me@example.com" || len(usage.Windows) != 3 {
		t.Fatalf("usage = %+v", usage)
	}
	if window := usage.Windows[0]; window.Label != "5-hour" || window.UsedPercent != 1 || window.ResetsAt.Unix() != 1793570389 {
		t.Errorf("primary = %+v", window)
	}
	if window := usage.Windows[1]; window.Label != "Weekly" || window.UsedPercent != 17 {
		t.Errorf("secondary = %+v", window)
	}
	if window := usage.Windows[2]; window.Label != "Monthly credits" || window.UsedPercent != 40 {
		t.Errorf("credits = %+v", window)
	}
}

func TestParseCodexUsageUsesMonthlyWindowsOnFreePlans(t *testing.T) {
	usage, err := parseCodexUsage([]byte(codexChatGPTAccount), []byte(`{"rateLimits":{"planType":"free","primary":{"usedPercent":5}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Windows) != 1 || usage.Windows[0].Label != "Monthly" {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseCodexUsageWithAnAPIKeyHasNoPlanLimits(t *testing.T) {
	if _, err := parseCodexUsage([]byte(`{"account":{"type":"apiKey"}}`), []byte(`{}`)); !errors.Is(err, ErrNoPlanLimits) {
		t.Fatalf("err = %v", err)
	}
}

// fakeCodexServer answers Codex's requests in the order Pearl sends them.
func fakeCodexServer(t *testing.T, account, limits string) string {
	return writeModelScript(t, "codex", `[ "$1" = app-server ] || exit 9
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
    *'"method":"account/read"'*) echo '{"method":"note","params":{}}'; echo '{"id":2,`+account+`}' ;;
    *'"method":"account/rateLimits/read"'*) echo '{"id":3,`+limits+`}' ;;
  esac
done
`)
}

func TestCodexUsageAsksTheAppServer(t *testing.T) {
	t.Setenv(codexPathEnvironment, fakeCodexServer(t,
		`"result":`+codexChatGPTAccount,
		`"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":8,"windowDurationMins":300}}}`))
	usage, err := CodexUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.Plan != "plus" || len(usage.Windows) != 1 || usage.Windows[0].UsedPercent != 8 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCodexUsageExplainsASignedOutCodex(t *testing.T) {
	t.Setenv(codexPathEnvironment, fakeCodexServer(t,
		`"result":{"account":null}`, `"error":{"code":-32600,"message":"codex account authentication required"}`))
	if _, err := CodexUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't signed in") || !strings.Contains(err.Error(), "login") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexUsageHandlesAnswersInEitherOrder(t *testing.T) {
	t.Setenv(codexPathEnvironment, writeModelScript(t, "codex", `while IFS= read -r line; do
  case "$line" in
    *'"method":"account/rateLimits/read"'*)
      echo '{"id":3,"error":{"code":-32600,"message":"authentication required"}}'
      echo '{"id":2,"result":{"account":null}}' ;;
  esac
done
`))
	if _, err := CodexUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't signed in") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexUsageReportsFailures(t *testing.T) {
	t.Setenv(codexPathEnvironment, fakeCodexServer(t,
		`"result":`+codexChatGPTAccount, `"error":{"code":-32603,"message":"boom"}`))
	if _, err := CodexUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv(codexPathEnvironment, writeModelScript(t, "codex", "echo 'it broke' >&2\nexit 1\n"))
	if _, err := CodexUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("err = %v", err)
	}
}

type antigravityUsageServer struct {
	summary, models string
	status          atomic.Int32
	project         atomic.Value
}

func startAntigravityUsageServer(t *testing.T, server *antigravityUsageServer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	profile, err := prepareAntigravityProfile()
	if err != nil {
		t.Fatal(err)
	}
	token := `{"client_id":"id","client_secret":"very-secret","refresh_token":"refresh","project_id":"proj"}`
	if err := os.WriteFile(filepath.Join(profile, "antigravity-acp", antigravityTokenFile), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if status := server.status.Load(); status != 0 {
				w.WriteHeader(int(status))
				return
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "refresh_token=refresh") {
				t.Errorf("token body = %s", body)
			}
			io.WriteString(w, `{"access_token":"access","expires_in":3600}`)
		case r.Header.Get("Authorization") != "Bearer access":
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasSuffix(r.URL.Path, ":retrieveUserQuotaSummary"):
			var request map[string]string
			_ = json.NewDecoder(r.Body).Decode(&request)
			server.project.Store(request["project"])
			io.WriteString(w, server.summary)
		case strings.HasSuffix(r.URL.Path, ":fetchAvailableModels"):
			io.WriteString(w, server.models)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	listener := httptest.NewServer(handler)
	t.Cleanup(listener.Close)
	originalToken, originalEndpoints := antigravityTokenEndpoint, antigravityQuotaEndpoints
	antigravityTokenEndpoint = listener.URL + "/token"
	antigravityQuotaEndpoints = []string{listener.URL}
	t.Cleanup(func() { antigravityTokenEndpoint, antigravityQuotaEndpoints = originalToken, originalEndpoints })
}

func TestAntigravityUsageAddsGeminiToAnOtherLabsSummary(t *testing.T) {
	server := &antigravityUsageServer{
		// These are the shapes the real service returned.
		summary: `{"groups":[{"displayName":"Claude and GPT models","buckets":[
			{"bucketId":"3p-weekly","remainingFraction":1,"resetTime":"2026-11-08T17:10:09Z","window":"weekly"},
			{"bucketId":"3p-5h","remainingFraction":0.75,"resetTime":"2026-11-01T22:10:09Z","window":"5h"},
			{"bucketId":"3p-off","remainingFraction":0,"disabled":true}]}]}`,
		models: `{"models":{
			"gemini-3-flash":{"modelProvider":"MODEL_PROVIDER_GOOGLE","quotaInfo":{"remainingFraction":0.9,"resetTime":"2026-11-01T22:10:09Z"}},
			"gemini-2.5-pro":{"modelProvider":"MODEL_PROVIDER_GOOGLE","quotaInfo":{"remainingFraction":0.6,"resetTime":"2026-11-01T22:10:09Z"}},
			"tab_flash_lite_preview":{"modelProvider":"MODEL_PROVIDER_GOOGLE","quotaInfo":{"remainingFraction":0}},
			"claude-sonnet-4-6":{"modelProvider":"MODEL_PROVIDER_ANTHROPIC","quotaInfo":{"remainingFraction":0}}}}`,
	}
	startAntigravityUsageServer(t, server)
	usage, err := AntigravityUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, window := range usage.Windows {
		labels = append(labels, window.Label)
	}
	want := "Gemini · Quota|Claude & GPT (shared) · 5-hour|Claude & GPT (shared) · Weekly"
	if got := strings.Join(labels, "|"); got != want {
		t.Fatalf("labels = %s", got)
	}
	if used := usage.Windows[0].UsedPercent; used < 39.9 || used > 40.1 {
		t.Errorf("Gemini should show its most-used model, got %v", used)
	}
	if usage.Windows[0].ResetsAt.IsZero() {
		t.Error("Gemini lost its reset time")
	}
	if used := usage.Windows[1].UsedPercent; used != 25 {
		t.Errorf("5-hour used = %v", used)
	}
	if project := server.project.Load(); project != "proj" {
		t.Errorf("project = %v", project)
	}
}

func TestAntigravityUsageKeepsGeminiFromTheSummary(t *testing.T) {
	startAntigravityUsageServer(t, &antigravityUsageServer{
		summary: `{"buckets":[{"bucketId":"gemini-5h","remainingFraction":0.5,"window":"5h"}]}`,
		models:  `{"models":{"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.1}}}}`,
	})
	usage, err := AntigravityUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Windows) != 1 || usage.Windows[0].Label != "Gemini · 5-hour" || usage.Windows[0].UsedPercent != 50 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAntigravityUsageFallsBackToModels(t *testing.T) {
	startAntigravityUsageServer(t, &antigravityUsageServer{
		summary: `{}`,
		models: `{"models":{"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.5},"weeklyQuotaInfo":{"remainingFraction":0.8}},
			"claude-sonnet-4-6":{"quotaInfo":{"remainingFraction":1}}}}`,
	})
	usage, err := AntigravityUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Windows) != 3 || usage.Windows[0].Label != "Gemini · Quota" || usage.Windows[1].Label != "Gemini · Weekly" || usage.Windows[2].Label != "Claude · Quota" {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAntigravityUsageMergesEndpointsByHighestUse(t *testing.T) {
	windows := combineAntigravityWindows(nil, nil)
	if len(windows) != 0 {
		t.Fatalf("windows = %+v", windows)
	}
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","remainingFraction":0.9,"window":"5h"}]}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","remainingFraction":0.2,"window":"5h"}]}`)
	}))
	defer second.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	original := antigravityQuotaEndpoints
	antigravityQuotaEndpoints = []string{first.URL, broken.URL, second.URL}
	defer func() { antigravityQuotaEndpoints = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	merged := antigravityQuotaWindows(ctx, "access", "", "retrieveUserQuotaSummary", antigravitySummaryWindows)
	if len(merged) != 1 || merged[0].UsedPercent != 80 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestAntigravityUsageFailuresKeepTheSignInPrivate(t *testing.T) {
	server := &antigravityUsageServer{}
	server.status.Store(http.StatusBadRequest)
	startAntigravityUsageServer(t, server)
	_, err := AntigravityUsage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pearl auth login antigravity") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "very-secret") || strings.Contains(err.Error(), "refresh") {
		t.Fatalf("leaked the sign-in: %v", err)
	}
	server.status.Store(0)
	server.summary, server.models = `{}`, `{}`
	if _, err := AntigravityUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "didn't report") {
		t.Fatalf("err = %v", err)
	}
}

func TestAntigravityUsageNeedsASignIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, err := AntigravityUsage(context.Background()); !errors.Is(err, ErrAntigravitySignIn) {
		t.Fatalf("err = %v", err)
	}
}

func TestAntigravityClientDoesNotFollowRedirects(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	request, err := http.NewRequest(http.MethodPost, redirect.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer access")
	var value any
	if err := antigravityJSON(request, &value); err == nil || followed {
		t.Fatalf("err=%v followed=%v", err, followed)
	}
}

func TestBillingFor(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := BillingFor("claude"); got != "claude" {
		t.Errorf("signed in = %s", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "key")
	if got := BillingFor("claude"); got != BillingAPI {
		t.Errorf("API key = %s", got)
	}
	if got := BillingFor("chatgpt"); got != "chatgpt" {
		t.Errorf("chatgpt = %s", got)
	}
}

func TestUsageNamesTheSignedInAccounts(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", `if [ "$1" = auth ]; then echo '{"loggedIn":true,"email":"me@example.com"}'; exit 0; fi
IFS= read -r request
echo '{"type":"control_response","response":{"subtype":"success","request_id":"pearl-usage","response":{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":3}}}}}'
sleep 30
`))
	usage, err := ClaudeUsage(context.Background())
	if err != nil || usage.Account != "me@example.com" {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "key")
	usage, err = ClaudeUsage(context.Background())
	if !errors.Is(err, ErrNoPlanLimits) || usage.Account != "me@example.com" {
		t.Fatalf("with an API key: usage = %+v, err = %v", usage, err)
	}
}

func TestClaudeSignedOutIsItsOwnError(t *testing.T) {
	t.Setenv(claudePathEnvironment, writeModelScript(t, "claude", `echo '{"loggedIn":false}'`))
	if _, err := ClaudeUsage(context.Background()); !errors.Is(err, ErrClaudeSignedOut) {
		t.Fatalf("err = %v", err)
	}
}
