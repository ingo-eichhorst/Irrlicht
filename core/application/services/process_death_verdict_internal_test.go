package services

import (
	"sync"
	"testing"
)

// TestMarkProcessDeathVerdictAdmitsExactlyOneCaller pins the property the
// recorded process-death event rests on: of N callers racing for one session's
// verdict, exactly ONE is told it set it.
//
// WHY THIS SHAPE, and not the end-to-end burst next door. The end-to-end test
// observes the race's *symptom* — two recorded events — which only appears when
// the scheduler actually interleaves two callers inside the check-then-set
// window. Removing the gate was measured to produce that interleaving in 2 of
// 300 `-race` runs, so as a regression gate it was green at `-count=1` and only
// reliably red around `-count=60`: a check that, on the repo's actual gate, is
// indistinguishable from one that found nothing. That is the exact defect class
// the #1796 arc exists to remove, so it is not an acceptable shape for the test
// guarding this fix.
//
// This test asserts the CONTRACT instead — "at most one true" — which the
// mutation violates by construction rather than by timing. Deleting the
// existence check makes every caller return true, so it is red on the first
// run, every run, at `-count=1`.
//
// It lives in package services (not services_test) precisely so it can call the
// unexported method directly: the alternative was widening the production API
// with a test-only hook to reach a property that is internal by nature.
func TestMarkProcessDeathVerdictAdmitsExactlyOneCaller(t *testing.T) {
	// newSessionDetector, not a bare &SessionDetector{}: the package's own
	// guarded-construction tripwire (TestSessionDetectorIsNeverBuiltByBareLiteral,
	// #1400/#1450) rejects the literal because it skips every map allocation and
	// the resulting panic surfaces in the writer rather than here.
	d := newSessionDetector()

	const callers = 64
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(callers)

	granted := make([]bool, callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			granted[i] = d.markProcessDeathVerdict("raced-session")
		}(i)
	}
	start.Done()
	done.Wait()

	won := 0
	for _, g := range granted {
		if g {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("markProcessDeathVerdict returned true to %d of %d concurrent callers, want exactly 1 — "+
			"the check-and-set is not atomic, so more than one caller believes it converted the session and "+
			"the recording claims the process died more than once",
			won, callers)
	}
}

// TestMarkProcessDeathVerdictIsTerminalUntilForgotten covers the other half of
// the contract, which the concurrency test above cannot see: the verdict stays
// set across LATER calls, and only forgetProcessDeathVerdict reopens it. The
// liveness sweep calls the exit path every few seconds for as long as the row
// survives, so a verdict that decayed on its own would emit a second death
// event for the same session minutes later — sequentially, with no race
// involved at all.
func TestMarkProcessDeathVerdictIsTerminalUntilForgotten(t *testing.T) {
	d := newSessionDetector()
	const sid = "swept-session"

	if !d.markProcessDeathVerdict(sid) {
		t.Fatal("first call returned false — nothing had claimed the verdict yet")
	}
	for i := 0; i < 5; i++ {
		if d.markProcessDeathVerdict(sid) {
			t.Fatalf("sweep call %d was told it set the verdict — every call after the first must answer false", i+2)
		}
	}

	// HandlePIDAssigned reaches this when a process comes back; the session is
	// then judged on its own merits again.
	d.forgetProcessDeathVerdict(sid)
	if !d.markProcessDeathVerdict(sid) {
		t.Fatal("after forgetProcessDeathVerdict the verdict was still held — a resumed session that dies again would record nothing")
	}
}
