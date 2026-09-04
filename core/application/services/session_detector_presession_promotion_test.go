package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitadapter "irrlicht/core/adapters/outbound/git"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/ports/inbound"
)

// TestSessionDetector_PreSession_GracefulPromotionWhenCWDKnown_Issue906 is a
// regression test for issue #906: a mistral-vibe presession (proc-<pid>,
// born before the real ~/.vibe/logs/session/<id>/messages.jsonl directory
// appears) was silently dropped via the crude same-PID cleanup
// (cleanupStalePIDHolders, KindTranscriptRemoved) instead of the graceful
// CWD-matched promotion (cleanupPreSessionsForProject, KindPreSessionRemoved)
// — losing the presession's own ready->working->ready arc with no trace of
// having been superseded.
//
// Root cause: EnrichNewSession resolves a real vibe session's cwd via
// git.Adapter.GetCWDFromTranscript, which only knew the Kiro-CLI same-basename
// sidecar convention (messages.json) and Antigravity's history.jsonl — vibe's
// meta.json (fixed filename, cwd nested under environment.working_directory)
// matched neither, so state.CWD stayed "" at finalizeNewSession time and the
// CWD fallback in cleanupPreSessionsForProject never got a chance to fire,
// leaving same-PID cleanup as the only path that ever reconciled the
// presession — even for a genuinely different (post-/clear) session.
//
// This test drives the real git.Adapter (not a mock) against an actual
// messages.jsonl + meta.json pair on disk, proving cwd resolves in time for
// the graceful promotion to win.
func TestSessionDetector_PreSession_GracefulPromotionWhenCWDKnown_Issue906(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "cwd") // must exist on disk: admitNewSession's cwdMissing gate rejects a nonexistent cwd
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(dir, "session_real_1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(sessionDir, "messages.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	metaBody := `{"environment":{"working_directory":"` + cwd + `"}}`
	if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(metaBody), 0o644); err != nil {
		t.Fatal(err)
	}

	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "mistral-vibe"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	det := services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW:      pw,
		Repo:    repo,
		Log:     &mockLogger{},
		Git:     gitadapter.New(),
		Metrics: &mockMetrics{},
		Version: "test",
	})
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	// Presession born for the vibe process (mirrors processlifecycle/scanner.go
	// minting proc-<pid> from a matched-but-not-yet-transcripted process). Its
	// ProjectDir is the scanner's process-derived label, deliberately distinct
	// from the real session's ProjectDir (vibe uses the session-dir name) so
	// the graceful match can only succeed via the CWD fallback below.
	tw.ch <- agent.Event{
		Type:       agent.EventNewSession,
		SessionID:  "proc-70001",
		ProjectDir: "-Users-test-vibe-clear-project",
		CWD:        cwd,
	}
	waitForCondition(func() bool { s, _ := repo.Load("proc-70001"); return s != nil }, time.Second)

	// The real session directory appears — no CWD on the event itself
	// (vibe's FilesUnderRoot watcher carries none), so state.CWD must come
	// from EnrichNewSession's transcript-sidecar resolution.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "session_real_1",
		ProjectDir:     "session_real_1",
		TranscriptPath: transcriptPath,
	}
	waitForCondition(func() bool { s, _ := repo.Load("session_real_1"); return s != nil }, time.Second)
	// cleanupPreSessionsForProject runs synchronously within the same
	// finalizeNewSession call that just saved session_real_1, but on a
	// different goroutine than this poll — wait for its actual effect
	// (proc-70001 gone) rather than a fixed sleep guessing how long that takes.
	waitForCondition(func() bool { s, _ := repo.Load("proc-70001"); return s == nil }, time.Second)
	cancel()
	<-done

	if s, _ := repo.Load("proc-70001"); s != nil {
		t.Errorf("presession proc-70001 should have been retired once the real session arrived")
	}

	assertRetiredViaGracefulPromotion(t, rec, "proc-70001")
}

