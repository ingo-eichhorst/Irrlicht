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

	var sawGracefulPromotion, sawCrudeSamePIDCleanup bool
	for _, ev := range rec.snapshot() {
		if ev.SessionID != "proc-70001" {
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
		t.Errorf("expected proc-70001 to be retired via KindPreSessionRemoved (graceful CWD-matched promotion)")
	}
	if sawCrudeSamePIDCleanup {
		t.Errorf("proc-70001 was retired via the crude same-PID cleanup path instead of graceful promotion")
	}
}
