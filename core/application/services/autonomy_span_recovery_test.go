package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Mutation fixtures for what #1905's recording fix ADDS.
//
// None of these had a "before the fix" to run red against: they pin behaviour
// that did not exist. Each therefore names the MUTATION it catches and, where
// it can, computes the wrong answer into the failure message, per AGENTS.md's
// rule for a guard with no red-first history.

// The lower-bound mark separates a start Irrlicht MEASURED from one it merely
// discovered, and it has to be right in both directions.
//
// MUTATIONS CAUGHT, one per case:
//
//   - marking nothing: a run of unknown start would be reported as measured,
//     which is the wrong number the marking exists to stop;
//   - marking everything: an observed `→ working` transition would report as
//     an estimate, discrediting the numbers that ARE measured;
//   - marking a recovered run: a start the previous daemon measured would be
//     downgraded to a guess on every restart, so the longest runs on the
//     machine would end up the least trusted ones.
func TestAutonomySpanLowerBound_MarksExactlyTheUnmeasuredStarts(t *testing.T) {
	const t0 = 1_700_000_000

	t.Run("born working is a lower bound", func(t *testing.T) {
		d := bornWorkingDetector(newJournalSpanStore())
		st := d.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), t0)
		if !st.AutonomySpanStartLowerBound {
			t.Fatal("a session met already working reports its start as measured — nothing " +
				"measured it, so its duration is a floor and the row has to say so")
		}
	})

	t.Run("an observed transition is measured", func(t *testing.T) {
		d, _, st := spanFixture()
		// Poisoned first: the flag must be SET false by the transition, not
		// merely left alone. A session that carried a lower-bound run earlier
		// in its life would otherwise taint a fully measured one later.
		st.AutonomySpanStartLowerBound = true
		d.applyAutonomySpanTransition(st, session.StateWorking, t0)
		if st.AutonomySpanStartLowerBound {
			t.Fatal("a run whose opening transition was observed is marked as an estimate")
		}
	})

	t.Run("a recovered run keeps its measured start", func(t *testing.T) {
		store := newJournalSpanStore()
		first := bornWorkingDetector(store)
		firstState := first.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), t0)
		// Give the first daemon an OBSERVED start, the way a session already
		// alive when its run began would have.
		first.applyAutonomySpanTransition(firstState, session.StateReady, t0+10)
		first.applyAutonomySpanTransition(firstState, session.StateWorking, t0+600)

		second := bornWorkingDetector(store)
		revived := second.buildNewSessionState(agent.Identity{Name: "copilot"}, birthEvent(), t0+900)
		if revived.AutonomySpanStartLowerBound {
			t.Fatal("a run adopted from the journal is marked as an estimate — that start was " +
				"measured by the daemon that died holding it, which is why the journal exists")
		}
		if revived.AutonomySpanStart == nil || *revived.AutonomySpanStart != t0+600 {
			t.Fatalf("recovered start = %v, want %d", revived.AutonomySpanStart, int64(t0+600))
		}
	})
}

// A recovered run that NOBODY rediscovers is closed where it was last seen —
// not at "now", and not never.
//
// MUTATIONS CAUGHT:
//
//   - closing at `now`: the run absorbs the daemon's whole downtime, so a
//     30-minute run interrupted by an overnight reboot becomes an 8-hour one;
//   - never closing: the run stays "in progress" forever, so a session that
//     ended months ago is still reported as running;
//   - adopting it late: a rediscovery after the window would splice the
//     downtime, and everything since, into one fabricated run.
func TestAutonomySpanRecovery_UnadoptedRunClosesWhereItWasLastSeen(t *testing.T) {
	const (
		startedAt = 1_700_000_000
		lastSeen  = startedAt + 1800
		bootedAt  = lastSeen + 8*3600 // an overnight reboot
	)

	store := newJournalSpanStore()
	store.open["gone"] = outbound.AutonomySpan{
		Start: startedAt, End: lastSeen, Project: "irrlicht", Session: "gone",
		Kind: session.AutonomyKindTopLevel, Running: true,
	}

	d := bornWorkingDetector(store)
	d.now = func() time.Time { return time.Unix(bootedAt, 0) }
	d.loadRecoverableAutonomySpans()

	// Inside the window nothing is settled: the session may still turn up.
	d.settleUnadoptedAutonomySpans(bootedAt + 1)
	if len(store.spans) != 0 {
		t.Fatalf("settled %d runs inside the adoption window, want 0 — the session had not yet "+
			"had its chance to be rediscovered", len(store.spans))
	}

	past := bootedAt + int64(autonomyRecoveryWindow/time.Second) + 1
	d.settleUnadoptedAutonomySpans(past)
	if len(store.spans) != 1 {
		t.Fatalf("settled %d runs past the window, want 1 — an unadopted run must not stay open forever",
			len(store.spans))
	}
	got := store.spans[0]
	if got.End != lastSeen {
		t.Fatalf("closed at %d (a %ds run), want %d (a %ds run) — closing at `now` credits the run "+
			"with the whole time the daemon was down",
			got.End, past-startedAt, int64(lastSeen), int64(lastSeen-startedAt))
	}
	if got.Reason != session.AutonomyReasonUnknown {
		t.Errorf("end reason = %q, want %q — nothing observed why it stopped, and %q would claim "+
			"it finished its turn", got.Reason, session.AutonomyReasonUnknown, session.StateReady)
	}
	if got.Running {
		t.Error("a settled run is still marked running")
	}

	if _, ok := d.adoptRecoveredAutonomySpan("gone"); ok {
		t.Error("a settled run was adopted after the fact — that is resurrection, not recovery")
	}
}

