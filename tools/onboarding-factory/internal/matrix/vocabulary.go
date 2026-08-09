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

// ---------------------------------------------------------------------------
// Maturity ladder (#1369)
// ---------------------------------------------------------------------------

// Maturity is an adapter's declared stage on the four-rung ladder the site
// docs and the README have described in prose since the compatibility grid
// existed (site/docs/adapters.html, "Maturity Stages"). Until #1369 it was
// prose ONLY: a hand-maintained word in a markdown table with nothing reading
// it, so an adapter's declared stage and the evidence in replaydata/ could not
// disagree — there was no relation between them to violate.
//
// The tokens live here rather than beside the data for the same reason the
// display states do: they are a closed set, and the one place a state is
// spelled out is the schema (#1367).
const (
	MaturityPlanned = "planned" // named in the grid; no adapter package yet
	MaturityAlpha   = "alpha"   // STATE ONLY — the three-state model works, metrics are not claimed
	MaturityBeta    = "beta"    // every state-core scenario is settled
	MaturityStable  = "stable"  // every core scenario, metrics included, is settled
)

// Maturities is the closed set, in ascending order. Order is load-bearing:
// MaturityRank indexes into it, and the `of validate` maturity gate compares
// ranks (declared must not exceed earned).
var Maturities = []string{MaturityPlanned, MaturityAlpha, MaturityBeta, MaturityStable}

// IsValidMaturity reports whether v is a known maturity token. Unlike the
// assessment axes, empty is NOT valid: an adapter that appears in the
// capability model has to say where it claims to be.
func IsValidMaturity(v string) bool { return slices.Contains(Maturities, v) }

// MaturityRank returns v's position on the ladder, or -1 when v is not a
// maturity token.
func MaturityRank(v string) int { return slices.Index(Maturities, v) }

// ---------------------------------------------------------------------------
// Capability states (#1369)
// ---------------------------------------------------------------------------

// A capability state is what an adapter's relationship to one behavioural
// trait is, and it is deliberately THREE-valued rather than a boolean.
//
// The two-valued model — "adapter has capability X" — was tried and pruned
// (#529's meta.capability_vocab, a 26-feature per-adapter boolean vector).
// Re-fitted against today's matrix it explained 5 of the 52 not-applicable
// cells for the five adapters it covered and produced THREE false positives,
// each one a live cell it would have declared dead (codex
// tool-gate-permission-prompt, foreground-subagent, background-subagent — all
// pending-record today, all claimed dead by a stale permission_hooks:false /
// subagents:false). That is why it was orphaned, and why re-introducing the
// same shape would fail the same way.
//
// The reason a boolean cannot work is visible in the data: of the 126 cells
// that are structurally dead today, ALL 74 "unobservable" ones have
// agent_supports ∈ {yes, partial} — the agent HAS the feature — and are dead
// only because the feature never reaches the Source the daemon tails. Merging
// those with "the agent lacks the feature" loses exactly the distinction the
// display vocabulary already draws between StateUnobservable and
// StateNotApplicable. Hence three values, one per outcome.
const (
	// CapabilityAbsent — the agent does not have the feature at all.
	// Derives StateNotApplicable. A property of the AGENT; stable across
	// daemon releases.
	CapabilityAbsent = "absent"
	// CapabilityUntraced — the agent has the feature, but exercising it leaves
	// no trace in any Source the adapter reads. Derives StateUnobservable.
	// A property of the (agent, feature, transport, adapter) tuple: it flips
	// the moment the vendor persists a field or the adapter starts tailing a
	// second Source. claudecode's subscription-detection is exactly that — it
	// is observable ONLY through the statusLine hook POST, not the transcript.
	CapabilityUntraced = "untraced"
	// CapabilityTraced — the feature exists and reaches a Source. The default
	// for any (adapter, trait) pair the model does not mention, so a new
	// adapter declares only what is missing.
	CapabilityTraced = "traced"
)

// CapabilityStates is the closed set of capability states.
var CapabilityStates = []string{CapabilityAbsent, CapabilityUntraced, CapabilityTraced}

// IsValidCapabilityState reports whether v names a capability state. Empty is
// valid and means CapabilityTraced — see the const block.
func IsValidCapabilityState(v string) bool {
	return v == "" || slices.Contains(CapabilityStates, v)
}

