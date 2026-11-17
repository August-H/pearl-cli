package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/chatgpt"
	"github.com/August-H/pearl-cli/openrouter_request"
)

func TestChatGPTProviderUsesAccountModelsAndKeepsOpenRouterSettings(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", directory)
	if _, err := openrouter_request.ConfigureOpenRouterModel("openrouter/saved"); err != nil {
		t.Fatal(err)
	}
	credentials, _ := json.Marshal(map[string]any{"active_client_id": "oaiapp_test", "accounts": []any{map[string]any{"client_id": "oaiapp_test", "email": "user@example.com", "access_token": "secret-account-token", "scopes": "resource.invoke chatgpt.tokens.use.direct", "expires_at": time.Now().Add(time.Hour)}}})
	if err := os.WriteFile(filepath.Join(directory, "chatgpt.json"), credentials, 0600); err != nil {
		t.Fatal(err)
	}
	mockModelCatalogueTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/models" || r.Header.Get("Authorization") != "Bearer secret-account-token" {
			t.Errorf("catalog request = %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"account-model","display_name":"Account model","visibility":"list"},{"slug":"other-model","display_name":"Other model","visibility":"list"}]}`))}, nil
	})
	if code := runProvider([]string{"chatgpt"}); code != 0 {
		t.Fatalf("provider exit = %d", code)
	}
	settings, _ := openrouter_request.LoadAgentSettings()
	if settings.Provider != "chatgpt" || settings.ChatGPTModel != "account-model" || settings.Model != "openrouter/saved" {
		t.Fatal("provider did not select account model independently")
	}
	original := loadModelCatalogs
	loadModelCatalogs = func(ctx context.Context) []modelCatalog { return []modelCatalog{loadChatGPTCatalog(ctx)} }
	t.Cleanup(func() { loadModelCatalogs = original })
	output, code := captureTestStdout(t, func() int { return runModel([]string{"--list"}) })
	if code != 0 {
		t.Errorf("models exit=%d", code)
	}
	if !strings.Contains(output, "account-model") || strings.Contains(output, "openrouter/free") {
		t.Fatalf("wrong ChatGPT model list: %s", output)
	}
	if code := runModel([]string{"--free"}); code == 0 {
		t.Error("ChatGPT accepted OpenRouter Free Tier")
	}
	if code := runModel([]string{"--set", "openrouter/not-a-chatgpt-model"}); code == 0 {
		t.Error("ChatGPT accepted another provider's model")
	}
	if code := runModel([]string{"--set", "other-model"}); code != 0 {
		t.Error("could not select account model")
	}
	output, _ = captureTestStdout(t, func() int { return runAuth([]string{"status"}) })
	if strings.Contains(output, "secret-account-token") || !strings.Contains(output, "user@example.com") {
		t.Fatal("status exposed token or omitted account")
	}
	if _, err := saveOpenRouterConfiguration("test-key", "openrouter/new"); err != nil {
		t.Fatal(err)
	}
	settings, _ = openrouter_request.LoadAgentSettings()
	if settings.Provider != "openrouter" || settings.Model != "openrouter/new" || settings.ChatGPTModel != "other-model" {
		t.Error("configure did not preserve separate provider preferences")
	}
}

func writeAuthTestAccounts(t *testing.T) {
	t.Helper()
	stubAccounts(t)
	original := checkClaudeSignIn
	checkClaudeSignIn = func(context.Context) bool { return false }
	t.Cleanup(func() { checkClaudeSignIn = original })
	accounts := []map[string]any{
		{"client_id": "oaiapp_first", "email": "first@example.com", "access_token": "first-token"},
		{"client_id": "oaiapp_second", "email": "second@example.com", "access_token": "second-token"},
		{"client_id": "oaiapp_third", "email": "third@example.com"},
	}
	for _, account := range accounts {
		account["scopes"] = "resource.invoke chatgpt.tokens.use.direct"
		account["expires_at"] = time.Now().Add(time.Hour)
	}
	data, err := json.Marshal(map[string]any{"active_client_id": "oaiapp_second", "accounts": accounts})
	if err != nil {
		t.Fatal(err)
	}
	writeChatGPTAccounts(t, string(data))
}

