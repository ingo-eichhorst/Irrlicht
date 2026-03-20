// Package codex provides an inbound adapter that watches OpenAI Codex CLI
// transcript files under ~/.codex/*/*.jsonl.
package codex

import "irrlicht/core/adapters/inbound/fswatcher"

// AdapterName identifies sessions originating from OpenAI Codex.
const AdapterName = "codex"

// rootDir is the path relative to $HOME where Codex stores transcripts.
const rootDir = ".codex"

// New creates a file-system watcher for Codex transcripts.
func New() *fswatcher.Watcher {
	return fswatcher.New(rootDir, AdapterName)
}
