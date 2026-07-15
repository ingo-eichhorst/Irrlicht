package vibe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// sidecarState is the subset of Vibe's meta.json the adapter needs: the
// working directory (for project labeling + PID binding), the active model
// (display + context-window resolution), and the running context-token count
// (context-utilization bar). Vibe rewrites meta.json after every turn, so the
// values are always the latest turn's — idempotent to re-read, and correct on
// live tailing. On backfill of a finished transcript only the final turn's
// numbers survive, so context tokens land on the last turn_done; cwd and model
// are stable across the session and unaffected.
type sidecarState struct {
	cwd           string
	model         string
	contextTokens int64
	// sessionPromptTokens / sessionCompletionTokens are the session-CUMULATIVE
	// LLM token counts (monotonic). The parser emits their per-turn DELTA as the
	// turn's usage contribution, which is correct on live-tail (each turn_done
	// reads a fresh cumulative) and on backfill (the first turn_done emits the
	// whole session cumulative once; later ones see no delta).
	sessionPromptTokens     int64
	sessionCompletionTokens int64
	// contextWindow is vibe's EFFECTIVE auto-compaction threshold for the
	// active model — the token budget vibe itself targets before compacting.
	// It is the meaningful "context full" mark for a vibe session (mistral
	// models aren't in the capacity window map), so the parser emits it as
	// the context-utilization window. Resolved by resolveAutoCompactThreshold:
	// config.models[] can override the global config.auto_compact_threshold
	// per model, and upstream's effective value is always the active model's
	// own (config.get_active_model().auto_compact_threshold), not the global.
	contextWindow int64
}

// vibeModelConfig is one entry of vibe's config.models[] array — the
// per-model overrides layered on top of config.auto_compact_threshold.
type vibeModelConfig struct {
	// Alias is the model's short identifier — what config.active_model
	// matches against (e.g. "mistral-medium-3.5" in a live 2.19.1 config).
	// Deliberately NOT Name: models[].name carries an unrelated CLI/product
	// label ("mistral-vibe-cli-latest" in that same config) that never
	// matches active_model, so matching on Name silently fails to resolve
	// any override at all.
	Alias                string `json:"alias"`
	AutoCompactThreshold int64  `json:"auto_compact_threshold"`
}

// resolveAutoCompactThreshold returns the EFFECTIVE auto-compaction threshold
// for the active model: its own config.models[] override when one is set,
// falling back to the global default otherwise. Upstream resolves this as
// config.get_active_model().auto_compact_threshold —
// _apply_global_auto_compact_threshold only pushes the global value into
// models that didn't already set their own, so the two diverge whenever a
// per-model override exists. Never returns something worse than the input
// global: an unmatched or zero-valued override both fall through to it.
func resolveAutoCompactThreshold(activeModel string, global int64, models []vibeModelConfig) int64 {
	for _, m := range models {
		if m.Alias == activeModel && m.AutoCompactThreshold > 0 {
			return m.AutoCompactThreshold
		}
	}
	return global
}

// sidecarCache memoizes the last decode by (mtime, size) so a backfill of a
// long transcript reads the static sidecar once, not once per line.
type sidecarCache struct {
	mtime time.Time
	size  int64
	state *sidecarState
}

// matches reports whether the cached state is still fresh for fi — same
// mtime and size as what was last read.
func (c *sidecarCache) matches(fi os.FileInfo) bool {
	return c.state != nil && fi.ModTime().Equal(c.mtime) && fi.Size() == c.size
}

// sidecarPath is meta.json sitting next to the transcript.
func sidecarPath(transcriptPath string) string {
	return filepath.Join(filepath.Dir(transcriptPath), "meta.json")
}

// readSidecar decodes meta.json next to transcriptPath, memoized by
// (mtime, size). A missing or unparseable sidecar returns the last good state
// (nil on first failure) so enrichment degrades quietly — state, cwd, and
// model still flow from whatever was read last.
func readSidecar(transcriptPath string, cache *sidecarCache) *sidecarState {
	path := sidecarPath(transcriptPath)
	fi, err := os.Stat(path)
	if err != nil {
		return cache.state
	}
	if cache.matches(fi) {
		return cache.state
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache.state
	}

	var raw struct {
		Environment struct {
			WorkingDirectory string `json:"working_directory"`
		} `json:"environment"`
		Config struct {
			ActiveModel          string            `json:"active_model"`
			AutoCompactThreshold int64             `json:"auto_compact_threshold"`
			Models               []vibeModelConfig `json:"models"`
		} `json:"config"`
		Stats struct {
			ContextTokens          int64 `json:"context_tokens"`
			SessionPromptTokens    int64 `json:"session_prompt_tokens"`
			SessionCompletionToken int64 `json:"session_completion_tokens"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return cache.state
	}

	st := &sidecarState{
		cwd:                     raw.Environment.WorkingDirectory,
		model:                   raw.Config.ActiveModel,
		contextTokens:           raw.Stats.ContextTokens,
		sessionPromptTokens:     raw.Stats.SessionPromptTokens,
		sessionCompletionTokens: raw.Stats.SessionCompletionToken,
		contextWindow:           resolveAutoCompactThreshold(raw.Config.ActiveModel, raw.Config.AutoCompactThreshold, raw.Config.Models),
	}
	cache.mtime = fi.ModTime()
	cache.size = fi.Size()
	cache.state = st
	return st
}

// cwdFromSidecar reads just the working directory from a transcript's sidecar,
// for the first PID-discovery attempt before the tailer has learned the cwd.
func cwdFromSidecar(transcriptPath string) string {
	var cache sidecarCache
	if st := readSidecar(transcriptPath, &cache); st != nil {
		return st.cwd
	}
	return ""
}
