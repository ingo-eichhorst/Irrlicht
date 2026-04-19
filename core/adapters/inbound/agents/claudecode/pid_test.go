package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// withTestDeps swaps sessionsDir, pidAlive, and discoverByCWD for the duration
// of a test and restores them afterward. alive is the set of PIDs considered
// live. fallbackPids is what the stubbed discoverByCWD should return as
// candidates before the wrapped disambiguator runs.
func withTestDeps(t *testing.T, alive map[int]bool, fallbackPids []int) string {
	t.Helper()
	dir := t.TempDir()

	origSessionsDir := sessionsDir
	origPidAlive := pidAlive
	origDiscoverByCWD := discoverByCWD

	sessionsDir = dir
	pidAlive = func(pid int) bool { return alive[pid] }
	discoverByCWD = func(_ string, _ string, disambiguate func([]int) int) (int, error) {
		if len(fallbackPids) == 0 {
			return 0, nil
		}
		if disambiguate != nil {
			return disambiguate(fallbackPids), nil
		}
		return fallbackPids[0], nil
	}

	t.Cleanup(func() {
		sessionsDir = origSessionsDir
		pidAlive = origPidAlive
		discoverByCWD = origDiscoverByCWD
	})
	return dir
}

func writeMeta(t *testing.T, dir string, pid int, sessionID string) {
	t.Helper()
	// Mirror Claude's on-disk schema at ~/.claude/sessions/<pid>.json.
	// We intentionally write only the fields DiscoverPID consumes; real
	// files include cwd/startedAt/kind/entrypoint which json.Unmarshal
	// silently drops.
	meta := claudeSessionMeta{PID: pid, SessionID: sessionID}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

func transcriptFor(sessionID string) string {
	return "/Users/x/.claude/projects/foo/" + sessionID + ".jsonl"
}

// writeMetaAt writes a metadata file and then sets its mtime to the given time.
// Used by #169 regression tests to simulate stale ~/.claude/sessions/<pid>.json
// entries left behind after a /clear.
func writeMetaAt(t *testing.T, dir string, pid int, sessionID string, mtime time.Time) {
	t.Helper()
	writeMeta(t, dir, pid, sessionID)
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// transcriptForWithFile creates a real transcript file in t.TempDir so that
// os.Stat returns a meaningful mtime. Returns the absolute path.
func transcriptForWithFile(t *testing.T, sessionID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestDiscoverPID_StrongMatchByMetadata(t *testing.T) {
	const sid = "aaaa-1111"
	dir := withTestDeps(t, map[int]bool{42: true}, nil)
	writeMeta(t, dir, 42, sid)

	pid, err := DiscoverPID("/repo", transcriptFor(sid), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 42 {
		t.Fatalf("got pid=%d, want 42", pid)
	}
}

func TestDiscoverPID_StrongMatchAmongMultipleMetadataFiles(t *testing.T) {
	const wantSID = "bbbb-2222"
	dir := withTestDeps(t, map[int]bool{100: true, 200: true, 300: true}, nil)
	writeMeta(t, dir, 100, "other-1")
	writeMeta(t, dir, 200, wantSID)
	writeMeta(t, dir, 300, "other-3")

	pid, err := DiscoverPID("/repo", transcriptFor(wantSID), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 200 {
		t.Fatalf("got pid=%d, want 200", pid)
	}
}

func TestDiscoverPID_DeadPIDMetadataIsSkipped(t *testing.T) {
	const sid = "cccc-3333"
	// Metadata says pid=42 owns the session, but 42 is dead.
	// No live fallback candidates → returns 0.
	dir := withTestDeps(t, map[int]bool{}, nil)
	writeMeta(t, dir, 42, sid)

	pid, err := DiscoverPID("/repo", transcriptFor(sid), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 0 {
		t.Fatalf("got pid=%d, want 0 (dead metadata should be ignored)", pid)
	}
}

func TestDiscoverPID_CorruptMetadataIsIgnored(t *testing.T) {
	const sid = "dddd-4444"
	dir := withTestDeps(t, map[int]bool{50: true}, nil)

	// Write a garbage file alongside a valid one.
	if err := os.WriteFile(filepath.Join(dir, "99999.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	writeMeta(t, dir, 50, sid)

	pid, err := DiscoverPID("/repo", transcriptFor(sid), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 50 {
		t.Fatalf("got pid=%d, want 50", pid)
	}
}

func TestDiscoverPID_FallbackAmbiguousReturnsZero(t *testing.T) {
	// No metadata match → fallback runs → cwd has two live claude processes.
	// The fix: ambiguous fallback returns 0, not a guess.
	withTestDeps(t, map[int]bool{100: true, 200: true}, []int{100, 200})

	pid, err := DiscoverPID("/repo", transcriptFor("unknown-sid"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 0 {
		t.Fatalf("got pid=%d, want 0 (ambiguous fallback must not guess)", pid)
	}
}

func TestDiscoverPID_FallbackSingleCandidate(t *testing.T) {
	// No metadata match, cwd has exactly one claude process → return it.
	withTestDeps(t, map[int]bool{777: true}, []int{777})

	pid, err := DiscoverPID("/repo", transcriptFor("unknown-sid"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 777 {
		t.Fatalf("got pid=%d, want 777", pid)
	}
}

func TestDiscoverPID_FallbackExcludesPIDsClaimedByOthers(t *testing.T) {
	// Two claude processes live in /repo. Metadata says pid=200 owns a
	// different sessionId, leaving only pid=100 as a fallback candidate.
	// Even though the target sessionId has no metadata file yet, the
	// fallback must return 100 (not 0 and not 200).
	dir := withTestDeps(t,
		map[int]bool{100: true, 200: true},
		[]int{100, 200},
	)
	writeMeta(t, dir, 200, "other-session")

	pid, err := DiscoverPID("/repo", transcriptFor("new-sid"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 100 {
		t.Fatalf("got pid=%d, want 100 (metadata should exclude 200 from fallback)", pid)
	}
}

func TestDiscoverPID_EmptyTranscriptFallsBackToCWD(t *testing.T) {
	// No transcriptPath → can't derive sessionId → skip strong match, go
	// straight to fallback.
	withTestDeps(t, map[int]bool{42: true}, []int{42})

	pid, err := DiscoverPID("/repo", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 42 {
		t.Fatalf("got pid=%d, want 42", pid)
	}
}

func TestDiscoverPID_StaleMetadataDoesNotBlockCWDFallback(t *testing.T) {
	// Regression for #169. After /clear, Claude keeps the same PID (62896)
	// but ~/.claude/sessions/62896.json still points at the OLD sessionId.
	// The new session's transcript was written after /clear, so it is newer
	// than the stale metadata; DiscoverPID must NOT treat the stale entry
	// as a hard negative filter, and must return 62896 via the CWD fallback.
	dir := withTestDeps(t, map[int]bool{62896: true}, []int{62896})
	writeMetaAt(t, dir, 62896, "old-session", time.Now().Add(-30*time.Second))
	transcript := transcriptForWithFile(t, "new-session")

	pid, err := DiscoverPID("/repo", transcript, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 62896 {
		t.Fatalf("got pid=%d, want 62896 (stale metadata must not block fallback)", pid)
	}
}

func TestDiscoverPID_FreshMetadataStillBlocksConcurrentSession(t *testing.T) {
	// #109 protection regression guard: two concurrent Claude processes in
	// the same CWD, both with fresh metadata. The one whose metadata points
	// at a different sessionId must still be filtered out.
	dir := withTestDeps(t,
		map[int]bool{100: true, 200: true},
		[]int{100, 200},
	)
	writeMetaAt(t, dir, 200, "other-session", time.Now())
	transcript := transcriptForWithFile(t, "new-sid")

	pid, err := DiscoverPID("/repo", transcript, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 100 {
		t.Fatalf("got pid=%d, want 100 (fresh metadata must still exclude 200)", pid)
	}
}

func TestDiscoverPID_NoTranscriptFileFallsBackSafely(t *testing.T) {
	// When the transcript file doesn't exist on disk (os.Stat fails),
	// wantMTime is zero and the mtime gate must be inert — current
	// negative-filtering behavior must be preserved.
	dir := withTestDeps(t,
		map[int]bool{100: true, 200: true},
		[]int{100, 200},
	)
	writeMeta(t, dir, 200, "other-session")

	pid, err := DiscoverPID("/repo", transcriptFor("new-sid"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 100 {
		t.Fatalf("got pid=%d, want 100 (gate must be inert without transcript mtime)", pid)
	}
}

func TestSessionIDFromTranscript(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/a/b/c/deadbeef-1234.jsonl", "deadbeef-1234"},
		{"", ""},
		{"/a/b/not-a-transcript.txt", ""},
		{"foo.jsonl", "foo"},
	}
	for _, tc := range cases {
		if got := sessionIDFromTranscript(tc.in); got != tc.want {
			t.Errorf("sessionIDFromTranscript(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
