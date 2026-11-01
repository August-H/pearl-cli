package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/openrouter_request"
	"golang.org/x/term"
)

func runModel(args []string) int {
	var (
		list    bool
		free    bool
		refresh bool
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
		case "--update-cache":
			// Not for people: this is how the picker checks in the background.
			return updateModelCatalogCache()
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
			fmt.Println("  --refresh   Wait for an up-to-date list instead of using the saved one")
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
		if currentProvider() != openrouter_request.ProviderOpenRouter {
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

// setModel selects a model by ID. A model from a signed-in connection also
// selects that connection. Otherwise the ID is an OpenRouter one.
func setModel(id string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Only wait for a new list if the saved one doesn't have this model.
	var rows []modelRow
	if catalogs, _, ok := readSavedModelCatalogs(); ok {
		rows = connectedModelRows(catalogs, currentProvider(), id)
	}
	known := modelRowsInclude(rows, id)
	if !known {
		rows = connectedModelRows(refreshModelCatalogs(ctx), currentProvider(), id)
		known = modelRowsInclude(rows, id)
	}
	if !known && currentProvider() != openrouter_request.ProviderOpenRouter {
		return printError("Model", fmt.Errorf("model %q isn't offered by your signed-in accounts; run `pearl model --list`", id))
	}
	settingsPath, err := saveModelChoice(rows, id)
	if err != nil {
		return printError("Model", err)
	}
	fmt.Println("Model:", modelLabel(id))
	fmt.Println("Saved to", settingsPath)
	return 0
}

func modelRowsInclude(rows []modelRow, id string) bool {
	for _, row := range rows {
		if row.kind == modelRowOption && row.id == id {
			return true
		}
	}
	return false
}

// saveModelChoice saves a chosen model. The harness follows from its provider.
func saveModelChoice(rows []modelRow, id string) (string, error) {
	for _, row := range rows {
		if row.kind == modelRowOption && row.id == id && row.connection != "" {
			return openrouter_request.ConfigureProviderModel(row.connection, id)
		}
	}
	return openrouter_request.ConfigureAgentModel(id)
}

// modelCatalog is what one sign-in offers.
type modelCatalog struct {
	lab       string
	provider  string
	login     string
	connected bool
	models    []daemon.AgentModel
	err       error
}

// loadModelCatalogs asks every connection for its models at once, so it takes as
// long as the slowest one.
var loadModelCatalogs = func(ctx context.Context) []modelCatalog {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	catalogs := []modelCatalog{
		{lab: "anthropic", provider: openrouter_request.ProviderClaude, login: "claude"},
		{lab: "openai", provider: openrouter_request.ProviderChatGPT, login: "chatgpt"},
		{lab: "google", provider: openrouter_request.ProviderAntigravity, login: "antigravity"},
	}
	load := map[string]func(*modelCatalog){
		openrouter_request.ProviderClaude: func(catalog *modelCatalog) {
			if catalog.connected = daemon.ClaudeSignedIn(ctx); catalog.connected {
				catalog.models, catalog.err = daemon.ClaudeModels(ctx)
			}
		},
		openrouter_request.ProviderChatGPT: func(catalog *modelCatalog) {
			*catalog = loadChatGPTCatalog(ctx)
		},
		openrouter_request.ProviderAntigravity: func(catalog *modelCatalog) {
			if _, err := daemon.FindAntigravity(); err == nil && daemon.AntigravitySignedIn() {
				catalog.connected = true
				catalog.models, catalog.err = daemon.AntigravityModels(ctx)
			}
		},
	}
	var wait sync.WaitGroup
	for index := range catalogs {
		wait.Add(1)
		go func(catalog *modelCatalog) {
			defer wait.Done()
			load[catalog.provider](catalog)
		}(&catalogs[index])
	}
	wait.Wait()
	return catalogs
}

func loadChatGPTCatalog(ctx context.Context) modelCatalog {
	catalog := modelCatalog{lab: "openai", provider: openrouter_request.ProviderChatGPT, login: "chatgpt"}
	client := newChatGPTClient()
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return catalog
	}
	for _, account := range accounts {
		catalog.connected = catalog.connected || account.Active && account.Connected
	}
	if catalog.connected {
		models, err := client.Models(ctx)
		catalog.err = err
		for _, model := range models {
			name := model.Name
			if name == "" {
				name = readableModelID(model.ID)
			}
			catalog.models = append(catalog.models, daemon.AgentModel{ID: model.ID, Name: name})
		}
	}
	return catalog
}

// connectedModelRows lists the models from every signed-in connection, by lab.
func connectedModelRows(catalogs []modelCatalog, provider, currentID string) []modelRow {
	var rows []modelRow
	for _, catalog := range catalogs {
		if len(catalog.models) == 0 {
			continue
		}
		rows = append(rows, modelRow{kind: modelRowHeader, provider: catalog.lab})
		for _, model := range catalog.models {
			rows = append(rows, modelRow{
				kind: modelRowOption, provider: catalog.lab, connection: catalog.provider,
				id: model.ID, name: model.Name,
				current: catalog.provider == provider && model.ID == currentID,
			})
		}
	}
	return rows
}

// modelCatalogNotice says what's missing from the list and why.
func modelCatalogNotice(catalogs []modelCatalog) string {
	var parts, logins []string
	for _, catalog := range catalogs {
		if catalog.err != nil {
			parts = append(parts, "Couldn't load "+modelLabDisplayName(catalog.lab)+" models: "+dashboardText(catalog.err.Error()))
		} else if !catalog.connected {
			logins = append(logins, catalog.login)
		}
	}
	if len(logins) > 0 {
		parts = append(parts, "Sign in for more models: pearl auth login "+strings.Join(logins, "|"))
	}
	return strings.Join(parts, " · ")
}

// modelChoices returns what `pearl model` offers: the models of every signed-in
// connection. OpenRouter's catalog is the fallback for people who aren't signed
// in to any, and it needs OpenRouter selected.
func modelChoices(ctx context.Context, currentID string, refresh bool) ([]modelRow, string, error) {
	catalogs := currentModelCatalogs(ctx, refresh)
	provider := currentProvider()
	if rows := connectedModelRows(catalogs, provider, currentID); len(rows) > 0 {
		return rows, modelCatalogNotice(catalogs), nil
	}
	if provider != openrouter_request.ProviderOpenRouter {
		if notice := modelCatalogNotice(catalogs); notice != "" {
			return nil, "", errors.New(notice)
		}
		return nil, "", errors.New("no models are available from your signed-in accounts")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, cached, fetchedAt, err := openrouter_request.ResolveModels(ctx, true)
	if err != nil {
		return nil, "", err
	}
	notice := modelCatalogNotice(catalogs)
	if cached && !fetchedAt.IsZero() {
		notice = fmt.Sprintf("Could not refresh · cached list from %s", fetchedAt.Local().Format("2006-01-02 15:04"))
	}
	return buildModelRows(models, currentID), notice, nil
}

func listModels(currentID string, refresh bool) int {
	rows, notice, err := modelChoices(context.Background(), currentID, refresh)
	if err != nil {
		return printError("Model", err)
	}
	if currentID != "" {
		fmt.Println("Current:", currentID)
	}
	printGroupedModels(rows, currentID)
	if notice != "" {
		fmt.Fprintln(os.Stderr, notice)
	}
	return 0
}

func printGroupedModels(rows []modelRow, currentID string) {
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
	fmt.Println("Loading models...")
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
	settingsPath, err := saveModelChoice(picker.rows, chosen)
	if err != nil {
		return printError("Model", err)
	}
	fmt.Println("Model:", modelLabel(chosen))
	fmt.Println("Saved to", settingsPath)
	return 0
}

func loadModelPicker(ctx context.Context, currentID string, refresh bool) (*modelPickerState, error) {
	rows, notice, err := modelChoices(ctx, currentID, refresh)
	if err != nil {
		return nil, err
	}
	return newModelPickerState(rows, currentID, notice), nil
}

func dashboardModelPickerCommand(arguments []string) (interactive, refresh bool) {
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
	return modelLabel(currentID)
}
