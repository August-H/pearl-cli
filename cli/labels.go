package cli

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

// billingLabel names how a run is paid for. Jobs store the plain value.
func billingLabel(provider string) string {
	switch provider {
	case openrouter_request.ProviderChatGPT:
		return "ChatGPT subscription"
	case openrouter_request.ProviderClaude:
		return "Claude subscription"
	case openrouter_request.ProviderAntigravity:
		return "Gemini subscription"
	case openrouter_request.ProviderOpenRouter, daemon.BillingAPI:
		return "API"
	}
	return provider
}

// harnessLabel names a harness. Jobs store the plain value.
func harnessLabel(harness string) string {
	switch harness {
	case store.HarnessPearl:
		return "Pearl"
	case store.HarnessCodex:
		return "Codex"
	case store.HarnessClaude:
		return "Claude Code"
	case store.HarnessAntigravity:
		return "Antigravity"
	}
	return harness
}

// modelLabel is the name to show for a model: "Opus 5.5" rather than "opus". Each
// account names its own models, and the saved list has those names. Jobs store the
// plain ID, and commands such as `pearl model --set` take it.
func modelLabel(id string) string {
	id = strings.TrimSpace(id)
	if name := savedModelNames()[id]; name != "" {
		return name
	}
	return readableModelID(id)
}

var (
	claudeModelID = regexp.MustCompile(`^claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?(?:\[[^\]]*\])?$`)
	gptModelID    = regexp.MustCompile(`^gpt-(\d+(?:\.\d+)?)((?:-[a-z0-9]+)*)$`)
	geminiModelID = regexp.MustCompile(`^gemini-(\d+(?:\.\d+)?)((?:-[a-z0-9]+)*)$`)
)

// readableModelID covers full IDs that aren't in the saved list, such as the
// version a job reports it ran on. Anything else, like an OpenRouter ID or an alias
// with no known version, stays as it is.
func readableModelID(id string) string {
	if match := claudeModelID.FindStringSubmatch(id); match != nil {
		version := match[2]
		if match[3] != "" {
			version += "." + match[3]
		}
		return capitalized(match[1]) + " " + version
	}
	if match := gptModelID.FindStringSubmatch(id); match != nil {
		return "GPT-" + match[1] + modelNameWords(match[2])
	}
	if match := geminiModelID.FindStringSubmatch(id); match != nil {
		words := strings.TrimPrefix(match[2], "-")
		for _, level := range []string{"low", "medium", "high"} {
			if base, found := strings.CutSuffix(words, "-"+level); found {
				return "Gemini " + match[1] + modelNameWords("-"+base) + " (" + capitalized(level) + ")"
			}
		}
		return "Gemini " + match[1] + modelNameWords(match[2])
	}
	return id
}

func modelNameWords(suffix string) string {
	var name strings.Builder
	for _, word := range strings.Split(strings.TrimPrefix(suffix, "-"), "-") {
		if word != "" {
			name.WriteString(" " + capitalized(word))
		}
	}
	return name.String()
}

func capitalized(word string) string {
	if word == "" {
		return ""
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

var modelNames struct {
	sync.Mutex
	path     string
	modified time.Time
	names    map[string]string
}

// savedModelNames rereads the saved list only when it changes, because the
// dashboard asks for every job row on every frame.
func savedModelNames() map[string]string {
	path, err := modelCatalogCachePath()
	if err != nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	modelNames.Lock()
	defer modelNames.Unlock()
	if modelNames.path == path && modelNames.modified.Equal(info.ModTime()) {
		return modelNames.names
	}
	catalogs, _, _ := readSavedModelCatalogs()
	names := map[string]string{}
	for _, catalog := range catalogs {
		for _, model := range catalog.models {
			if model.Name != "" {
				names[model.ID] = model.Name
			}
		}
	}
	modelNames.path, modelNames.modified, modelNames.names = path, info.ModTime(), names
	return names
}
