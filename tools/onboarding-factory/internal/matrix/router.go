// Package matrix is the single canonical model of the agent-onboarding
// scenario × adapter matrix. Before this package, "the matrix" was implicit —
// reconstructed on every read from scenarios.json + per-agent capabilities.json
// + on-disk assessment/recording artifacts by ~10 independent consumers (gates,
// viewer, docs), each with its own jq filter / loop / disposition logic. That
// duplication is what let cells drift silently (the pi/streaming-partial-writes
// desync #507's consistency-gate was built to catch).
//
// This file holds the PURE decision tables — no IO — ported verbatim from the
// bash gates so their behaviour is preserved bit-for-bit:
//
//   - Route          ← scripts/lib/consistency-gate.sh  cs_route
//   - Disposition    ← scripts/lib/completeness-gate.sh  cg_disposition (steps 3-8)
//   - CellVerdict    ← scripts/lib/consistency-gate.sh  cs_cell_verdict
//   - DeriveDisplayState ← internal/viewer/catalog.go    deriveDisplayState
//
// The loader (matrix.go) assembles CellState values by combining these tables
// with the file/disk facts; gates and the viewer become thin clients.
package matrix

import "strings"

// Route classifies what an assessment's three axes IMPLY, per the
// assess/SKILL.md verdict matrix. Mirrors cs_route exactly, including the
// default: a parseable assessment whose axes are all empty/absent routes
// record_now (the else branch), the same as the bash `else "record_now"`.
type Route string

const (
	RouteFrozen       Route = "frozen"       // agent can't, or daemon can't observe → matrix MUST be applicable:false
	RouteRecordNow    Route = "record_now"   // supports + daemon + driver all green → record it
	RouteDriverGap    Route = "driver_gap"   // recordable after an extend-driver step
	RouteInconclusive Route = "inconclusive" // daemon unknown → re-assess before routing
	RouteNone         Route = ""             // assessment absent or malformed (caller skips)
)

// computeRoute ports cs_route. Inputs are the three axis strings (empty when
// the key is absent in a parseable assessment).
func computeRoute(supports, daemon, driver string) Route {
	switch {
	case supports == SupportsNo || supports == SupportsUnknown:
		return RouteFrozen
	case daemon == DaemonIncapable || daemon == DaemonNotApplicable:
		return RouteFrozen
	case daemon == DaemonUnknown:
		return RouteInconclusive
	case strings.HasPrefix(driver, DriverGapPrefix):
		return RouteDriverGap
	default:
		return RouteRecordNow
	}
}

// Disposition is the completeness-gate verdict for a cell: where it sits in the
// onboarding pipeline. Terminal verdicts (recorded / applicable_false /
// driver_gap) pass the gate; non-terminal (unassessed / assessed_not_recorded)
// fail it.
type Disposition string

const (
	DispRecorded          Disposition = "recorded"
	DispApplicableFalse   Disposition = "applicable_false"
	DispDriverGap         Disposition = "driver_gap"
	DispAssessedNotRecord Disposition = "assessed_not_recorded"
	DispUnassessed        Disposition = "unassessed"
)

// IsTerminal reports whether a disposition passes the completeness gate.
func (d Disposition) IsTerminal() bool {
	switch d {
	case DispRecorded, DispApplicableFalse, DispDriverGap:
		return true
	default:
		return false
	}
}

// ApplicableState is the by_adapter.<agent>.applicable rollup over a
// coverage_id's recipe variants — ported from cs_applicable_state.
//
//	AppAbsent — no by_adapter entry for the agent under this coverage_id
//	AppFalse  — EVERY variant is explicitly applicable:false
//	AppTrue   — at least one variant is recordable (applicable absent or true)
type ApplicableState string

const (
	AppAbsent ApplicableState = "absent"
	AppTrue   ApplicableState = "true"
	AppFalse  ApplicableState = "false"
)

// Verdict is the consistency-gate decision for one un-recorded cell.
type Verdict string

const (
	VerdictOK               Verdict = "ok"
	VerdictContradictRecord Verdict = "CONTRADICTION_RECORD_NOW"
	VerdictContradictFrozen Verdict = "CONTRADICTION_FROZEN"
)

// CellVerdict ports cs_cell_verdict — the pure decision table at the heart of
// the consistency gate. A recorded cell short-circuits to ok. A record_now
// assessment paired with applicable:false and NO documented record_blocked is
// the silent desync the gate exists to catch; a non-empty blocked reason makes
// the pairing consistent (the deferral is documented in the assessment). A
// frozen assessment paired with applicable:true is the inverse contradiction.
func CellVerdict(route Route, appl ApplicableState, recorded bool, blocked string) Verdict {
	if recorded {
		return VerdictOK
	}
	switch route {
	case RouteRecordNow:
		if appl == AppFalse && blocked == "" {
			return VerdictContradictRecord
		}
	case RouteFrozen:
		if appl == AppTrue {
			return VerdictContradictFrozen
		}
	}
	return VerdictOK
}

// DeriveDisplayState rolls the three orthogonal facts — agent support, daemon
// capability, driver capability — plus the MEASURED recording status and whether
// the cell is applicable for a live recording, up into one matrix display state
// (#476). Ported from viewer's deriveDisplayState so the viewer and the matrix
// model never diverge. Precedence is fixed and daemon-before-driver: a product
// (daemon) problem outranks a tooling (driver) gap because it's the more
// fundamental blocker.
//
// applicable is false when the cell's recipe is marked applicable:false — a
// deliberate deferral (e.g. a record_blocked behavior covered by a unit test).
// Such a cell will never be recorded here, so it is terminal (not applicable),
// not the actionable pending-record an un-recorded recordable cell gets.
//
// Every value returned here is a constant from vocabulary.go — the schema is
// the only place a state is spelled out (#1367).
func DeriveDisplayState(supports, daemon, driver string, hasRecording, applicable bool) string {
	switch supports {
	case SupportsNo:
		return StateNotApplicable
	case "", SupportsUnknown:
		return StateUnknown
	}
	// An axis value outside the vocabulary reads as unknown rather than
	// falling through to the optimistic arms below. Without this a malformed
	// cell derives to "observed" and inflates the coverage numbers — the worst
	// possible default, and one `of validate` alone cannot prevent because it
	// gates CI, not this function (#1367 review).
	if !IsValidAgentSupports(supports) {
		return StateUnknown
	}
	switch {
	case daemon == DaemonNotApplicable:
		return StateNotApplicable
	case daemon == DaemonIncapable:
		return StateUnobservable
	case daemon == DaemonBug:
		return StateBlockedDaemon
	case strings.HasPrefix(driver, DriverGapPrefix):
		return StateBlockedDriver
	case daemon == "" || daemon == DaemonUnknown:
		return StateUnknown
	case !IsValidDaemonCapability(daemon):
		return StateUnknown
	}
	if !hasRecording {
		if !applicable {
			return StateNotApplicable
		}
		return StatePendingRecord
	}
	return StateObserved
}
