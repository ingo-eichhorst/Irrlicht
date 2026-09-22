package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// runNoEvidencePass seeds one ready session (a child when parentID is set)
// whose transcript has been parsed but holds no substantive event
// (LastEventType == "", NoSubstantiveActivity false — the shape an EMPTY pass
// leaves), fires trigger against it, and returns the state saved by the pass
// that trigger caused.
//
// It observes the pass itself, not the absence of a change: it waits for
// seedFromDisk's own save, takes the Save count as a baseline, and polls past
// that baseline before reading the state. A fixed sleep let a slow seed's save
// land after the baseline and stand in for the trigger's pass.
func runNoEvidencePass(t *testing.T, id, parentID, path string, trigger func(*services.SessionDetector, *mockAgentWatcher)) string {
	t.Helper()
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{}, nil
	}}
	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	now := time.Now().Unix()
	repo.states[id] = &session.SessionState{
		SessionID:       id,
		ParentSessionID: parentID,
		State:           session.StateReady,
		TranscriptPath:  path,
		FirstSeen:       now,
		UpdatedAt:       now,
		Metrics:         &session.SessionMetrics{},
	}

	done := make(chan error, 1)
	defer func() { <-done }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- det.Run(ctx) }()

	start := time.Now()
	waitForCondition(func() bool { return repo.savesCount() > 0 }, 5*time.Second)
	baseline := repo.savesCount()
	if baseline == 0 {
		t.Fatalf("seedFromDisk saved nothing within %v — cannot separate its save from the trigger's", time.Since(start))
	}

	start = time.Now()
	trigger(det, tw)
	waitForCondition(func() bool { return repo.savesCount() > baseline }, 5*time.Second)
	if repo.savesCount() <= baseline {
		t.Fatalf("no activity pass saved within %v — the trigger never reached the detector", time.Since(start))
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.lastSavedState[id]
}

// TestSessionDetector_IdlePromptOnPostClearSession_StaysReady is the defect
// test for #2034. After `/clear`, Claude Code's new transcript holds only the
// local-command wrappers, which the parser skips, so the session's metrics
// carry no substantive event at all. 60s later the Notification/idle_prompt
// hook fires. Its synthetic pass reads zero lines, so NoSubstantiveActivity
// stays false and the pass reaches the classifier; the idle_prompt hold is
// stale (!IsAgentDone), and the ladder's total last rung,
// transcript_activity, decided working on no evidence. Recorded live on
// session fd0a7643 as `transcript activity (ready → working)`.
func TestSessionDetector_IdlePromptOnPostClearSession_StaysReady(t *testing.T) {
	const id, path = "clear1", "/home/.claude/projects/-Users-test/clear1.jsonl"
	got := runNoEvidencePass(t, id, "", path, func(det *services.SessionDetector, _ *mockAgentWatcher) {
		det.HandleIdlePromptHook(id, path)
	})
	if got != session.StateReady {
		t.Errorf("after idle_prompt on a session with no transcript event: state = %q, want ready", got)
	}
}

// TestSessionDetector_NoEvidenceChildActivity_StillWorking is a lock for the
// child exemption: a child with no transcript event yet must still classify
// working, so hasActiveChildren counts it and the stale-session sweep cannot
// release its parent (#889). It passes before and after #2034's guard.
func TestSessionDetector_NoEvidenceChildActivity_StillWorking(t *testing.T) {
	const id, path = "child1", "/home/.claude/projects/-Users-test/p/subagents/child1.jsonl"
	got := runNoEvidencePass(t, id, "parent1", path, func(_ *services.SessionDetector, tw *mockAgentWatcher) {
		tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: id, ProjectDir: "-Users-test", TranscriptPath: path}
	})
	if got != session.StateWorking {
		t.Errorf("child with no transcript event: state = %q, want working (#889 exemption)", got)
	}
}
