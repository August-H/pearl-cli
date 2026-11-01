package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

func runModel(args []string) int {
	var (
		list    bool
		free    bool
		refresh = true
		setID   string
	)
	rest := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		name, value, hasValue := strings.Cut(argument, "=")
		switch name {
		case "--list":
			list = true
		case "--free":
			free = true
		case "--refresh":
			refresh = true
		case "--set":
			if hasValue {
				setID = value
			} else if index+1 < len(args) {
				index++
				setID = args[index]
			} else {
				fmt.Fprintln(os.Stderr, "Usage: pearl model [--list] [--set <model-id>] [--free] [--refresh]")
				return 2
			}
		case "-h", "--help":
			fmt.Println("Usage: pearl model [--list] [--set <model-id>] [--free] [--refresh]")
			fmt.Println("  no flags    Open the interactive model picker")
			fmt.Println("  --list      Print the grouped model list")
			fmt.Println("  --set ID    Set the model directly")
			fmt.Println("  --free      Use the Free Tier (openrouter/free)")
			fmt.Println("  --refresh   Refresh the model list (the default)")
			return 0
		default:
			rest = append(rest, argument)
		}
	}
	if len(rest) != 0 {
		fmt.Fprintln(os.Stderr, "Usage: pearl model [--list] [--set <model-id>] [--free] [--refresh]")
		return 2
	}
	if free && setID != "" {
		fmt.Fprintln(os.Stderr, "Use either --free or --set, not both")
		return 2
	}

	if free {
		if currentProvider() == openrouter_request.ProviderChatGPT {
			return printError("Model", fmt.Errorf("--free is an OpenRouter option; run `pearl provider openrouter` first"))
		}
		return setModel(openrouter_request.FreeTierModel)
	}
	if setID != "" {
		if strings.TrimSpace(setID) == "" {
			fmt.Fprintln(os.Stderr, "Model ID cannot be empty")
			return 2
		}
		return setModel(strings.TrimSpace(setID))
	}

	currentID := currentModelID()
	if list || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return listModels(currentID, refresh)
	}
	return pickModel(currentID, refresh)
}

func currentModelID() string {
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(openrouter_request.AgentModel(settings))
}

func currentProvider() string {
	settings, _ := openrouter_request.LoadAgentSettings()
	return openrouter_request.AgentProvider(settings)
}

func setModel(id string) int {
	if currentProvider() == openrouter_request.ProviderChatGPT {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := newChatGPTClient().Models(ctx)
		if err != nil {
			return printError("Model", err)
		}
		found := false
		for _, m := range models {
			found = found || m.ID == id
		}
		if !found {
			return printError("Model", fmt.Errorf("model %q is not in this ChatGPT account's catalog; run `pearl model --list`", id))
		}
	}
	settingsPath, err := openrouter_request.ConfigureAgentModel(id)
	if err != nil {
		return printError("Model", err)
	}
	fmt.Println("Model:", id)
	fmt.Println("Saved to", settingsPath)
	return 0
}

func resolveModelCatalogue(ctx context.Context, refresh bool) ([]openrouter_request.OpenRouterModel, bool, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if currentProvider() == openrouter_request.ProviderChatGPT {
		models, err := newChatGPTClient().Models(ctx)
		var result []openrouter_request.OpenRouterModel
		for _, m := range models {
			result = append(result, openrouter_request.OpenRouterModel{ID: m.ID, Name: m.Name, ContextLength: m.ContextWindow})
		}
		return result, false, time.Time{}, err
	}
	return openrouter_request.ResolveModels(ctx, refresh)
}

func listModels(currentID string, refresh bool) int {
	models, cached, fetchedAt, err := resolveModelCatalogue(context.Background(), refresh)
	if err != nil {
		return printError("Model", err)
	}
	if currentID != "" {
		fmt.Println("Current:", currentID)
	}
	printGroupedModels(models, currentID)
	if cached && !fetchedAt.IsZero() {
		fmt.Fprintf(os.Stderr, "Could not refresh; showing cached list from %s\n",
			fetchedAt.Local().Format("2006-01-02 15:04"))
	}
	return 0
}

func printGroupedModels(models []openrouter_request.OpenRouterModel, currentID string) {
	rows := buildActiveModelRows(models, currentID)
	for _, lab := range buildModelLabRows(rows) {
		fmt.Println()
		fmt.Println(modelLabTitle(lab))
		for _, row := range rows {
			if row.kind == modelRowOption && row.provider == lab.provider {
				printModelLine(row.id, row.name, row.context, currentID)
			}
		}
	}
}

func printModelLine(id, name, contextLength, currentID string) {
	marker := "  "
	if id == currentID {
		marker = "* "
	}
	line := marker + id
	if short := modelDisplayName(id, name); short != "" {
		line += "  " + short
	}
	if contextLength != "" {
		line += "  (" + contextLength + " ctx)"
	}
	fmt.Println(line)
}

func modelDisplayName(id, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, id) {
		return ""
	}
	if provider := openrouter_request.ModelProvider(id); strings.HasPrefix(name, provider+": ") {
		name = strings.TrimPrefix(name, provider+": ")
	}
	return name
}

func pickModel(currentID string, refresh bool) int {
	fmt.Println("Loading models from", currentProvider()+"...")
	picker, err := loadModelPicker(context.Background(), currentID, refresh)
	if err != nil {
		return printError("Model", err)
	}
	chosen, err := runModelTUI(os.Stdin, os.Stdout, picker.rows, currentID, picker.notice)
	if err != nil {
		return printError("Model", err)
	}
	if chosen == "" {
		fmt.Println("Model unchanged:", displayCurrentModel(currentID))
		return 0
	}
	return setModel(chosen)
}

func loadModelPicker(ctx context.Context, currentID string, refresh bool) (*modelPickerState, error) {
	models, cached, fetchedAt, err := resolveModelCatalogue(ctx, refresh)
	if err != nil {
		return nil, err
	}
	notice := ""
	if cached && !fetchedAt.IsZero() {
		notice = fmt.Sprintf("Could not refresh · cached list from %s",
			fetchedAt.Local().Format("2006-01-02 15:04"))
	}
	return newModelPickerState(buildActiveModelRows(models, currentID), currentID, notice), nil
}

func buildActiveModelRows(models []openrouter_request.OpenRouterModel, currentID string) []modelRow {
	if currentProvider() != openrouter_request.ProviderChatGPT {
		return buildModelRows(models, currentID)
	}
	rows := []modelRow{{kind: modelRowHeader, provider: "openai"}}
	for _, m := range models {
		rows = append(rows, modelRow{kind: modelRowOption, provider: "openai", id: m.ID, name: m.Name, current: m.ID == currentID, context: openrouter_request.FormatContextLength(m.ContextLength), inputPrice: "ChatGPT plan", outputPrice: "ChatGPT plan"})
	}
	return rows
}

func dashboardModelPickerCommand(arguments []string) (interactive, refresh bool) {
	refresh = true
	for _, argument := range arguments {
		if argument != "--refresh" {
			return false, false
		}
		refresh = true
	}
	return true, refresh
}

func displayCurrentModel(currentID string) string {
	if currentID == "" {
		return "(unset)"
	}
	return currentID
}
