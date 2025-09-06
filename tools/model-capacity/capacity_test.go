package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCapacityManager(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-capacity.json")
	
	testConfig := CapacityConfig{
		Version: "1.0.0",
		Models: map[string]ModelCapacity{
			"claude-3.5-sonnet": {
				ContextWindow:    200000,
				MaxOutput:        8192,
				CharToTokenRatio: 3.5,
				Family:           "claude-3.5",
				DisplayName:      "Claude 3.5 Sonnet",
			},
		},
		Fallback: ModelCapacity{
			ContextWindow:    128000,
			CharToTokenRatio: 4.0,
			DisplayName:      "Unknown Model",
			Family:           "unknown",
		},
	}
	
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)
	
	// Test creating capacity manager
	cm, err := NewCapacityManager(configPath)
	assert.NoError(t, err)
	assert.NotNil(t, cm)
	assert.NotNil(t, cm.config)
	assert.Equal(t, "1.0.0", cm.config.Version)
}

func TestGetModelCapacity(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-capacity.json")
	
	testConfig := CapacityConfig{
		Version: "1.0.0",
		Models: map[string]ModelCapacity{
			"claude-3.5-sonnet": {
				ContextWindow:    200000,
				MaxOutput:        8192,
				CharToTokenRatio: 3.5,
				Family:           "claude-3.5",
				DisplayName:      "Claude 3.5 Sonnet",
			},
		},
		FamilyDefaults: map[string]ModelCapacity{
			"claude-3": {
				ContextWindow:    200000,
				MaxOutput:        4096,
				CharToTokenRatio: 3.4,
				Family:           "claude-3",
			},
		},
		Fallback: ModelCapacity{
			ContextWindow:    128000,
			MaxOutput:        4096,
			CharToTokenRatio: 4.0,
			Family:           "unknown",
			DisplayName:      "Unknown Model",
		},
	}
	
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)
	
	cm, err := NewCapacityManager(configPath)
	require.NoError(t, err)
	
	// Test exact model match
	capacity := cm.GetModelCapacity("claude-3.5-sonnet")
	assert.Equal(t, int64(200000), capacity.ContextWindow)
	assert.Equal(t, 3.5, capacity.CharToTokenRatio)
	assert.Equal(t, "claude-3.5", capacity.Family)
	
	// Test family-based fallback
	capacity = cm.GetModelCapacity("claude-3-opus")
	assert.Equal(t, int64(200000), capacity.ContextWindow)
	assert.Equal(t, 3.4, capacity.CharToTokenRatio)
	assert.Equal(t, "claude-3", capacity.Family)
	assert.Contains(t, capacity.DisplayName, "claude-3-opus")
	
	// Test unknown model fallback
	capacity = cm.GetModelCapacity("gpt-4")
	assert.Equal(t, int64(128000), capacity.ContextWindow)
	assert.Equal(t, 4.0, capacity.CharToTokenRatio)
	assert.Equal(t, "unknown", capacity.Family)
	assert.Contains(t, capacity.DisplayName, "gpt-4")
}

func TestEstimateTokensFromContent(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-capacity.json")
	
	testConfig := CapacityConfig{
		Version: "1.0.0",
		Models: map[string]ModelCapacity{
			"claude-3.5-sonnet": {
				ContextWindow:    200000,
				CharToTokenRatio: 4.0, // 4 chars per token for easy calculation
				Family:           "claude-3.5",
				DisplayName:      "Claude 3.5 Sonnet",
			},
		},
		Fallback: ModelCapacity{
			ContextWindow:    128000,
			CharToTokenRatio: 4.0,
			Family:           "unknown",
		},
	}
	
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)
	
	cm, err := NewCapacityManager(configPath)
	require.NoError(t, err)
	
	// Test with known model
	text := "This is a test string with exactly forty chars" // 40 chars
	estimation := cm.EstimateTokensFromContent(text, "claude-3.5-sonnet")
	
	assert.Equal(t, int64(10), estimation.Tokens) // 40 chars / 4 = 10 tokens
	assert.True(t, estimation.IsEstimated)
	assert.Equal(t, "char_estimation", estimation.Method)
	assert.Equal(t, 0.8, estimation.Confidence)
	
	// Test with unknown model
	estimation = cm.EstimateTokensFromContent(text, "unknown-model")
	assert.Equal(t, int64(10), estimation.Tokens)
	assert.True(t, estimation.IsEstimated)
	assert.Equal(t, "char_estimation", estimation.Method)
	assert.Equal(t, 0.5, estimation.Confidence) // Lower confidence for unknown model
}

