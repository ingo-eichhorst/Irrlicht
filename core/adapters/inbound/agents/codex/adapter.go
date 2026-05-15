// Package codex provides an inbound adapter that watches OpenAI Codex CLI
// transcript files under ~/.codex/*/*.jsonl.
package codex

import (
	"os"
	"path/filepath"
)

// AdapterName identifies sessions originating from OpenAI Codex.
const AdapterName = "codex"

// ProcessName is the OS-level executable name for Codex CLI, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "codex"

// defaultRootDir is the path relative to $HOME where Codex stores session
// transcripts by default. Sessions live under sessions/YYYY/MM/DD/*.jsonl
// (deep nesting).
const defaultRootDir = ".codex/sessions"

// codexHomeEnvVar is the upstream Codex env var that relocates the agent's
// home directory (default: $HOME/.codex). When set, sessions move to
// $CODEX_HOME/sessions.
const codexHomeEnvVar = "CODEX_HOME"

// sessionsDir returns the directory the Codex adapter should watch. When
// CODEX_HOME is set, sessions live under $CODEX_HOME/sessions; otherwise
// the default $HOME-relative path is returned.
func sessionsDir() string {
	if v := os.Getenv(codexHomeEnvVar); v != "" {
		return filepath.Join(v, "sessions")
	}
	return defaultRootDir
}
