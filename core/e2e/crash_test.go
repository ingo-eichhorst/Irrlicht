package e2e_test

import (
	"context"
	"fmt"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// TestSession_NoCancelledState_OnSIGKILL verifies that when an agent process
// is killed with SIGKILL mid-session, the resulting session never enters a
// "cancelled" (or any non-canonical) state. Per project convention there are
// only three states: working, waiting, ready.
//
// Concretely: the scanner-tracked pre-session must either remain in a
// canonical state or be deleted entirely — but never end up in a forbidden
// state at any point during the kill+cleanup flow.
func TestSession_NoCancelledState_OnSIGKILL(t *testing.T) {
	cmd, _ := startFakeClaudeProcess(t)

	scanner := processlifecycle.NewScanner(fakeProcessName(), "test", 200*time.Millisecond)
	repo := newMemRepo()

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Track watcher exits so we can assert clean shutdown after cancel.
	scannerDone := make(chan struct{})
	detectorDone := make(chan struct{})
	go func() { _ = scanner.Watch(ctx); close(scannerDone) }()
	go func() { _ = detector.Run(ctx); close(detectorDone) }()

	preID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, preID, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared", preID)
	}

	// Pre-session starts in `ready` (no transcript, no activity yet).
	if s, _ := repo.Load(preID); s == nil || s.State != session.StateReady {
		t.Fatalf("initial state: got %v, want %q", s, session.StateReady)
	}

	// SIGKILL the process — explicitly, to validate the kill-signal path.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	_ = cmd.Wait()

	// Poll for ~3s, recording every state observation. The session must
	// either stay in a canonical state or get deleted; nothing else.
	deadline := time.After(3 * time.Second)
	for done := false; !done; {
		select {
		case <-deadline:
			done = true
		case <-time.After(50 * time.Millisecond):
		}
		s, _ := repo.Load(preID)
		if s == nil {
			done = true
			continue
		}
		if !session.IsCanonicalState(s.State) {
			t.Fatalf("forbidden state observed after SIGKILL: %q", s.State)
		}
	}

	// Pre-session has no transcript, so onRemoved deletes it. Confirm.
	if s, _ := repo.Load(preID); s != nil {
		t.Errorf("pre-session %s still present after SIGKILL: state=%q", preID, s.State)
	}

	cancel()
	assertWatchersExited(t, 2*time.Second, map[string]chan struct{}{
		"scanner":  scannerDone,
		"detector": detectorDone,
	})
}
