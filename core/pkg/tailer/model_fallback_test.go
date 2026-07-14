package tailer

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/capacity"
)

// TestModelConfigFallback_GateControlsConfigRead pins issue #440: when a
// transcript carries no in-band model, the daemon path fills ModelName from
// the operator's ~/.claude/settings.json, while the replay path
// (DisableModelConfigFallback) must NOT read operator config — so committed
// fixture goldens stay byte-identical across machines and CI.
func TestModelConfigFallback_GateControlsConfigRead(t *testing.T) {
	// Hermetic HOME with a configured default model. The transcript has no
	// in-band model, so ONLY the config fallback could populate ModelName.
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"model":"claude-sonnet-4-20250514"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Derive the expected value from the same code the daemon uses, so the
	// assertion is independent of NormalizeModelName's exact mapping.
	want := getClaudeModel(home)
	if want == "" {
		t.Fatalf("test setup: getClaudeModel(%q) returned empty", home)
	}

	lines := []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
	}

	t.Run("daemon path fills from config", func(t *testing.T) {
		tl := newTestTailer(writeTranscriptLines(t, lines))
		m, err := tl.TailAndProcess()
		if err != nil {
			t.Fatal(err)
		}
		if m.ModelName != want {
			t.Fatalf("ModelName = %q, want %q (config fallback should fire)", m.ModelName, want)
		}
	})

	t.Run("replay path stays hermetic", func(t *testing.T) {
		tl := newTestTailer(writeTranscriptLines(t, lines))
		tl.DisableModelConfigFallback()
		m, err := tl.TailAndProcess()
		if err != nil {
			t.Fatal(err)
		}
		if m.ModelName != "" {
			t.Fatalf("ModelName = %q, want %q (fallback disabled; config must not be read)", m.ModelName, "")
		}
	})
}

// TestModelConfigFallback_NonClaudeAdapterStaysEmpty pins issue #1019: a
// mistral-vibe session whose meta.json sidecar hasn't been written yet (e.g.
// the brief window right after a `/clear` rotation, before Vibe's next
// message lazily creates the new session directory) must NOT pick up the
// operator's unrelated claude-code model preference from
// ~/.claude/settings.json. Before the fix, "mistral-vibe" matched neither the
// "pi" nor "codex" switch case and fell into a catch-all default that read
// Claude's config for any adapter, contaminating the session with a
// claude-sonnet model name it never used.
func TestModelConfigFallback_NonClaudeAdapterStaysEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"model":"claude-sonnet-4-20250514"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	lines := []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
	}

	tl := NewTranscriptTailer(writeTranscriptLines(t, lines), &testParser{}, "mistral-vibe")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.ModelName != "" {
		t.Fatalf("ModelName = %q, want empty (mistral-vibe has no meta.json yet and must not fall back to claude-code's config)", m.ModelName)
	}
}
