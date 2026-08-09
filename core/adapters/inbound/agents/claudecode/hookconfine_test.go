package claudecode

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transcriptStem is a plausible Claude Code transcript filename stem. The
// handler derives the session id from it, so it has to look like one for the
// dispatch path to be reached at all — otherwise a rejection could not be told
// apart from "the id came out empty".
const transcriptStem = "11111111-2222-3333-4444-555555555555"

// confineTestHome relocates $HOME to a temp dir and returns that dir plus an
// existing project directory inside the adapter's declared transcript root
// ($HOME/.claude/projects). Both are absolute.
func confineTestHome(t *testing.T) (home, projectDir string) {
	t.Helper()
	// Delegates rather than relocating again: two relocations in one test
	// would leave a path handed out by the first no longer inside the root.
	home = hookTestHome(t)
	return home, mkdirAllOrFail(t, filepath.Join(home, defaultProjectsDir, "-Users-someone-repo"))
}

// writeTranscriptFile writes a minimal transcript at dir/<stem>.jsonl and
// returns its path.
func writeTranscriptFile(t *testing.T, dir, stem string) string {
	t.Helper()
	path := filepath.Join(dir, stem+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// hookTestHomeMarker is created inside the temp $HOME hookTestHome installs.
// Its presence is how a second call within the same test knows the relocation
// already happened and reuses that root — replacing it would invalidate the
// path an earlier call already returned.
const hookTestHomeMarker = ".irrlicht-hook-test"

// hookTestHome relocates $HOME to a temp directory for the duration of the
// test, once, and returns it.
func hookTestHome(t *testing.T) string {
	t.Helper()
	if home := os.Getenv("HOME"); home != "" {
		if st, err := os.Stat(filepath.Join(home, hookTestHomeMarker)); err == nil && st.IsDir() {
			return home
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, hookTestHomeMarker), 0o700); err != nil {
		t.Fatalf("mark test home: %v", err)
	}
	return home
}

// inTreeTranscript materializes <stem>.jsonl inside the adapter's declared
// transcript root and returns its absolute path. Hook tests need a real
// in-tree file because the receiver confines every caller-supplied path to
// that root (issue #1361) — a fabricated literal is now, correctly, refused.
func inTreeTranscript(t *testing.T, stem string) string {
	t.Helper()
	dir := mkdirAllOrFail(t, filepath.Join(hookTestHome(t), defaultProjectsDir, "p"))
	return writeTranscriptFile(t, dir, stem)
}

// TestHookHandler_OutOfTreeTranscriptRejected is the issue #1361 defect test.
// The hook endpoint is local and unauthenticated, so transcript_path is
// attacker-controlled; a path outside the adapter's declared transcript root
// must never reach the detector, which opens it.
//
// The status stays 200 — a refusal is reported by the log and the counter, not
// on the wire (hookjson.RejectPath explains why). So the dispatch assertion,
// not the status, is what carries this test.
func TestHookHandler_OutOfTreeTranscriptRejected(t *testing.T) {
	confineTestHome(t)
	outside := writeTranscriptFile(t, t.TempDir(), transcriptStem)

	target := &mockTarget{}
	rec := postHook(t, NewHookHandler(target, nil, nil, mockLogger{}), hookPayload{
		TranscriptPath: outside,
		HookEventName:  HookPermissionRequest,
		ToolName:       "Bash",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 — a confinement refusal fails open on the wire (see hookjson.RejectPath)", rec.Code)
	}
	if calls := target.getCalls(); len(calls) != 0 {
		t.Errorf("handler forwarded an out-of-tree transcript_path to the detector: %+v", calls)
	}
}

// TestHookHandler_ParentTraversalTranscriptRejected covers the lexical escape:
// a path that starts inside the declared root and climbs out of it.
func TestHookHandler_ParentTraversalTranscriptRejected(t *testing.T) {
	_, projectDir := confineTestHome(t)
	outside := writeTranscriptFile(t, t.TempDir(), transcriptStem)
	// Built by concatenation, not filepath.Join, because Join cleans: the
	// point is a path whose LEXICAL prefix is the declared root and which
	// climbs out of it. Enough ".." to bottom out at "/", where they stop.
	traversal := projectDir + strings.Repeat("/..", 24) + outside

	target := &mockTarget{}
	rec := postHook(t, NewHookHandler(target, nil, nil, mockLogger{}), hookPayload{
		TranscriptPath: traversal,
		HookEventName:  HookPermissionRequest,
		ToolName:       "Bash",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 — a confinement refusal fails open on the wire (see hookjson.RejectPath)", rec.Code)
	}
	if calls := target.getCalls(); len(calls) != 0 {
		t.Errorf("handler forwarded a parent-traversal transcript_path to the detector: %+v", calls)
	}
}

// TestHookHandler_SymlinkEscapeTranscriptRejected is the ordering test: the
// path is lexically inside the declared root and only leaves it once symlinks
// are resolved. A containment check that runs BEFORE symlink resolution passes
// the two tests above and fails this one.
func TestHookHandler_SymlinkEscapeTranscriptRejected(t *testing.T) {
	_, projectDir := confineTestHome(t)
	outside := writeTranscriptFile(t, t.TempDir(), "secret")
	link := filepath.Join(projectDir, transcriptStem+".jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	target := &mockTarget{}
	rec := postHook(t, NewHookHandler(target, nil, nil, mockLogger{}), hookPayload{
		TranscriptPath: link,
		HookEventName:  HookPermissionRequest,
		ToolName:       "Bash",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 — a confinement refusal fails open on the wire (see hookjson.RejectPath)", rec.Code)
	}
	if calls := target.getCalls(); len(calls) != 0 {
		t.Errorf("handler followed a symlink out of the declared root: %+v", calls)
	}
}

// TestHookHandler_InTreeTranscriptStillAccepted is a LOCK, not a defect test:
// it pins the behaviour confinement must not break, and passes on main by
// construction.
func TestHookHandler_InTreeTranscriptStillAccepted(t *testing.T) {
	_, projectDir := confineTestHome(t)
	inTree := writeTranscriptFile(t, projectDir, transcriptStem)

	target := &mockTarget{}
	rec := postHook(t, NewHookHandler(target, nil, nil, mockLogger{}), hookPayload{
		TranscriptPath: inTree,
		HookEventName:  HookPermissionRequest,
		ToolName:       "Bash",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 for a transcript inside the declared root", rec.Code)
	}
	calls := target.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 dispatch for an in-tree transcript, got %d", len(calls))
	}
	if calls[0].sessionID != transcriptStem {
		t.Errorf("sessionID: got %q, want %q", calls[0].sessionID, transcriptStem)
	}
}

// mkdirAllOrFail creates dir (and parents) and returns it.
func mkdirAllOrFail(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}
