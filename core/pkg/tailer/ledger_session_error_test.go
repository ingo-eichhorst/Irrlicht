package tailer

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// TestLedger_SessionErrorSurvivesRestart is #1815's tailer-side regression test:
// the sticky session-level failure must be carried through a daemon restart,
// because the post-restart pass reads ZERO new transcript bytes and therefore
// cannot re-derive it.
//
// Simulates the restart the way the daemon performs it — GetLedgerState from the
// dying tailer, JSON round-trip through the on-disk ledger, SetLedgerState into a
// brand-new tailer — rather than by copying a struct field, so a field that
// serialises but does not deserialise (or vice versa) is caught. That
// one-directional failure is exactly what session.SessionError's own
// UnmarshalJSON exists to prevent, and it is invisible to an in-memory copy.
func TestLedger_SessionErrorSurvivesRestart(t *testing.T) {
	retryIn := 616*time.Millisecond + 452*time.Microsecond
	attempt, maxAttempts, status := 3, 10, 529

	before := newLedgerTestTailer()
	before.sessionError = &SessionError{
		Phase:       ErrorPhaseRetrying,
		Class:       "rate_limit_error",
		Message:     "Repeated 529 Overloaded errors.",
		HTTPStatus:  &status,
		Attempt:     &attempt,
		MaxAttempts: &maxAttempts,
		RetryIn:     &retryIn,
	}

	after := restartThroughLedger(t, before)

	got := after.sessionError
	if got == nil {
		t.Fatalf("the sticky session error did not survive the ledger round-trip — " +
			"#1815: a restarted daemon re-classifies the session off a frozen transcript and reads green")
	}
	if got.Phase != ErrorPhaseRetrying || got.Class != "rate_limit_error" ||
		got.Message != "Repeated 529 Overloaded errors." {
		t.Errorf("verdict changed across the restart: got %+v", got)
	}
	// Every numeric field is a pointer precisely because absence and zero are
	// different facts, so the round-trip has to preserve the distinction rather
	// than just the struct.
	assertIntPtr(t, "HTTPStatus", got.HTTPStatus, status)
	assertIntPtr(t, "Attempt", got.Attempt, attempt)
	assertIntPtr(t, "MaxAttempts", got.MaxAttempts, maxAttempts)
	if got.RetryIn == nil || *got.RetryIn != retryIn {
		t.Errorf("RetryIn = %v, want %v — the fractional retry delay did not round-trip", got.RetryIn, retryIn)
	}
}

// TestLedger_ClearedSessionErrorStaysCleared is a LOCK, not a defect test: it
// passes before #1815 by construction, and pins the half of the clearing rule
// that persistence could most easily break.
//
// A nil sticky error means the session RECOVERED. If the ledger treated nil as
// "nothing to say" and carried the previous value forward instead, a recovered
// session would come back red after every restart — the mirror-image bug, and the
// one SessionMetrics.SessionError's doc warns a carry-forward helper would cause.
func TestLedger_ClearedSessionErrorStaysCleared(t *testing.T) {
	before := newLedgerTestTailer()
	before.sessionError = nil

	if got := restartThroughLedger(t, before).sessionError; got != nil {
		t.Errorf("a cleared session error came back as %+v after a restart — "+
			"nil must persist as nil, or recovery never survives a restart", got)
	}
}

// TestLedger_PreFieldLedgerRehydratesToNoError pins the no-schema-bump decision:
// a ledger written before #1815 simply lacks the key, and must rehydrate to the
// exact pre-fix behaviour rather than failing to load.
//
// This is what makes the additive field safe without discarding every live
// session's ledger — the claim LedgerState.SessionError's doc makes, asserted
// against a real pre-field payload rather than left as prose.
func TestLedger_PreFieldLedgerRehydratesToNoError(t *testing.T) {
	// A ledger body with no session_error key at all — what every ledger on
	// disk looks like today.
	raw := []byte(`{"schema_version":` + strconv.Itoa(LedgerSchemaVersion) + `,"last_offset":4096,"last_event_type":"assistant"}`)

	var s LedgerState
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("a pre-#1815 ledger no longer parses: %v", err)
	}
	if s.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil for a ledger written before the field existed", s.SessionError)
	}

	after := newLedgerTestTailer()
	after.SetLedgerState(s)
	if after.sessionError != nil {
		t.Errorf("rehydrated sticky error = %+v, want nil", after.sessionError)
	}
	if after.lastOffset != 4096 {
		t.Errorf("lastOffset = %d, want 4096 — the rest of the ledger must still load", after.lastOffset)
	}
}

// restartThroughLedger puts src's durable state through the same three steps the
// daemon performs across a restart — capture, serialise to the on-disk form and
// back, rehydrate into a fresh tailer — and returns that fresh tailer.
func restartThroughLedger(t *testing.T, src *TranscriptTailer) *TranscriptTailer {
	t.Helper()
	encoded, err := json.Marshal(src.GetLedgerState())
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	var decoded LedgerState
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal ledger %s: %v", encoded, err)
	}
	dst := newLedgerTestTailer()
	dst.SetLedgerState(decoded)
	return dst
}

func assertIntPtr(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %d — an absent number is not the same fact as a zero one", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %d, want %d", name, *got, want)
	}
}

// newLedgerTestTailer builds the minimum TranscriptTailer the ledger paths
// touch. GetLedgerState and SetLedgerState both read and write t.metrics, so a
// bare &TranscriptTailer{} nil-derefs — the zero value is not a usable tailer.
func newLedgerTestTailer() *TranscriptTailer {
	return &TranscriptTailer{metrics: &SessionMetrics{}}
}
