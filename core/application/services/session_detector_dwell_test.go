package services_test

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestSessionDetector_StaleRefreshFinishesAnOutstandingDwell is the #1366
// liveness guarantee end to end, and it is the test a review found missing:
// everything else pinned the PREDICATE (shouldRevisitIdleSession returns true)
// rather than the CONSEQUENCE (the ticker then actually runs a classify pass).
//
// That gap matters more than it looks. The entire safety argument for the
// grace timer is that a deferred waiting→working is DELAYED and never DROPPED,
// and on a session receiving no further transcript events the only thing that
// makes that true is this chain: refreshStaleSessions →
// shouldRevisitIdleSession → reclassifyFromTranscript → a real pass. That last
// hop has three preconditions of its own (the 5s interval, a non-empty
// transcript path, and the #570 consent gate), none of which the predicate
// test exercises. If any silently blocks, the session advertises "needs a
// human" for the life of the process — the expensive, silent error graceFor
// exists to prevent, arriving by the back door.
//
// The observable is UpdatedAt moving, which is the same signal its counterpart
// TestSessionDetector_StaleRefreshLeavesAnUnheldIdleSessionAlone asserts must
// NOT move; that test is the LOCK on the scoping, this one is the fix. A state
// assertion is not available here because this harness's metrics collector
// leaves state.Metrics nil, so the classifier has nothing to re-decide from —
// see TestSessionDetector_StaleRefreshExpiresAHookHoldOnAWaitingSession for
// the same reasoning applied to the #1360 ceiling.
func TestSessionDetector_StaleRefreshFinishesAnOutstandingDwell(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)

	// Same fixture as the #1360 tests: waiting, quiet transcript, stale
	// UpdatedAt, project already resolved so the idle-retry path cannot be
	// mistaken for the thing that woke the session.
	newHeldWaitingSession(t, repo, "dwell1")

	before, err := repo.Load("dwell1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// No hold is placed, deliberately: the #1360 arm of the predicate must not
	// be what selects this session, or the test would pass without the #1366
	// arm existing at all.
	det.StartDwellForTest("dwell1", session.StateWaiting, session.StateWorking, time.Now())

	det.RunStaleSessionRefreshForTest()

	after, err := repo.Load("dwell1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.UpdatedAt == before.UpdatedAt {
		t.Fatalf("the ticker never ran a classify pass for a session with a dwell outstanding "+
			"(UpdatedAt still %d) — nothing else revisits a quiet non-working session, so the "+
			"deferred transition is dropped rather than delayed", before.UpdatedAt)
	}
}
