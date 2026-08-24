package replayengine

import (
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// TestConvertSessionError_DeepCopiesPointerFields is the mutation fixture for
// convertSessionError's deep copy.
//
// The copy is a guard the change added, so per AGENTS.md it owes a mutation
// seen red rather than a comment asserting it matters. Replacing the four
// copyIntPtr/copyDurationPtr calls with plain aliasing left the suite green,
// which meant nothing checked it.
//
// It is not theoretical. The tailer keeps its SessionError sticky ACROSS
// passes, so an aliased pointer hands the domain a window into live tailer
// state that a later pass can rewrite underneath a session snapshot already
// handed to the API — the same aliasing class as the mockRepo race behind the
// recurring services -race flakes.
func TestConvertSessionError_DeepCopiesPointerFields(t *testing.T) {
	status, attempt, max := 429, 1, 10
	delay := 616 * time.Millisecond
	src := &tailer.SessionError{
		Phase:       tailer.ErrorPhaseRetrying,
		Class:       "rate_limit",
		Message:     "slow down",
		HTTPStatus:  &status,
		Attempt:     &attempt,
		MaxAttempts: &max,
		RetryIn:     &delay,
	}

	got := convertSessionError(src)
	if got == nil {
		t.Fatal("conversion returned nil for a non-nil error")
	}

	// The next tailer pass climbs the retry ladder, in place.
	status, attempt, max = 503, 7, 20
	delay = 9 * time.Second

	if *got.HTTPStatus != 429 || *got.Attempt != 1 || *got.MaxAttempts != 10 {
		t.Errorf("converted error aliases the tailer's memory: status=%d attempt=%d max=%d, "+
			"want 429/1/10 — a later pass mutated a snapshot already converted",
			*got.HTTPStatus, *got.Attempt, *got.MaxAttempts)
	}
	if *got.RetryIn != 616*time.Millisecond {
		t.Errorf("RetryIn aliases the tailer's memory: %v, want 616ms", *got.RetryIn)
	}
}

// TestConvertSessionError_NilInNilOut pins that a healthy session stays
// distinguishable from one carrying a zero-valued error.
func TestConvertSessionError_NilInNilOut(t *testing.T) {
	if got := convertSessionError(nil); got != nil {
		t.Errorf("nil must convert to nil, got %+v", got)
	}
}

// TestConvertSessionError_AbsentFieldsStayNil is the terminal shape: no
// status, no counters, no delay. They must arrive in the domain as nil, not as
// pointers to zero, or a consumer would render "attempt 0 of 0".
func TestConvertSessionError_AbsentFieldsStayNil(t *testing.T) {
	got := convertSessionError(&tailer.SessionError{
		Phase:   tailer.ErrorPhaseTerminal,
		Class:   "provider",
		Message: "API Error: API returned an empty or malformed response (HTTP 200)",
	})
	if got == nil {
		t.Fatal("conversion returned nil for a non-nil error")
	}
	if got.HTTPStatus != nil || got.Attempt != nil || got.MaxAttempts != nil || got.RetryIn != nil {
		t.Errorf("absent numbers must stay nil through the conversion, got %+v", got)
	}
	if got.Phase.Known() != true {
		t.Error("a terminal phase must survive the conversion as a known phase")
	}
}
