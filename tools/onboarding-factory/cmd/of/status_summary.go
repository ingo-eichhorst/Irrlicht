package main

import (
	"fmt"
	"io"
)

// agentSummary is one agent's cell counts, bucketed by the matrix display
// state. Every value matrix.DeriveDisplayState can return maps to exactly one
// bucket, so the buckets always sum to Total — see buckets() and the
// total-preserving test. That property is the point: a summary that quietly
// drops a state under-reports how much work is left, which is worse than no
// summary at all.
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
	// NotApplicable ← "n.a.": out of scope for this agent.
	NotApplicable int `json:"not_applicable"`
	// Unknown ← "unknown": not yet assessed, or assessed with empty axes.
	Unknown int `json:"unknown"`
	Total   int `json:"total"`
}

// buckets returns the sum of every bucket. It must equal Total; a mismatch
// means matrix gained a display state this summary does not know about.
func (a agentSummary) buckets() int {
	return a.Recorded + a.Pending + a.Blocked + a.Unobservable + a.NotApplicable + a.Unknown
}

// add folds one cell's display state into the right bucket.
func (a *agentSummary) add(displayState string) {
	a.Total++
	switch displayState {
	case "observed":
		a.Recorded++
	case "pending-record":
		a.Pending++
	case "blocked-daemon", "blocked-driver":
		a.Blocked++
	case "unobservable":
		a.Unobservable++
	case "n.a.":
		a.NotApplicable++
	default:
		// "unknown" and anything matrix adds later. Counting an unrecognised
		// state as unknown keeps the buckets total-preserving instead of
		// silently discarding the cell; the test that sums them then still
		// passes, and `of status` remains the place to see the raw state.
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

const summaryRowFormat = "%-14s %9d %8d %8d %13d %6d %8d %6d\n"

func printSummaryText(stdout io.Writer, view summaryView) {
	fmt.Fprintf(stdout, "per-agent cell counts — %s, %d cells\n\n",
		plural(len(view.Agents), "agent"), view.Total.Total)
	fmt.Fprintf(stdout, "%-14s %9s %8s %8s %13s %6s %8s %6s\n",
		"agent", "recorded", "pending", "blocked", "unobservable", "n/a", "unknown", "total")
	for _, a := range view.Agents {
		printSummaryRow(stdout, a)
	}
	printSummaryRow(stdout, view.Total)
	// Without this, "recorded" reads as "has a recording" and the complement
	// reads as the remaining work. It is neither — see agentSummary.Recorded.
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "note: recorded = display state \"observed\". A cell blocked by a daemon bug or driver gap")
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
	fmt.Fprintf(stdout, summaryRowFormat,
		a.Agent, a.Recorded, a.Pending, a.Blocked, a.Unobservable, a.NotApplicable, a.Unknown, a.Total)
}
