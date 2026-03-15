package capacity

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ModelCapacity represents the capacity configuration for a specific model
type ModelCapacity struct {
	ContextWindow    int64             `json:"context_window"`
	MaxOutput        int64             `json:"max_output"`
	CharToTokenRatio float64           `json:"char_to_token_ratio"`
	Family           string            `json:"family"`
	DisplayName      string            `json:"display_name"`
	Notes            string            `json:"notes,omitempty"`
	BetaFeatures     map[string]int64  `json:"beta_features,omitempty"`
}

// CapacityConfig represents the entire model capacity configuration
type CapacityConfig struct {
	Version        string                    `json:"version"`
	LastUpdated    string                    `json:"last_updated"`
	Models         map[string]ModelCapacity  `json:"models"`
	FamilyDefaults map[string]ModelCapacity  `json:"family_defaults"`
	Fallback       ModelCapacity             `json:"fallback"`
	EstimationNotes map[string]string        `json:"estimation_notes"`
}

// CapacityManager handles model capacity loading and caching
type CapacityManager struct {
	config     *CapacityConfig
	configPath string
	lastModified time.Time
	mu         sync.RWMutex
}

// TokenEstimation represents estimated or actual token usage
type TokenEstimation struct {
	Tokens     int64   `json:"tokens"`
	IsEstimated bool   `json:"is_estimated"`
	Method     string  `json:"method"` // "exact", "char_estimation", "fallback"
	Confidence float64 `json:"confidence"` // 0.0-1.0
}

// ContextUtilization represents context usage metrics
type ContextUtilization struct {
	TokensUsed              int64   `json:"tokens_used"`
	ContextCapacity         int64   `json:"context_capacity"`
	UtilizationPercentage   float64 `json:"utilization_percentage"`
	EstimatedTokensRemaining int64  `json:"estimated_tokens_remaining"`
	IsEstimated             bool    `json:"is_estimated"`
	ModelName               string  `json:"model_name"`
	ModelFamily             string  `json:"model_family"`
	LastTokenCount          int64   `json:"last_token_count"`
	PressureLevel           string  `json:"pressure_level"` // "safe", "caution", "warning", "critical"
}

// NewCapacityManager creates a new capacity manager
func NewCapacityManager(configPath string) (*CapacityManager, error) {
	cm := &CapacityManager{
		configPath: configPath,
	}
	
	if err := cm.LoadConfig(); err != nil {
		return nil, fmt.Errorf("failed to load initial config: %w", err)
	}
	
	return cm, nil
}

// LoadConfig loads or reloads the model capacity configuration
func (cm *CapacityManager) LoadConfig() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	
	// Check if file has been modified since last load
	info, err := os.Stat(cm.configPath)
	if err != nil {
		return fmt.Errorf("config file not found: %w", err)
	}
	
	if !cm.lastModified.IsZero() && !info.ModTime().After(cm.lastModified) {
		// File hasn't changed, no need to reload
		return nil
	}
	
	file, err := os.Open(cm.configPath)
	if err != nil {
		return fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()
	
	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	
	var config CapacityConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}
	
	cm.config = &config
	cm.lastModified = info.ModTime()
	
	return nil
}

// GetModelCapacity retrieves capacity info for a specific model
func (cm *CapacityManager) GetModelCapacity(modelName string) ModelCapacity {
	// Try to reload config if it's been modified (this needs write lock)
	cm.LoadConfig()
	
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	
	// Direct model lookup
	if capacity, exists := cm.config.Models[modelName]; exists {
		return capacity
	}
	
	// Try fuzzy matching for common variations
	modelLower := strings.ToLower(modelName)
	
	// Handle specific pattern matches first
	if strings.Contains(modelLower, "opus") && strings.Contains(modelLower, "4") {
		if capacity, exists := cm.config.Models["claude-4.1-opus"]; exists {
			capacity.DisplayName = fmt.Sprintf("%s (matched: claude-4.1-opus)", modelName)
			return capacity
		}
	}
	
	if strings.Contains(modelLower, "sonnet") && strings.Contains(modelLower, "4") {
		if capacity, exists := cm.config.Models["claude-4-sonnet"]; exists {
			capacity.DisplayName = fmt.Sprintf("%s (matched: claude-4-sonnet)", modelName)
			return capacity
		}
	}
	
	if strings.Contains(modelLower, "3.5") && strings.Contains(modelLower, "sonnet") {
		if capacity, exists := cm.config.Models["claude-3.5-sonnet"]; exists {
			capacity.DisplayName = fmt.Sprintf("%s (matched: claude-3.5-sonnet)", modelName)
			return capacity
		}
	}
	
	// Try family-based lookup
	for family, defaultCapacity := range cm.config.FamilyDefaults {
		if strings.Contains(strings.ToLower(modelName), strings.ToLower(family)) {
			// Create a capacity based on family defaults
			capacity := defaultCapacity
			capacity.DisplayName = fmt.Sprintf("%s (family: %s)", modelName, family)
			capacity.Family = family
			return capacity
		}
	}
	
	// Fallback to default capacity
	fallback := cm.config.Fallback
	fallback.DisplayName = fmt.Sprintf("%s (unknown)", modelName)
	fallback.Family = "unknown"
	return fallback
}

// EstimateTokensFromContent estimates token count from text content
func (cm *CapacityManager) EstimateTokensFromContent(content string, modelName string) TokenEstimation {
	capacity := cm.GetModelCapacity(modelName)
	
	if capacity.CharToTokenRatio == 0 {
		capacity.CharToTokenRatio = cm.config.Fallback.CharToTokenRatio
	}
	
	tokens := int64(float64(len(content)) / capacity.CharToTokenRatio)
	
	// Confidence based on known model vs fallback
	confidence := 0.8
	if capacity.Family == "unknown" {
		confidence = 0.5
	}
	
	return TokenEstimation{
		Tokens:     tokens,
		IsEstimated: true,
		Method:     "char_estimation",
		Confidence: confidence,
	}
}

// CalculateContextUtilization computes context utilization metrics
func (cm *CapacityManager) CalculateContextUtilization(tokensUsed int64, modelName string, isEstimated bool) ContextUtilization {
	capacity := cm.GetModelCapacity(modelName)
	
	utilizationPercentage := (float64(tokensUsed) / float64(capacity.ContextWindow)) * 100
	remainingTokens := capacity.ContextWindow - tokensUsed
	
	// Determine pressure level
	pressureLevel := "safe"
	if utilizationPercentage >= 96 {
		pressureLevel = "critical"
	} else if utilizationPercentage >= 81 {
		pressureLevel = "warning"
	} else if utilizationPercentage >= 51 {
		pressureLevel = "caution"
	}
	
	return ContextUtilization{
		TokensUsed:              tokensUsed,
		ContextCapacity:         capacity.ContextWindow,
		UtilizationPercentage:   utilizationPercentage,
		EstimatedTokensRemaining: remainingTokens,
		IsEstimated:             isEstimated,
		ModelName:               modelName,
		ModelFamily:             capacity.Family,
		LastTokenCount:          tokensUsed,
		PressureLevel:           pressureLevel,
	}
}

// FormatTokenCount returns human-readable token count
func FormatTokenCount(tokens int64) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	} else if tokens < 1000000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	} else {
		return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
	}
}

// FormatUtilizationPercentage returns formatted percentage string
func FormatUtilizationPercentage(percentage float64) string {
	return fmt.Sprintf("%.1f%%", percentage)
}

// GetPressureLevelIcon returns an appropriate icon for pressure level
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