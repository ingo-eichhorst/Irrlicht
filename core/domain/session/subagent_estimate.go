// subagent_estimate.go derives a parent session's displayed task estimate
// from its child sessions' estimates (issue #622). Orchestration sessions
// are where an ETA matters most and where the parent's own signal is
// weakest: subagents emit markers into their own transcripts and each child
// session's metrics already carry an estimate, but children collapse into a
// badge on the parent row (#593), so those estimates were computed and never
// displayed.
package session

import "time"

// SubagentEstimateSource tags estimates aggregated from child sessions.
// Sibling of the "marker" and "tasks" sources set by the metrics adapter.
const SubagentEstimateSource = "subagents"

// ApplySubagentTaskEstimate fills or refreshes the parent's displayed task
// estimate from its working children. The parent's own estimate (marker- or
// tasks-sourced) wins while fresh per FresherTaskEstimate; the subagent
// aggregate takes over when the parent has none or its own went stale while
// children kept reporting. Mutates parent.Metrics in place — the same
// enrichment contract as InheritRateLimits and the Subagents summary. The
// tailer ledger owns the agent's own estimate, so an overwrite here is
// rebuilt from it on the parent's next ComputeMetrics pass.
func ApplySubagentTaskEstimate(parent *SessionState, children []*SessionState, now time.Time) {
	if parent == nil || parent.Metrics == nil {
		return
	}
	agg, aggEta := aggregateSubagentEstimate(parent, children)
	own := parent.Metrics.TaskEstimate
	if own != nil && own.Source == SubagentEstimateSource {
		// A previously-applied aggregate is not the agent's own signal —
		// recompute from live children instead of comparing against it.
		own = nil
	}
	if agg == nil {
		if own == nil && parent.Metrics.TaskEstimate != nil {
			// Lingering aggregate with no eligible children left: clear it.
			// The next ComputeMetrics pass restores the agent's own estimate.
			parent.Metrics.TaskEstimate = nil
			parent.Metrics.TaskCompletionEta = nil
		}
		return
	}
	if FresherTaskEstimate(own, agg, now) == agg {
		parent.Metrics.TaskEstimate = agg
		parent.Metrics.TaskCompletionEta = aggEta
	}
}

// aggregateSubagentEstimate sums the estimates of the parent's WORKING
// children: completed/total rounds are summed, UpdatedAt is the freshest
// child stamp, and the eta is the latest child TaskCompletionEta (the wave
// ends when its slowest member does). Children that finished drop out — the
// aggregate describes the open wave, not the parent's full plan; the
// tooltip's source attribution makes that scope visible. Returns (nil, nil)
// when no working child carries an estimate.
func aggregateSubagentEstimate(parent *SessionState, children []*SessionState) (*TaskEstimate, *int64) {
	var est *TaskEstimate
	var maxEta *int64
	for _, c := range children {
		if c == nil || c.ParentSessionID != parent.SessionID || c.State != StateWorking {
			continue
		}
		if c.Metrics == nil || c.Metrics.TaskEstimate == nil {
			continue
		}
		ce := c.Metrics.TaskEstimate
		if est == nil {
			est = &TaskEstimate{Source: SubagentEstimateSource}
		}
		est.TotalRounds += ce.TotalRounds
		est.CompletedRounds += ce.CompletedRounds
		if ce.UpdatedAt > est.UpdatedAt {
			est.UpdatedAt = ce.UpdatedAt
		}
		if eta := c.Metrics.TaskCompletionEta; eta != nil && (maxEta == nil || *eta > *maxEta) {
			v := *eta
			maxEta = &v
		}
	}
	return est, maxEta
}
