package matrix

import (
	"fmt"
	"slices"
	"strings"
)

// This file is the schema for the onboarding matrix's vocabulary: the single
// definition of every token the factory derives, stores on disk, or renders,
// plus the check that enforces it.
//
// Before #1367 there was no such definition. The cell state existed only as
// bare literals inside the switch that PRODUCED it (DeriveDisplayState) and
// the switches that CONSUMED it (`of status --summary`, the viewer's
// _displayMeta) — so the same state came to be spelled with a dot by the
// producer and with a slash by the summary's column header and every viewer
// legend row, and two cells on disk picked up the dotted spelling behind a
// compatibility arm in computeRoute that nothing validated.
//
// SPELLING — why "n/a" and not the dotted form:
//
//  1. It is already the dominant on-disk spelling (164 stored capability
//     values against 8) AND the only one that was ever stored deliberately —
//     the dotted form was only ever DERIVED, so retiring it costs no data
//     churn beyond the 8 stray values that were themselves the bug.
//  2. It is already the spelling of every human-facing LABEL — the
//     `of status --summary` column header and all three viewer legend rows —
//     so the dotted form would have meant changing the text people read to
//     the less conventional variant.
//  3. It is the conventional English abbreviation, and the rest of the repo
//     (the web dashboard, the macOS History view, precheck.sh) already uses it
//     for "not available".
//
// WHY THE TOKENS ARE UNTYPED STRINGS, unlike Route and Disposition next door
// in router.go: a named string type would not have helped. Untyped constants
// convert freely, so a comparison against the dotted literal compiles just the
// same under `type DisplayState string` — the type cannot catch this ticket's
// failure mode. And DisplayState is derived-only, never parsed from disk, so there is
// no unmarshal boundary for a type to guard. The values that DO come from
// disk are the three assessment axes, which are necessarily strings and can
// only be guarded by a validator — which is what ValidateAxes below is.

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

// DisplayStates is the closed set of display states, in pipeline order. The
// viewer mirrors this set in JavaScript (_displayMeta), which is why
// TestViewerRendersEveryDisplayState reads it — that test is what keeps the
// two languages from drifting.
var DisplayStates = []string{
	StateObserved, StatePendingRecord, StateBlockedDaemon,
	StateBlockedDriver, StateUnobservable, StateNotApplicable, StateUnknown,
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

	// DriverGapPrefix marks an open-ended gap:<primitive> value on the driver
	// axis, whose other values are "ready" and the shared not-applicable token.
	DriverGapPrefix = "gap:"
)

// AgentSupportsValues is the closed set for the agent_supports axis.
var AgentSupportsValues = []string{SupportsYes, SupportsPartial, SupportsNo, SupportsUnknown}

// DaemonCapabilityValues is the closed set for the daemon_capability axis.
var DaemonCapabilityValues = []string{DaemonFull, DaemonBug, DaemonIncapable, DaemonUnknown, DaemonNotApplicable}

// retiredSpellings maps a retired token to the canonical token that replaced
// it. It is deliberately separate from the closed sets above: a retired
// spelling has to be rejected on EVERY axis, including the open-ended
// driver_capability one that has no closed set to check against. Checking
// retired-before-valid also means a retired token can never be re-admitted by
// someone adding it to a closed set.
var retiredSpellings = map[string]string{
	"n.a.": StateNotApplicable, // retired-spelling-ok — #1367
}

// CanonicalFor returns the canonical replacement for a retired spelling, and
// whether v was retired at all.
func CanonicalFor(v string) (string, bool) {
	c, ok := retiredSpellings[v]
	return c, ok
}

// IsValidAgentSupports reports whether v is a valid agent_supports value. An
// empty value is valid: the axis is omitempty on disk and readers default it
// to "unknown".
func IsValidAgentSupports(v string) bool { return v == "" || slices.Contains(AgentSupportsValues, v) }

// IsValidDaemonCapability reports whether v is a valid daemon_capability
// value. Empty is valid, as for IsValidAgentSupports.
func IsValidDaemonCapability(v string) bool {
	return v == "" || slices.Contains(DaemonCapabilityValues, v)
}

// IsValidDriverCapability reports whether v is a valid driver_capability
// value. This axis is open-ended — gap:<primitive> names whichever driver step
// is missing — so it is checked structurally rather than against a closed set.
//
// It is deliberately permissive beyond that: two kiro-cli cells carry
// driver_capability="full", a daemon-axis token that predates this schema.
// Turning that into a hard failure would mean rewriting a semantic claim
// (whether the author meant "ready") that #1367 has no evidence for, so those
// values are left alone and a closed driver set is deferred. The retired-
// spelling check still covers this axis, which is what #1367 actually owns.
func IsValidDriverCapability(v string) bool {
	if strings.HasPrefix(v, DriverGapPrefix) {
		return len(v) > len(DriverGapPrefix)
	}
	return true
}

// ValidateAxes checks one tier's three assessment axes and returns a finding
// message per violation, empty when the tier is clean. tier names the JSON
// block the values came from ("metadata" or "details.assessment") so a finding
// points at the exact field to edit.
//
// It lives HERE, beside the vocabulary it enforces, rather than in the `of
// validate` command: the schema package is importable by every writer and
// reader of a cell, so the definition and its enforcement cannot drift apart —
// which is the same class of split #1367 exists to close.
func ValidateAxes(tier, supports, daemon, driver string) []string {
	var findings []string
	check := func(axis, value string, valid func(string) bool, expected string) {
		field := tier + "." + axis
		if canonical, retired := CanonicalFor(value); retired {
			findings = append(findings, fmt.Sprintf(
				"%s is %q, a retired spelling — use %q (#1367)", field, value, canonical))
			return
		}
		if !valid(value) {
			findings = append(findings, fmt.Sprintf("%s is %q (%s)", field, value, expected))
		}
	}
	check("agent_supports", supports, IsValidAgentSupports,
		"allowed: "+strings.Join(AgentSupportsValues, ", "))
	check("daemon_capability", daemon, IsValidDaemonCapability,
		"allowed: "+strings.Join(DaemonCapabilityValues, ", "))
	check("driver_capability", driver, IsValidDriverCapability,
		"a "+DriverGapPrefix+" value must name a primitive")
	return findings
}
