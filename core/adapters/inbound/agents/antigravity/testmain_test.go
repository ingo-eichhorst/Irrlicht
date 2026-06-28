package antigravity

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain pins the model-capacity (LiteLLM) cache to a committed snapshot
// (testdata/model-capacity-cache.json) so the replay test that resolves a
// context window is hermetic. Without it, GetModelCapacity reads whatever
// LiteLLM data happens to be cached at ~/.local/share/irrlicht on the dev
// machine or CI runner — which passes on a warm cache but resolves a zero
// context window on a cold one (a fresh CI runner has no cache), making
// TestReplayResolvesStagedStore fail with ContextWindow=0. The env var is the
// same escape hatch cachePath() honors in production, so setting it here
// changes nothing about how the daemon ships (mirrors the onboarding-factory
// replay TestMain).
func TestMain(m *testing.M) {
	if abs, err := filepath.Abs(filepath.Join("testdata", "model-capacity-cache.json")); err == nil {
		_ = os.Setenv("IRRLICHT_MODEL_CAPACITY_CACHE", abs)
	}
	os.Exit(m.Run())
}
