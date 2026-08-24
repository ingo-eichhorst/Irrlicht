package session

import (
	"encoding/json"
	"time"
)

// ErrorPhase says whether the agent is still trying, or has given up.
//
// This is the distinction StateError alone cannot carry and the one #1803's
// provider-overloaded-retry and provider-overloaded-terminal scenarios turn
// on: both sit in StateError, and what separates them is whether another
// attempt is coming. A user looking at a red session wants to know whether to
// wait or to intervene, and that is exactly this field.
//
// IT IS THE ADAPTER'S VERDICT, NOT A DERIVED ONE — and specifically NOT
// `Attempt == MaxAttempts`. That equality looks like the obvious derivation
// and is wrong against the recorded data: across every api_error in
// replaydata (16 events, claudecode 2-9_token-quota-exhausted), the attempt
// counter never once reaches maxRetries — the observed range is 1..5 of 10,
// and the ladder is abandoned when the user intervenes rather than exhausted.
// Meanwhile the shapes that ARE terminal carry no counters at all: claudecode
// writes an `isApiErrorMessage` assistant event with no retry fields, and
// copilot writes `session.error` with none either. Deriving from the counters
// would therefore report every real give-up as "still retrying" and every real
// retry as nothing in particular.
type ErrorPhase string

const (
	// ErrorPhaseUnknown is the zero value, and an honest answer rather than a
	// missing one: the transcript reported a failure without saying whether
	// another attempt follows. Real, not hypothetical — a copilot
	// `session.error` with errorType "query" is exactly this.
	ErrorPhaseUnknown ErrorPhase = ""

	// ErrorPhaseRetrying means the agent has another attempt scheduled. The
	// session is failing but not yet lost, and the informative behaviour is to
	// sit red for the whole retry window rather than flicker per attempt.
	ErrorPhaseRetrying ErrorPhase = "retrying"

	// ErrorPhaseTerminal means no further attempt is coming: the turn is over
	// and it failed. Only the next turn the user starts clears it.
	ErrorPhaseTerminal ErrorPhase = "terminal"
)

// Known reports whether p is an actual phase rather than the unknown zero
// value. Mirrors SignalTier.Known for the same reason: callers must be able to
// tell "the agent said terminal" from "the agent said nothing", and a bare
// comparison against ErrorPhaseTerminal silently conflates them.
func (p ErrorPhase) Known() bool {
	return p == ErrorPhaseRetrying || p == ErrorPhaseTerminal
}

// SessionError is one session-level failure: the provider refused or failed
// the call, credentials were rejected, the agent process died mid-turn, or
// Irrlicht could not read the session. It is what puts a session in
// StateError, and what a UI renders on the red error line (#1801, #1802).
//
// DISTINCT FROM ParsedEvent.IsError, deliberately and permanently. IsError is
// a tool_result failure — a grep that matched nothing, a build that broke, a
// command that exited non-zero. That is the agent working normally and must
// never turn a session red. Eight adapters set IsError today and nothing reads
// it; this type is a separate channel rather than an extension of it, so the
// two can never be confused by a future reader (see #1796's "Why this is
// cheaper than it looks").
//
// EVERY NUMERIC FIELD IS A POINTER, and that is load-bearing rather than
// stylistic. The recorded shapes disagree about which numbers exist at all:
//
//   - claudecode `system`/`api_error` carries status, retryInMs, retryAttempt
//     and maxRetries — the rich case;
//   - claudecode's terminal `isApiErrorMessage` assistant event carries NONE
//     of them; its own text reads "API Error: API returned an empty or
//     malformed response (HTTP 200)", where even the number in the prose is
//     the transport's success code, not a failure code;
//   - copilot `session.error` carries statusCode for errorType "rate_limit"
//     and omits it entirely for errorType "query".
//
// With plain ints, all three absences read as 0 — and "attempt 0 of 0" would
// derive as a give-up from data that said nothing. This is the same trap
// ParsedEvent.PendingBackgroundAgentCount documents as "absence must not be
// read as zero", in a place where getting it wrong invents a verdict instead
// of losing one.
type SessionError struct {
	// Phase is whether another attempt is coming. See ErrorPhase.
	Phase ErrorPhase `json:"phase,omitempty"`

	// Class is the adapter's normalized failure class — "rate_limit",
	// "quota", "auth", "context_limit", "provider", "query", "process_death".
	// Free-form on purpose: the vocabularies genuinely differ per agent
	// (copilot types its own errorType; claudecode's is nested inside the
	// error object) and flattening them into a shared enum here would mean
	// inventing mappings before #1799/#1800 have looked at the payloads.
	Class string `json:"class,omitempty"`

	// Message is the human-readable failure text, verbatim from the agent.
	// This is what a user actually reads on the error line, so it is carried
	// rather than reconstructed — the agents' own wording is better than
	// anything derived from Class and status.
	Message string `json:"message,omitempty"`

	// HTTPStatus is the provider's status code, or nil when the transcript
	// carries none. Nil is common, not exceptional — see the type comment.
	HTTPStatus *int `json:"http_status,omitempty"`

	// Attempt is the 1-based retry attempt this error belongs to, or nil when
	// the agent does not report one.
	//
	// It counts attempts within ONE API call and resets to 1 for the next
	// call — verified against the recorded ladder, which runs 1..5, then
	// restarts at 1 after the user queues a message. It is not a
	// session-lifetime counter and must not be summed across errors.
	Attempt *int `json:"attempt,omitempty"`

	// MaxAttempts is the agent's own retry ceiling for this call (claudecode
	// reports 10), or nil when unreported. Carried for display — "attempt 3
	// of 10" is far more informative than "attempt 3" — and explicitly NOT as
	// the input to a terminal/retrying derivation; see ErrorPhase.
	MaxAttempts *int `json:"max_attempts,omitempty"`

	// RetryIn is how long the agent will wait before the next attempt, or nil
	// when unreported.
	//
	// A time.Duration rather than a number because the source is fractional:
	// claudecode writes retryInMs as a float (616.4520045919932 in the
	// recordings), so an int-milliseconds field would silently truncate. Nil
	// rather than zero because "retrying immediately" and "no delay was
	// reported" are different facts, and ErrorPhaseRetrying with a nil RetryIn
	// is the honest "another attempt is coming, timing unstated".
	//
	// It is deliberately NOT serialized as a bare Duration. Go marshals one as
	// unlabelled nanoseconds (616452004), and every other serialized time
	// quantity in this package names its unit — ElapsedSeconds,
	// RateLimitWindow.WindowMinutes, RateLimitForecastEta. The JS and Swift
	// clients that render this (#1801, #1802) would have to know, from nothing
	// in the payload, both the unit and that it is the only field using it. So
	// SessionError carries explicit Marshal/UnmarshalJSON emitting
	// `retry_in_ms` as a fractional number, which is also the unit the agents
	// themselves report.
	//
	// No struct tag: the custom marshaller owns this field's wire form, and a
	// tag here would be dead code that reads like the source of truth.
	RetryIn *time.Duration `json:"-"`
}

