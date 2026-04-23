package capacity

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// ModelPricing holds per-token pricing in USD per million tokens.
type ModelPricing struct {
	InputPerMTok float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
	// CacheReadPerMTok covers both Anthropic cache hits and OpenAI cached input.
	CacheReadPerMTok float64 `json:"cache_read_per_mtok"`
	// CacheCreationPerMTok is the 5-minute (default) cache-write rate.
	// Kept populated even when 5m/1h sub-rates are available, for callers
	// that pass a single cache-creation bucket.
	CacheCreationPerMTok float64 `json:"cache_creation_per_mtok"`
	// CacheCreation5mPerMTok is the Anthropic ephemeral 5-minute cache-write rate.
	// Zero means use CacheCreationPerMTok as fallback.
	CacheCreation5mPerMTok float64 `json:"cache_creation_5m_per_mtok,omitempty"`
	// CacheCreation1hPerMTok is the Anthropic ephemeral 1-hour cache-write rate
	// (~2× the 5m rate per Anthropic docs).
	// Zero means fall back to CacheCreation5mPerMTok or CacheCreationPerMTok.
	CacheCreation1hPerMTok float64 `json:"cache_creation_1h_per_mtok,omitempty"`
}

// ModelCapacity represents the capacity configuration for a specific model.
type ModelCapacity struct {
	ContextWindow int64         `json:"context_window"`
	MaxOutput     int64         `json:"max_output"`
	Family        string        `json:"family"`
	DisplayName   string        `json:"display_name"`
	Pricing       *ModelPricing `json:"pricing,omitempty"`
}

// CapacityConfig is a pure LiteLLM-sourced model table.
type CapacityConfig struct {
	Version     string                   `json:"version"`
	LastUpdated string                   `json:"last_updated"`
	Models      map[string]ModelCapacity `json:"models"`
}

// CapacityManager serves model capacity lookups from the LiteLLM cache,
// reloading transparently when the cache file's mtime advances.
type CapacityManager struct {
	mu              sync.RWMutex
	config          *CapacityConfig
	cachePath       string
	lastModified    time.Time
	loggedMisses    map[string]bool // tracks models already warned about missing pricing
	loggedMissesMu  sync.Mutex
}

// NewForTest constructs a CapacityManager backed by an in-memory model map.
// Tests use this to inject synthetic LiteLLM-style entries without touching disk.
func NewForTest(models map[string]ModelCapacity) *CapacityManager {
	copied := make(map[string]ModelCapacity, len(models))
	for k, v := range models {
		copied[k] = v
	}
	return &CapacityManager{
		config: &CapacityConfig{Models: copied},
	}
}

// maybeReload re-reads the cache file when its mtime is newer than the
// last load. Silent on error — missing or corrupt cache leaves the current
// config in place. Returns true if models were refreshed.
func (cm *CapacityManager) maybeReload() bool {
	if cm.cachePath == "" {
		return false
	}
	info, err := os.Stat(cm.cachePath)
	if err != nil {
		return false
	}
	cm.mu.RLock()
	unchanged := !cm.lastModified.IsZero() && !info.ModTime().After(cm.lastModified)
	cm.mu.RUnlock()
	if unchanged {
		return false
	}

	remote := LoadCachedRemoteData()
	if remote == nil {
		return false
	}

	cm.mu.Lock()
	cm.config = remote
	cm.lastModified = info.ModTime()
	cm.mu.Unlock()
	return true
}

// GetModelCapacity looks up a model by exact name. Returns a zero value when
// the model is not in the cache (or the cache is absent): no context window,
// no pricing. Callers must treat zero ContextWindow as "unknown".
func (cm *CapacityManager) GetModelCapacity(modelName string) ModelCapacity {
	cm.maybeReload()

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.config == nil {
		return ModelCapacity{}
	}
	return cm.config.Models[modelName]
}

// MergeRemoteModels replaces the model table with the given remote config.
// Retained for tests and for one-shot population after a synchronous fetch.
func (cm *CapacityManager) MergeRemoteModels(remote *CapacityConfig) {
	if remote == nil {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.config == nil {
		cm.config = &CapacityConfig{Models: make(map[string]ModelCapacity)}
	}
	for name, cap := range remote.Models {
		cm.config.Models[name] = cap
	}
}

// logPricingMiss emits a one-per-model warning when pricing is absent.
func (cm *CapacityManager) logPricingMiss(modelName string) {
	if modelName == "" {
		return
	}
	cm.loggedMissesMu.Lock()
	if cm.loggedMisses == nil {
		cm.loggedMisses = make(map[string]bool)
	}
	if !cm.loggedMisses[modelName] {
		cm.loggedMisses[modelName] = true
		log.Printf("irrlicht/capacity: no pricing for model %q — cost will be 0 until LiteLLM cache is refreshed", modelName)
	}
	cm.loggedMissesMu.Unlock()
}

// EstimateCostUSD calculates the cost in USD from token breakdowns.
// Returns 0 when pricing data is unavailable (model missing from LiteLLM,
// or cache not yet fetched). Logs a one-per-model warning on miss so silent
// zero-pricing is observable in daemon logs.
func (cm *CapacityManager) EstimateCostUSD(modelName string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int64) float64 {
	cap := cm.GetModelCapacity(modelName)
	if cap.Pricing == nil {
		cm.logPricingMiss(modelName)
		return 0
	}
	p := cap.Pricing
	cost := float64(inputTokens)*p.InputPerMTok +
		float64(outputTokens)*p.OutputPerMTok +
		float64(cacheReadTokens)*p.CacheReadPerMTok +
		float64(cacheCreationTokens)*p.CacheCreationPerMTok
	return cost / 1_000_000
}

// EstimateCostFromBreakdown calculates USD cost using per-bucket token counts
// including separate 5m and 1h Anthropic cache-write rates. Falls back to the
// legacy single-bucket CacheCreationPerMTok when sub-rates are not populated.
func (cm *CapacityManager) EstimateCostFromBreakdown(modelName string, input, output, cacheRead, cacheCreate5m, cacheCreate1h int64) float64 {
	cap := cm.GetModelCapacity(modelName)
	if cap.Pricing == nil {
		cm.logPricingMiss(modelName)
		return 0
	}
	p := cap.Pricing

	// Choose cache-creation rate: prefer sub-rates when populated.
	var cacheCreateCost float64
	if p.CacheCreation5mPerMTok > 0 || p.CacheCreation1hPerMTok > 0 {
		cacheCreateCost = float64(cacheCreate5m)*p.CacheCreation5mPerMTok +
			float64(cacheCreate1h)*p.CacheCreation1hPerMTok
	} else {
		cacheCreateCost = float64(cacheCreate5m+cacheCreate1h) * p.CacheCreationPerMTok
	}

	cost := float64(input)*p.InputPerMTok +
		float64(output)*p.OutputPerMTok +
		float64(cacheRead)*p.CacheReadPerMTok +
		cacheCreateCost
	return cost / 1_000_000
}

// FormatTokenCount returns human-readable token count.
func FormatTokenCount(tokens int64) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	} else if tokens < 1000000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
}

// FormatUtilizationPercentage returns formatted percentage string.
func FormatUtilizationPercentage(percentage float64) string {
	return fmt.Sprintf("%.1f%%", percentage)
}

// GetPressureLevelIcon returns an icon for the pressure level.
func GetPressureLevelIcon(level string) string {
	switch level {
	case "safe":
		return "🟢"
	case "caution":
		return "🟡"
	case "warning":
		return "🔴"
	case "critical":
		return "⚠️"
	default:
		return "❓"
	}
}
