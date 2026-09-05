package openrouter_request

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// OpenRouterModelsURL lists every model available through OpenRouter.
	OpenRouterModelsURL = "https://openrouter.ai/api/v1/models"
	// FreeTierModel is Pearl's free-router default.
	FreeTierModel = "openrouter/free"

	modelsCacheFilename = "models_cache.json"
	modelsCacheTTL      = 24 * time.Hour
	modelsFetchTimeout  = 15 * time.Second
	modelsMaxBytes      = 8 << 20
)

// OpenRouterModel is the subset of OpenRouter's /models response Pearl needs.
type OpenRouterModel struct {
	ID            string
	Name          string
	ContextLength int64
	Created       int64
}

type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Created       int64  `json:"created"`
		ContextLength int64  `json:"context_length"`
	} `json:"data"`
}

// FetchModels returns the live OpenRouter model catalogue.
func FetchModels(ctx context.Context) ([]OpenRouterModel, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, OpenRouterModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "pearl-cli")
	// The catalogue is public; a configured key is only a nicety.
	if apiKey, err := loadOpenRouterAPIKey(); err == nil && apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: modelsFetchTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenRouter models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf(
			"OpenRouter models returned %s: %s",
			response.Status, strings.TrimSpace(string(body)),
		)
	}
	var decoded openRouterModelsResponse
	if err := json.NewDecoder(
		io.LimitReader(response.Body, modelsMaxBytes),
	).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode OpenRouter models: %w", err)
	}
	models := make([]OpenRouterModel, 0, len(decoded.Data))
	for _, entry := range decoded.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(models, OpenRouterModel{
			ID:            id,
			Name:          strings.TrimSpace(entry.Name),
			ContextLength: entry.ContextLength,
			Created:       entry.Created,
		})
	}
	return models, nil
}

type cachedModelsFile struct {
	FetchedAt int64             `json:"fetched_at"`
	Models    []OpenRouterModel `json:"models"`
}

func modelsCachePath() string {
	files := pearlConfigFiles(modelsCacheFilename, false)
	if len(files) == 0 {
		return ""
	}
	return files[0]
}

