package kirocli

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"irrlicht/core/pkg/transcript"
)

// sidecarModelState is the slice of Kiro's <uuid>.json metadata sidecar the
// dashboard needs: session_state.rts_model_state. Kiro keeps it current per
// completed turn ("auto" when the user hasn't pinned a model — exactly what
// kiro's own TUI footer shows).
type sidecarModelState struct {
	ModelInfo struct {
		ModelID             string `json:"model_id"`
		ContextWindowTokens int64  `json:"context_window_tokens"`
	} `json:"model_info"`
	ContextUsagePercentage float64 `json:"context_usage_percentage"`
}

// sidecarCache memoizes one readSidecarModelState result keyed by the
// sidecar's (mtime, size). Live tailing sees a fresh sidecar every turn, so
// the cache is a no-op there — it exists for BACKFILL of a long transcript,
// where every historical turn_done would otherwise re-scan the same static
// file: N turn_dones × an O(N)-sized sidecar is O(N²) without it.
type sidecarCache struct {
	mtime time.Time
	size  int64
	state *sidecarModelState
}

// readSidecarModelState extracts rts_model_state from the metadata sidecar
// next to a Kiro transcript (<base>.jsonl → <base>.json). Returns nil when
// there is no sidecar or the path is absent.
//
// The sidecar holds the agent's full conversation state and can grow to MBs
// over a long session; session_state.conversation_metadata in particular is
// huge. The decoder therefore token-walks to session_state.rts_model_state
// and unmarshals only that small object, skipping every sibling — the same
// strategy as transcript.ExtractCWDFromSidecar. Called once per turn_done,
// not per line, and memoized via cache while the file is unchanged.
func readSidecarModelState(transcriptPath string, cache *sidecarCache) *sidecarModelState {
	if !strings.HasSuffix(transcriptPath, ".jsonl") {
		return nil
	}
	sidecar := strings.TrimSuffix(transcriptPath, ".jsonl") + ".json"
	fi, err := os.Stat(sidecar)
	if err != nil {
		return nil
	}
	if cache != nil && cache.state != nil &&
		fi.ModTime().Equal(cache.mtime) && fi.Size() == cache.size {
		return cache.state
	}
	f, err := os.Open(sidecar)
	if err != nil {
		return nil
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if !enterObject(dec) {
		return nil
	}
	// Walk top-level keys to session_state, then its keys to rts_model_state.
	if !walkToKey(dec, "session_state") || !enterObject(dec) {
		return nil
	}
	if !walkToKey(dec, "rts_model_state") {
		return nil
	}
	var state sidecarModelState
	if err := dec.Decode(&state); err != nil {
		return nil
	}
	if cache != nil {
		*cache = sidecarCache{mtime: fi.ModTime(), size: fi.Size(), state: &state}
	}
	return &state
}

// enterObject consumes the next token and reports whether it opened an object.
func enterObject(dec *json.Decoder) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	d, ok := tok.(json.Delim)
	return ok && d == '{'
}

// walkToKey advances the decoder inside the current object until it has
// consumed the given key (leaving the decoder positioned at its value),
// skipping every other member. Returns false when the object ends first.
func walkToKey(dec *json.Decoder, key string) bool {
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return false
		}
		k, _ := keyTok.(string)
		if k == key {
			return true
		}
		if err := transcript.SkipJSONValue(dec); err != nil {
			return false
		}
	}
	return false
}