func TestAuthChecksSavedAccountsAndShowsNumberedRefreshCommands(t *testing.T) {
	writeAuthTestAccounts(t)
	var checked []string
	mockModelCatalogueTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != "https://api.openai.com/v1/models" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		checked = append(checked, r.Header.Get("Authorization"))
		status := http.StatusUnauthorized
		if r.Header.Get("Authorization") == "Bearer first-token" {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"test-model","visibility":"list"}]}`))}, nil
	})
	for _, command := range []string{"accounts", "status"} {
		checked = nil
		output, code := captureTestStdout(t, func() int { return runAuth([]string{command}) })
		if code != 0 || !strings.Contains(output, "* 2  second@example.com  refresh required") ||
			!strings.Contains(output, "To refresh this account, run `pearl auth login 2`.") {
			t.Fatalf("%s: code=%d output=%s", command, code, output)
		}
		if strings.Contains(output, "oaiapp_") || strings.Contains(output, "-token") || strings.Contains(output, "auth login 1") || strings.Contains(output, "--account") {
			t.Fatalf("%s: IDs, credentials, or refresh advice for a valid account: %s", command, output)
		}
		if command == "accounts" {
			if len(checked) != 2 || !strings.Contains(output, "  1  first@example.com  connected") ||
				!strings.Contains(output, "  3  third@example.com  signed out") || !strings.Contains(output, "auth login 3") {
				t.Fatalf("accounts: checked=%v output=%s", checked, output)
			}
		} else if len(checked) != 1 || checked[0] != "Bearer second-token" || strings.Contains(output, "first@example.com") {
			t.Fatalf("status: checked=%v output=%s", checked, output)
		}
		accounts, err := newChatGPTClient().Accounts(context.Background())
		if err != nil || !accounts[1].Active {
			t.Fatalf("checking accounts changed the active sign-in: %v", err)
		}
	}
}

func TestAuthCheckFailureDoesNotAskForRefresh(t *testing.T) {
	writeAuthTestAccounts(t)
	for _, status := range []int{0, http.StatusTooManyRequests, http.StatusInternalServerError} {
		mockModelCatalogueTransport(t, func(*http.Request) (*http.Response, error) {
			if status == 0 {
				return nil, errors.New("offline")
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		output, code := captureTestStdout(t, func() int { return runAuth([]string{"status"}) })
		if code != 0 || !strings.Contains(output, "could not check") || strings.Contains(output, "To refresh this account") || strings.Contains(output, "refresh required") {
			t.Fatalf("status=%d code=%d output=%s", status, code, output)
		}
	}
}

func TestAuthResolvesNumbersAndKeepsRegistrationIDs(t *testing.T) {
	writeAuthTestAccounts(t)
	client := newChatGPTClient()
	for selector, want := range map[string]string{"1": "oaiapp_first", "2": "oaiapp_second", "3": "oaiapp_third", "oaiapp_second": "oaiapp_second"} {
		got, err := resolveAuthAccount(context.Background(), client, selector)
		if err != nil || got != want {
			t.Errorf("selector=%q got=%q err=%v", selector, got, err)
		}
	}
	for _, selector := range []string{"0", "-1", "4"} {
		if _, err := resolveAuthAccount(context.Background(), client, selector); err == nil {
			t.Errorf("accepted out-of-range account %q", selector)
		}
	}
	previous := openAuthBrowser
	opened := 0
	openAuthBrowser = func(address string) error {
		opened++
		parsed, err := url.Parse(address)
		if err != nil || parsed.Query().Get("client_id") != "oaiapp_third" {
			t.Errorf("numeric login did not reuse the signed-out registration")
		}
		// End the login attempt without exchanging credentials.
		callback, _ := url.Parse(parsed.Query().Get("redirect_uri"))
		callback.RawQuery = url.Values{"state": {parsed.Query().Get("state")}, "error": {"access_denied"}}.Encode()
		response, err := http.Get(callback.String())
		if err == nil {
			response.Body.Close()
		}
		return err
	}
	t.Cleanup(func() { openAuthBrowser = previous })
	for _, args := range [][]string{{"login", "3"}, {"login", "chatgpt", "--account", "3"}} {
		before := opened
		if code := runAuth(args); code != 1 || opened != before+1 {
			t.Fatalf("login %v: code=%d browser opened=%d times", args, code, opened-before)
		}
	}
	for _, args := range [][]string{{"login", "0"}, {"login", "-1"}, {"login", "4"}} {
		before := opened
		if code := runAuth(args); code != 1 || opened != before {
			t.Fatalf("invalid login %v: code=%d browser opened=%d times", args, code, opened-before)
		}
	}
	// Selecting by number uses the same saved ordering.
	mockModelCatalogueTransport(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"test-model","visibility":"list"}]}`))}, nil
	})
	if code := runAuth([]string{"use", "1"}); code != 0 {
		t.Fatalf("numeric use returned %d", code)
	}
	accounts, err := client.Accounts(context.Background())
	if err != nil || !accounts[0].Active {
		t.Fatalf("numeric use did not select the first account: %v", err)
	}
}

func TestAuthRecognizesSignInErrors(t *testing.T) {
	if !chatgpt.SignInRequired(chatgpt.ResponseError(401, "")) || !chatgpt.SignInRequired(chatgpt.ResponseError(403, "")) {
		t.Fatal("authentication rejection was not recognized")
	}
}

func TestAuthHelpAndInvalidArguments(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{{"login"}, {"login", "other"}, {"login", "2", "--new-account"}, {"login", "2", "--account", "1"}, {"login", "2", "extra"}, {"login", "chatgpt", "--new-account", "--account", "id"}, {"use"}, {"logout", "extra"}} {
		if code := runAuth(args); code != 2 {
			t.Errorf("args %v exited %d", args, code)
		}
	}
	output, _ := captureTestStdout(t, func() int { return Run([]string{"auth", "--help"}) })
	if !strings.Contains(output, "Continue with ChatGPT") || !strings.Contains(output, "auth login <number> [--no-browser]") {
		t.Error("missing account sign-in help")
	}
}
