package session_test

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestDiedMidTurn_NilReceiver covers the one clause the #1817 move ADDED, and
// which nothing else reaches: both production callers already hold a non-nil
// row by the time they ask, so deleting the guard is green everywhere.
//
// The guard is not decoration. The predicate is exported domain API now, and
// its whole contract is that an unknown answer means "not a crash" — a nil
// receiver is the most unknown answer there is, and panicking on it would be
// the one failure mode that is worse than answering false.
func TestDiedMidTurn_NilReceiver(t *testing.T) {
	var s *session.SessionState
	if s.DiedMidTurn() {
		t.Fatal("DiedMidTurn() on a nil row = true, want false — every clause fails closed, and nil is the least-known input of all")
	}
}

// TestDiedMidTurn_ClausesInThisPackage exercises the predicate directly, in its
// own package. The daemon-side suite covers the same clauses through
// SessionDetector.HandleProcessExit, which is the right test for the daemon and
// the wrong one for a domain predicate that now has a second consumer: it can
// only fail for reasons that involve the detector.
func TestDiedMidTurn_ClausesInThisPackage(t *testing.T) {
	// midTurn is the shape that must answer true: actively working, with a tool
	// call the agent never came back to close.
	midTurn := func() *session.SessionState {
		return &session.SessionState{
			SessionID: "s1",
			State:     session.StateWorking,
			Metrics: &session.SessionMetrics{
				LastEventType:     "assistant",
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Bash"},
			},
		}
	}

	if !midTurn().DiedMidTurn() {
		t.Fatal("the mid-turn baseline answers false — every case below would then pass vacuously")
	}

	cases := []struct {
		name    string
		mutate  func(*session.SessionState)
		because string
	}{
		{"not-working", func(s *session.SessionState) {
			s.State = session.StateReady
		}, "a session that was not working had nothing in flight to lose"},

		{"already-error", func(s *session.SessionState) {
			s.State = session.StateError
		}, "error means this already ran; re-converting is what #1800's verdict registry exists to stop"},

		{"child-session", func(s *session.SessionState) {
			s.ParentSessionID = "parent-1"
		}, "a subagent's process IS its parent's process"},

		{"no-metrics", func(s *session.SessionState) {
			s.Metrics = nil
		}, "nothing is known, so nothing is claimed"},

		{"agent-done", func(s *session.SessionState) {
			s.Metrics.LastEventType = "turn_done"
			s.Metrics.HasOpenToolCall = false
			s.Metrics.LastOpenToolNames = nil
		}, "the turn had ended and classification had merely not caught up"},

		{"user-interrupt", func(s *session.SessionState) {
			s.Metrics.LastWasUserInterrupt = true
		}, "the USER stopped it"},

		{"tool-denial", func(s *session.SessionState) {
			s.Metrics.LastWasToolDenial = true
		}, "the user denied a tool and the agent stopped"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := midTurn()
			tc.mutate(s)
			if s.DiedMidTurn() {
				t.Fatalf("DiedMidTurn() = true, want false — %s", tc.because)
			}
		})
	}
}
