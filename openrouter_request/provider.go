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
)

type providerContextKey struct{}

func AgentProvider(settings Settings) string {
	if settings.Provider == "" {
		return ProviderOpenRouter
	}
	return settings.Provider
}

func AgentModel(settings Settings) string {
	if AgentProvider(settings) == ProviderChatGPT {
		return settings.ChatGPTModel
	}
	return settings.Model
}

func ConfigureProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != ProviderOpenRouter && provider != ProviderChatGPT {
		return "", errors.New("provider must be openrouter or chatgpt")
	}
	return configureAgentSetting("provider", provider)
}

func WithChatGPT(ctx context.Context) context.Context {
	return context.WithValue(ctx, providerContextKey{}, chatgpt.NewClient())
}
