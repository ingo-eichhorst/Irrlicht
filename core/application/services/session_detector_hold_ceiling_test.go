package services_test

import (
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// newHeldWaitingSession persists a session that is pinned at waiting by a hook
// hold whose release never arrived: quiet transcript, stale UpdatedAt, project
// already resolved so the idle-retry path has nothing to do and cannot be
// mistaken for the thing that unpinned it.
func newHeldWaitingSession(t *testing.T, repo *mockRepo, sessionID string) {
	t.Helper()
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, sessionID+".jsonl")
	writeOldTranscript(t, transcriptPath, time.Minute)

	old := time.Now().Add(-time.Hour).Unix()
	repo.Save(&session.SessionState{
		SessionID:      sessionID,
		State:          session.StateWaiting,
		Adapter:        "claude-code",
		TranscriptPath: transcriptPath,
		CWD:            dir,
		ProjectName:    "proj",
		Metrics:        &session.SessionMetrics{LastEventType: "turn_done"},
		PID:            4242,
		FirstSeen:      old,
		UpdatedAt:      old,
	})
}

// TestSessionDetector_StaleRefreshExpiresAHookHoldOnAWaitingSession is the
// second #1360 defect test, and it FAILED against the first version of this
// branch — the one that added ceilings to the policy table but nothing else.
//
// A ceiling is only ever evaluated inside SignalHolds.Overlay, and Overlay is
// only reached from the classify pipeline. refreshStaleSessions — the only
// thing that revisits a session no transcript event is arriving for — ran that
// pipeline for StateWorking alone; StateWaiting and StateReady got
// retryIdleProjectResolution, which does not reclassify.
//
// Both ceilings this issue adds bound holds that pin the session at *waiting*.
// So in the exact scenario #1360 describes — the release is lost and the agent
// then goes quiet — no further pass ever runs, Overlay is never called again,
// and a declared ceiling can never fire. The session stays pinned for the life
// of the process, which is the defect the ceilings were supposed to remove.
//
// This is also why compactHoldTimeout has always worked and looked like proof
// the mechanism was sound: it pins at *working*, the one state the ticker
// already re-read.
//
// The observable asserted here is the recorded hold_expired event rather than
// the session's new state, and deliberately: this harness's metrics collector
// is a mock that leaves state.Metrics nil, so the classifier has nothing to
// re-decide from and any state assertion would be measuring the mock. The
// recorded event is the sharper claim anyway — it can only be emitted by a
// real Overlay call inside a real classify pass, which is precisely the thing
// that was not happening. The user-visible "no longer pinned at waiting" half
// is covered by TestClassify_HookHoldCeilingUnpinsAStuckSession.
func TestSessionDetector_StaleRefreshExpiresAHookHoldOnAWaitingSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)

	rec := &mockRecorder{}
	det.SetRecorder(rec)

	newHeldWaitingSession(t, repo, "held1")

	// The PermissionRequest hook landed three days ago; PostToolUse never came.
	// Deliberately far past any defensible ceiling rather than just past the
	// current one: this test is about the ticker reaching the session at all,
	// so retuning permissionPromptHoldTimeout must not be able to break it.
	// (It could, once: an earlier version held two hours back and started
	// failing the moment that constant grew to twelve.)
	det.HoldSignalForTest("held1", session.SignalPermissionPrompt, time.Now().Add(-72*time.Hour))

	det.RunStaleSessionRefreshForTest()

	var expired *lifecycle.Event
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindHoldExpired {
			e := ev
			expired = &e
		}
	}
	if expired == nil {
		t.Fatalf("no %q event — the ticker never ran a classify pass for the held waiting session, "+
			"so its ceiling could not fire", lifecycle.KindHoldExpired)
	}
	if expired.SessionID != "held1" {
		t.Errorf("expiry attributed to %q, want %q", expired.SessionID, "held1")
	}
	if expired.SignalKind != string(session.SignalPermissionPrompt) {
		t.Errorf("SignalKind = %q, want %q", expired.SignalKind, session.SignalPermissionPrompt)
	}
}

// TestSessionDetector_StaleRefreshLeavesAnUnheldIdleSessionAlone is the
// counterweight, and a LOCK on behaviour that predates #1360.
//
// The fix above makes refreshStaleSessions re-read transcripts for
// non-working sessions, which is work the ticker deliberately did not do
// before. Scoping it to sessions that actually have a hold outstanding is what
// keeps that from becoming a per-tick transcript re-read of every idle session
// on the machine — so the scoping needs a test, or the next refactor will
// quietly widen it.
func TestSessionDetector_StaleRefreshLeavesAnUnheldIdleSessionAlone(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)

	newHeldWaitingSession(t, repo, "quiet1") // same fixture, but no hold placed

	before, err := repo.Load("quiet1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	det.RunStaleSessionRefreshForTest()

	after, err := repo.Load("quiet1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.State != before.State || after.UpdatedAt != before.UpdatedAt {
		t.Errorf("an idle session with no hold outstanding must not be reclassified by the ticker: "+
			"state %q→%q, updatedAt %d→%d",
			before.State, after.State, before.UpdatedAt, after.UpdatedAt)
	}
}
