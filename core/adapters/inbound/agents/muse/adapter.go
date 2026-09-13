// Package muse provides an inbound adapter that watches Meta Muse Code
// transcript files under
// ~/.local/share/muse/sessions/<YYYY>/<MM>/<DD>/<session-uuid>/session.jsonl.
//
// Muse (Meta's `muse` CLI, v1.2.1 at the time this adapter was written) ships
// as a bash launcher that `exec`s a version-pinned native binary
// (muse-bin-<version>-R<build>) — exec replaces the process image, so only
// the pinned binary is ever observed running, never the wrapper
// (format-spec §2). Each session appends one JSON record per line to a
// per-session session.jsonl and keeps sibling files this stage's discovery
// code must ignore: .session.lock (a "pid=<N>" stamp plus, while the session
// is live, an advisory-flock file descriptor — format-spec §1), cron.db and
// session.peer-history.sqlite3 (per-session SQLite stores this stage never
// opens), a cli-<uuid>.log debug log, and three optional subdirectories —
// approval-review/ (a separate LLM-judge review stream, one file per
// reviewed call, never named session.jsonl), tool-outputs/ (a spool
// directory), and subagent/<child-id>/ (nested child-session copies, see
// parentSessionIDFromPath below). The root's ~/.local/share/muse/
// session-index.db sidecar is deliberately never read by this adapter:
// format-spec §6 measured it out of sync with the live session set (2 of 14
// sessions indexed at read time, one of those carrying placeholder-only
// data), so it is not a reliable source for anything — see agent.go's
// Source doc.
//
// This is stage 1 of the adapter (issue #1960): identity, discovery, and
// process binding only. Parsing session.jsonl's own event vocabulary (turn
// boundaries, tool calls, permission prompts, errors, token accounting) is
// stage 2's scope — this package's Parser is a placeholder that skips every
// line (see agent.go).
package muse

import (
	"os"
	"path/filepath"
	"regexp"
)

// AdapterName identifies sessions originating from Meta Muse Code.
const AdapterName = "muse"

// transcriptFilename is the constant basename Muse writes for every session;
// the session ID therefore comes from the parent directory, not the
// filename (format-spec §1).
const transcriptFilename = "session.jsonl"

// defaultRootDir is the path relative to $HOME where Muse stores session
// directories. format-spec's research found no env var or config key that
// relocates it (the live install it read from used the plain XDG default);
// UNVERIFIED beyond that — no relocation attempt (e.g. via $XDG_DATA_HOME)
// was actually tried, so an override honored in source but undocumented, the
// way vibe's $VIBE_HOME turned out to be, cannot be ruled out.
const defaultRootDir = ".local/share/muse/sessions"

// subagentDirName is the directory Muse nests child-session copies under,
// one level above each child's own session.jsonl (format-spec §9):
// <parent-session-dir>/subagent/<child-id>/session.jsonl.
const subagentDirName = "subagent"

// sessionLockFilename is declared here (rather than only in pid.go) because
// sessionIDFromPath's directory-shape checks and pid.go's lock lookup must
// agree on the sibling filename that is NOT a transcript.
const sessionLockFilename = ".session.lock"

// processCmdPattern recognizes a running Muse process on the full command
// line. Anchoring on the exact binary name would break on every muse
// release, since the OS process name embeds the version and build number
// (muse-bin-1.2.1-R2847.1, format-spec §2) — so the pattern matches the
// muse-bin- prefix, bounded by "/" or start and whitespace or end, the same
// "stable prefix, not full name" reasoning junie's pattern uses for its
// version-varying install layout. The wrapper script (`muse` on $PATH) is
// deliberately not matched: format-spec §2 confirms exec replaces its
// process image before a session ever begins, so it is never observed
// running as a distinct process.
const processCmdPattern = `(^|/)muse-bin-[^/\s]+(\s|$)`

var processCmdRegex = regexp.MustCompile(processCmdPattern)

