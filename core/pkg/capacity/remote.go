package capacity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	cacheTTL  = 24 * time.Hour
	cacheFile = "model-capacity-cache.json"
)

// liteLLMURL is a var (not const) so tests can redirect fetches to an
// httptest.Server. Production callers must not mutate it.
var liteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// SetLiteLLMURLForTest redirects the LiteLLM fetch URL for the duration of
// the test, restoring it on cleanup. Exists because cross-package tests
// (e.g. the irrlichd refresh loop) need to stub the endpoint.
func SetLiteLLMURLForTest(t interface{ Cleanup(func()) }, url string) {
	orig := liteLLMURL
	liteLLMURL = url
	t.Cleanup(func() { liteLLMURL = orig })
}

// liteLLMEntry represents a single model entry from LiteLLM's JSON.
type liteLLMEntry struct {
	MaxInputTokens              int64   `json:"max_input_tokens"`
	MaxOutputTokens             int64   `json:"max_output_tokens"`
	InputCostPerToken           float64 `json:"input_cost_per_token"`
	OutputCostPerToken          float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost float64 `json:"cache_creation_input_token_cost"`
	// CacheCreation1hInputTokenCost is the Anthropic ephemeral 1-hour cache write
	// rate. Absent from most LiteLLM entries; zero means fall back to the 5m rate.
	CacheCreation1hInputTokenCost float64 `json:"cache_creation_input_token_cost_above_1hr"`
	LiteLLMProvider               string  `json:"litellm_provider"`
	Mode                          string  `json:"mode"`
}

// cachedCapacity wraps capacityConfig with cache metadata.
type cachedCapacity struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Config    capacityConfig `json:"config"`
}

// cachePath returns the path for the cached remote capacity data.
func cachePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(homeDir, ".local", "share", "irrlicht")
	return filepath.Join(dir, cacheFile), nil
}

// FetchAndCacheLiteLLMData fetches model data from LiteLLM and caches it locally.
// Non-fatal: callers should fall back to embedded data on error.
func FetchAndCacheLiteLLMData() (*capacityConfig, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(liteLLMURL)
	if err != nil {
		return nil, fmt.Errorf("fetch LiteLLM data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LiteLLM returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read LiteLLM response: %w", err)
	}

	config, err := parseLiteLLMData(body)
	if err != nil {
		return nil, err
	}

	// Cache to disk (non-fatal if this fails).
	_ = saveCachedData(config)

	return config, nil
}

// parseLiteLLMData converts LiteLLM's JSON format to our capacityConfig.
func parseLiteLLMData(data []byte) (*capacityConfig, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse LiteLLM JSON: %w", err)
	}

	config := &capacityConfig{
		Version:     "remote-v2",
		LastUpdated: time.Now().Format("2006-01-02"),
		Models:      make(map[string]ModelCapacity),
	}

	for key, rawEntry := range raw {
		// Skip provider-prefixed entries (e.g. "bedrock/anthropic.claude...")
		// and the sample_spec entry. Only keep canonical model IDs.
		if strings.Contains(key, "/") || key == "sample_spec" {
			continue
		}

		var entry liteLLMEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			continue
		}

		if entry.MaxInputTokens <= 0 {
			continue
		}

		// Skip non-chat models.
		if entry.Mode != "" && entry.Mode != "chat" {
			continue
		}

		mc := ModelCapacity{
			ContextWindow: entry.MaxInputTokens,
			MaxOutput:     entry.MaxOutputTokens,
			DisplayName:   key,
			Family:        deriveFamilyFromLiteLLM(key, entry.LiteLLMProvider),
		}

		// Convert per-token pricing to per-million-token pricing.
		if entry.InputCostPerToken > 0 || entry.OutputCostPerToken > 0 {
			p := &ModelPricing{
				InputPerMTok:         entry.InputCostPerToken * 1_000_000,
				OutputPerMTok:        entry.OutputCostPerToken * 1_000_000,
				CacheReadPerMTok:     entry.CacheReadInputTokenCost * 1_000_000,
				CacheCreationPerMTok: entry.CacheCreationInputTokenCost * 1_000_000,
			}
			// When LiteLLM publishes the 1h rate, populate both sub-rates.
			// CacheCreationPerMTok stays equal to the 5m rate as the legacy fallback.
			if entry.CacheCreation1hInputTokenCost > 0 {
				p.CacheCreation5mPerMTok = entry.CacheCreationInputTokenCost * 1_000_000
				p.CacheCreation1hPerMTok = entry.CacheCreation1hInputTokenCost * 1_000_000
			}
			mc.Pricing = p
		}

		config.Models[key] = mc
	}

	return config, nil
}

// deriveFamilyFromLiteLLM infers a model family string.
func deriveFamilyFromLiteLLM(modelID, provider string) string {
	lower := strings.ToLower(modelID)
	switch {
	case strings.Contains(lower, "claude-4") ||
		strings.Contains(lower, "claude-opus-4") ||
		strings.Contains(lower, "claude-sonnet-4") ||
		strings.Contains(lower, "claude-haiku-4"):
		return "claude-4"
	case strings.Contains(lower, "claude-3.7"):
		return "claude-3.7"
	case strings.Contains(lower, "claude-3.5"):
		return "claude-3.5"
	case strings.Contains(lower, "claude-3"):
		return "claude-3"
	case strings.Contains(lower, "gpt-5"):
		return "gpt-5"
	case strings.Contains(lower, "gpt-4"):
		return "gpt-4"
	default:
		return provider
	}
}

// loadCachedRemoteData reads previously cached remote capacity data.
// Returns nil only when the cache file is missing or unreadable. Stale
// caches (older than cacheTTL) are still returned: pricing rarely shifts
// fast enough to make day-old data dangerous, and serving zero cost
// silently is worse than serving slightly stale numbers. The daemon's
// background refresh loop uses IsCacheStale separately to decide when
// to refetch.
func loadCachedRemoteData() *capacityConfig {
	path, err := cachePath()
	if err != nil {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var cached cachedCapacity
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil
	}

	return &cached.Config
}

// saveCachedData writes capacity config to the cache file.
func saveCachedData(config *capacityConfig) error {
	path, err := cachePath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	cached := cachedCapacity{
		FetchedAt: time.Now(),
		Config:    *config,
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// IsCacheStale returns true if the cache doesn't exist or has expired.
func IsCacheStale() bool {
	path, err := cachePath()
	if err != nil {
		return true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}

	var cached cachedCapacity
	if err := json.Unmarshal(data, &cached); err != nil {
		return true
	}

	return time.Since(cached.FetchedAt) > cacheTTL
}
