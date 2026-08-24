package session

import "testing"

// TestMergeMetrics_CarriesSessionError is the #1256 trap, aimed at #1798's new
// field.
//
// newMergedMetrics is an explicit allowlist: a field the literal omits is
// silently reset to its zero value on every merge, with nothing failing. That
// is not hypothetical here — it is exactly how TranscriptPermissionPending
// came to be computed correctly by the parser, carried correctly through
// replay's separate converter, and dropped on the only path that mattered,
// while the parser, classifier and replay-fixture suites were all green.
//
// SessionError would fail the same way and be even harder to notice, because
// the field only ever has a value on a session that is already broken.
func TestMergeMetrics_CarriesSessionError(t *testing.T) {
	status := 429
	newM := &SessionMetrics{
		LastEventType: "assistant",
		SessionError: &SessionError{
			Phase:      ErrorPhaseRetrying,
			Class:      "rate_limit",
			Message:    "slow down",
			HTTPStatus: &status,
		},
	}
	oldM := &SessionMetrics{LastEventType: "user"}

	merged := MergeMetrics(newM, oldM)

	if merged.SessionError == nil {
		t.Fatal("SessionError was dropped by the merge — it is missing from " +
			"newMergedMetrics' allowlist, so the classifier can never see it on the live path")
	}
	if merged.SessionError.Class != "rate_limit" || merged.SessionError.Phase != ErrorPhaseRetrying {
		t.Errorf("SessionError content mangled: %+v", merged.SessionError)
	}
	if merged.SessionError.HTTPStatus == nil || *merged.SessionError.HTTPStatus != 429 {
		t.Errorf("HTTPStatus lost through the merge: %+v", merged.SessionError.HTTPStatus)
	}
}

// TestMergeMetrics_ClearedSessionErrorStaysCleared is the other direction, and
// the one a carry-forward helper would break.
//
// The tailer owns the clearing rule and expresses "cleared" as a nil
// SessionError on the fresh pass. If the field were ever added to
// carryForwardOverlayState — which is the natural-looking change, since that
// helper is where RateLimit and the cache-bloat group live — the old error
// would be restored on top of the fresh nil and no session could ever leave
// StateError again.
//
// This is a LOCK: it passes by construction against the shipped code and
// exists to fail on a future edit, not as red-first evidence of a defect.
func TestMergeMetrics_ClearedSessionErrorStaysCleared(t *testing.T) {
	oldM := &SessionMetrics{
		LastEventType: "assistant",
		SessionError:  &SessionError{Phase: ErrorPhaseTerminal, Class: "provider"},
	}
	// The successful next turn: the tailer cleared its sticky error, so the
	// fresh pass carries none.
	newM := &SessionMetrics{LastEventType: "turn_done"}

	merged := MergeMetrics(newM, oldM)

	if merged.SessionError != nil {
		t.Fatalf("a cleared error was resurrected from the previous pass (%+v) — SessionError "+
			"has been added to a carryForward* helper, which makes the error permanent because "+
			"the tailer signals 'cleared' with exactly this nil", merged.SessionError)
	}
}

// TestSessionError_AbsentNumbersStayAbsent pins the pointer decision that the
// recorded payloads forced.
//
// claudecode's terminal `isApiErrorMessage` event and copilot's
// errorType:"query" both carry no status and no retry counters at all. With
// plain ints those absences would read as 0, and any consumer deriving
// "attempt 0 of 0 → gave up" would be inventing a verdict from data that said
// nothing. A LOCK on the shape, not a defect test.
func TestSessionError_AbsentNumbersStayAbsent(t *testing.T) {
	e := &SessionError{
		Phase:   ErrorPhaseTerminal,
		Class:   "provider",
		Message: "API Error: API returned an empty or malformed response (HTTP 200)",
	}

	if e.HTTPStatus != nil || e.Attempt != nil || e.MaxAttempts != nil || e.RetryIn != nil {
		t.Fatalf("a status-free terminal error must leave every numeric field nil, got %+v", e)
	}
	if e.IsRetrying() {
		t.Error("a terminal error must not report as retrying")
	}
	// The phase is what says "gave up" — deliberately NOT the counters.
	if !e.Phase.Known() {
		t.Error("a terminal error must carry a known phase")
	}

	// And the genuinely-unknown case stays distinguishable from both.
	unknown := &SessionError{Class: "query", Message: "could not connect"}
	if unknown.Phase.Known() {
		t.Error("an unreported phase must not read as known")
	}
	if unknown.IsRetrying() {
		t.Error("an unknown phase must not read as retrying")
	}
}
