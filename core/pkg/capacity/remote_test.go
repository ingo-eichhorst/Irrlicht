package capacity

import (
	"testing"
)

func TestParseLiteLLMData_BasicMapping(t *testing.T) {
	data := []byte(`{
		"claude-sonnet-4-6": {
			"max_input_tokens": 200000,
			"max_output_tokens": 64000,
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
			"litellm_provider": "anthropic",
			"mode": "chat"
		}
	}`)

	config, err := parseLiteLLMData(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(config.Models) != 1 {
		t.Fatalf("got %d models, want 1", len(config.Models))
	}

	mc, ok := config.Models["claude-sonnet-4-6"]
	if !ok {
		t.Fatal("missing claude-sonnet-4-6")
	}

	if mc.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", mc.ContextWindow)
	}
	if mc.MaxOutput != 64000 {
		t.Errorf("MaxOutput = %d, want 64000", mc.MaxOutput)
	}
	if mc.Family != "claude-4" {
		t.Errorf("Family = %q, want %q", mc.Family, "claude-4")
	}
	if mc.Pricing == nil {
		t.Fatal("Pricing is nil")
	}
	if mc.Pricing.InputPerMTok != 3.0 {
		t.Errorf("InputPerMTok = %f, want 3.0", mc.Pricing.InputPerMTok)
	}
	if mc.Pricing.OutputPerMTok != 15.0 {
		t.Errorf("OutputPerMTok = %f, want 15.0", mc.Pricing.OutputPerMTok)
	}
}

func TestParseLiteLLMData_SkipsProviderPrefixed(t *testing.T) {
	data := []byte(`{
		"claude-sonnet-4-6": {
			"max_input_tokens": 200000,
			"max_output_tokens": 64000,
			"litellm_provider": "anthropic",
			"mode": "chat"
		},
		"bedrock/anthropic.claude-sonnet-4-6": {
			"max_input_tokens": 200000,
			"max_output_tokens": 64000,
			"litellm_provider": "bedrock",
			"mode": "chat"
		},
		"sample_spec": {
			"max_input_tokens": 100000,
			"litellm_provider": "sample"
		}
	}`)

	config, err := parseLiteLLMData(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(config.Models) != 1 {
		t.Fatalf("got %d models, want 1 (should skip prefixed and sample_spec)", len(config.Models))
	}
	if _, ok := config.Models["claude-sonnet-4-6"]; !ok {
		t.Error("missing canonical claude-sonnet-4-6")
	}
}

func TestParseLiteLLMData_SkipsNoContextWindow(t *testing.T) {
	data := []byte(`{
		"model-with-tokens": {
			"max_input_tokens": 100000,
			"litellm_provider": "test",
			"mode": "chat"
		},
		"model-without-tokens": {
			"max_input_tokens": 0,
			"litellm_provider": "test",
			"mode": "chat"
		}
	}`)

	config, err := parseLiteLLMData(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(config.Models) != 1 {
		t.Fatalf("got %d models, want 1", len(config.Models))
	}
}

func TestDeriveFamilyFromLiteLLM(t *testing.T) {
	tests := []struct {
		modelID  string
		provider string
		want     string
	}{
		{"claude-sonnet-4-6", "anthropic", "claude-4"},
		{"claude-opus-4-1", "anthropic", "claude-4"},
		{"claude-haiku-4-5", "anthropic", "claude-4"},
		{"claude-3.5-sonnet", "anthropic", "claude-3.5"},
		{"claude-3.7-sonnet", "anthropic", "claude-3.7"},
		{"claude-3-haiku", "anthropic", "claude-3"},
		{"gpt-5.3-codex", "openai", "gpt-5"},
		{"gpt-4o", "openai", "gpt-4"},
		{"gemini-pro", "google", "google"},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := deriveFamilyFromLiteLLM(tt.modelID, tt.provider)
			if got != tt.want {
				t.Errorf("deriveFamilyFromLiteLLM(%q, %q) = %q, want %q", tt.modelID, tt.provider, got, tt.want)
			}
		})
	}
}

func TestMergeRemoteModels_PreservesExisting(t *testing.T) {
	cm := DefaultCapacityManager()
	if cm == nil {
		t.Fatal("DefaultCapacityManager returned nil")
	}

	// Get the existing context window for a known model.
	existing := cm.GetModelCapacity("claude-opus-4-6")
	if existing.ContextWindow != 200000 {
		t.Fatalf("pre-merge ContextWindow = %d, want 200000", existing.ContextWindow)
	}

	// Merge remote data that includes both a conflicting and a new model.
	remote := &CapacityConfig{
		Models: map[string]ModelCapacity{
			"claude-opus-4-6": {
				ContextWindow: 999999, // should NOT override existing
			},
			"brand-new-model": {
				ContextWindow:    500000,
				MaxOutput:        16000,
				CharToTokenRatio: 3.5,
				Family:           "new",
				DisplayName:      "Brand New Model",
			},
		},
	}

	cm.MergeRemoteModels(remote)

	// Existing model should be unchanged.
	after := cm.GetModelCapacity("claude-opus-4-6")
	if after.ContextWindow != 200000 {
		t.Errorf("post-merge ContextWindow = %d, want 200000 (should preserve existing)", after.ContextWindow)
	}

	// New model should be available.
	newModel := cm.GetModelCapacity("brand-new-model")
	if newModel.ContextWindow != 500000 {
		t.Errorf("brand-new-model ContextWindow = %d, want 500000", newModel.ContextWindow)
	}
}

func TestFallback_NoContextWindow(t *testing.T) {
	cm := DefaultCapacityManager()
	if cm == nil {
		t.Fatal("DefaultCapacityManager returned nil")
	}

	mc := cm.GetModelCapacity("totally-unknown-model-xyz")
	if mc.ContextWindow != 0 {
		t.Errorf("fallback ContextWindow = %d, want 0", mc.ContextWindow)
	}
	if mc.Family != "unknown" {
		t.Errorf("fallback Family = %q, want %q", mc.Family, "unknown")
	}
}
