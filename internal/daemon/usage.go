package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/openrouter_request"
)

// UsageWindow is one limit on a plan.
type UsageWindow struct {
	Label       string
	UsedPercent float64
	ResetsAt    time.Time
}

// Usage is what a subscription has left.
type Usage struct {
	Plan    string
	Account string
	Windows []UsageWindow
}

// ErrNoPlanLimits means the sign-in is an API key, which has no plan limits.
var ErrNoPlanLimits = errors.New("this sign-in uses an API key, so it has no plan limits")

// These mean there's no sign-in to report on, rather than that reading one failed.
var (
	ErrClaudeSignedOut = errors.New("Claude Code isn't signed in; run `pearl auth login claude`")
	ErrCodexSignedOut  = errors.New("ChatGPT's limits come from Codex, which isn't signed in; run `codex login`")
)

const (
	usageTimeout       = 20 * time.Second
	usageRequestLimit  = 10 * time.Second
	usageResponseBytes = 4 << 20
)

func usageWindow(label string, usedPercent float64, resetsAt time.Time) UsageWindow {
	return UsageWindow{Label: label, UsedPercent: max(0, min(100, usedPercent)), ResetsAt: resetsAt}
}

func usageWindowName(minutes int64) string {
	switch {
	case minutes >= 43200:
		return "Monthly"
	case minutes >= 10080:
		return "Weekly"
	case minutes <= 0:
		return "Session"
	}
	return fmt.Sprintf("%d-hour", (minutes+30)/60)
}

// ClaudeUsage asks the installed CLI for this account's limits, the same request
// T3 Code makes. It sends no prompt, so it uses no tokens.
func ClaudeUsage(ctx context.Context) (Usage, error) {
	signIn := claudeSignIn(ctx)
	if !signIn.LoggedIn {
		return Usage{}, ErrClaudeSignedOut
	}
	account := Usage{Account: signIn.Email}
	if BillingFor(openrouter_request.ProviderClaude) == BillingAPI {
		return account, ErrNoPlanLimits
	}
	response, err := claudeControlRequest(ctx, "pearl-usage", "get_usage")
	if err != nil {
		return account, err
	}
	usage, err := parseClaudeUsage(response)
	usage.Account = signIn.Email
	return usage, err
}

