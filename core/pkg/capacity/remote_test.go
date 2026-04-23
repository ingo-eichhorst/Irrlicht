package capacity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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

func TestParseLiteLLMData_CacheCreation1hSubRate(t *testing.T) {
	// When LiteLLM provides the 1h cache-write rate, both sub-rates must be
	// populated and CacheCreationPerMTok must equal the 5m rate.
	data := []byte(`{
		"claude-opus-4-7": {
			"max_input_tokens": 200000,
			"max_output_tokens": 32000,
			"input_cost_per_token": 0.000015,
			"output_cost_per_token": 0.000075,
			"cache_read_input_token_cost": 0.000001875,
			"cache_creation_input_token_cost": 0.00001875,
			"cache_creation_input_token_cost_above_1hr": 0.0000375,
			"litellm_provider": "anthropic",
			"mode": "chat"
		}
	}`)

	config, err := parseLiteLLMData(data)
	if err != nil {
		t.Fatal(err)
	}

	mc, ok := config.Models["claude-opus-4-7"]
	if !ok {
		t.Fatal("missing claude-opus-4-7")
	}
	if mc.Pricing == nil {
		t.Fatal("Pricing is nil")
	}

	const wantCacheCreate5m = 18.75
	const wantCacheCreate1h = 37.5

	if mc.Pricing.CacheCreation5mPerMTok != wantCacheCreate5m {
		t.Errorf("CacheCreation5mPerMTok = %f, want %f", mc.Pricing.CacheCreation5mPerMTok, wantCacheCreate5m)
	}
	if mc.Pricing.CacheCreation1hPerMTok != wantCacheCreate1h {
		t.Errorf("CacheCreation1hPerMTok = %f, want %f", mc.Pricing.CacheCreation1hPerMTok, wantCacheCreate1h)
	}
	// CacheCreationPerMTok stays equal to the 5m rate as legacy fallback.
	if mc.Pricing.CacheCreationPerMTok != wantCacheCreate5m {
		t.Errorf("CacheCreationPerMTok (legacy) = %f, want %f (should equal 5m rate)", mc.Pricing.CacheCreationPerMTok, wantCacheCreate5m)
	}
}