// assertRetiredViaGracefulPromotion checks that sessionID was retired via the
// graceful CWD-matched promotion (KindPreSessionRemoved), not the crude
// same-PID cleanup path (KindTranscriptRemoved) — the distinction issue #906
// is about.
func assertRetiredViaGracefulPromotion(t *testing.T, rec *mockRecorder, sessionID string) {
	t.Helper()
	var sawGracefulPromotion, sawCrudeSamePIDCleanup bool
	for _, ev := range rec.snapshot() {
		if ev.SessionID != sessionID {
			continue
		}
		switch ev.Kind {
		case lifecycle.KindPreSessionRemoved:
			sawGracefulPromotion = true
		case lifecycle.KindTranscriptRemoved:
			sawCrudeSamePIDCleanup = true
		}
	}
	if !sawGracefulPromotion {
		t.Errorf("expected %s to be retired via KindPreSessionRemoved (graceful CWD-matched promotion)", sessionID)
	}
	if sawCrudeSamePIDCleanup {
		t.Errorf("%s was retired via the crude same-PID cleanup path instead of graceful promotion", sessionID)
	}
}

// TestSessionDetector_SetSessionSupersededHandler_FiresOnProjectMatch covers
// the one reconciliation path SessionDetector.SetSessionSupersededHandler
// exists for and PIDManager's own tests cannot reach.
//
// PIDManager owns three supersession paths and pid_manager_test.go registers
// its handler straight onto the PIDManager to exercise them. The fourth lives
// here: cleanupPreSessionsForProject deletes a matched pre-session's row
// directly rather than through PIDManager, and reads the handler back off
// pidMgr (retirePreSession). Registering through the SessionDetector wrapper
// is the only way that path ever gets a handler, so without this test the
// wrapper is a seam nothing re-runs — it lost its only production caller when
// #1875 removed the remote-control re-key wiring.
//
// It also pins the ordering the re-key contract depends on: the handler is
// invoked BEFORE the row is deleted (#997), so a handler's own Load(oldID)
// still resolves.
func TestSessionDetector_SetSessionSupersededHandler_FiresOnProjectMatch(t *testing.T) {
	cwd := t.TempDir() // admitNewSession's cwdMissing gate rejects a nonexistent cwd
	// cleanupPreSessionsForProject only runs for a transcript-backed arrival
	// (session_detector_activity.go gates it on ev.TranscriptPath != ""), so
	// the real session needs a transcript on disk or the path under test is
	// never entered and the assertion below would fail for the wrong reason.
	transcript := filepath.Join(t.TempDir(), "real.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "claude-code"})
	repo := newMockRepo()
	det := services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW:      newMockProcessWatcher(),
		Repo:    repo,
		Log:     &mockLogger{},
		Git:     gitadapter.New(),
		Metrics: &mockMetrics{},
		Version: "test",
	})

	type supersession struct {
		oldID, newID string
		oldRowLoaded bool
	}
	fired := make(chan supersession, 4)
	det.SetSessionSupersededHandler(func(oldID, newID string) {
		s, _ := repo.Load(oldID)
		fired <- supersession{oldID: oldID, newID: newID, oldRowLoaded: s != nil}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(20 * time.Millisecond)

	const projectDir = "-Users-test-superseded-project"
	tw.ch <- agent.Event{
		Type:       agent.EventNewSession,
		SessionID:  "proc-70051",
		ProjectDir: projectDir,
		CWD:        cwd,
	}
	waitForCondition(func() bool { s, _ := repo.Load("proc-70051"); return s != nil }, time.Second)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "session_real_superseded",
		ProjectDir:     projectDir,
		CWD:            cwd,
		TranscriptPath: transcript,
	}
	waitForCondition(func() bool { s, _ := repo.Load("session_real_superseded"); return s != nil }, time.Second)

	select {
	case got := <-fired:
		if got.oldID != "proc-70051" || got.newID != "session_real_superseded" {
			t.Errorf("handler got (%q, %q), want (proc-70051, session_real_superseded)", got.oldID, got.newID)
		}
		if !got.oldRowLoaded {
			t.Error("handler ran AFTER the pre-session row was deleted; a re-key handler's own Load(oldID) must still resolve (#997)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SetSessionSupersededHandler's callback never fired for the project-matched pre-session retirement")
	}
}
