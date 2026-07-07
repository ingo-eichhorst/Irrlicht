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
}

// sidecarCache memoizes the last decode by (mtime, size) so a backfill of a
// long transcript reads the static sidecar once, not once per line.
type sidecarCache struct {
	mtime time.Time
	size  int64
	state *sidecarState
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
	if cache.state != nil && fi.ModTime().Equal(cache.mtime) && fi.Size() == cache.size {
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
			ActiveModel string `json:"active_model"`
		} `json:"config"`
		Stats struct {
			ContextTokens int64 `json:"context_tokens"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return cache.state
	}

	st := &sidecarState{
		cwd:           raw.Environment.WorkingDirectory,
		model:         raw.Config.ActiveModel,
		contextTokens: raw.Stats.ContextTokens,
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
