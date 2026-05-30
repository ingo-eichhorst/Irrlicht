package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain pins the model-capacity (LiteLLM) cache to a committed snapshot
// (testdata/model-capacity-cache.json) so the byte-identity goldens are
// hermetic. Without this, model_name / cost resolution reads whatever LiteLLM
// data is cached at ~/.local/share/irrlicht on the dev machine or CI runner —
// which made synthetic gpt-5.x fixtures flake (a cold cache resolves the model
// differently than a warm one, e.g. gpt-5.4 → gpt-5.5 only when the snapshot
// holds gpt-5.5). The env var is the same escape hatch cachePath() honors in
// production, so setting it here changes nothing about how the daemon ships.
func TestMain(m *testing.M) {
	if abs, err := filepath.Abs(filepath.Join("testdata", "model-capacity-cache.json")); err == nil {
		_ = os.Setenv("IRRLICHT_MODEL_CAPACITY_CACHE", abs)
	}
	os.Exit(m.Run())
}
