package tailer

import (
	"math"
	"testing"
)

// assertPressure is a test helper that checks utilization and pressure level.
func assertPressure(t *testing.T, m *SessionMetrics, wantPressure string, wantUtilApprox float64) {
	t.Helper()
	if m.PressureLevel != wantPressure {
		t.Errorf("PressureLevel = %q, want %q (utilization=%.1f%%)", m.PressureLevel, wantPressure, m.ContextUtilization)
	}
	if math.Abs(m.ContextUtilization-wantUtilApprox) > 1.0 {
		t.Errorf("ContextUtilization = %.1f%%, want ~%.1f%%", m.ContextUtilization, wantUtilApprox)
	}
}

func TestContextUtilization_KnownModel_Sonnet45(t *testing.T) {
	// claude-sonnet-4-5 → 1M effective window (per LiteLLM).
	// 600K tokens / 1M = 60% → "caution"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-5-20250514",
				"usage": map[string]interface{}{
					"input_tokens":  float64(550000),
					"output_tokens": float64(50000),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	assertPressure(t, m, "caution", 60.0)
}

func TestContextUtilization_KnownModel_Opus46(t *testing.T) {
	// claude-opus-4-6 → 1M effective window (per LiteLLM).
	// 900K tokens / 1M = 90% → "critical"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "claude-opus-4-6-20250715",
				"usage": map[string]interface{}{
					"input_tokens":  float64(850000),
					"output_tokens": float64(50000),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	assertPressure(t, m, "critical", 90.0)
}

func TestContextUtilization_UnknownModel_ShowsTokensOnly(t *testing.T) {
	// Unknown model: no context window assumption, pressure is "unknown",
	// raw token count is still available.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "some-unknown-model",
				"usage": map[string]interface{}{
					"input_tokens":  float64(90000),
					"output_tokens": float64(10000),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	if m.PressureLevel != "unknown" {
		t.Errorf("PressureLevel = %q, want %q", m.PressureLevel, "unknown")
	}
	if m.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (unknown)", m.ContextWindow)
	}
	if m.ContextUtilization != 0 {
		t.Errorf("ContextUtilization = %.1f, want 0", m.ContextUtilization)
	}
	if m.TotalTokens != 100000 {
		t.Errorf("TotalTokens = %d, want 100000", m.TotalTokens)
	}
	// New flag introduced for the macOS UI: lets the client render a
	// tokens-only label when the daemon has no context-window data,
	// instead of silently hiding the context column.
	if !m.ContextWindowUnknown {
		t.Error("ContextWindowUnknown should be true for unknown model")
	}
}

func TestContextUtilization_KnownModel_ContextWindowUnknown_StaysFalse(t *testing.T) {
	// Sanity: known models with pricing must NOT carry the unknown flag.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-6",
				"usage": map[string]interface{}{
					"input_tokens":  float64(1000),
					"output_tokens": float64(500),
				},
			},
		},
	})
	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.ContextWindowUnknown {
		t.Error("ContextWindowUnknown should be false for known model")
	}
	if m.ContextWindow == 0 {
		t.Error("ContextWindow should be populated for known model")
	}
}

func TestContextUtilization_Codex53_Uses256KContextWindow(t *testing.T) {
	// gpt-5.3-codex should use 256K context window.
	// 35,650 / 256,000 = 13.93% → "safe"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "gpt-5.3-codex",
				"usage": map[string]interface{}{
					"input":      float64(1093),
					"output":     float64(509),
					"cacheRead":  float64(34048),
					"cacheWrite": float64(0),
					"totalTokens": float64(35650),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	if m.ContextWindow != 256000 {
		t.Fatalf("ContextWindow = %d, want 256000", m.ContextWindow)
	}
	assertPressure(t, m, "safe", 13.93)
}

func TestContextUtilization_GPT5Preview_Uses256KContextWindow(t *testing.T) {
	// gpt-5.9-codex-preview resolves via the LiteLLM-sourced 256K window.
	// 102,400 / 256,000 = 40% → "safe"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "gpt-5.9-codex-preview",
				"usage": map[string]interface{}{
					"input_tokens":  float64(102400),
					"output_tokens": float64(0),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	if m.ContextWindow != 256000 {
		t.Fatalf("ContextWindow = %d, want 256000", m.ContextWindow)
	}
	assertPressure(t, m, "safe", 40.0)
}

func TestContextUtilization_ExtendedContext1M(t *testing.T) {
	// Model with [1m] suffix → 1M window
	// 180K tokens / 1M = 18% → "safe"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "claude-opus-4-6[1m]",
				"usage": map[string]interface{}{
					"input_tokens":  float64(170000),
					"output_tokens": float64(10000),
				},
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	assertPressure(t, m, "safe", 18.0)
	if m.ModelName != "claude-opus-4-6" {
		t.Errorf("ModelName = %q, want %q", m.ModelName, "claude-opus-4-6")
	}
}

func TestContextUtilization_TranscriptContextWindow(t *testing.T) {
	// context_management.context_window overrides capacity lookup
	// 80K tokens / 100K (from transcript) = 80% → "warning"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{
			"type":      "assistant",
			"timestamp": ts(0),
			"message": map[string]interface{}{
				"model": "claude-sonnet-4-5",
				"usage": map[string]interface{}{
					"input_tokens":  float64(70000),
					"output_tokens": float64(10000),
				},
			},
			"context_management": map[string]interface{}{
				"context_window": float64(100000),
			},
		},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	assertPressure(t, m, "warning", 80.0)
}

func TestContextUtilization_PressureLevels(t *testing.T) {
	// claude-sonnet-4-5 → 1M effective window (per LiteLLM).
	tests := []struct {
		name         string
		inputTokens  float64
		wantPressure string
	}{
		{"safe", 250000, "safe"},           // 25% of 1M
		{"caution", 650000, "caution"},     // 65% of 1M
		{"warning", 825000, "warning"},     // 82.5% of 1M
		{"critical", 925000, "critical"},   // 92.5% of 1M
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTranscriptLines(t, []map[string]interface{}{
				{
					"type":      "assistant",
					"timestamp": ts(0),
					"message": map[string]interface{}{
						"model": "claude-sonnet-4-5",
						"usage": map[string]interface{}{
							"input_tokens":  tt.inputTokens,
							"output_tokens": float64(0),
						},
					},
				},
			})

			tailer := newTestTailer(path)
			m, err := tailer.TailAndProcess()
			if err != nil {
				t.Fatal(err)
			}

			if m.PressureLevel != tt.wantPressure {
				t.Errorf("PressureLevel = %q, want %q (util=%.1f%%)",
					m.PressureLevel, tt.wantPressure, m.ContextUtilization)
			}
		})
	}
}

func TestNormalizeModelName_NewModels(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-6-20250715", "claude-opus-4-6"},
		{"claude-opus-4-6[1m]", "claude-opus-4-6"},
		{"claude-sonnet-4-6-20250601", "claude-sonnet-4-6"},
		{"claude-sonnet-4-5-20250514", "claude-sonnet-4-5"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5"},
		{"claude-opus-4-1-20250805", "claude-opus-4-1"},
		{"claude-3.5-sonnet", "claude-3.5-sonnet"},
		{"sonnet", "claude-sonnet-4-6"},
		{"haiku", "claude-haiku-4-5"},
		{"some-unknown-model", "some-unknown-model"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeModelName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeModelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
