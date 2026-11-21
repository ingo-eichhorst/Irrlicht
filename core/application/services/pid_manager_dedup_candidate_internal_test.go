package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestIsDedupDeleteCandidate pins the same-PID candidate policy that the
// assignment-time, seed and periodic paths share (issue #1992).
//
// The two PID <= 0 rows are issue #2157's red-first cases: on the unfixed code
// a root at PID 0 (or below) was a candidate against another root at the same
// non-PID, which let the periodic sweep delete every PID-0 root except the
// newest. Seen red before the guard existed. Every other row is a lock that
// passes before and after the fix.
func TestIsDedupDeleteCandidate(t *testing.T) {
	root := func(id string, pid int) *session.SessionState {
		return &session.SessionState{SessionID: id, Adapter: "claude-code", PID: pid}
	}
	subagent := root("child", 4242)
	subagent.ParentSessionID = "parent"

	tests := []struct {
		name   string
		victim *session.SessionState
		pid    int
		winner *session.SessionState
		want   bool
	}{
		{"PID-0 root against a PID-0 winner is not a duplicate (#2157)", root("old", 0), 0, root("new", 0), false},
		{"negative-PID root against a same-PID winner is not a duplicate (#2157)", root("old", -1), -1, root("new", -1), false},
		{"lock: older root sharing a real PID is a candidate", root("old", 4242), 4242, root("new", 4242), true},
		{"lock: a root on a different PID is not a candidate", root("old", 1111), 4242, root("new", 4242), false},
		{"lock: the winner itself is not a candidate", root("new", 4242), 4242, root("new", 4242), false},
		{"lock: a subagent on its parent's PID is not a candidate", subagent, 4242, root("parent", 4242), false},
		{"lock: a proc-* victim is not a candidate", root("proc-4242", 4242), 4242, root("new", 4242), false},
		{"lock: a proc-* winner evicts nothing (#1960)", root("old", 4242), 4242, root("proc-4242", 4242), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDedupDeleteCandidate(tt.victim, tt.pid, tt.winner); got != tt.want {
				t.Fatalf("isDedupDeleteCandidate(victim=%s pid=%d, pid=%d, winner=%s) = %v, want %v",
					tt.victim.SessionID, tt.victim.PID, tt.pid, tt.winner.SessionID, got, tt.want)
			}
		})
	}
}
