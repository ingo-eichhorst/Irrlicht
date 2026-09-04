package session

// Autonomy spans (#1905).
//
// An autonomy span is one unbroken stretch of `working` before the session
// left that state. The END REASON — which state it left FOR — is the signal
// the feature is about:
//
//	StateWaiting → it needed a human. The number the feature is really about.
//	StateReady   → the turn finished on its own.
//	StateError   → it broke.
//
// The duration alone cannot tell those apart, so a span carries both.

// AutonomyEndReasons returns the end-reason vocabulary: every canonical state
// a session can leave `working` FOR, in canonical order.
//
// DERIVED, never retyped. Two reasons, and both are load-bearing:
//
//   - The vocabulary IS "the canonical states minus working". A fifth state
//     joins it here for free, which is the rule AGENTS.md states for every
//     other consumer of this list.
//   - tools/state-vocabulary-lint.sh (#1804) fails any single line that names
//     three-or-more-but-not-all of the canonical values. A hand-typed list of
//     these three is exactly that shape — the linter would be right to refuse
//     it, because the day a fifth state lands the list is silently wrong.
func AutonomyEndReasons() []string {
	out := make([]string, 0, len(canonicalStates)-1)
	for _, s := range CanonicalStates() {
		if s == StateWorking {
			continue
		}
		out = append(out, s)
	}
	return out
}

// IsAutonomyEndReason reports whether reason is a state a span can end in.
// `working` is not: a span that is still working has not ended.
func IsAutonomyEndReason(reason string) bool {
	return IsCanonicalState(reason) && reason != StateWorking
}

// Autonomy end-reason priorities for the run strip's pixel-collapse rule
// (#1905, design decision 4). When one device-pixel column of the strip holds
// several spans, the column paints the HIGHEST-priority reason in it.
//
// The order is the session-history strip's own (services.statePriority*,
// #1805), where one error in a bucket paints the whole bucket red: the failure
// state outranks the needs-a-human state, which outranks the finished-cleanly
// state. `TestAutonomyReasonLadderMatchesHistoryBar` in the services package
// pins the two ladders together so they cannot drift.
//
// Declared one value per line rather than as an ordered slice: the ladder is
// NOT the canonical order (canonical puts `ready` after `waiting`; the ladder
// puts it below), so it cannot be derived from CanonicalStates and has to be
// stated. One state per line keeps it out of the vocabulary linter's sights.
const (
	autonomyPriorityUnknown = 0
	autonomyPriorityReady   = 1
	autonomyPriorityWaiting = 2
	autonomyPriorityError   = 3
)

// AutonomyReasonPriority returns a span end reason's rank on the collapse
// ladder. An unrecognized reason ranks below every real one, so a build that
// cannot name a reason never outranks activity it can.
func AutonomyReasonPriority(reason string) int {
	switch reason {
	case StateError:
		return autonomyPriorityError
	case StateWaiting:
		return autonomyPriorityWaiting
	case StateReady:
		return autonomyPriorityReady
	}
	return autonomyPriorityUnknown
}
