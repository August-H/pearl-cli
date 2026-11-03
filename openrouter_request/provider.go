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

// WithModel returns these settings with one run's model chosen for it. An empty
// provider leaves them alone.
func (settings Settings) WithModel(provider, model string) Settings {
	if provider == "" {
		return settings
	}
	settings.Provider = provider
	switch provider {
	case ProviderChatGPT:
		settings.ChatGPTModel = model
	case ProviderClaude:
		settings.ClaudeModel = model
	case ProviderAntigravity:
		settings.AntigravityModel = model
	default:
		settings.Model = model
	}
	return settings
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