func TestCalculateContextUtilization(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-capacity.json")
	
	testConfig := CapacityConfig{
		Version: "1.0.0",
		Models: map[string]ModelCapacity{
			"claude-3.5-sonnet": {
				ContextWindow:    100000, // Using 100K for easy percentage calculation
				MaxOutput:        8192,
				CharToTokenRatio: 3.5,
				Family:           "claude-3.5",
				DisplayName:      "Claude 3.5 Sonnet",
			},
		},
		Fallback: ModelCapacity{
			ContextWindow:    128000,
			CharToTokenRatio: 4.0,
			Family:           "unknown",
		},
	}
	
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)
	
	cm, err := NewCapacityManager(configPath)
	require.NoError(t, err)
	
	// Test different utilization levels
	testCases := []struct {
		tokens        int64
		expectedPercentage float64
		expectedPressure   string
	}{
		{25000, 25.0, "safe"},      // 25%
		{60000, 60.0, "caution"},   // 60%
		{85000, 85.0, "warning"},   // 85%
		{98000, 98.0, "critical"},  // 98%
	}
	
	for _, tc := range testCases {
		utilization := cm.CalculateContextUtilization(tc.tokens, "claude-3.5-sonnet", false)
		
		assert.Equal(t, tc.tokens, utilization.TokensUsed)
		assert.Equal(t, int64(100000), utilization.ContextCapacity)
		assert.InDelta(t, tc.expectedPercentage, utilization.UtilizationPercentage, 0.1)
		assert.Equal(t, int64(100000-tc.tokens), utilization.EstimatedTokensRemaining)
		assert.Equal(t, tc.expectedPressure, utilization.PressureLevel)
		assert.Equal(t, "claude-3.5-sonnet", utilization.ModelName)
		assert.Equal(t, "claude-3.5", utilization.ModelFamily)
		assert.False(t, utilization.IsEstimated)
	}
}

func TestFormatTokenCount(t *testing.T) {
	testCases := []struct {
		tokens   int64
		expected string
	}{
		{500, "500"},
		{1500, "1.5K"},
		{50000, "50.0K"},
		{1500000, "1.5M"},
	}
	
	for _, tc := range testCases {
		result := FormatTokenCount(tc.tokens)
		assert.Equal(t, tc.expected, result)
	}
}

func TestFormatUtilizationPercentage(t *testing.T) {
	testCases := []struct {
		percentage float64
		expected   string
	}{
		{25.5, "25.5%"},
		{67.89, "67.9%"},
		{100.0, "100.0%"},
	}
	
	for _, tc := range testCases {
		result := FormatUtilizationPercentage(tc.percentage)
		assert.Equal(t, tc.expected, result)
	}
}

func TestGetPressureLevelIcon(t *testing.T) {
	testCases := []struct {
		level    string
		expected string
	}{
		{"safe", "🟢"},
		{"caution", "🟡"},
		{"warning", "🔴"},
		{"critical", "⚠️"},
		{"unknown", "❓"},
	}
	
	for _, tc := range testCases {
		result := GetPressureLevelIcon(tc.level)
		assert.Equal(t, tc.expected, result)
	}
}

func TestConfigHotReload(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-capacity.json")
	
	// Initial config
	testConfig := CapacityConfig{
		Version: "1.0.0",
		Models: map[string]ModelCapacity{
			"claude-3.5-sonnet": {
				ContextWindow:    200000,
				CharToTokenRatio: 3.5,
				Family:           "claude-3.5",
				DisplayName:      "Claude 3.5 Sonnet",
			},
		},
		Fallback: ModelCapacity{
			ContextWindow:    128000,
			CharToTokenRatio: 4.0,
			Family:           "unknown",
		},
	}
	
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)
	
	cm, err := NewCapacityManager(configPath)
	require.NoError(t, err)
	
	// Verify initial state
	capacity := cm.GetModelCapacity("claude-3.5-sonnet")
	assert.Equal(t, 3.5, capacity.CharToTokenRatio)
	
	// Wait a bit to ensure timestamp difference
	time.Sleep(10 * time.Millisecond)
	
	// Update config
	testConfig.Models["claude-3.5-sonnet"] = ModelCapacity{
		ContextWindow:    200000,
		CharToTokenRatio: 3.0, // Changed value
		Family:           "claude-3.5",
		DisplayName:      "Claude 3.5 Sonnet Updated",
	}
	
	data, err = json.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)
	
	// Force reload by calling GetModelCapacity (which calls LoadConfig)
	capacity = cm.GetModelCapacity("claude-3.5-sonnet")
	assert.Equal(t, 3.0, capacity.CharToTokenRatio) // Should reflect updated value
	assert.Contains(t, capacity.DisplayName, "Updated")
}