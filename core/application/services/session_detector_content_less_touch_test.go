package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_ContentLessTouch_DoesNotForceWorking is the regression
// test for issue #905. A real fswatcher pass that writes zero new bytes to
// the transcript (mistral-vibe touches messages.jsonl when handling a
// synchronous slash command like /help, but persists no new content) must
// NOT force a ready session back to working on the stale LastEventType left
// over from the session's prior turn_done.
//
// This differs from #329 (TestSessionDetector_Activity_NoSubstantiveActivity_
// HoldsState): #329's tailer pass parses at least one line (Skip=true, so
// NoSubstantiveActivity=true short-circuits classification entirely). Here
// the tailer parses zero new lines, so NoSubstantiveActivity stays false and
// classification runs — only the force-bounce itself must be suppressed,
// gated on real transcript growth.
func TestSessionDetector_ContentLessTouch_DoesNotForceWorking(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	rec := &mockRecorder{}
	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	path := filepath.Join(t.TempDir(), "messages.jsonl")
	content := []byte(`{"role":"user","content":"hi"}` + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write transcript fixture: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript fixture: %v", err)
	}

	repo.states["vibe1"] = &session.SessionState{
		SessionID:          "vibe1",
		State:              session.StateReady,
		TranscriptPath:     path,
		LastTranscriptSize: fi.Size(), // matches the file on disk: no growth this pass
		FirstSeen:          time.Now().Unix(),
		UpdatedAt:          time.Now().Unix(),
		EventCount:         5,
		Metrics: &session.SessionMetrics{
			LastEventType:         "turn_done", // stale — would satisfy the old force-bounce predicate
			NoSubstantiveActivity: false,       // zero-line pass, not #329's Skip=true case
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "vibe1",
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
	}

	// Give the pass time to land. There is no observable counter bump to poll
	// on (a content-less touch intentionally leaves EventCount/UpdatedAt
	// alone — see observeTranscriptGrowth), so this test settles on a fixed
	// wait instead of waitForCondition.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("vibe1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (content-less touch must not force ready->working)", state.State)
	}
	for _, ev := range rec.snapshot() {
		if ev.SessionID == "vibe1" && ev.Kind == lifecycle.KindStateTransition {
			t.Errorf("unexpected state transition recorded: %+v", ev)
		}
	}
}

// TestSessionDetector_ContentLessTouch_SyntheticHookStillForces guards the
// inverse: a hook-synthetic event (dispatchHookActivity's PreToolUse/
// PermissionRequest/PreCompact injection) must still force ready->working
// even though it precedes the transcript flush and so — like the buggy case
// above — carries zero transcript growth. Without this, fixing #905 would
// regress #307/#657's hook-driven immediate transitions.
func TestSessionDetector_ContentLessTouch_SyntheticHookStillForces(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	rec := &mockRecorder{}
	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := []byte(`{"role":"user","content":"hi"}` + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write transcript fixture: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript fixture: %v", err)
	}

	repo.states["cc1"] = &session.SessionState{
		SessionID:          "cc1",
		State:              session.StateReady,
		TranscriptPath:     path,
		LastTranscriptSize: fi.Size(),
		FirstSeen:          time.Now().Unix(),
		UpdatedAt:          time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			LastEventType:         "turn_done",
			NoSubstantiveActivity: false,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { <-done }()
	defer cancel()
	time.Sleep(20 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "cc1",
		ProjectDir:     "-Users-test",
		TranscriptPath: path,
		Synthetic:      true,
	}

	forced := func() bool {
		for _, ev := range rec.snapshot() {
			if ev.SessionID == "cc1" && ev.Kind == lifecycle.KindStateTransition {
				return true
			}
		}
		return false
	}
	waitForCondition(forced, time.Second)
	if !forced() {
		t.Errorf("expected a state-transition lifecycle event for cc1 (synthetic hook event must still force ready->working)")
	}
}
