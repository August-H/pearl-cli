package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/filelock"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

type modelCatalogueTransportFunc func(*http.Request) (*http.Response, error)

func (transport modelCatalogueTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func mockModelCatalogueTransport(t *testing.T, transport modelCatalogueTransportFunc) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func readTestSettingsModel(t *testing.T, dir string) string {
	t.Helper()
	contents, err := os.ReadFile(dir + "/settings.json")
	if err != nil {
		// PEARL_CONFIG_DIR may resolve through the OS config dir on some
		// platforms; fall back to the durable settings loader.
		settings, loadErr := openrouter_request.LoadAgentSettings()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return settings.Model
	}
	var settings openrouter_request.Settings
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	return settings.Model
}

func TestModelListAndPickerKeepVisibleCachedModels(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	mockModelCatalogueTransport(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})
	models := recentTestModels([]openrouter_request.OpenRouterModel{
		{ID: "openai/gpt-5.5"}, {ID: "anthropic/claude-fable-5.1"},
		{ID: "openai/gpt-5"}, {ID: "openai/gpt-5:free"},
		{ID: "openai/gpt-5:batch"}, {ID: "anthropic/claude-fable-5.1:batch"},
		{ID: "openai/gpt-5-pro"}, {ID: "openai/gpt-6.1-sol-pro:free"},
	})
	contents, err := json.Marshal(struct {
		FetchedAt int64                                `json:"fetched_at"`
		Models    []openrouter_request.OpenRouterModel `json:"models"`
	}{FetchedAt: time.Now().Unix(), Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "models_cache.json"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openrouter_request.ConfigureAgentModel("openai/gpt-5"); err != nil {
		t.Fatal(err)
	}
	output, exitCode := captureTestStdout(t, func() int { return Run([]string{"model", "--list"}) })
	if exitCode != 0 {
		t.Fatalf("model --list exit=%d output=%q", exitCode, output)
	}
	for _, model := range models {
		if strings.HasSuffix(model.ID, ":batch") || strings.Contains(model.ID, "-pro") {
			if strings.Contains(output, model.ID) {
				t.Fatalf("model list includes hidden model %s:\n%s", model.ID, output)
			}
			continue
		}
		if !strings.Contains(output, model.ID) {
			t.Fatalf("model list dropped %s:\n%s", model.ID, output)
		}
	}
	picker, err := loadModelPicker(context.Background(), "openai/gpt-5", true)
	if err != nil {
		t.Fatal(err)
	}
	picker.activate()
	if len(picker.visible) != 3 || picker.visible[picker.selected].id != "openai/gpt-5" {
		t.Fatalf("cached picker = %#v", picker)
	}
	if !strings.Contains(picker.notice, "Could not refresh") {
		t.Fatalf("missing cache fallback notice: %q", picker.notice)
	}
}

func TestModelListAndPickerHideMediaAndOldModelsFromLiveAndCachedCatalogues(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	if _, err := openrouter_request.ConfigureAgentModel(openrouter_request.FreeTierModel); err != nil {
		t.Fatal(err)
	}
	offline := false
	mockModelCatalogueTransport(t, func(*http.Request) (*http.Response, error) {
		if offline {
			return nil, errors.New("offline")
		}
		body := fmt.Sprintf(`{"data":[
			{"id":"openai/chat","created":%[1]d,"architecture":{"input_modalities":["text"],"output_modalities":["text"]}},
			{"id":"openai/vision-chat","created":%[1]d,"architecture":{"input_modalities":["text","image","video","audio"],"output_modalities":["text"]}},
			{"id":"google/opaque-a","created":%[1]d,"architecture":{"input_modalities":["text"],"output_modalities":["text","image"]}},
			{"id":"google/opaque-b","created":%[1]d,"architecture":{"input_modalities":["text"],"output_modalities":["video"]}},
			{"id":"google/opaque-c","created":%[1]d,"architecture":{"input_modalities":["text"],"output_modalities":["audio","text"]}},
			{"id":"speech/transcriber","created":%[1]d,"architecture":{"input_modalities":["audio"],"output_modalities":["text"]}},
			{"id":"legacy/plain","created":%[1]d},
			{"id":"legacy/gpt-image","created":%[1]d},
			{"id":"legacy/gpt-audio","created":%[1]d},
			{"id":"legacy/video-model","created":%[1]d},
			{"id":"oldlab/obsolete","created":%[2]d},
			{"id":"oldlab/undated"},
			{"id":"oldlab/future","created":%[3]d}
		]}`, time.Now().Add(-time.Hour).Unix(), time.Now().AddDate(-1, -6, 0).Unix(), time.Now().Add(time.Hour).Unix())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	output, code := captureTestStdout(t, func() int { return Run([]string{"model", "--list"}) })
	if code != 0 {
		t.Fatalf("live model list exit=%d output=%q", code, output)
	}
	want := []string{openrouter_request.FreeTierModel, "openai/chat", "openai/vision-chat", "legacy/plain"}
	for _, id := range want {
		if !strings.Contains(output, id) {
			t.Fatalf("text model %s missing from list:\n%s", id, output)
		}
	}
	for _, id := range []string{"google/opaque-a", "google/opaque-b", "google/opaque-c", "speech/transcriber", "legacy/gpt-image", "legacy/gpt-audio", "legacy/video-model", "oldlab/obsolete", "oldlab/undated", "oldlab/future"} {
		if strings.Contains(output, id) {
			t.Fatalf("hidden model %s remains in list:\n%s", id, output)
		}
	}
	offline = true
	// Selecting a hidden model previously must not reintroduce it as a custom
	// option, even when the live fetch fails and the picker falls back to cache.
	for _, currentID := range []string{"google/opaque-a", "openai/gpt-image-unknown", "oldlab/obsolete", "oldlab/undated"} {
		picker, err := loadModelPicker(context.Background(), currentID, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(picker.notice, "Could not refresh") || len(picker.visible) != 3 {
			t.Fatalf("cached lab menu = %#v", picker)
		}
		var got []string
		for _, row := range picker.rows {
			if row.kind == modelRowOption {
				got = append(got, row.id)
			}
		}
		// Rows retain provider order; lab menus independently pin favorites.
		if strings.Join(got, ",") != strings.Join([]string{want[0], want[3], want[1], want[2]}, ",") {
			t.Fatalf("cached text models = %v", got)
		}
		for index, lab := range picker.visible {
			if lab.provider == "openai" {
				picker.selected = index
				break
			}
		}
		picker.activate()
		picker.applyAction("end")
		if id := picker.activate(); id != "openai/vision-chat" {
			t.Fatalf("text model selection = %q", id)
		}
	}
}

func TestModelRefreshesOnEveryRunAndPickerOpen(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	if _, err := openrouter_request.ConfigureAgentModel(openrouter_request.FreeTierModel); err != nil {
		t.Fatal(err)
	}
	requests := 0
	mockModelCatalogueTransport(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != openrouter_request.OpenRouterModelsURL {
			return nil, fmt.Errorf("unexpected request: %s", request.URL)
		}
		requests++
		body := fmt.Sprintf(`{"data":[{"id":"anthropic/refreshed-%d","name":"Anthropic: Refreshed %d","created":%d}]}`, requests, requests, time.Now().Add(-time.Hour).Unix())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	for run := 1; run <= 2; run++ {
		output, code := captureTestStdout(t, func() int { return Run([]string{"model", "--list"}) })
		if code != 0 || requests != run || !strings.Contains(output, fmt.Sprintf("anthropic/refreshed-%d", run)) {
			t.Fatalf("run %d exit=%d requests=%d output=%q", run, code, requests, output)
		}
	}
	picker, err := loadModelPicker(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	picker.activate()
	if requests != 3 || len(picker.visible) != 1 || picker.visible[0].id != "anthropic/refreshed-3" || picker.notice != "" {
		t.Fatalf("picker did not refresh: requests=%d picker=%#v", requests, picker)
	}
}

func TestDashboardModelCommandUsesPickerOnlyForBrowsing(t *testing.T) {
	for _, test := range []struct {
		arguments            []string
		interactive, refresh bool
	}{
		{nil, true, false},
		{[]string{"--refresh"}, true, true},
		{[]string{"--list"}, false, false},
		{[]string{"--free"}, false, false},
		{[]string{"--set", "custom/model"}, false, false},
		{[]string{"--refresh", "--list"}, false, false},
		{[]string{"--bogus"}, false, false},
	} {
		interactive, refresh := dashboardModelPickerCommand(test.arguments)
		if interactive != test.interactive || refresh != test.refresh {
			t.Fatalf("model %v picker=%v refresh=%v", test.arguments, interactive, refresh)
		}
	}
}

func TestModelFreeSetsFreeTier(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	if _, err := openrouter_request.ConfigureProvider(openrouter_request.ProviderOpenRouter); err != nil {
		t.Fatal(err)
	}
	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"model", "--free"})
	})
	if exitCode != 0 {
		t.Fatalf("model --free exit=%d output=%q", exitCode, output)
	}
	if !strings.Contains(output, "openrouter/free") {
		t.Fatalf("model --free output=%q", output)
	}
	if got := readTestSettingsModel(t, configDirectory); got != "openrouter/free" {
		t.Fatalf("settings model = %q", got)
	}
}

func TestModelSetSavesCustomID(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", configDirectory)
	if _, err := openrouter_request.ConfigureProvider(openrouter_request.ProviderOpenRouter); err != nil {
		t.Fatal(err)
	}
	output, exitCode := captureTestStdout(t, func() int {
		return Run([]string{"model", "--set", "anthropic/claude-fable-5.1"})
	})
	if exitCode != 0 {
		t.Fatalf("model --set exit=%d output=%q", exitCode, output)
	}
	if !strings.Contains(output, "anthropic/claude-fable-5.1") {
		t.Fatalf("model --set output=%q", output)
	}
	if got := readTestSettingsModel(t, configDirectory); got != "anthropic/claude-fable-5.1" {
		t.Fatalf("settings model = %q", got)
	}
}

func TestModelRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{"model", "--bogus"},
		{"model", "extra-arg"},
		{"model", "--free", "--set", "x"},
		{"model", "--set"},
	} {
		if exitCode := Run(args); exitCode != 2 {
			t.Fatalf("Run(%v) exit=%d, want 2", args, exitCode)
		}
	}
}

func TestMain(m *testing.M) {
	// Tests must not depend on what's signed in on this machine.
	loadModelCatalogs = func(context.Context) []modelCatalog { return nil }
	checkClaudeSignIn = func(context.Context) bool { return false }
	// Nor may they touch its real config, or start the test binary as a refresh.
	startModelCatalogRefresh = func() {}
	configDirectory, err := os.MkdirTemp("", "pearl-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("PEARL_CONFIG_DIR", configDirectory)
	code := m.Run()
	os.RemoveAll(configDirectory)
	os.Exit(code)
}

func testModelCatalogs() []modelCatalog {
	return []modelCatalog{
		{lab: "anthropic", provider: openrouter_request.ProviderClaude, login: "claude", connected: true,
			models: []daemon.AgentModel{{ID: "opus", Name: "Opus 5.5"}, {ID: "haiku", Name: "Haiku 5.5"}}},
		{lab: "openai", provider: openrouter_request.ProviderChatGPT, login: "chatgpt", connected: true,
			models: []daemon.AgentModel{{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol"}}},
		{lab: "google", provider: openrouter_request.ProviderAntigravity, login: "antigravity", connected: true,
			models: []daemon.AgentModel{{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)"}}},
	}
}

func useTestModelCatalogs(t *testing.T, catalogs []modelCatalog) {
	t.Helper()
	original := loadModelCatalogs
	loadModelCatalogs = func(context.Context) []modelCatalog { return catalogs }
	t.Cleanup(func() { loadModelCatalogs = original })
}

func writeModelTestSettings(t *testing.T, settings string) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("PEARL_CONFIG_DIR", directory)
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestModelMenuShowsEverySignedInLab(t *testing.T) {
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openrouter/free","chatgpt_model":"gpt-6.1-sol"}`)
	useTestModelCatalogs(t, testModelCatalogs())
	picker, err := loadModelPicker(context.Background(), "gpt-6.1-sol", true)
	if err != nil {
		t.Fatal(err)
	}
	var labs []string
	for _, lab := range picker.visible {
		labs = append(labs, lab.provider+":"+lab.name)
	}
	if got := strings.Join(labs, ","); got != "anthropic:2 models,openai:1 model,google:1 model" {
		t.Fatalf("labs = %s", got)
	}
	current := ""
	for _, row := range picker.rows {
		if row.current {
			current += row.id
		}
	}
	if current != "gpt-6.1-sol" {
		t.Fatalf("current = %q", current)
	}
	if picker.notice != "" {
		t.Fatalf("notice = %q", picker.notice)
	}
}

func TestChoosingAModelSelectsItsProviderAndHarness(t *testing.T) {
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openrouter/free","chatgpt_model":"gpt-6.1-sol"}`)
	rows := connectedModelRows(testModelCatalogs(), openrouter_request.ProviderChatGPT, "gpt-6.1-sol")
	for _, test := range []struct{ id, provider, harness string }{
		{"opus", openrouter_request.ProviderClaude, store.HarnessClaude},
		{"gemini-3.8-flash-high", openrouter_request.ProviderAntigravity, store.HarnessAntigravity},
		{"gpt-6.1-sol", openrouter_request.ProviderChatGPT, store.HarnessCodex},
	} {
		if _, err := saveModelChoice(rows, test.id); err != nil {
			t.Fatal(err)
		}
		settings, err := openrouter_request.LoadAgentSettings()
		if err != nil {
			t.Fatal(err)
		}
		if settings.Provider != test.provider || openrouter_request.AgentModel(settings) != test.id {
			t.Fatalf("after choosing %s: provider=%s model=%s", test.id, settings.Provider, openrouter_request.AgentModel(settings))
		}
		if harness := daemon.DefaultHarness(); harness != test.harness {
			t.Fatalf("after choosing %s: harness = %s, want %s", test.id, harness, test.harness)
		}
	}
	// Each provider remembers its own model.
	settings, _ := openrouter_request.LoadAgentSettings()
	if settings.ClaudeModel != "opus" || settings.AntigravityModel != "gemini-3.8-flash-high" || settings.Model != "openrouter/free" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestModelSetSwitchesProviderAndRejectsUnknownModels(t *testing.T) {
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openrouter/free","chatgpt_model":"gpt-6.1-sol"}`)
	useTestModelCatalogs(t, testModelCatalogs())
	if output, code := captureTestStdout(t, func() int { return Run([]string{"model", "--set", "haiku"}) }); code != 0 || !strings.Contains(output, "Model: Haiku 5.5") {
		t.Fatalf("set = %d %q", code, output)
	}
	if harness := daemon.DefaultHarness(); harness != store.HarnessClaude {
		t.Fatalf("harness = %s", harness)
	}
	if code := Run([]string{"model", "--set", "not-a-model"}); code == 0 {
		t.Fatal("accepted a model no account offers")
	}
	if settings, _ := openrouter_request.LoadAgentSettings(); settings.ClaudeModel != "haiku" {
		t.Fatalf("a rejected model changed the settings: %+v", settings)
	}
}

func TestModelListPrintsEveryLab(t *testing.T) {
	writeModelTestSettings(t, `{"provider":"claude","model":"openrouter/free","claude_model":"opus"}`)
	useTestModelCatalogs(t, testModelCatalogs())
	output, code := captureTestStdout(t, func() int { return Run([]string{"model", "--list"}) })
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"Current: opus", "Anthropic", "* opus  Opus 5.5", "OpenAI", "gpt-6.1-sol", "Google", "gemini-3.8-flash-high"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q:\n%s", want, output)
		}
	}
}

func TestModelMenuExplainsWhatIsMissing(t *testing.T) {
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openrouter/free","chatgpt_model":"gpt-6.1-sol"}`)
	catalogs := testModelCatalogs()
	catalogs[0].connected, catalogs[0].models = false, nil
	catalogs[2].models, catalogs[2].err = nil, errors.New("server down")
	useTestModelCatalogs(t, catalogs)
	picker, err := loadModelPicker(context.Background(), "gpt-6.1-sol", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(picker.visible) != 1 || picker.visible[0].provider != "openai" {
		t.Fatalf("labs = %+v", picker.visible)
	}
	for _, want := range []string{"Couldn't load Google models: server down", "pearl auth login claude"} {
		if !strings.Contains(picker.notice, want) {
			t.Fatalf("notice = %q", picker.notice)
		}
	}
	for index := range catalogs {
		catalogs[index].connected, catalogs[index].models, catalogs[index].err = false, nil, nil
	}
	if _, err := loadModelPicker(context.Background(), "gpt-6.1-sol", true); err == nil || !strings.Contains(err.Error(), "pearl auth login claude|chatgpt|antigravity") {
		t.Fatalf("no sign-ins error = %v", err)
	}
}

func TestHarnessCommandAndFlagAreGone(t *testing.T) {
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openrouter/free","chatgpt_model":"gpt-6.1-sol"}`)
	if code := Run([]string{"harness"}); code == 0 {
		t.Fatal("pearl harness still runs")
	}
	for _, job := range [][]string{
		{"job", "--harness", "codex", "fix it"},
		{"job", "fix it", "--harness", "codex"},
		{"job", "fix it", "--harness=codex"},
	} {
		// Failing matters: after the prompt, it would silently join the prompt.
		if code := Run(job); code == 0 {
			t.Fatalf("pearl %v still works", job)
		}
	}
	if code := runProvider([]string{"claude"}); code == 0 {
		t.Fatal("pearl provider should point at pearl model for Claude")
	}
}

func stubModelCatalogRefresh(t *testing.T) *int {
	t.Helper()
	started := 0
	original := startModelCatalogRefresh
	startModelCatalogRefresh = func() { started++ }
	t.Cleanup(func() { startModelCatalogRefresh = original })
	return &started
}

func saveTestModelCatalogs(t *testing.T, age time.Duration) {
	t.Helper()
	if err := saveModelCatalogs(testModelCatalogs()); err != nil {
		t.Fatal(err)
	}
	path, err := modelCatalogCachePath()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved savedModelCatalogs
	if err := json.Unmarshal(contents, &saved); err != nil {
		t.Fatal(err)
	}
	saved.FetchedAt = time.Now().Add(-age)
	contents, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func failIfModelsLoad(t *testing.T) {
	t.Helper()
	original := loadModelCatalogs
	loadModelCatalogs = func(context.Context) []modelCatalog {
		t.Error("loaded the models instead of using the saved list")
		return nil
	}
	t.Cleanup(func() { loadModelCatalogs = original })
}

func TestModelPickerOpensFromTheSavedListWithoutWaiting(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	started := stubModelCatalogRefresh(t)
	saveTestModelCatalogs(t, time.Minute)
	failIfModelsLoad(t)
	picker, err := loadModelPicker(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !modelRowsInclude(picker.rows, "opus") || !modelRowsInclude(picker.rows, "gemini-3.8-flash-high") {
		t.Fatalf("rows=%v", picker.rows)
	}
	if *started != 0 {
		t.Fatalf("a recent list was checked again %d times", *started)
	}
}

func TestModelPickerChecksAnOldListInTheBackground(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	started := stubModelCatalogRefresh(t)
	saveTestModelCatalogs(t, modelCatalogMaxAge+time.Minute)
	failIfModelsLoad(t)
	picker, err := loadModelPicker(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !modelRowsInclude(picker.rows, "opus") || *started != 1 {
		t.Fatalf("rows=%v started=%d", picker.rows, *started)
	}
}

func TestModelPickerWaitsWhenNothingIsSaved(t *testing.T) {
	for name, contents := range map[string]string{
		"missing": "", "corrupt": "{not json", "empty": `{"catalogs":[{"provider":"claude"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
			started := stubModelCatalogRefresh(t)
			if contents != "" {
				path, err := modelCatalogCachePath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			useTestModelCatalogs(t, testModelCatalogs())
			picker, err := loadModelPicker(context.Background(), "", false)
			if err != nil {
				t.Fatal(err)
			}
			if !modelRowsInclude(picker.rows, "opus") || *started != 0 {
				t.Fatalf("rows=%v started=%d", picker.rows, *started)
			}
			if _, _, ok := readSavedModelCatalogs(); !ok {
				t.Fatal("the list wasn't saved for next time")
			}
		})
	}
}

func TestModelRefreshFlagWaitsForANewList(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	saveTestModelCatalogs(t, time.Minute)
	useTestModelCatalogs(t, []modelCatalog{{lab: "anthropic", provider: "claude", connected: true,
		models: []daemon.AgentModel{{ID: "brand-new"}}}})
	picker, err := loadModelPicker(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !modelRowsInclude(picker.rows, "brand-new") || modelRowsInclude(picker.rows, "opus") {
		t.Fatalf("rows=%v", picker.rows)
	}
}

func TestModelRefreshKeepsAListWhenACheckFails(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	saveTestModelCatalogs(t, time.Hour)
	useTestModelCatalogs(t, []modelCatalog{
		{lab: "anthropic", provider: "claude", connected: true, err: errors.New("timed out")},
		{lab: "openai", provider: "chatgpt", login: "chatgpt"},
		{lab: "google", provider: "antigravity", connected: true, err: errors.New("no route")},
	})
	refreshModelCatalogs(context.Background())
	saved, fetchedAt, ok := readSavedModelCatalogs()
	if !ok || time.Since(fetchedAt) > time.Minute {
		t.Fatalf("ok=%v fetchedAt=%v", ok, fetchedAt)
	}
	byProvider := map[string]modelCatalog{}
	for _, catalog := range saved {
		byProvider[catalog.provider] = catalog
	}
	if len(byProvider["claude"].models) != 2 || byProvider["claude"].err != nil {
		t.Errorf("a failed check lost the list: %+v", byProvider["claude"])
	}
	if len(byProvider["chatgpt"].models) != 0 {
		t.Errorf("a signed-out account kept its list: %+v", byProvider["chatgpt"])
	}
}

func TestSavedModelListKeepsAFailureWithNoList(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	if err := saveModelCatalogs([]modelCatalog{
		{lab: "anthropic", provider: "claude", connected: true, models: []daemon.AgentModel{{ID: "opus"}}},
		{lab: "google", provider: "antigravity", connected: true, err: errors.New("no route")},
	}); err != nil {
		t.Fatal(err)
	}
	saved, _, ok := readSavedModelCatalogs()
	if !ok || !strings.Contains(modelCatalogNotice(saved), "Couldn't load Google models: no route") {
		t.Fatalf("ok=%v notice=%q", ok, modelCatalogNotice(saved))
	}
}

func TestModelSetUsesTheSavedListThenChecks(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	writeModelTestSettings(t, `{"provider":"chatgpt","model":"openai/gpt-5","chatgpt_model":"gpt-6.1-sol"}`)
	saveTestModelCatalogs(t, time.Hour)
	failIfModelsLoad(t)
	if _, exitCode := captureTestStdout(t, func() int { return setModel("opus") }); exitCode != 0 {
		t.Fatalf("exit=%d", exitCode)
	}
	settings, err := openrouter_request.LoadAgentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Provider != openrouter_request.ProviderClaude || settings.ClaudeModel != "opus" {
		t.Fatalf("settings=%+v", settings)
	}

	useTestModelCatalogs(t, []modelCatalog{{lab: "anthropic", provider: "claude", connected: true,
		models: []daemon.AgentModel{{ID: "brand-new"}}}})
	if _, exitCode := captureTestStdout(t, func() int { return setModel("brand-new") }); exitCode != 0 {
		t.Fatalf("a model missing from the saved list wasn't rechecked: exit=%d", exitCode)
	}
}

func TestSigningInOrOutClearsTheSavedList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Claude Code script needs a POSIX shell")
	}
	script := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEARL_CLAUDE_PATH", script)
	for _, action := range []string{"login", "logout"} {
		t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
		saveTestModelCatalogs(t, time.Minute)
		captureTestStdout(t, func() int { return claudeAuthCommand(action) })
		if _, _, ok := readSavedModelCatalogs(); ok {
			t.Errorf("%s left the old list", action)
		}
	}
}

func TestModelCacheUpdateSavesAndSkipsWhenAnotherIsRunning(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	useTestModelCatalogs(t, testModelCatalogs())
	path, err := modelCatalogCachePath()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if locked, err := filelock.TryLock(lock); err != nil || !locked {
		t.Fatalf("locked=%v err=%v", locked, err)
	}
	if exitCode := runModel([]string{"--update-cache"}); exitCode != 0 {
		t.Fatalf("exit=%d", exitCode)
	}
	if _, _, ok := readSavedModelCatalogs(); ok {
		t.Fatal("ran while another update held the lock")
	}
	filelock.Unlock(lock)
	if exitCode := runModel([]string{"--update-cache"}); exitCode != 0 {
		t.Fatalf("exit=%d", exitCode)
	}
	if _, _, ok := readSavedModelCatalogs(); !ok {
		t.Fatal("the update didn't save the list")
	}
}