// A session torn down while still WORKING files its run.
//
// MUTATION CAUGHT: the shipped teardown settled only a PENDING end, so a
// session whose agent process exited mid-run — which is how most sessions
// actually end — recorded nothing at all. Removing the still-open branch loses
// the span entirely and the count goes to zero.
func TestAutonomySpan_TeardownWhileWorkingStillFilesTheRun(t *testing.T) {
	const t0 = 1_700_000_000
	const goneAt = t0 + 4200

	d, store, st := spanFixture()
	d.now = func() time.Time { return time.Unix(goneAt, 0) }
	d.applyAutonomySpanTransition(st, session.StateWorking, t0)
	st.State = session.StateWorking

	d.settleAutonomySpanOnTeardown(st)

	if len(store.spans) != 1 {
		t.Fatalf("got %d spans, want 1 — a %d-minute run whose agent exited mid-flight was lost",
			len(store.spans), (goneAt-t0)/60)
	}
	got := store.spans[0]
	if got.Start != t0 || got.End != goneAt {
		t.Errorf("span = [%d,%d], want [%d,%d]", got.Start, got.End, int64(t0), int64(goneAt))
	}
	if got.Reason != session.AutonomyReasonUnknown {
		t.Errorf("end reason = %q, want %q — the session vanished, so nothing observed why the "+
			"run stopped", got.Reason, session.AutonomyReasonUnknown)
	}
}

// The journal is RECONCILED, not merely appended to: the sync writes exactly
// the runs that are open right now.
//
// MUTATIONS CAUGHT:
//
//   - upsert-only (no removal): a session that vanished between ticks keeps an
//     entry nothing will ever close, so it is reported as running forever and
//     adopted as a run by the next daemon to start;
//   - throttling on time alone: a run that just ended would keep being served
//     as still-running until the interval elapsed.
func TestSyncOpenAutonomySpans_WritesExactlyTheRunsThatAreOpen(t *testing.T) {
	const t0 = 1_700_000_000

	store := newJournalSpanStore()
	d := newSessionDetector()
	d.log = &gateLogger{}
	d.autonomySpans = store

	working := &session.SessionState{SessionID: "a", ProjectName: "p", AutonomySpanStart: ptrInt64(t0)}
	idle := &session.SessionState{SessionID: "b", ProjectName: "p"}

	if !d.syncOpenAutonomySpans([]*session.SessionState{working, idle}, t0+5) {
		t.Fatal("the first sync did not write")
	}
	if len(store.open) != 1 {
		t.Fatalf("journal holds %d runs, want 1 — only the working session has one open", len(store.open))
	}
	if _, ok := store.open["a"]; !ok {
		t.Fatalf("journal is missing the open run: %+v", store.open)
	}

	// The run ends. The set changed, so the write happens NOW rather than
	// waiting out autonomyOpenSyncInterval.
	working.AutonomySpanStart = nil
	if !d.syncOpenAutonomySpans([]*session.SessionState{working, idle}, t0+6) {
		t.Fatal("a changed open set did not write immediately — a finished run would go on " +
			"being served as still running")
	}
	if len(store.open) != 0 {
		t.Fatalf("journal still holds %d runs after every run ended: %+v", len(store.open), store.open)
	}
}

func ptrInt64(v int64) *int64 { return &v }
