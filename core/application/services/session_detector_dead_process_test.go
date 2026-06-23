package services_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// appendTranscriptLine appends one line to the transcript at path, failing the
// test on any I/O error. Used by the refresh-pass tests to grow a transcript
// between activity events.
func appendTranscriptLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	_ = f.Close()
}

// TestSessionDetector_Activity_FrozenTranscript_DoesNotBumpUpdatedAt is the
// regression test for issue #667. A gemini-cli session whose process died
// mid-turn (pid=0, transcript frozen on a function_call_output) stays working,
// and the 5s refreshStaleSessions ticker re-reads its frozen transcript. That
// re-read used to refresh UpdatedAt to wall-clock "now" every pass, so the
// ready-TTL age-out (which measures now-UpdatedAt) never fired and the session
// was pinned working forever (~23h observed).
//
// The fix gates the activity bump on real transcript growth. A no-op refresh
// over a frozen transcript must touch neither UpdatedAt nor EventCount; a pass
// that observes new bytes must bump both. The state machine still runs every
// pass, so a genuine transition is unaffected.
func TestSessionDetector_Activity_FrozenTranscript_DoesNotBumpUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "gem.jsonl")
	if err := os.WriteFile(tp, []byte("{\"type\":\"user\"}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "gemini-cli"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)

	// gemini-cli dead-process shape: working, pid=0, last real event was a tool
	// result. LastTranscriptSize=0 so the first activity pass sees growth.
	oldTs := time.Now().Add(-time.Hour).Unix()
	repo.states["gem"] = &session.SessionState{
		SessionID:          "gem",
		Adapter:            "gemini-cli",
		State:              session.StateWorking,
		PID:                0,
		TranscriptPath:     tp,
		FirstSeen:          oldTs,
		UpdatedAt:          oldTs,
		LastTranscriptSize: 0,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall: false,
			LastEventType:   "function_call_output",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Let seedFromDisk finish (it re-reads metrics but never bumps EventCount /
	// LastTranscriptSize) before injecting activity.
	time.Sleep(50 * time.Millisecond)

	// Terminal:true bypasses the activity debounce so each event is processed
	// immediately and synchronously on the Run goroutine — no 2s coalescing
	// timer, so the frozen-vs-grown passes are deterministic. Terminal is read
	// only by the debounce fast-path; it does not affect classification.
	activity := func() agent.Event {
		return agent.Event{Type: agent.EventActivity, SessionID: "gem", TranscriptPath: tp, Terminal: true}
	}

	// Pass A — the transcript grew (0 → S1): UpdatedAt and EventCount must bump.
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.eventCountOf("gem") == 1 }, 2*time.Second)
	if got := repo.eventCountOf("gem"); got != 1 {
		t.Fatalf("grown transcript: EventCount = %d, want 1", got)
	}
	tsA := repo.updatedAtOf("gem")
	if tsA <= oldTs {
		t.Fatalf("grown transcript did not advance UpdatedAt: got %d, want > %d", tsA, oldTs)
	}

	// Pass B — the transcript is frozen (size unchanged): no bump. The pass
	// produces no observable field change, so wait on the Save count.
	savesBefore := repo.savesCount()
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.savesCount() > savesBefore }, 2*time.Second)
	if got := repo.eventCountOf("gem"); got != 1 {
		t.Fatalf("frozen transcript bumped EventCount: got %d, want 1 (unchanged)", got)
	}
	if got := repo.updatedAtOf("gem"); got != tsA {
		t.Fatalf("frozen-transcript refresh bumped UpdatedAt: got %d, want %d (unchanged)", got, tsA)
	}

	// Pass C — append new bytes, then refresh: bumping must resume.
	appendTranscriptLine(t, tp, "{\"type\":\"assistant\"}\n")

	tw.ch <- activity()
	waitForCondition(func() bool { return repo.eventCountOf("gem") == 2 }, 2*time.Second)
	if got := repo.eventCountOf("gem"); got != 2 {
		t.Fatalf("grown transcript after freeze: EventCount = %d, want 2", got)
	}
}

