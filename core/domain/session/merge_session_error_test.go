package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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

// TestSessionError_RetryInSerializesWithItsUnitNamed is #1798's wire contract,
// and the reason SessionError carries a custom marshaller at all.
//
// A bare `*time.Duration` marshals to unlabelled nanoseconds (616452004). The
// JS and Swift clients that render this field (#1801, #1802) would have to
// know both the unit and that this is the only field in the payload using it,
// from nothing in the payload itself — while every other serialized time
// quantity in this package names its unit in the key.
func TestSessionError_RetryInSerializesWithItsUnitNamed(t *testing.T) {
	d := 616452004 * time.Nanosecond // the real recorded 616.452004ms
	b, err := json.Marshal(&SessionError{
		Phase:   ErrorPhaseRetrying,
		Class:   "rate_limit",
		RetryIn: &d,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)

	if !strings.Contains(got, `"retry_in_ms"`) {
		t.Errorf("payload does not name the unit — a consumer cannot tell what the number means.\ngot: %s", got)
	}
	if strings.Contains(got, "616452004") {
		t.Errorf("payload carries raw nanoseconds.\ngot: %s", got)
	}
	// Fractional milliseconds must survive: the whole reason RetryIn is a
	// Duration rather than an int is that the source value has a fraction.
	if !strings.Contains(got, "616.452004") {
		t.Errorf("the fractional millisecond value was lost.\ngot: %s", got)
	}
}

// TestSessionError_JSONRoundTrips guards against the custom encoder being
// write-only. Without UnmarshalJSON every reload of a persisted session would
// silently drop the retry delay — one-directional plumbing, the same class of
// bug as a field missing from the merge allowlist.
func TestSessionError_JSONRoundTrips(t *testing.T) {
	status, attempt, max := 429, 3, 10
	d := 616452004 * time.Nanosecond
	original := &SessionError{
		Phase:       ErrorPhaseRetrying,
		Class:       "rate_limit",
		Message:     "slow down",
		HTTPStatus:  &status,
		Attempt:     &attempt,
		MaxAttempts: &max,
		RetryIn:     &d,
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back SessionError
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !original.Equal(&back) {
		t.Errorf("round trip lost or changed data:\n before: %+v\n  after: %+v", original, &back)
	}
}

// TestSessionError_JSONRoundTripsWithEverythingAbsent is the other half, and
// the case the recorded terminal shapes actually produce: no status, no
// counters, no delay. Absent must come back absent, never as a zero that a
// consumer would render as "attempt 0 of 0".
func TestSessionError_JSONRoundTripsWithEverythingAbsent(t *testing.T) {
	original := &SessionError{
		Phase:   ErrorPhaseTerminal,
		Class:   "provider",
		Message: "API Error: API returned an empty or malformed response (HTTP 200)",
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "retry_in_ms") || strings.Contains(string(b), "attempt") {
		t.Errorf("absent optional fields must be omitted entirely, not emitted as zero.\ngot: %s", b)
	}

	var back SessionError
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.RetryIn != nil || back.Attempt != nil || back.MaxAttempts != nil || back.HTTPStatus != nil {
		t.Errorf("an absent field came back non-nil: %+v", &back)
	}
	if !original.Equal(&back) {
		t.Errorf("round trip changed data:\n before: %+v\n  after: %+v", original, &back)
	}
}
