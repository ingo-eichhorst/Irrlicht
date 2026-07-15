// Package vibe provides an inbound adapter that watches Mistral Vibe
// transcript files under $VIBE_HOME/logs/session/<session-id>/messages.jsonl
// (default ~/.vibe/logs/session).
//
// Mistral Vibe (https://github.com/mistralai/mistral-vibe) is Mistral AI's
// open-source CLI coding agent. It appends one JSON object per line to a
// per-session messages.jsonl and keeps a sibling meta.json sidecar with the
// working directory, active model, and running token/cost stats — the JSONL
// itself carries no cwd, model, or usage, so those derive from the sidecar
// (mirroring kiro-cli and antigravity).
package vibe

import (
	"path/filepath"
	"regexp"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
)

// AdapterName identifies sessions originating from Mistral Vibe. It matches
// the onboarding-factory column slug (replaydata/agents/mistral-vibe/).
const AdapterName = "mistral-vibe"

// transcriptFilename is the constant basename Vibe writes for every session;
// the session ID therefore comes from the parent directory, not the filename.
const transcriptFilename = "messages.jsonl"

// defaultRootDir is the path relative to $HOME where Vibe stores session
// directories when $VIBE_HOME is unset. Each session is a <session-id>/
// folder holding messages.jsonl (the conversation) and meta.json (the
// sidecar).
const defaultRootDir = ".vibe/logs/session"

// vibeHomeEnvVar is the upstream Vibe env var that relocates the agent's home
// directory (default: ~/.vibe). Verified against the installed package source
// at v2.19.1 — vibe/core/paths/_vibe_home.py resolves
//
//	SESSION_LOG_DIR = VIBE_HOME/"logs"/"session"
//	VIBE_HOME       = os.getenv("VIBE_HOME") or ~/.vibe
//
// so when it is set, sessions move to $VIBE_HOME/logs/session. It is honored
// in source but absent from Vibe's docs — the previous justification here
// ("Vibe documents no env var that relocates this root") read the docs, not
// the source, and was wrong: undocumented is not absent.
//
// Note a deliberate narrowing vs upstream: Vibe applies expanduser().resolve()
// to the value, so it also accepts "~/x" and cwd-relative paths. irrlicht
// honors absolute values only (agentpaths.FromEnv logs and ignores the rest),
// matching every sibling adapter rather than reimplementing shell expansion.
const vibeHomeEnvVar = "VIBE_HOME"

// sessionsDir returns the directory the Vibe adapter should watch —
// $VIBE_HOME/logs/session when that override is set, else defaultRootDir.
func sessionsDir() string {
	return agentpaths.FromEnv("vibe", vibeHomeEnvVar, defaultRootDir, "logs", "session")
}

// processCmdPattern recognizes a running Vibe process on the full command
// line. Vibe ships as a Python console-script (no setproctitle), so the OS
// process name is the interpreter, not "vibe" — an ExactName match on "vibe"
// would never fire. The launched command line is
//
//	<uv-tools>/mistral-vibe/bin/python3 <prefix>/bin/vibe [args]
//
// so we anchor on either the uv tool-venv interpreter path or a `vibe`
// executable token bounded by "/" and a space/end. `/vibe/logs` (the
// transcript root) does not match — `/vibe` there is followed by "/", not a
// space or end — so the daemon's own watchers never self-trip the matcher.
const processCmdPattern = `(^|/)vibe( |$)|mistral-vibe/bin/python`

var processCmdRegex = regexp.MustCompile(processCmdPattern)

// sessionIDFromPath derives the session ID from a transcript path, and
// reports "" for any file the adapter does not own so the watcher skips it.
// Vibe writes
//
//	~/.vibe/logs/session/<session-id>/messages.jsonl
//
// so the ID is the <session-id> directory one level above the file. Only the
// constant messages.jsonl is accepted; the sibling meta.json (and anything
// else) returns "" so exactly one session is minted per directory.
func sessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	id := filepath.Base(filepath.Dir(path))
	if isEmptyOrRootDirName(id) {
		return ""
	}
	return id
}

// isEmptyOrRootDirName reports whether filepath.Base/Dir bottomed out at the
// filesystem root instead of yielding a real directory name.
func isEmptyOrRootDirName(name string) bool {
	return name == "" || name == "." || name == string(filepath.Separator)
}