func parseClaudeUsage(response []byte) (Usage, error) {
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	type money struct {
		AmountMinor float64 `json:"amount_minor"`
		Currency    string  `json:"currency"`
		Exponent    int     `json:"exponent"`
	}
	var result struct {
		SubscriptionType    string `json:"subscription_type"`
		RateLimitsAvailable bool   `json:"rate_limits_available"`
		RateLimits          *struct {
			FiveHour    *window `json:"five_hour"`
			SevenDay    *window `json:"seven_day"`
			ModelScoped []struct {
				window
				DisplayName string `json:"display_name"`
			} `json:"model_scoped"`
			Spend *struct {
				Enabled bool   `json:"enabled"`
				Used    *money `json:"used"`
				Limit   *money `json:"limit"`
			} `json:"spend"`
			ExtraUsage *struct {
				IsEnabled    bool     `json:"is_enabled"`
				MonthlyLimit *float64 `json:"monthly_limit"`
				UsedCredits  *float64 `json:"used_credits"`
				Utilization  *float64 `json:"utilization"`
			} `json:"extra_usage"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return Usage{}, fmt.Errorf("read Claude Code's usage: %w", err)
	}
	if !result.RateLimitsAvailable || result.RateLimits == nil {
		return Usage{}, errors.New("Claude Code doesn't report limits for this account")
	}
	usage := Usage{Plan: result.SubscriptionType}
	limits := result.RateLimits
	add := func(label string, value *window) {
		if value != nil && value.Utilization != nil {
			usage.Windows = append(usage.Windows, usageWindow(label, *value.Utilization, parseUsageTime(value.ResetsAt)))
		}
	}
	add("5-hour", limits.FiveHour)
	add("Weekly", limits.SevenDay)
	for index := range limits.ModelScoped {
		scoped := &limits.ModelScoped[index]
		if scoped.DisplayName != "" {
			add("Weekly · "+scoped.DisplayName, &scoped.window)
		}
	}
	spend := limits.Spend
	switch {
	case spend != nil && spend.Enabled && spend.Used != nil && spend.Limit != nil && spend.Limit.AmountMinor > 0 &&
		spend.Used.Currency == spend.Limit.Currency && spend.Used.Exponent == spend.Limit.Exponent:
		usage.Windows = append(usage.Windows, usageWindow("Monthly spend", spend.Used.AmountMinor/spend.Limit.AmountMinor*100, time.Time{}))
	case limits.ExtraUsage != nil && limits.ExtraUsage.IsEnabled:
		extra := limits.ExtraUsage
		if extra.UsedCredits != nil && extra.MonthlyLimit != nil && *extra.MonthlyLimit > 0 {
			usage.Windows = append(usage.Windows, usageWindow("Monthly spend", *extra.UsedCredits / *extra.MonthlyLimit * 100, time.Time{}))
		} else if extra.Utilization != nil {
			usage.Windows = append(usage.Windows, usageWindow("Monthly spend", *extra.Utilization, time.Time{}))
		}
	}
	return usage, nil
}

func parseUsageTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// CodexUsage asks Codex for its login's limits, the same request T3 Code makes.
// Pearl's own ChatGPT sign-in can run models but can't read limits, so this needs
// Codex to be signed in too. It uses no tokens.
func CodexUsage(ctx context.Context) (Usage, error) {
	path, err := FindCodex()
	if err != nil {
		return Usage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, "app-server")
	command.Dir = os.TempDir()
	command.WaitDelay = 2 * time.Second
	var stderr codexTailBuffer
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return Usage{}, fmt.Errorf("connect to Codex: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Usage{}, fmt.Errorf("connect to Codex: %w", err)
	}
	stop, err := openrouter_request.StartProcessGroup(command)
	if err != nil {
		return Usage{}, fmt.Errorf("start Codex: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		stop()
		_ = command.Wait()
	}()
	const initializeID, accountID, limitsID = 1, 2, 3
	for _, message := range []map[string]any{
		{"id": initializeID, "method": "initialize", "params": map[string]any{
			"clientInfo": map[string]string{"name": "pearl", "title": "Pearl", "version": "1"},
		}},
		{"method": "initialized"},
		{"id": accountID, "method": "account/read", "params": map[string]any{"refreshToken": false}},
		{"id": limitsID, "method": "account/rateLimits/read"},
	} {
		encoded, _ := json.Marshal(message)
		if _, err := stdin.Write(append(encoded, '\n')); err != nil {
			return Usage{}, fmt.Errorf("send Codex a request: %w", err)
		}
	}
	// The answers can arrive in either order, so wait for both before judging them.
	answers := map[int]json.RawMessage{}
	failures := map[int]string{}
	reader := bufio.NewReaderSize(stdout, 1<<20)
	answered := func(id int) bool {
		_, result := answers[id]
		_, failure := failures[id]
		return result || failure
	}
	for !answered(accountID) || !answered(limitsID) {
		line, readErr := reader.ReadBytes('\n')
		var message struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &message) == nil && message.ID != nil {
			if message.Error != nil {
				failures[*message.ID] = message.Error.Message
			} else {
				answers[*message.ID] = message.Result
			}
		}
		if readErr != nil && (!answered(accountID) || !answered(limitsID)) {
			_ = stdin.Close()
			_ = command.Wait()
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = readErr.Error()
			}
			return Usage{}, errors.New("Codex didn't answer: " + detail)
		}
	}
	if failure, failed := failures[accountID]; failed {
		return Usage{}, errors.New("Codex couldn't read its account: " + failure)
	}
	if codexAccountSignedOut(answers[accountID]) {
		return Usage{}, ErrCodexSignedOut
	}
	if failure, failed := failures[limitsID]; failed {
		return Usage{}, errors.New("Codex couldn't read usage: " + failure)
	}
	return parseCodexUsage(answers[accountID], answers[limitsID])
}

func codexAccountSignedOut(account json.RawMessage) bool {
	var result struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	return json.Unmarshal(account, &result) != nil || result.Account == nil
}

func parseCodexUsage(account, limits []byte) (Usage, error) {
	var who struct {
		Account struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := json.Unmarshal(account, &who); err != nil {
		return Usage{}, fmt.Errorf("read Codex's account: %w", err)
	}
	if !strings.EqualFold(who.Account.Type, "chatgpt") {
		return Usage{}, ErrNoPlanLimits
	}
	type window struct {
		UsedPercent        *float64 `json:"usedPercent"`
		WindowDurationMins *int64   `json:"windowDurationMins"`
		ResetsAt           int64    `json:"resetsAt"`
	}
	type snapshot struct {
		LimitID         string  `json:"limitId"`
		PlanType        string  `json:"planType"`
		Primary         *window `json:"primary"`
		Secondary       *window `json:"secondary"`
		IndividualLimit *struct {
			RemainingPercent *float64 `json:"remainingPercent"`
			ResetsAt         int64    `json:"resetsAt"`
		} `json:"individualLimit"`
	}
	var result struct {
		RateLimits        snapshot            `json:"rateLimits"`
		RateLimitsByLimit map[string]snapshot `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(limits, &result); err != nil {
		return Usage{}, fmt.Errorf("read Codex's usage: %w", err)
	}
	current := result.RateLimits
	if codex, ok := result.RateLimitsByLimit["codex"]; ok {
		current = codex
	}
	usage := Usage{Plan: who.Account.PlanType, Account: who.Account.Email}
	if usage.Plan == "" {
		usage.Plan = current.PlanType
	}
	if current.LimitID != "" && current.LimitID != "codex" {
		return usage, nil
	}
	// Free and Go plans have one monthly window where paid plans have a 5-hour one.
	primaryMinutes := int64(300)
	if current.PlanType == "free" || current.PlanType == "go" {
		primaryMinutes = 43200
	}
	for _, entry := range []struct {
		window   *window
		fallback int64
	}{{current.Primary, primaryMinutes}, {current.Secondary, 10080}} {
		if entry.window == nil || entry.window.UsedPercent == nil {
			continue
		}
		minutes := entry.fallback
		if entry.window.WindowDurationMins != nil {
			minutes = *entry.window.WindowDurationMins
		}
		usage.Windows = append(usage.Windows, usageWindow(usageWindowName(minutes), *entry.window.UsedPercent, usageUnixTime(entry.window.ResetsAt)))
	}
	if individual := current.IndividualLimit; individual != nil && individual.RemainingPercent != nil {
		usage.Windows = append(usage.Windows, usageWindow("Monthly credits", 100-*individual.RemainingPercent, usageUnixTime(individual.ResetsAt)))
	}
	return usage, nil
}

func usageUnixTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

// These are variables so tests can use a local server.
var (
	antigravityTokenEndpoint    = "https://oauth2.googleapis.com/token"
	antigravityUserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo"
	antigravityQuotaEndpoints   = []string{"https://daily-cloudcode-pa.googleapis.com", "https://cloudcode-pa.googleapis.com"}
)

// antigravityClient doesn't follow redirects, so a redirect can't carry the token
// anywhere else.
var antigravityClient = &http.Client{
	Timeout:       usageRequestLimit,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// AntigravityUsage reads this account's quota with Pearl's saved Google sign-in,
// the same requests T3 Code makes. It uses no quota.
func AntigravityUsage(ctx context.Context) (Usage, error) {
	if !AntigravitySignedIn() {
		return Usage{}, ErrAntigravitySignIn
	}
	profile, err := AntigravityProfile()
	if err != nil {
		return Usage{}, err
	}
	contents, err := os.ReadFile(filepath.Join(profile, "antigravity-acp", antigravityTokenFile))
	if err != nil {
		return Usage{}, fmt.Errorf("read the Antigravity sign-in: %w", err)
	}
	var saved struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
		ProjectID    string `json:"project_id"`
	}
	if err := json.Unmarshal(contents, &saved); err != nil || saved.ClientID == "" || saved.RefreshToken == "" {
		return Usage{}, errors.New("the saved Antigravity sign-in can't be read; run `pearl auth login antigravity`")
	}
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityTokenEndpoint, strings.NewReader(url.Values{
		"client_id": {saved.ClientID}, "client_secret": {saved.ClientSecret},
		"refresh_token": {saved.RefreshToken}, "grant_type": {"refresh_token"},
	}.Encode()))
	if err != nil {
		return Usage{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := antigravityJSON(request, &token); err != nil || token.AccessToken == "" {
		return Usage{}, errors.New("Google didn't accept the saved Antigravity sign-in; run `pearl auth login antigravity`")
	}
	account := antigravityEmail(ctx, token.AccessToken)
	summary := antigravityQuotaWindows(ctx, token.AccessToken, saved.ProjectID, "retrieveUserQuotaSummary", antigravitySummaryWindows)
	models := antigravityQuotaWindows(ctx, token.AccessToken, saved.ProjectID, "fetchAvailableModels", antigravityModelWindows)
	windows := combineAntigravityWindows(summary, models)
	if len(windows) == 0 {
		return Usage{Account: account}, errors.New("Antigravity didn't report any limits")
	}
	return Usage{Account: account, Windows: windows}, nil
}

// antigravityEmail names the signed-in Google account. Limits still work without
// it, so a failure just leaves the name out.
func antigravityEmail(ctx context.Context, accessToken string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, antigravityUserInfoEndpoint, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	var user struct {
		Email string `json:"email"`
	}
	if antigravityJSON(request, &user) != nil {
		return ""
	}
	return user.Email
}

// combineAntigravityWindows puts Gemini first. The summary may cover only other
// labs' models, and then Gemini's quota is only in the per-model list.
func combineAntigravityWindows(summary, models []UsageWindow) []UsageWindow {
	const gemini = "Gemini ·"
	windows := append([]UsageWindow(nil), summary...)
	hasGemini := false
	for _, window := range summary {
		hasGemini = hasGemini || strings.HasPrefix(window.Label, gemini)
	}
	for _, window := range models {
		if len(summary) == 0 || !hasGemini && strings.HasPrefix(window.Label, gemini) {
			windows = append(windows, window)
		}
	}
	sort.SliceStable(windows, func(left, right int) bool {
		leftGemini, rightGemini := strings.HasPrefix(windows[left].Label, gemini), strings.HasPrefix(windows[right].Label, gemini)
		if leftGemini != rightGemini {
			return leftGemini
		}
		return windows[left].Label < windows[right].Label
	})
	return windows
}

func antigravityJSON(request *http.Request, value any) error {
	response, err := antigravityClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, usageResponseBytes)).Decode(value)
}

// antigravityQuotaWindows asks every endpoint, because an account's quota can live
// on either one, and keeps the higher reading of any limit they both report.
func antigravityQuotaWindows(
	ctx context.Context, accessToken, project, operation string,
	parse func([]byte) []antigravityWindow,
) []UsageWindow {
	body := []byte("{}")
	if project != "" {
		body, _ = json.Marshal(map[string]string{"project": project})
	}
	results := make([][]antigravityWindow, len(antigravityQuotaEndpoints))
	var wait sync.WaitGroup
	for index, endpoint := range antigravityQuotaEndpoints {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1internal:"+operation, bytes.NewReader(body))
			if err != nil {
				return
			}
			request.Header.Set("Authorization", "Bearer "+accessToken)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", antigravityUserAgent())
			var raw json.RawMessage
			if antigravityJSON(request, &raw) == nil {
				results[index] = parse(raw)
			}
		}()
	}
	wait.Wait()
	var merged []antigravityWindow
	positions := map[string]int{}
	for _, result := range results {
		for _, window := range result {
			position, seen := positions[window.id]
			if !seen {
				positions[window.id] = len(merged)
				merged = append(merged, window)
			} else if window.UsedPercent > merged[position].UsedPercent {
				merged[position] = window
			}
		}
	}
	windows := make([]UsageWindow, 0, len(merged))
	for _, window := range merged {
		windows = append(windows, window.UsageWindow)
	}
	return windows
}

