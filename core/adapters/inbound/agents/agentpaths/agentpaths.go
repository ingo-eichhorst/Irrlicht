// Package agentpaths resolves the transcript directory an inbound agent
// adapter should watch, honoring the upstream agent's own env-var override
// for its home or session root.
//
// Several upstream agents let the user relocate that root (CODEX_HOME,
// CLAUDE_CONFIG_DIR, PI_CODING_AGENT_SESSION_DIR, KIRO_HOME, ...). The rule
// for honoring one is identical in every case, so it lives here once instead
// of being reimplemented per adapter — and forgotten by the next one.
package agentpaths

import (
	"log"
	"os"
	"path/filepath"
)

// FromEnv returns the directory the named adapter should watch.
//
// When envVar holds an absolute path, the result is that path joined with
// subdir — pass no subdir when the env var names the watched directory
// itself rather than a root above it. Otherwise defaultDir is returned as-is;
// callers pass a $HOME-relative default, which fswatcher.New resolves against
// the user's home directory.
//
// Non-absolute env values are rejected: nothing expands a shell metacharacter
// here, so a value like "~/custom" is a misconfiguration rather than a path.
// Rejections are logged under adapter's name so they surface instead of
// silently watching the wrong place. adapter is the log prefix, which is not
// necessarily the adapter's AdapterName constant (e.g. the claudecode package
// logs "claudecode" while its AdapterName is "claude-code").
func FromEnv(adapter, envVar, defaultDir string, subdir ...string) string {
	if v := os.Getenv(envVar); v != "" {
		cleaned := filepath.Clean(v)
		if filepath.IsAbs(cleaned) {
			return filepath.Join(append([]string{cleaned}, subdir...)...)
		}
		log.Printf("%s: ignoring %s=%q — must be an absolute path (no shell expansion)", adapter, envVar, v)
	}
	return defaultDir
}
