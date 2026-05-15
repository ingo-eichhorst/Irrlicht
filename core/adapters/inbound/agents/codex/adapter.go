// Package codex provides an inbound adapter that watches OpenAI Codex CLI
// transcript files under ~/.codex/*/*.jsonl.
package codex

import (
	"log"
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
// CODEX_HOME is set to an absolute path, sessions live under
// $CODEX_HOME/sessions; non-absolute values (relative paths, unexpanded ~)
// are logged and ignored to surface misconfiguration. The env var is read
// once at Agent() construction; a daemon restart is required after changing
// it.
func sessionsDir() string {
	if v := os.Getenv(codexHomeEnvVar); v != "" {
		cleaned := filepath.Clean(v)
		if filepath.IsAbs(cleaned) {
			return filepath.Join(cleaned, "sessions")
		}
		log.Printf("codex: ignoring %s=%q — must be an absolute path (no shell expansion)", codexHomeEnvVar, v)
	}
	return defaultRootDir
}
