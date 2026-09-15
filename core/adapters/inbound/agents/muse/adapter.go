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
// Stage 1 of the adapter (issue #1960) covered identity, discovery, and
// process binding — this file, pid.go, agent.go, icons.go. Stage 2 added the
// real session.jsonl event parser — turn boundaries, tool calls, permission
// prompts, errors, and token accounting — see parser.go's package doc.
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
// independent, approval-only stream sharing the child's UUID, alongside the
// nested <parent>/subagent/<child-id>/ copy that carries the run/task/tool
// content for the same logical session.
//
// # Correction (issue #1960 stage 3): the two files are DISJOINT, not duplicates
//
// A prior version of this function suppressed that top-level shadow (by
// returning "" for it), on the assumption that the nested copy was already
// "the complete, authoritative transcript" and the shadow was therefore
// redundant. Direct comparison of all 11 shadow/nested pairs found in a real
// 14-session corpus disproved that: every nested copy carries ZERO
// kind:"approval" records (verified by counting payload_type/payload.kind
// across every line of both files — task/run/tool_batch.effect.* only), and
// every one of those 11 approval records — the sole signal driving the
// adapter's permission_requested/permission_completed events, hence the
// "waiting" session state, see parser.go's Waiting section — lived ONLY in
// the shadow this function used to discard. Suppressing the shadow therefore
// discarded the ONLY copy of a subagent's own approval-wait signal, with no
// duplicate anywhere else to fall back on.
//
// The fix: both halves now resolve to the SAME id here (this function no
// longer distinguishes nested from top-level shadow at all — the shape
// check below runs for every path), and parentSessionIDFromPath tags the
// top-level shadow as a child of the SAME parent the nested copy already
// reports (via subagentShadowParentID). This is safe against the daemon's
// existing session-identity model (core/domain/agent/source.go: sessions are
// deduplicated by ID in the repo, not by transcript path, and
// backfillExistingSession only ever sets an EMPTY TranscriptPath — never
// overwrites an already-known one) PROVIDED the nested copy is discovered
// before the shadow, which is what makes the shadow's later arrival a
// backfill onto an existing session rather than a race to seed a new one
// from the wrong (tiny, approval-only) file. Verified, not assumed: `stat -f
// %B` (APFS file-birth time) on all 11 real pairs on this machine shows the
// nested file created 15–175s before its shadow in every case — consistent
// with the mechanism (a subagent must exist and propose a tool call, both
// recorded in the nested stream, before that call can ever need approval).
//
// See subagentShadowParentID's own doc for what remains UNVERIFIED about
// the day-boundary and the theoretical shadow-observed-before-nested-exists
// race this ordering evidence does not fully rule out.
func sessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	sessionDir := filepath.Dir(path)
	id := filepath.Base(sessionDir)
	if !sessionIDPattern.MatchString(id) {
		return ""
	}
	// Nested child session (format-spec §9: every child that produced
	// output gets its own <parent>/subagent/<child-id>/ directory, always)
	// and top-level shadow (the anomaly above) both mint the same id here —
	// parentSessionIDFromPath is what tells them apart for parent linkage.
	return id
}

// parentSessionIDFromPath derives a session's parent from its own path.
// Muse encodes the ordinary case in directory nesting
// (<parent-session-dir>/subagent/<child-id>/session.jsonl, format-spec §9),
// so no file content needs reading (unlike Codex, which reads a header
// record for the same purpose). For a top-level session.jsonl (nothing
// above it is named "subagent") it falls back to subagentShadowParentID,
// which reports a parent when this top-level id is actually the anomaly's
// shadow half of some sibling's subagent tree (format-spec §1, and see
// sessionIDFromPath's doc for the stage-3 correction that made this
// fallback necessary — a genuine top-level session, the common case, has no
// such sibling and still gets "" here). Returns "" for anything the
// adapter does not otherwise own.
func parentSessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	sessionDir := filepath.Dir(path)
	subagentDir := filepath.Dir(sessionDir)
	if filepath.Base(subagentDir) == subagentDirName {
		parentDir := filepath.Dir(subagentDir)
		parentID := filepath.Base(parentDir)
		if !sessionIDPattern.MatchString(parentID) {
			return ""
		}
		return parentID
	}
	id := filepath.Base(sessionDir)
	if !sessionIDPattern.MatchString(id) {
		return ""
	}
	return subagentShadowParentID(path, id)
}

// subagentShadowParentID reports the parent session id when the top-level
// session directory named id (a sibling of path, under the same YYYY/MM/DD
// date bucket) is the approval-only shadow format-spec §1 documents: it
// returns the OTHER session's id when some sibling session directory under
// the same date bucket has a subagent/<id>/session.jsonl of its own, i.e.
// some sibling session's child-session tree already claims id as a nested
// copy — and "" when id is an ordinary top-level session with no such
// sibling (the common case).
//
// Scoped to the date bucket (not the whole sessions root) both because
// format-spec's one reproduced example was same-day (parent and child
// directories dated identically) and to keep the scan cheap — a handful to
// a few dozen stat calls per newly-seen top-level session, not a walk of
// the adapter's entire history.
//
// UNVERIFIED, both left for a follow-up to confirm against a live install:
// (1) whether a session can ever span a day boundary, which would put the
// shadow and its nested copy in different date buckets and defeat this scan
// entirely (format-spec §1 also could not verify this); (2) the
// theoretical race where the top-level shadow's own transcript-create event
// is processed by a live watcher before the nested subagent directory has
// appeared on disk — this function's ReadDir/Stat calls would see "not
// shadowed" at that exact instant and the shadow would (briefly) seed its
// own top-level session before the nested copy's later event backfills the
// correct parent onto it via the daemon's existing SessionID-keyed dedup
// (see sessionIDFromPath's doc). Direct measurement on this machine (`stat
// -f %B`, APFS file-birth time) found the nested file created 15–175s
// before its shadow in all 11 real pairs — a comfortable margin against a
// live watcher's normal processing latency — so this race is judged
// unlikely in practice, not ruled out. A ReadDir failure (unreadable date
// directory, root not yet fully populated) is treated as "cannot tell"
// rather than "is a shadow": guessing a legitimate top-level session has an
// undiscoverable parent would be a worse failure than rarely missing a real
// shadow's parent link on this one pass (a later pass, once the directory
// is readable, still finds it).
func subagentShadowParentID(path, id string) string {
	dateDir := filepath.Dir(filepath.Dir(path))
	entries, err := os.ReadDir(dateDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == id {
			continue
		}
		nested := filepath.Join(dateDir, e.Name(), subagentDirName, id, transcriptFilename)
		if _, err := os.Stat(nested); err == nil {
			return e.Name()
		}
	}
	return ""
}
