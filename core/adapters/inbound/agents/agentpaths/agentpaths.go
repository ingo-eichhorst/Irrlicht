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

// AbsRoot resolves a declared transcript root to an absolute path: used as-is
// when already absolute, otherwise joined under the user's home directory.
//
// This is the other half of the rule FromEnv starts. A root reaching the
// runtime is either an absolute path (an env override was honored) or a
// $HOME-relative default like ".claude/projects", and every consumer has to
// close that gap the same way — the fswatcher that watches the tree and the
// hook receiver that confines caller-supplied paths to it must agree on where
// the tree is, or confinement guards a different directory than the one being
// watched (issue #1361). Exported here, next to FromEnv, so there is one
// implementation rather than one per consumer.
//
// It is purely lexical: nothing is created, statted, or symlink-resolved.
// Callers that need a real on-disk identity resolve symlinks themselves.
func AbsRoot(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dir), nil
}
