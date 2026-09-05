package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// The LIVE producer's half of the run-kind classification (#1905 subagents).
//
// The daemon holds a parent `working` while its children run, so a subagent's
// span is a nested interval inside its parent's. Which of the two a span was
// has to be written onto the row AS IT IS CLOSED — the session state that knows
// the answer is deleted when the session ends, and nothing can recover it
// afterwards.

// A span closed for a CHILD session is stamped as a subagent run and carries
// the parent it reported to.
func TestAutonomySpan_ChildSessionIsStampedAsASubagentRun(t *testing.T) {
	const t0 = 1_700_000_000
	d, store, st := spanFixture()
	st.ParentSessionID = "parent-42"
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateReady, t0 + 30},
		step{session.StateWaiting, t0 + 300}, // settles the pending ready close
	)
	if len(store.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(store.spans))
	}
	got := store.spans[0]
	if got.Kind != session.AutonomyKindSubagent {
		t.Fatalf("span Kind = %q, want %q — a child's run must be excludable from the headline",
			got.Kind, session.AutonomyKindSubagent)
	}
	if got.Parent != "parent-42" {
		t.Fatalf("span Parent = %q, want %q — the link is what lets a nested run be attributed "+
			"to the run that contains it", got.Parent, "parent-42")
	}
}

// A span closed for a TOP-LEVEL session is stamped as one, EXPLICITLY, and
// carries no parent.
//
// The explicitness is the point: leaving it blank would make a live-measured
// row indistinguishable on disk from a row written before the field existed,
// and those two must never mean the same thing.
func TestAutonomySpan_TopLevelSessionIsStampedExplicitly(t *testing.T) {
	const t0 = 1_700_000_000
	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateWaiting, t0 + 60},
	)
	if len(store.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(store.spans))
	}
	got := store.spans[0]
	if got.Kind != session.AutonomyKindTopLevel {
		t.Fatalf("span Kind = %q, want %q stated outright — a blank would be indistinguishable from "+
			"a pre-classification row", got.Kind, session.AutonomyKindTopLevel)
	}
	if got.Parent != "" {
		t.Fatalf("span Parent = %q, want empty", got.Parent)
	}
	// And never `unknown`: the live path is holding the session state, where an
	// empty ParentSessionID means "no parent", not "nobody looked".
	if got.Kind == session.AutonomyKindUnknown {
		t.Fatal("the live path recorded an unknown kind — it always knows which it is")
	}
}