// sessionErrorJSON is SessionError's wire form. Split out rather than hand-
// writing the encoder so the field list stays a struct — a hand-rolled
// json.Marshal would have to be edited in two places every time a field is
// added, which is the drift newMergedMetrics' allowlist already demonstrates
// the cost of.
//
// Only RetryIn differs from the Go shape; everything else is copied by name so
// the two cannot disagree about a tag.
type sessionErrorJSON struct {
	Phase       ErrorPhase `json:"phase,omitempty"`
	Class       string     `json:"class,omitempty"`
	Message     string     `json:"message,omitempty"`
	HTTPStatus  *int       `json:"http_status,omitempty"`
	Attempt     *int       `json:"attempt,omitempty"`
	MaxAttempts *int       `json:"max_attempts,omitempty"`
	RetryInMs   *float64   `json:"retry_in_ms,omitempty"`
}

// MarshalJSON emits RetryIn as fractional milliseconds under an explicitly
// unit-named key. See the RetryIn field for why.
func (e SessionError) MarshalJSON() ([]byte, error) {
	out := sessionErrorJSON{
		Phase:       e.Phase,
		Class:       e.Class,
		Message:     e.Message,
		HTTPStatus:  e.HTTPStatus,
		Attempt:     e.Attempt,
		MaxAttempts: e.MaxAttempts,
	}
	if e.RetryIn != nil {
		ms := float64(*e.RetryIn) / float64(time.Millisecond)
		out.RetryInMs = &ms
	}
	return json.Marshal(out)
}

// UnmarshalJSON is MarshalJSON's inverse, so a persisted session state round-
// trips. Without it the custom encoder would be write-only and every reload
// would silently drop the retry delay — the same class of one-directional
// plumbing bug as a field missing from the merge allowlist.
func (e *SessionError) UnmarshalJSON(b []byte) error {
	var in sessionErrorJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*e = SessionError{
		Phase:       in.Phase,
		Class:       in.Class,
		Message:     in.Message,
		HTTPStatus:  in.HTTPStatus,
		Attempt:     in.Attempt,
		MaxAttempts: in.MaxAttempts,
	}
	if in.RetryInMs != nil {
		d := time.Duration(*in.RetryInMs * float64(time.Millisecond))
		e.RetryIn = &d
	}
	return nil
}

// IsRetrying reports whether another attempt is known to be coming.
//
// Deliberately false for ErrorPhaseUnknown: an unknown phase is not a retry,
// and a caller that wants to distinguish "not retrying" from "we were not told"
// must read Phase itself. Named as a question about the phase rather than as a
// general predicate so no caller mistakes it for "is this error still open".
func (e *SessionError) IsRetrying() bool {
	return e != nil && e.Phase == ErrorPhaseRetrying
}

// Equal reports whether two errors carry the same content, treating nil as a
// value.
//
// Compares the POINTED-TO values, not the pointers: two errors parsed from two
// passes over the same transcript line hold equal-but-distinct *int fields, so
// a pointer comparison would report every pass as a change. That is the whole
// reason it cannot be `==`.
//
// NO PRODUCTION CALLER YET — it is used by this package's tests and is here
// for #1801, which needs it to avoid re-broadcasting a session whose error has
// not actually changed across a poll (the job subagentSummary.Equal does at
// session_detector_activity.go's re-broadcast check). Said plainly rather than
// implied, so nobody reads this comment as a description of behaviour that
// exists. The same holds for IsRetrying above.
func (e *SessionError) Equal(o *SessionError) bool {
	if e == nil || o == nil {
		return e == o
	}
	return e.Phase == o.Phase &&
		e.Class == o.Class &&
		e.Message == o.Message &&
		eqPtr(e.HTTPStatus, o.HTTPStatus) &&
		eqPtr(e.Attempt, o.Attempt) &&
		eqPtr(e.MaxAttempts, o.MaxAttempts) &&
		eqPtr(e.RetryIn, o.RetryIn)
}

// eqPtr compares two optional values by what they point AT, treating nil as a
// value: two nils are equal, a nil and a non-nil are not. One generic helper
// rather than one per pointed-to type, so a field added to SessionError with a
// new numeric type does not need a fourth near-identical copy of it.
func eqPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
