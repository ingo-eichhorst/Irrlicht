// Package vibe provides an inbound adapter that watches Mistral Vibe
// transcript files under ~/.vibe/logs/session/<session-id>/messages.jsonl.
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
)

// AdapterName identifies sessions originating from Mistral Vibe. It matches
// the onboarding-factory column slug (replaydata/agents/mistral-vibe/).
const AdapterName = "mistral-vibe"

// transcriptFilename is the constant basename Vibe writes for every session;
// the session ID therefore comes from the parent directory, not the filename.
const transcriptFilename = "messages.jsonl"

// defaultRootDir is the path relative to $HOME where Vibe stores session
// directories. Each session is a <session-id>/ folder holding messages.jsonl
// (the conversation) and meta.json (the sidecar). Vibe documents no env var
// that relocates this root, so it is constant — kept as a function to mirror
// the sibling adapters.
func sessionsDir() string { return ".vibe/logs/session" }

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
