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
	"strings"
	"testing"
	"time"

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
		{nil, true, true},
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