// TestSessionDetector_Activity_PID0_NonSubstantiveGrowth_DoesNotBumpUpdatedAt is
// the regression test for issue #735. An Antigravity IDE conversation is
// transcript-first (PID==0). Its transcript is a system log that keeps appending
// SYSTEM steps (CONVERSATION_HISTORY/CHECKPOINT — the parser marks them Skip=true)
// after the agent has stopped, so the file keeps growing with no agent turn.
// Raw byte growth used to re-bump UpdatedAt to wall-clock "now" on every refresh
// tick, so now-UpdatedAt never crossed readyTTL and the PID==0 liveness sweep
// could never reap the session — it lingered as a ghost until a daemon restart.
//
// The fix gates the activity bump on substantive parse activity for PID==0
// sessions: a pass that consumed only skipped lines (NoSubstantiveActivity) must
// leave UpdatedAt and EventCount untouched even though the file grew, so the
// session can age out via the existing PID==0 sweep (which
// TestCheckPIDLiveness_DeadProcessWorking_Reaped covers). A pass carrying a real
// agent event still bumps.
func TestSessionDetector_Activity_PID0_NonSubstantiveGrowth_DoesNotBumpUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(tp, []byte("{\"source\":\"MODEL\",\"type\":\"RUN_COMMAND\"}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// funcMetrics simulates the Antigravity tailer: the `substantive` flag drives
	// NoSubstantiveActivity, mirroring the real parser (real agent lines are
	// substantive; SYSTEM-log noise is Skip=true → non-substantive). LastEventType
	// stays mid-turn (function_call_output) so the session remains working — the
	// stuck-ghost shape — and no state transition muddies UpdatedAt.
	var substantive atomic.Bool
	substantive.Store(true)
	fm := &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:         "function_call_output",
			NoSubstantiveActivity: !substantive.Load(),
		}, nil
	}}

	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "antigravity"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetectorWithMetrics(tw, pw, repo, fm)

	// Antigravity IDE ghost shape: working, pid=0. LastTranscriptSize=0 so the
	// first activity pass sees growth.
	oldTs := time.Now().Add(-time.Hour).Unix()
	repo.states["agy"] = &session.SessionState{
		SessionID:          "agy",
		Adapter:            "antigravity",
		State:              session.StateWorking,
		PID:                0,
		TranscriptPath:     tp,
		FirstSeen:          oldTs,
		UpdatedAt:          oldTs,
		LastTranscriptSize: 0,
		Metrics:            &session.SessionMetrics{LastEventType: "function_call_output"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Let seedFromDisk finish before injecting activity.
	time.Sleep(50 * time.Millisecond)

	activity := func() agent.Event {
		return agent.Event{Type: agent.EventActivity, SessionID: "agy", TranscriptPath: tp, Terminal: true}
	}

	// Pass A — a real agent event grew the transcript: UpdatedAt and EventCount bump.
	substantive.Store(true)
	appendTranscriptLine(t, tp, "{\"source\":\"USER_EXPLICIT\",\"type\":\"USER_INPUT\"}\n")
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.eventCountOf("agy") == 1 }, 2*time.Second)
	if got := repo.eventCountOf("agy"); got != 1 {
		t.Fatalf("substantive growth: EventCount = %d, want 1", got)
	}
	tsA := repo.updatedAtOf("agy")
	if tsA <= oldTs {
		t.Fatalf("substantive growth did not advance UpdatedAt: got %d, want > %d", tsA, oldTs)
	}

	// Pass B — the transcript GREW (size changes) but only with skipped SYSTEM-log
	// noise (NoSubstantiveActivity). The bump must be suppressed so UpdatedAt can
	// age out. This is the #735 regression: before the fix, byte growth bumped
	// UpdatedAt here and pinned the session perpetually fresh.
	substantive.Store(false)
	appendTranscriptLine(t, tp, "{\"source\":\"SYSTEM\",\"type\":\"CONVERSATION_HISTORY\"}\n")
	savesBefore := repo.savesCount()
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.savesCount() > savesBefore }, 2*time.Second)
	if got := repo.eventCountOf("agy"); got != 1 {
		t.Fatalf("noise-only growth bumped EventCount: got %d, want 1 (unchanged)", got)
	}
	if got := repo.updatedAtOf("agy"); got != tsA {
		t.Fatalf("noise-only growth bumped UpdatedAt: got %d, want %d (unchanged)", got, tsA)
	}

	// Pass C — a real agent event again: bumping resumes.
	substantive.Store(true)
	appendTranscriptLine(t, tp, "{\"source\":\"MODEL\",\"type\":\"RUN_COMMAND\"}\n")
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.eventCountOf("agy") == 2 }, 2*time.Second)
	if got := repo.eventCountOf("agy"); got != 2 {
		t.Fatalf("substantive growth after noise: EventCount = %d, want 2", got)
	}
}
