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
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		warnOnce(adapter+"\x00"+envVar, func() {
			log.Printf("%s: ignoring %s=%q — must be an absolute path (no shell expansion)", adapter, envVar, v)
		})
	}
	return defaultDir
}

// warned tracks which (adapter, envVar) pairs have already logged a rejected
// override, so the warning is emitted once per process rather than once per
// call. FromEnv used to be reached only when the daemon built its watchers; it
// is now also on the hook receiver's per-request path (issue #1361), and that
// endpoint is local and unauthenticated — an un-deduped log would let any local
// process drive unbounded log volume by POSTing in a loop while a misconfigured
// override is set.
var warned sync.Map

func warnOnce(key string, emit func()) {
	if _, seen := warned.LoadOrStore(key, struct{}{}); !seen {
		emit()
	}
}

// resetWarnOnce clears the dedupe. Tests that assert on the warning must call
// it first, or they pass or fail depending on whether an earlier test in the
// package happened to warn for the same adapter and env var.
func resetWarnOnce() { warned = sync.Map{} }

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
//
// An empty dir is an error, not $HOME. Joining "" under the home directory
// would silently answer "the user's entire home directory" for what is really
// an adapter that failed to resolve its root — and for the confinement caller
// that is a fail-open, which is the one direction this must never take.
func AbsRoot(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("agentpaths: empty transcript root")
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dir), nil
}

// NewestFileWithSuffix returns the path of the most recently modified file
// under root (recursively) whose name ends in suffix, or "" if there is none.
//
// Both hook-capable adapters need the same thing for the same reason: the
// newest transcript is the closest proxy on disk for "the version of this
// agent the user is running now" (#1365). Codex reads a cli_version out of its
// session header, Claude Code reads a version stamp off any transcript line —
// different files, different fields, identical walk. Keeping the walk here
// means a third adapter supplies only its own field extraction, and a fix to
// the traversal (symlinks, unreadable dirs, tie-breaking) lands once.
//
// Callers pass a root already resolved through AbsRoot. Handing this a
// $HOME-relative default instead walks the daemon's CWD and returns "" — no
// error, no empty-directory signal, just an answer that reads as "this agent
// has no sessions".
//
// Walk errors are ignored per entry rather than aborting: an unreadable
// subdirectory should cost that subtree, not the whole answer.
func NewestFileWithSuffix(root, suffix string) string {
	var newestPath string
	var newestMod int64
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if mod := info.ModTime().UnixNano(); mod > newestMod {
			newestMod = mod
			newestPath = path
		}
		return nil
	})
	return newestPath
}
