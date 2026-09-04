package main

import (
	"fmt"
	"io"
	"strconv"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// agentSummary is one agent's cell counts, bucketed by the matrix display
// state. Every value matrix.DeriveDisplayState can return maps to exactly one
// bucket — add()'s default arm guarantees it — so the buckets always sum to
// Total. That property is the point: a summary that quietly drops a state
// under-reports how much work is left, which is worse than no summary at all.
// TestStatusSummaryAgreesWithFullDump is what enforces it, by cross-checking
// against `of status --json` rather than against this type itself.
type agentSummary struct {
	Agent string `json:"agent"`
	// Recorded ← "observed": assessed recordable AND has a recording. NOT the
	// same as "has a recording on disk": DeriveDisplayState consults the
	// recording only after the daemon/driver switch, so a cell whose daemon
	// axis is bug/incapable — or whose driver has a gap — is counted under
	// blocked/unobservable even when a recording exists. 11 cells in the
	// current corpus are in exactly that state, so "482 − recorded" is not
	// the number of un-recorded cells.
	Recorded int `json:"recorded"`
	// Pending ← "pending-record": assessed recordable, not yet recorded.
	Pending int `json:"pending"`
	// Blocked ← "blocked-daemon" + "blocked-driver": recordable in principle,
	// held up by a daemon bug or a driver gap. Merged because the issue asks
	// for one "blocked" number; the two states stay separable in `of status`.
	Blocked int `json:"blocked"`
	// Unobservable ← "unobservable": the daemon cannot observe this at all.
	Unobservable int `json:"unobservable"`
	// NotApplicable ← matrix.StateNotApplicable: out of scope for this agent.
	// The JSON key stays "not_applicable" — it names the bucket, not the
	// display token, and renaming it would break `--summary --json` readers.
	NotApplicable int `json:"not_applicable"`
	// Unknown ← "unknown": not yet assessed, or assessed with empty axes.
	Unknown int `json:"unknown"`
	Total   int `json:"total"`
	// Maturity is the tier the adapter CLAIMS in replaydata/agents/adapters.json,
	// Earned the highest tier its core-12 standing supports (#1369). They are
	// reported side by side because the interesting rows are the ones where
	// they differ: claiming more than is earned is a `of validate` failure,
	// claiming less is a promotion nobody has taken.
	Maturity string `json:"maturity,omitempty"`
	Earned   string `json:"earned,omitempty"`
	// CoreSettled counts how many of the core twelve are settled — observed,
	// or derived dead by the capability model. It is the single number that
	// says how far this adapter is from `stable`.
	CoreSettled int `json:"core_settled"`
	CoreTotal   int `json:"core_total"`
}

// add folds one cell's display state into the right bucket.
func (a *agentSummary) add(displayState string) {
	a.Total++
	switch displayState {
	case matrix.StateObserved:
		a.Recorded++
	case matrix.StatePendingRecord:
		a.Pending++
	case matrix.StateBlockedDaemon, matrix.StateBlockedDriver:
		a.Blocked++
	case matrix.StateUnobservable:
		a.Unobservable++
	case matrix.StateNotApplicable:
		a.NotApplicable++
	default:
		// "unknown", plus any state matrix adds later. Bucketing an
		// unrecognised state here rather than dropping it is what keeps the
		// row total honest; `of status` remains the place to see the raw
		// state, and TestStatusSummaryCounts is what pins the known ones.
		a.Unknown++
	}
}

type summaryView struct {
	ExecutionProfile string         `json:"execution_profile"`
	Agents           []agentSummary `json:"agents"`
	Total            agentSummary   `json:"total"`
}

// buildSummaryView counts each agent's cells by display state. It folds the
// SAME statusView `of status` renders rather than re-reading the matrix, so
// the summary cannot disagree with the full dump it summarises — and the
// --agent / --scenario filters already applied to that view carry over for
// free.
func buildSummaryView(m *matrix.Matrix, view statusView) summaryView {
	out := summaryView{
		ExecutionProfile: string(m.ExecutionProfile()),
		Total:            agentSummary{Agent: "total"},
	}
	rows := make(map[string]*agentSummary, len(view.Agents))
	for _, a := range view.Agents {
		rows[a] = &agentSummary{Agent: a}
	}
	for _, sv := range view.Scenarios {
		for _, a := range view.Agents {
			c, ok := sv.Cells[a]
			if !ok {
				continue // no cell for this (agent, scenario) — not a state
			}
			rows[a].add(c.DisplayState)
			out.Total.add(c.DisplayState)
		}
	}
	coreTotal := len(matrix.CoreScenarios())
	for _, a := range view.Agents {
		// Maturity and core standing are properties of the ADAPTER, read
		// straight from the matrix, so unlike the counts they are unaffected
		// by a --scenario filter. Folding them out of the filtered view would
		// have `of status --scenario basic-turn --summary` report every
		// adapter as one twelfth of the way to stable.
		rows[a].Maturity = m.Capabilities().Maturity(a)
		rows[a].Earned = m.EarnedMaturity(a)
		rows[a].CoreTotal = coreTotal
		for _, s := range m.CoreStanding(a) {
			if s.Settled {
				rows[a].CoreSettled++
			}
		}
		out.Total.CoreSettled += rows[a].CoreSettled
		out.Agents = append(out.Agents, *rows[a])
	}
	out.Total.CoreTotal = coreTotal * len(view.Agents)
	return out
}

// One column-width spec for header and rows alike, so they cannot drift.
const summaryRowFormat = "%-14s %9s %8s %8s %13s %6s %8s %6s %8s %8s %6s\n"

func printSummaryText(stdout io.Writer, view summaryView) {
	fmt.Fprintf(stdout, "per-agent cell counts — %s, %d cells (profile %s)\n\n",
		plural(len(view.Agents), "agent"), view.Total.Total, view.ExecutionProfile)
	// The not-applicable column header reads the SAME schema token `of status`
	// prints per cell, so the two commands cannot drift apart again (#1367).
	fmt.Fprintf(stdout, summaryRowFormat,
		"agent", "recorded", "pending", "blocked", matrix.StateUnobservable, matrix.StateNotApplicable, matrix.StateUnknown, "total",
		"maturity", "earned", "core")
	for _, a := range view.Agents {
		printSummaryRow(stdout, a)
	}
	printSummaryRow(stdout, view.Total)
	// Without this, "recorded" reads as "has a recording" and the complement
	// reads as the remaining work. It is neither — see agentSummary.Recorded.
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "note: recorded = display state %q. A cell blocked by a daemon bug or driver gap\n", matrix.StateObserved)
	fmt.Fprintln(stdout, "counts under blocked/unobservable even if it has a recording, so total − recorded is not")
	fmt.Fprintln(stdout, "the un-recorded count. Use `of status` for per-cell detail.")
	fmt.Fprintf(stdout, "note: core = settled core-%d scenarios (observed, or derived dead by the capability\n", len(matrix.CoreScenarios()))
	fmt.Fprintln(stdout, "model). maturity is the claim in replaydata/agents/adapters.json, earned is what the core")
	fmt.Fprintln(stdout, "standing supports; `of validate` fails when a claim exceeds it. Both ignore --scenario.")
}

// dash renders an absent claim as "-" rather than as blank. A blank cell in a
// whitespace-aligned table is indistinguishable from a rendering bug, and it
// also silently changes the column count for anything parsing the row.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// plural renders "1 agent" / "2 agents".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func printSummaryRow(stdout io.Writer, a agentSummary) {
	n := strconv.Itoa
	fmt.Fprintf(stdout, summaryRowFormat, a.Agent,
		n(a.Recorded), n(a.Pending), n(a.Blocked), n(a.Unobservable), n(a.NotApplicable), n(a.Unknown), n(a.Total),
		dash(a.Maturity), dash(a.Earned), n(a.CoreSettled)+"/"+n(a.CoreTotal))
}
