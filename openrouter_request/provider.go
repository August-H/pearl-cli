package openrouter_request

import (
	"context"
	"errors"
	"github.com/August-H/pearl-cli/internal/chatgpt"
	"strings"
)

const (
	ProviderOpenRouter = "openrouter"
	ProviderChatGPT    = "chatgpt"
	// These two use the sign-in of a CLI the user installed.
	ProviderClaude      = "claude"
	ProviderAntigravity = "antigravity"
)

type providerContextKey struct{}

func AgentProvider(settings Settings) string {
	if settings.Provider == "" {
		return ProviderOpenRouter
	}
	return settings.Provider
}

func AgentModel(settings Settings) string {
	switch AgentProvider(settings) {
	case ProviderChatGPT:
		return settings.ChatGPTModel
	case ProviderClaude:
		return strings.TrimSpace(settings.ClaudeModel)
	case ProviderAntigravity:
		return strings.TrimSpace(settings.AntigravityModel)
	}
	return settings.Model
}

func validProvider(provider string) bool {
	switch provider {
	case ProviderOpenRouter, ProviderChatGPT, ProviderClaude, ProviderAntigravity:
		return true
	}
	return false
}

// ConfigureProviderModel selects a model and the provider it belongs to.
func ConfigureProviderModel(provider, model string) (string, error) {
	key := map[string]string{
		ProviderOpenRouter: "model", ProviderChatGPT: "chatgpt_model",
		ProviderClaude: "claude_model", ProviderAntigravity: "antigravity_model",
	}[provider]
	if key == "" {
		return "", errors.New("unknown provider " + provider)
	}
	if strings.TrimSpace(model) == "" {
		return "", errors.New("model cannot be empty")
	}
	if _, err := configureAgentSetting(key, strings.TrimSpace(model)); err != nil {
		return "", err
	}
	return ConfigureProvider(provider)
}

// coordinatorProvider picks the provider for autonomous mode's coordinator. It
// runs in Pearl's own agent loop, which Claude Code and Antigravity sign-ins
// can't drive, so with those selected it uses another connection if there is one.
// The jobs it creates still run with the selected model.
func coordinatorProvider(ctx context.Context, settings Settings) (string, error) {
	if strings.TrimSpace(settings.ChatGPTModel) != "" {
		if accounts, err := chatgpt.NewClient().Accounts(ctx); err == nil {
			for _, account := range accounts {
				if account.Active && account.Connected {
					return ProviderChatGPT, nil
				}
			}
		}
	}
	if _, err := loadOpenRouterAPIKey(); err == nil {
		return ProviderOpenRouter, nil
	}
	return "", errors.New("autonomous mode plans with ChatGPT or OpenRouter, even when its jobs use Claude or Google models; run `pearl auth login chatgpt` or `pearl configure`")
}

func ConfigureProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !validProvider(provider) {
		return "", errors.New("provider must be openrouter, chatgpt, claude or antigravity")
	}
	return configureAgentSetting("provider", provider)
}

func WithChatGPT(ctx context.Context) context.Context {
	return context.WithValue(ctx, providerContextKey{}, chatgpt.NewClient())
}
