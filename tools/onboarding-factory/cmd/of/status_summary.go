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
	Agents []agentSummary `json:"agents"`
	Total  agentSummary   `json:"total"`
}

// buildSummaryView counts each agent's cells by display state. It folds the
// SAME statusView `of status` renders rather than re-reading the matrix, so
// the summary cannot disagree with the full dump it summarises — and the
// --agent / --scenario filters already applied to that view carry over for
// free.
func buildSummaryView(view statusView) summaryView {
	out := summaryView{Total: agentSummary{Agent: "total"}}
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
	for _, a := range view.Agents {
		out.Agents = append(out.Agents, *rows[a])
	}
	return out
}

// One column-width spec for header and rows alike, so they cannot drift.
const summaryRowFormat = "%-14s %9s %8s %8s %13s %6s %8s %6s\n"

func printSummaryText(stdout io.Writer, view summaryView) {
	fmt.Fprintf(stdout, "per-agent cell counts — %s, %d cells\n\n",
		plural(len(view.Agents), "agent"), view.Total.Total)
	// The not-applicable column header reads the SAME schema token `of status`
	// prints per cell, so the two commands cannot drift apart again (#1367).
	fmt.Fprintf(stdout, summaryRowFormat,
		"agent", "recorded", "pending", "blocked", matrix.StateUnobservable, matrix.StateNotApplicable, matrix.StateUnknown, "total")
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
		n(a.Recorded), n(a.Pending), n(a.Blocked), n(a.Unobservable), n(a.NotApplicable), n(a.Unknown), n(a.Total))
}
