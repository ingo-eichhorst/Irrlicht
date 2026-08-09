package matrix

import "strings"

// This file is the schema for the onboarding matrix's vocabulary: the single
// definition of every token the factory derives, stores on disk, or renders.
//
// Before #1367 there was no such definition. The cell state existed only as
// bare literals inside the switch that PRODUCED it (DeriveDisplayState) and
// the switches that CONSUMED it (`of status --summary`, the viewer's
// _displayMeta) — so the same state came to be spelled "n.a." by the producer  retired-spelling-ok
// and "n/a" by the summary's column header and every viewer legend row, and
// two cells on disk picked up the dotted spelling behind a compatibility arm
// in computeRoute that nothing validated.
//
// SPELLING — why "n/a" and not the dotted form:
//
//  1. It is already the dominant on-disk spelling: 164 stored capability
//     values against 8, so canonicalising the other way would have rewritten
//     54 cell files instead of 2.
//  2. It is already the spelling of every human-facing LABEL — the
//     `of status --summary` column header and all three viewer legend rows —
//     so the dotted form would have meant changing the text people read to
//     the less conventional variant.
//  3. It is the conventional English abbreviation, and the rest of the repo
//     (the web dashboard, the macOS History view, precheck.sh) already uses it
//     for "not available".
//  4. The dotted form was only ever DERIVED — nothing but DeriveDisplayState
//     produced it — so retiring it costs no stored-data churn beyond the 8
//     stray values that were themselves the bug.
//
// The tokens below are untyped string constants rather than a defined type
// (unlike Route and Disposition in router.go). DisplayState travels as a plain
// string through CellState, the `of status --json` payload and the viewer's
// `display_state` field; giving it a named type would have rippled through
// every one of those signatures for no gain to this ticket, whose product is
// one agreed spelling reachable from one place.

// Display states — the rolled-up per-cell verdict DeriveDisplayState returns.
const (
	StateObserved      = "observed"       // assessed recordable AND has a recording
	StatePendingRecord = "pending-record" // assessed recordable, not yet recorded
	StateBlockedDaemon = "blocked-daemon" // trace exists but the daemon mis-handles it
	StateBlockedDriver = "blocked-driver" // the recording harness lacks a step type
	StateUnobservable  = "unobservable"   // leaves no trace the daemon could see
	StateNotApplicable = "n/a"            // out of scope for this agent — terminal
	StateUnknown       = "unknown"        // not assessed, or assessed with empty axes
)

// DisplayStates is the closed set of display states, in pipeline order.
var DisplayStates = []string{
	StateObserved, StatePendingRecord, StateBlockedDaemon,
	StateBlockedDriver, StateUnobservable, StateNotApplicable, StateUnknown,
}

// IsValidDisplayState reports whether s is a member of the display-state
// vocabulary.
func IsValidDisplayState(s string) bool {
	for _, v := range DisplayStates {
		if v == s {
			return true
		}
	}
	return false
}

// Assessment axis values. agent_supports and daemon_capability are closed
// sets; driver_capability is open-ended (see DriverGapPrefix).
const (
	SupportsYes     = "yes"
	SupportsPartial = "partial"
	SupportsNo      = "no"
	SupportsUnknown = "unknown"

	DaemonFull      = "full"
	DaemonBug       = "bug"
	DaemonIncapable = "incapable"
	DaemonUnknown   = "unknown"
	// DaemonNotApplicable is the SAME token as the display state: a cell whose
	// daemon axis is not applicable derives to StateNotApplicable. Aliasing
	// them here rather than repeating the literal is what keeps the stored
	// vocabulary and the rendered vocabulary from drifting apart again.
	DaemonNotApplicable = StateNotApplicable

	DriverReady = "ready"
	// DriverGapPrefix marks an open-ended gap:<primitive> value.
	DriverGapPrefix = "gap:"
)

// AgentSupportsValues is the closed set for the agent_supports axis.
var AgentSupportsValues = []string{SupportsYes, SupportsPartial, SupportsNo, SupportsUnknown}

// DaemonCapabilityValues is the closed set for the daemon_capability axis.
var DaemonCapabilityValues = []string{DaemonFull, DaemonBug, DaemonIncapable, DaemonUnknown, DaemonNotApplicable}

// RetiredSpellings maps a retired token to the canonical token that replaced
// it. It is deliberately separate from the closed sets above: a retired
// spelling has to be rejected on EVERY axis, including the open-ended
// driver_capability one that has no closed set to check against.
var RetiredSpellings = map[string]string{
	"n.a.": StateNotApplicable, // retired-spelling-ok — #1367
}

// CanonicalFor returns the canonical replacement for a retired spelling, and
// whether v was retired at all.
func CanonicalFor(v string) (string, bool) {
	c, ok := RetiredSpellings[v]
	return c, ok
}

// IsValidAgentSupports reports whether v is a valid agent_supports value. An
// empty value is valid: the axis is omitempty on disk and readers default it
// to "unknown".
func IsValidAgentSupports(v string) bool { return v == "" || inSet(AgentSupportsValues, v) }

// IsValidDaemonCapability reports whether v is a valid daemon_capability
// value. Empty is valid, as for IsValidAgentSupports.
func IsValidDaemonCapability(v string) bool { return v == "" || inSet(DaemonCapabilityValues, v) }

// IsValidDriverCapability reports whether v is a valid driver_capability
// value. This axis is open-ended — gap:<primitive> names whichever driver step
// is missing — so it is checked structurally rather than against a closed set.
//
// It is deliberately permissive beyond that: two kiro-cli cells carry
// driver_capability="full", a daemon-axis token that predates this schema.
// Turning that into a hard failure would mean rewriting a semantic claim
// (whether the author meant "ready") that #1367 has no evidence for, so those
// values are left alone and a closed driver set is deferred. The retired-
// spelling check in the validator still covers this axis, which is what #1367
// actually owns.
func IsValidDriverCapability(v string) bool {
	if strings.HasPrefix(v, DriverGapPrefix) {
		return len(v) > len(DriverGapPrefix)
	}
	return true
}

func inSet(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