// LoadCachedModels returns the cached catalogue when it is at most maxAge old.
func LoadCachedModels(maxAge time.Duration) ([]OpenRouterModel, time.Time, bool) {
	path := modelsCachePath()
	if path == "" {
		return nil, time.Time{}, false
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var cached cachedModelsFile
	if err := json.Unmarshal(contents, &cached); err != nil || len(cached.Models) == 0 {
		return nil, time.Time{}, false
	}
	fetched := time.Unix(cached.FetchedAt, 0)
	if maxAge > 0 && time.Since(fetched) > maxAge {
		return nil, time.Time{}, false
	}
	return cached.Models, fetched, true
}

// loadStaleCachedModels returns any cached catalogue regardless of age.
func loadStaleCachedModels() ([]OpenRouterModel, time.Time, bool) {
	path := modelsCachePath()
	if path == "" {
		return nil, time.Time{}, false
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var cached cachedModelsFile
	if err := json.Unmarshal(contents, &cached); err != nil || len(cached.Models) == 0 {
		return nil, time.Time{}, false
	}
	return cached.Models, time.Unix(cached.FetchedAt, 0), true
}

func saveCachedModels(models []OpenRouterModel) {
	path := modelsCachePath()
	if path == "" {
		return
	}
	encoded, err := json.Marshal(cachedModelsFile{
		FetchedAt: time.Now().Unix(),
		Models:    models,
	})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, append(encoded, '\n'), 0o600)
}

// ResolveModels returns the model catalogue, preferring a fresh cache.
// When refresh is false a fresh cache avoids the network entirely. When the
// network fails, any cache (even stale) is returned with cached=true so the
// caller can warn instead of failing outright.
func ResolveModels(ctx context.Context, refresh bool) (models []OpenRouterModel, cached bool, fetchedAt time.Time, err error) {
	if !refresh {
		if cachedModels, at, ok := LoadCachedModels(modelsCacheTTL); ok {
			return cachedModels, true, at, nil
		}
	}
	live, fetchErr := FetchModels(ctx)
	if fetchErr == nil {
		saveCachedModels(live)
		return live, false, time.Now(), nil
	}
	if cachedModels, at, ok := loadStaleCachedModels(); ok {
		return cachedModels, true, at, nil
	}
	return nil, false, time.Time{}, fetchErr
}

var (
	modelSizeToken      = regexp.MustCompile(`^(\d+(\.\d+)?[bmk]|a\d+(\.\d+)?[bmk]?|\d+x\d+[bmk]?)$`)
	modelEmbeddedNumber = regexp.MustCompile(`\d+(\.\d+)*`)
)

type modelCandidate struct {
	model   OpenRouterModel
	key     string
	version []int
}

// SelectLatestModels filters catalogue noise and keeps only the newest model
// per family so old generations (fable 5 next to fable 5.1) are hidden:
//
//   - hidden `~` entries and `*-latest` moving aliases are dropped,
//   - `:variant` routing suffixes (`:batch`, `:online`, ...) are dropped when
//     the plain model exists; lone `:free` models are kept with their full ID,
//   - models sharing a provider and version-stripped base name are grouped and
//     only the highest version wins (ties break by newest `created`).
//
// Version comparison is numeric per component. When one version is a prefix of
// another, a small extra component means a newer minor version (5.1 beats 5)
// while a large extra component means a dated snapshot (gpt-4o beats
// gpt-4o-2024-05-13, gpt-3.5-turbo beats gpt-3.5-turbo-0613).
func SelectLatestModels(models []OpenRouterModel) []OpenRouterModel {
	plainBases := make(map[string]bool, len(models))
	for _, model := range models {
		id := strings.TrimSpace(strings.TrimPrefix(model.ID, "~"))
		if id == "" || strings.HasPrefix(model.ID, "~") {
			continue
		}
		if _, _, hasVariant := strings.Cut(id, ":"); !hasVariant {
			plainBases[strings.ToLower(id)] = true
		}
	}

	best := make(map[string]modelCandidate)
	for _, model := range models {
		rawID := strings.TrimSpace(model.ID)
		if rawID == "" || strings.HasPrefix(rawID, "~") {
			continue
		}
		id, variant, hasVariant := strings.Cut(rawID, ":")
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if hasVariant && (plainBases[strings.ToLower(id)] || !strings.EqualFold(strings.TrimSpace(variant), "free")) {
			continue
		}
		provider, slug, _ := strings.Cut(id, "/")
		if slug == "" {
			provider, slug = "other", provider
		}
		if hasLatestToken(slug) {
			continue
		}
		key, version := modelFamilyKey(provider, slug)
		entry := modelCandidate{model: model, key: key, version: version}
		if current, found := best[key]; !found || newerModel(entry, current) {
			best[key] = entry
		}
	}

	winners := make([]OpenRouterModel, 0, len(best))
	for _, entry := range best {
		winners = append(winners, entry.model)
	}
	sort.Slice(winners, func(left, right int) bool {
		lProvider := ModelProvider(strings.ToLower(winners[left].ID))
		rProvider := ModelProvider(strings.ToLower(winners[right].ID))
		if lProvider != rProvider {
			return lProvider < rProvider
		}
		return strings.ToLower(winners[left].ID) < strings.ToLower(winners[right].ID)
	})
	return winners
}

func hasLatestToken(slug string) bool {
	for _, token := range strings.Split(slug, "-") {
		if strings.EqualFold(token, "latest") {
			return true
		}
	}
	return false
}

// modelFamilyKey maps a model slug to a grouping key plus its version parts.
// Size tokens (8b, 27b, 16k, a3b) stay in the key so different sizes remain
// distinct; numeric version tokens become "#" placeholders. Date tokens (years
// and the snapshot numbers trailing them, such as 2024-05-13 or 0613) are
// dropped from the key entirely so snapshots group with their base model;
// they still count for version comparison, where the shorter base wins.
func modelFamilyKey(provider, slug string) (string, []int) {
	var keyParts []string
	var version []int
	inDateRun := false
	for _, token := range strings.Split(strings.ToLower(slug), "-") {
		if token == "" {
			continue
		}
		if modelSizeToken.MatchString(token) {
			inDateRun = false
			keyParts = append(keyParts, token)
			continue
		}
		trimmed := token
		if strings.HasPrefix(trimmed, "v") && len(trimmed) > 1 && trimmed[1] >= '0' && trimmed[1] <= '9' {
			trimmed = trimmed[1:]
		}
		if isNumericVersion(trimmed) {
			parts := parseVersionParts(trimmed)
			if isDateToken(trimmed, inDateRun) {
				inDateRun = true
				version = append(version, parts...)
				continue
			}
			inDateRun = false
			keyParts = append(keyParts, "#")
			version = append(version, parts...)
			continue
		}
		inDateRun = false
		match := modelEmbeddedNumber.FindString(trimmed)
		if match == "" {
			keyParts = append(keyParts, token)
			continue
		}
		prefix, suffix := splitEmbeddedNumber(trimmed, match)
		// A trailing size letter belongs to the size (o1-style excluded):
		// tokens like "20b" are already handled above, so anything left with
		// a numeric run is a version (qwen3.8, 4o, o1).
		keyParts = append(keyParts, prefix+"#"+suffix)
		version = append(version, parseVersionParts(match)...)
	}
	return strings.ToLower(provider) + "/" + strings.Join(keyParts, "-"), version
}

// isDateToken reports whether a numeric token is a calendar fragment rather
// than a model version: a year (2024), a YYYYMMDD stamp (20260420), a YYMM or
// MMDD snapshot (2507, 0613), or a month/day trailing a year.
func isDateToken(token string, inDateRun bool) bool {
	if inDateRun {
		return true
	}
	switch len(token) {
	case 8:
		return strings.HasPrefix(token, "19") || strings.HasPrefix(token, "20")
	case 4:
		if strings.HasPrefix(token, "19") || strings.HasPrefix(token, "20") {
			return true
		}
		if token[0] == '0' {
			return true
		}
		// YYMM snapshot such as 2507.
		if token[0] == '2' && token[1] >= '0' && token[1] <= '9' &&
			(token[2] == '0' || token[2] == '1') && token[3] >= '0' && token[3] <= '9' {
			month, _ := strconv.Atoi(token[2:])
			return month >= 1 && month <= 12
		}
	}
	return false
}

func isNumericVersion(token string) bool {
	if token == "" {
		return false
	}
	dots := 0
	for _, character := range token {
		switch {
		case character >= '0' && character <= '9':
		case character == '.' && dots == 0:
			dots++
			// Allow only a single dot run separator style below; multiple
			// dots are rejected by the final parse check.
		default:
			return false
		}
	}
	parts := strings.Split(token, ".")
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

func parseVersionParts(token string) []int {
	var parts []int
	for _, piece := range strings.Split(token, ".") {
		number, err := strconv.Atoi(piece)
		if err != nil {
			continue
		}
		parts = append(parts, number)
	}
	return parts
}

func splitEmbeddedNumber(token, match string) (string, string) {
	index := strings.Index(token, match)
	if index < 0 {
		return token, ""
	}
	return token[:index], token[index+len(match):]
}

// newerModel reports whether entry should replace current as the family head.
func newerModel(entry, current modelCandidate) bool {
	if entry.model.ID == current.model.ID {
		return false
	}
	switch compareModelVersion(entry.version, current.version) {
	case 1:
		return true
	case -1:
		return false
	}
	if entry.model.Created != current.model.Created {
		return entry.model.Created > current.model.Created
	}
	return strings.ToLower(entry.model.ID) < strings.ToLower(current.model.ID)
}

func compareModelVersion(left, right []int) int {
	shared := len(left)
	if len(right) < shared {
		shared = len(right)
	}
	for index := 0; index < shared; index++ {
		if left[index] != right[index] {
			if left[index] < right[index] {
				return -1
			}
			return 1
		}
	}
	if len(left) == len(right) {
		return 0
	}
	var extra []int
	longerIsLeft := len(left) > len(right)
	if longerIsLeft {
		extra = left[shared:]
	} else {
		extra = right[shared:]
	}
	// Small extra components are minor versions (5.1 > 5); large ones are
	// dated snapshots (gpt-4o > gpt-4o-2024-05-13), where shorter wins.
	longerWins := extra[0] < 100
	if longerWins {
		if longerIsLeft {
			return 1
		}
		return -1
	}
	if longerIsLeft {
		return -1
	}
	return 1
}

// ModelProvider splits "provider/slug" display parts.
func ModelProvider(id string) string {
	provider, _, found := strings.Cut(id, "/")
	if !found || provider == "" {
		return "other"
	}
	return provider
}

// FormatContextLength renders a compact context size such as "1M" or "128K".
func FormatContextLength(length int64) string {
	switch {
	case length >= 1_000_000 && length%1_000_000 == 0:
		return fmt.Sprintf("%dM", length/1_000_000)
	case length >= 1_000_000:
		trimmed := strings.TrimRight(
			strings.TrimRight(fmt.Sprintf("%.1f", float64(length)/1_000_000), "0"), ".",
		)
		return trimmed + "M"
	case length >= 1000 && length%1000 == 0:
		return fmt.Sprintf("%dK", length/1000)
	case length > 0:
		return strconv.FormatInt(length, 10)
	default:
		return ""
	}
}