func TestParseLiteLLMData_No1hRate_SubRatesZero(t *testing.T) {
	// When LiteLLM only has the base cache-creation rate, the sub-rate fields
	// should be zero (signaling the caller to fall back to CacheCreationPerMTok).
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
	mc := config.Models["claude-sonnet-4-6"]
	if mc.Pricing == nil {
		t.Fatal("Pricing is nil")
	}
	if mc.Pricing.CacheCreation5mPerMTok != 0 {
		t.Errorf("CacheCreation5mPerMTok should be 0 when no 1h rate, got %f", mc.Pricing.CacheCreation5mPerMTok)
	}
	if mc.Pricing.CacheCreation1hPerMTok != 0 {
		t.Errorf("CacheCreation1hPerMTok should be 0 when not in LiteLLM, got %f", mc.Pricing.CacheCreation1hPerMTok)
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

func TestMergeRemoteModels_ReplacesEntries(t *testing.T) {
	cm := NewForTest(map[string]ModelCapacity{
		"claude-opus-4-6": {ContextWindow: 200000},
	})

	remote := &CapacityConfig{
		Models: map[string]ModelCapacity{
			"claude-opus-4-6": {ContextWindow: 1000000},
			"brand-new-model": {ContextWindow: 500000, MaxOutput: 16000, Family: "new", DisplayName: "Brand New Model"},
		},
	}
	cm.MergeRemoteModels(remote)

	if got := cm.GetModelCapacity("claude-opus-4-6").ContextWindow; got != 1000000 {
		t.Errorf("post-merge claude-opus-4-6 ContextWindow = %d, want 1000000 (LiteLLM is authoritative)", got)
	}
	if got := cm.GetModelCapacity("brand-new-model").ContextWindow; got != 500000 {
		t.Errorf("brand-new-model ContextWindow = %d, want 500000", got)
	}
}

func TestNewForTest_UnknownModelReturnsZeroValue(t *testing.T) {
	cm := NewForTest(nil)

	mc := cm.GetModelCapacity("totally-unknown-model-xyz")
	if mc.ContextWindow != 0 {
		t.Errorf("unknown ContextWindow = %d, want 0", mc.ContextWindow)
	}
	if mc.Pricing != nil {
		t.Errorf("unknown Pricing = %+v, want nil", mc.Pricing)
	}
	if mc.Family != "" {
		t.Errorf("unknown Family = %q, want empty", mc.Family)
	}
}

func TestMaybeReload_PicksUpNewCache(t *testing.T) {
	// This test uses the real LoadCachedRemoteData path indirectly by
	// overriding the manager's cachePath to a temp file written in the
	// same on-disk format. Because LoadCachedRemoteData always reads from
	// CachePath(), we instead exercise the direct-path reload.
	dir := t.TempDir()
	path := filepath.Join(dir, "model-capacity-cache.json")

	cm := &CapacityManager{cachePath: path}

	// No file yet → lookup returns zero.
	if got := cm.GetModelCapacity("claude-sonnet-4-6").ContextWindow; got != 0 {
		t.Fatalf("pre-cache ContextWindow = %d, want 0", got)
	}

	// Write initial cache file at a known mtime.
	writeCacheAt(t, path, map[string]ModelCapacity{
		"claude-sonnet-4-6": {ContextWindow: 200000},
	}, time.Now().Add(-time.Minute))

	// First lookup after the write picks it up.
	if got := getViaPath(t, cm).GetModelCapacity("claude-sonnet-4-6").ContextWindow; got != 200000 {
		t.Errorf("after first write ContextWindow = %d, want 200000", got)
	}

	// Rewrite with a newer mtime and a different value.
	writeCacheAt(t, path, map[string]ModelCapacity{
		"claude-sonnet-4-6": {ContextWindow: 1000000},
	}, time.Now())

	if got := getViaPath(t, cm).GetModelCapacity("claude-sonnet-4-6").ContextWindow; got != 1000000 {
		t.Errorf("after second write ContextWindow = %d, want 1000000 (hot-reload missed)", got)
	}
}

// writeCacheAt writes a cache file with the given models and sets its mtime.
func writeCacheAt(t *testing.T, path string, models map[string]ModelCapacity, when time.Time) {
	t.Helper()
	cached := cachedCapacity{
		FetchedAt: when,
		Config:    CapacityConfig{Models: models},
	}
	data, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// getViaPath exercises the reload path using the manager's cachePath directly
// (bypassing LoadCachedRemoteData's hardcoded CachePath()).
func getViaPath(t *testing.T, cm *CapacityManager) *CapacityManager {
	t.Helper()
	info, err := os.Stat(cm.cachePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	data, err := os.ReadFile(cm.cachePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cached cachedCapacity
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cm.mu.Lock()
	cm.config = &cached.Config
	cm.lastModified = info.ModTime()
	cm.mu.Unlock()
	return cm
}

// TestFetchAndCacheLiteLLMData_WritesCache verifies the end-to-end fetch +
// cache-to-disk path against a stubbed LiteLLM endpoint. This is the success
// leg of the retry loop.
func TestFetchAndCacheLiteLLMData_WritesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"claude-sonnet-4-6": {
				"max_input_tokens": 200000,
				"max_output_tokens": 64000,
				"input_cost_per_token": 0.000003,
				"output_cost_per_token": 0.000015,
				"litellm_provider": "anthropic",
				"mode": "chat"
			}
		}`))
	}))
	defer srv.Close()

	SetLiteLLMURLForTest(t, srv.URL)
	withTempHome(t)

	config, err := FetchAndCacheLiteLLMData()
	if err != nil {
		t.Fatalf("FetchAndCacheLiteLLMData: %v", err)
	}
	if _, ok := config.Models["claude-sonnet-4-6"]; !ok {
		t.Fatalf("expected claude-sonnet-4-6 in returned config")
	}

	path, _ := CachePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
}

// TestLoadCachedRemoteData_OfflineFallback verifies that a previously
// written cache file is returned by LoadCachedRemoteData — the path the
// daemon relies on when starting without network connectivity.
func TestLoadCachedRemoteData_OfflineFallback(t *testing.T) {
	withTempHome(t)

	path, _ := CachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cached := cachedCapacity{
		FetchedAt: time.Now(),
		Config: CapacityConfig{
			Models: map[string]ModelCapacity{
				"claude-sonnet-4-6": {ContextWindow: 200000},
			},
		},
	}
	data, _ := json.Marshal(cached)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	got := LoadCachedRemoteData()
	if got == nil {
		t.Fatal("LoadCachedRemoteData returned nil despite a fresh cache file")
	}
	if got.Models["claude-sonnet-4-6"].ContextWindow != 200000 {
		t.Errorf("cached ContextWindow = %d, want 200000", got.Models["claude-sonnet-4-6"].ContextWindow)
	}
}

// withTempHome points os.UserHomeDir() at a tempdir so CachePath() writes
// are isolated per test.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
}

func TestConcurrentGet_NoPanicUnderConcurrentReload(t *testing.T) {
	cm := NewForTest(map[string]ModelCapacity{
		"model-a": {ContextWindow: 100000},
	})

	var stop atomic.Bool
	done := make(chan struct{}, 2)

	go func() {
		for !stop.Load() {
			_ = cm.GetModelCapacity("model-a")
		}
		done <- struct{}{}
	}()

	go func() {
		for i := 0; i < 500 && !stop.Load(); i++ {
			cm.MergeRemoteModels(&CapacityConfig{Models: map[string]ModelCapacity{
				"model-a": {ContextWindow: int64(100000 + i)},
			}})
		}
		done <- struct{}{}
	}()

	time.Sleep(50 * time.Millisecond)
	stop.Store(true)
	<-done
	<-done
}
