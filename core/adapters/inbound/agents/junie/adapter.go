// Package junie provides an inbound adapter that watches JetBrains Junie
// transcript files under ~/.junie/sessions/<session-id>/events.jsonl.
//
// Junie (https://www.jetbrains.com/junie/) is JetBrains' coding agent
// (CLI + IDE). Each session appends one JSON event per line to a per-session
// events.jsonl and keeps sibling files the adapter must ignore: state.json
// (a rewritten snapshot), transcript.md (a rendered copy), and task-*
// subdirectories. The session root itself holds an index.jsonl the adapter
// must also skip, or a phantom "index" session would be minted.
//
// Junie additionally writes process sidecars under ~/.junie/processes/
// (<pid>-<session-id>-<hash>.json) naming the PID, session ID, and project
// path — a direct session→PID binding, stronger than the CWD scan sibling
// adapters need (see pid.go). Sidecars can outlive their process, so a PID
// read from one is liveness- and command-pattern-checked before being
// trusted.
//
// The events.jsonl schema is unversioned and reverse-engineered from live
// sessions; unknown event kinds are skipped, never fatal (see parser.go).
package junie

import (
	"path/filepath"
	"regexp"
	"strings"
)

// AdapterName identifies sessions originating from JetBrains Junie.
const AdapterName = "junie"

// transcriptFilename is the constant basename Junie writes for every session;
// the session ID therefore comes from the parent directory, not the filename.
const transcriptFilename = "events.jsonl"

// defaultRootDir is the path relative to $HOME where Junie stores session
// directories. Junie documents no env var that relocates ~/.junie ($JUNIE_DATA
// exists but points at the version/install store under ~/.local/share/junie,
// not at the session root — verified against a live install, 2026-08), so the
// root is a plain $HOME join.
const defaultRootDir = ".junie/sessions"

// sessionDirPrefix distinguishes session directories from the task-*
// subdirectories Junie nests inside them. Observed IDs look like
// session-260825-151320-19jp; anchoring on the prefix (rather than the full
// datestamp shape) keeps sessions visible if Junie ever tweaks the suffix
// format, while still rejecting task-* and any stray sibling directory.
const sessionDirPrefix = "session-"

// processCmdPattern recognizes a running Junie process on the full command
// line. The binary path ends in .../junie.app/Contents/MacOS/junie (macOS) or
// a bare .../junie, optionally followed by args (--acp=true for IDE-spawned
// instances, --session-id for resumes), so we anchor on a `junie` path token
// bounded by "/" and a space/end. The watched tree (~/.junie/sessions) does
// not match — there `junie` is preceded by "." not "/" — and intermediate
// path components like .../junie/2929.4/... or junie.app are followed by "/"
// or ".", so the daemon's own watchers never self-trip the matcher.
const processCmdPattern = `(^|/)junie( |$)`

var processCmdRegex = regexp.MustCompile(processCmdPattern)

// sessionIDFromPath derives the session ID from a transcript path, and
// reports "" for any file the adapter does not own so the watcher skips it.
// Junie writes
//
//	~/.junie/sessions/<session-id>/events.jsonl
//
// so the ID is the <session-id> directory one level above the file. Only the
// constant events.jsonl directly under a session-* directory is accepted:
// the root's index.jsonl (its parent is the root, not a session-* dir), the
// sibling state.json/transcript.md, and any events.jsonl inside a task-*
// subdirectory all return "" so exactly one session is minted per session
// directory.
func sessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	id := filepath.Base(filepath.Dir(path))
	if !strings.HasPrefix(id, sessionDirPrefix) {
		return ""
	}
	return id
}
