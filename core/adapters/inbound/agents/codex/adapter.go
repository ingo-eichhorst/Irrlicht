// Package codex provides an inbound adapter that watches OpenAI Codex CLI
// transcript files under ~/.codex/*/*.jsonl.
package codex

import (
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
)

// AdapterName identifies sessions originating from OpenAI Codex.
const AdapterName = "codex"

// rootDir is the path relative to $HOME where Codex stores session transcripts.
// Sessions live under sessions/YYYY/MM/DD/*.jsonl (deep nesting).
const rootDir = ".codex/sessions"

// New creates a file-system watcher for Codex transcripts.
// maxAge controls the maximum transcript file age; older files are ignored.
func New(maxAge time.Duration) *fswatcher.Watcher {
	return fswatcher.New(rootDir, AdapterName, maxAge)
}