// sessionIDPattern matches the shape of a Muse session id: a lowercase
// RFC-4122-style UUID (format-spec's samples are UUIDv7-shaped, e.g.
// "01a09c40-6dd8-7732-8c3f-5a7618ffaee4", but nothing in the spec pins the
// version nibble, so the pattern only checks the group-width shape). Used to
// reject non-session directories (the YYYY/MM/DD date components, the
// "sessions" root itself, or any other stray directory) from ever minting a
// phantom session.
var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// sessionIDFromPath derives the session ID from a transcript path, and
// reports "" for any file the adapter does not own so the watcher skips it.
// Muse writes
//
//	~/.local/share/muse/sessions/<YYYY>/<MM>/<DD>/<session-id>/session.jsonl
//
// for a top-level session, and
//
//	<parent-session-dir>/subagent/<child-id>/session.jsonl
//
// for a nested child session (format-spec §9) — both shapes are handled
// here identically: the ID is always the session.jsonl's immediate parent
// directory, validated against sessionIDPattern so a non-UUID parent (a
// date component, the subagent/ directory itself, the sessions root) never
// mints a session.
//
// format-spec §1 documents one further anomaly this function also handles:
// a subagent's child UUID can ALSO surface as a top-level dated directory
// (sessions/<Y>/<M>/<D>/<child-id>/session.jsonl) holding a short,
// independent, approval-only shadow stream sharing the child's UUID — the
// nested <parent>/subagent/<child-id>/ copy is the complete, authoritative
// transcript. isShadowedBySubagentCopy suppresses that top-level shadow (by
// returning "" for it) so it never mints a second "session" for an ID the
// nested copy already owns — see that function's own doc for what remains
// UNVERIFIED about the mechanism and the ordering.
func sessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	sessionDir := filepath.Dir(path)
	id := filepath.Base(sessionDir)
	if !sessionIDPattern.MatchString(id) {
		return ""
	}
	if filepath.Base(filepath.Dir(sessionDir)) == subagentDirName {
		// Nested child session: format-spec §9 confirms every child that
		// produced output gets its own <parent>/subagent/<child-id>/
		// directory, always. No shadow check applies here — the shadow, if
		// one exists, is the OTHER (top-level) file sharing this same id.
		return id
	}
	if isShadowedBySubagentCopy(path, id) {
		return ""
	}
	return id
}

// parentSessionIDFromPath derives a nested child session's parent from its
// own path — Muse encodes the relationship in directory nesting
// (<parent-session-dir>/subagent/<child-id>/session.jsonl, format-spec §9),
// so no file content needs reading (unlike Codex, which reads a header
// record for the same purpose). Returns "" for a top-level session.jsonl
// (nothing above it is named "subagent") and for anything the adapter does
// not otherwise own.
func parentSessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	sessionDir := filepath.Dir(path)
	subagentDir := filepath.Dir(sessionDir)
	if filepath.Base(subagentDir) != subagentDirName {
		return ""
	}
	parentDir := filepath.Dir(subagentDir)
	parentID := filepath.Base(parentDir)
	if !sessionIDPattern.MatchString(parentID) {
		return ""
	}
	return parentID
}

// isShadowedBySubagentCopy reports whether the top-level session directory
// named id (a sibling of path, under the same YYYY/MM/DD date bucket) is
// the approval-only shadow format-spec §1 documents: it returns true when
// SOME OTHER session directory under the same date bucket has a
// subagent/<id>/session.jsonl of its own, i.e. some sibling session's
// child-session tree already claims id as a nested, authoritative copy.
//
// Scoped to the date bucket (not the whole sessions root) both because
// format-spec's one reproduced example was same-day (parent and child
// directories dated identically) and to keep the scan cheap — a handful to
// a few dozen stat calls per newly-seen top-level session, not a walk of
// the adapter's entire history.
//
// UNVERIFIED, both left for stage 2 or a follow-up to confirm against a
// live install: (1) whether a session can ever span a day boundary, which
// would put the shadow and its authoritative nested copy in different date
// buckets and defeat this scan entirely (format-spec §1 also could not
// verify this); (2) the write ORDER of the two files — if the top-level
// shadow can exist on disk before the nested subagent directory is
// created, a watcher event on the shadow arriving first would still mint a
// transient session this function cannot yet see is a shadow. A ReadDir
// failure (unreadable date directory, root not yet fully populated) is
// treated as "cannot tell" rather than "is a shadow": suppressing a
// legitimate top-level session because a sibling directory was briefly
// unreadable would be a worse failure than rarely admitting a real shadow.
func isShadowedBySubagentCopy(path, id string) bool {
	dateDir := filepath.Dir(filepath.Dir(path))
	entries, err := os.ReadDir(dateDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == id {
			continue
		}
		nested := filepath.Join(dateDir, e.Name(), subagentDirName, id, transcriptFilename)
		if _, err := os.Stat(nested); err == nil {
			return true
		}
	}
	return false
}