// StructuralStateFor maps a capability state to the display state it derives,
// returning ok=false for the states that derive nothing. This is the whole of
// the derivation: everything else in capability.go is lookup and plumbing.
func StructuralStateFor(capState string) (displayState string, ok bool) {
	switch capState {
	case CapabilityAbsent:
		return StateNotApplicable, true
	case CapabilityUntraced:
		return StateUnobservable, true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------------------
// The core scenario set (#1369)
// ---------------------------------------------------------------------------

// CoreStateScenarios and CoreMetricsScenarios together are the CORE TWELVE:
// the only scenarios that gate a maturity promotion. The other 34 are
// optional — they are still assessed, still recorded, still rendered, and
// still gated for schema validity by `of validate`; they simply do not hold an
// adapter back from a tier.
//
// WHY A CORE SET EXISTS AT ALL. 84% of an onboarding PR is recorded fixtures:
// copilot's #1332 was 14,426 additions of which 12,130 were replaydata/ and
// only 2,218 were adapter code. Requiring all 46 before an adapter counts as
// anything makes the recording cost the gate on adoption, and the recording
// cost is the one part of onboarding that does not get cheaper with practice.
//
// HOW THESE TWELVE WERE CHOSEN. Not for coverage — for discriminating power,
// measured against the matrix as it actually stands. Each entry below is a
// scenario where a failure is a failure of the PRODUCT (a row that never
// appears, a session stuck in `working`, a $0 cost), and where the matrix
// shows adapters genuinely differing. Scenarios that every adapter passes
// identically discriminate nothing; scenarios dead for most adapters set an
// unmeetable bar. The twelve sit in between: across the eleven onboarded
// adapters they score 9–12 observed, and every miss is either a structural
// dead cell the capability model derives, or a real daemon/driver bug that
// SHOULD hold a promotion back.
//
// The set is spelled in code, not in replaydata/, on purpose. It is a policy
// decision that gates maturity claims, so weakening it has to be a reviewable
// diff against this comment rather than an edit to a data file.

// CoreStateScenarios gate `alpha` and above (four of them) and `beta` and
// above (all nine). They assert nothing about tokens, cost or model identity —
// only that the three-state model is correct — which is what makes `alpha`
// reachable by an adapter that has hooks and no transcript parser at all.
var CoreStateScenarios = []string{
	// --- the alpha floor: reachable on day one from lifecycle hooks alone ---
	// A row must exist before anything else about it can be right; this is
	// also the only cell that proves PID binding. Observed for all 11.
	"session-start",
	// The ready→working→ready topology IS the three-state model; every other
	// state assertion is a refinement of it. Observed for all 11.
	"basic-turn",
	// A tool call must not settle the turn. This is the single most common
	// false-`ready` bug — copilot closes a turn after EVERY tool call — and it
	// is what separates an adapter that tracks turns from one that tracks
	// lines. Observed for all 11.
	"auto-executed-tool-call",
	// The turn-done marker. Adapters without an explicit one (gemini-cli) run
	// a heuristic, and this is the cell that catches a wrong heuristic before
	// it ships. Observed for all 11.
	"turn-end-terminal-text",

	// --- the beta additions: need a real Source, not just hooks ---
	// Without it sessions leak as ghosts forever — the #727/#744 class, the
	// most-reported defect in this repo. hermes cannot yet drive it
	// (blocked-driver), which is exactly the kind of gap a tier should hold.
	"session-end",
	// Distinguishes an idle live session from a dead one. A reaper that gets
	// this wrong deletes rows out from under a working user.
	"long-idle-live-session",
	// `waiting` is one of the three states, and this is the only scenario that
	// exercises it on a fair footing across adapters. An adapter that never
	// reports `waiting` has two-thirds of the product.
	"user-blocking-question",
	// An errored turn must still settle. A session stuck in `working` forever
	// is the worst user-visible failure mode there is, and four adapters
	// genuinely cannot persist an error epilogue — the derivation says so
	// rather than the promotion silently ignoring it.
	"turn-aborted-by-error",
	// Session identity: two agents in one repo must not collapse into one row
	// or bind each other's PID. Every PID-discovery bug this repo has shipped
	// is visible here and nowhere else.
	"multiple-sessions-same-cwd",
}

// CoreMetricsScenarios gate `stable` only. Their absence from the alpha and
// beta floors is what the `alpha` tier MEANS: state only, no metrics.
var CoreMetricsScenarios = []string{
	// The model name is the key into the pricing table; get it wrong and the
	// entire session prices at $0, silently. Observed for all 11.
	"model-identification",
	// Tokens are the input to both cost and the context-percentage the user
	// actually watches. Two adapters cannot surface them (aider, kiro-cli);
	// both are derived, so neither is blocked from beta by it.
	"token-accounting",
	// The context-window percentage — the number users act on. copilot is
	// blocked-daemon here, which is a real bug and should hold stable back.
	"model-context-display",
}

// CoreScenarios returns the full core twelve, state scenarios first.
func CoreScenarios() []string {
	out := make([]string, 0, len(CoreStateScenarios)+len(CoreMetricsScenarios))
	out = append(out, CoreStateScenarios...)
	return append(out, CoreMetricsScenarios...)
}

// CoreAlphaScenarios is the alpha floor: the first four state scenarios, the
// ones reachable from lifecycle hooks with no transcript parser.
func CoreAlphaScenarios() []string { return CoreStateScenarios[:4] }

// IsCoreScenario reports whether a scenario name is one of the core twelve.
func IsCoreScenario(name string) bool {
	return slices.Contains(CoreStateScenarios, name) || slices.Contains(CoreMetricsScenarios, name)
}

// MaturityFloor returns the scenarios a given maturity requires to be settled.
// The floors are cumulative by construction (alpha ⊂ beta ⊂ stable), which is
// what lets the gate compare ranks instead of sets.
func MaturityFloor(maturity string) []string {
	switch maturity {
	case MaturityAlpha:
		return CoreAlphaScenarios()
	case MaturityBeta:
		return CoreStateScenarios
	case MaturityStable:
		return CoreScenarios()
	default: // planned — nothing is claimed, so nothing is required
		return nil
	}
}