func antigravityUserAgent() string {
	platform, architecture := runtime.GOOS, runtime.GOARCH
	if platform == "windows" {
		platform = "win32"
	}
	if architecture == "amd64" {
		architecture = "x64"
	}
	return fmt.Sprintf("antigravity/hub/1.0.0 (aidev_client; os_type=%s; arch=%s; cl=963137146)", platform, architecture)
}

type antigravityWindow struct {
	UsageWindow
	id string
}

type antigravityQuota struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

func (quota antigravityQuota) window(id, label string) (antigravityWindow, bool) {
	if quota.RemainingFraction == nil {
		return antigravityWindow{}, false
	}
	used := (1 - max(0, min(1, *quota.RemainingFraction))) * 100
	return antigravityWindow{usageWindow(label, used, parseUsageTime(quota.ResetTime)), id}, true
}

func antigravitySummaryWindows(body []byte) []antigravityWindow {
	type bucket struct {
		antigravityQuota
		BucketID    string `json:"bucketId"`
		Window      string `json:"window"`
		DisplayName string `json:"displayName"`
		Disabled    bool   `json:"disabled"`
	}
	type group struct {
		DisplayName string   `json:"displayName"`
		Buckets     []bucket `json:"buckets"`
	}
	var summary struct {
		Groups  []group  `json:"groups"`
		Buckets []bucket `json:"buckets"`
	}
	if json.Unmarshal(body, &summary) != nil {
		return nil
	}
	groups := summary.Groups
	hasBuckets := false
	for _, entry := range groups {
		hasBuckets = hasBuckets || len(entry.Buckets) > 0
	}
	if !hasBuckets {
		groups = []group{{Buckets: summary.Buckets}}
	}
	var windows []antigravityWindow
	for _, entry := range groups {
		for _, item := range entry.Buckets {
			if item.Disabled || item.BucketID == "" {
				continue
			}
			family := strings.TrimSpace(entry.DisplayName)
			switch {
			case strings.HasPrefix(item.BucketID, "gemini-"):
				family = "Gemini"
			case strings.HasPrefix(item.BucketID, "3p-"):
				family = "Claude & GPT (shared)"
			case family == "":
				family = item.BucketID
			}
			name := strings.TrimSpace(item.DisplayName)
			switch item.Window {
			case "weekly":
				name = "Weekly"
			case "5h":
				name = "5-hour"
			default:
				if name == "" {
					name = "Quota"
				}
			}
			if window, ok := item.window(item.BucketID, family+" · "+name); ok {
				windows = append(windows, window)
			}
		}
	}
	return windows
}

