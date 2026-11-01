package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestAuthHelpAndInvalidArguments(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{{"login"}, {"login", "other"}, {"login", "chatgpt", "--new-account", "--account", "id"}, {"use"}, {"logout", "extra"}} {
		if code := runAuth(args); code != 2 {
			t.Errorf("args %v exited %d", args, code)
		}
	}
	output, _ := captureTestStdout(t, func() int { return Run([]string{"auth", "--help"}) })
	if !strings.Contains(output, "Continue with ChatGPT") || !strings.Contains(output, "--no-browser") {
		t.Error("missing account sign-in help")
	}
}