// antigravityModelWindows reads the older form, where each model has its own
// quota. Models in one family share it, so each family keeps its highest reading.
func antigravityModelWindows(body []byte) []antigravityWindow {
	var result struct {
		Models map[string]struct {
			ModelProvider   string            `json:"modelProvider"`
			QuotaInfo       *antigravityQuota `json:"quotaInfo"`
			WeeklyQuotaInfo *antigravityQuota `json:"weeklyQuotaInfo"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &result) != nil {
		return nil
	}
	families := map[string]antigravityWindow{}
	for id, model := range result.Models {
		family := ""
		switch {
		case strings.HasPrefix(id, "gemini-"):
			// Google's other models here are internal, such as tab completion.
			family = "Gemini"
		case model.ModelProvider == "MODEL_PROVIDER_ANTHROPIC" || strings.HasPrefix(id, "claude-"):
			family = "Claude"
		case model.ModelProvider == "MODEL_PROVIDER_OPENAI" || strings.HasPrefix(id, "gpt-"):
			family = "GPT"
		default:
			continue
		}
		for _, entry := range []struct {
			quota        *antigravityQuota
			suffix, name string
		}{{model.QuotaInfo, "quota", "Quota"}, {model.WeeklyQuotaInfo, "weekly", "Weekly"}} {
			if entry.quota == nil {
				continue
			}
			key := family + "-" + entry.suffix
			window, ok := entry.quota.window(key, family+" · "+entry.name)
			if ok && (families[key].id == "" || window.UsedPercent > families[key].UsedPercent) {
				families[key] = window
			}
		}
	}
	windows := make([]antigravityWindow, 0, len(families))
	for _, window := range families {
		windows = append(windows, window)
	}
	sort.Slice(windows, func(left, right int) bool { return windows[left].id < windows[right].id })
	return windows
}
