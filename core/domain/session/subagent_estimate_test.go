package session

import (
	"testing"
	"time"
)

// estChild builds a working child of parentID carrying the given estimate/eta.
func estChild(parentID string, state string, est *TaskEstimate, eta *int64) *SessionState {
	return &SessionState{
		SessionID:       parentID + "-child",
		ParentSessionID: parentID,
		State:           state,
		Metrics:         &SessionMetrics{TaskEstimate: est, TaskCompletionEta: eta},
	}
}

func asUnix(t time.Time) int64 { return t.Unix() }

func TestApplySubagentTaskEstimate_FillsParentGap(t *testing.T) {
	// Parent has no own estimate; two working children report 2/6 and 1/3 →
	// aggregate 3/9, source "subagents", eta = the later child eta.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	eta1, eta2 := asUnix(now.Add(2*time.Minute)), asUnix(now.Add(5*time.Minute))
	parent := &SessionState{SessionID: "p", State: StateWorking, Metrics: &SessionMetrics{}}
	children := []*SessionState{
		estChild("p", StateWorking, &TaskEstimate{TotalRounds: 6, CompletedRounds: 2, UpdatedAt: asUnix(now.Add(-30 * time.Second)), Source: "marker"}, &eta1),
		estChild("p", StateWorking, &TaskEstimate{TotalRounds: 3, CompletedRounds: 1, UpdatedAt: asUnix(now.Add(-10 * time.Second)), Source: "marker"}, &eta2),
	}

	ApplySubagentTaskEstimate(parent, children, now)

	est := parent.Metrics.TaskEstimate
	if est == nil || est.Source != SubagentEstimateSource {
		t.Fatalf("TaskEstimate = %+v, want subagents-sourced aggregate", est)
	}
	if est.TotalRounds != 9 || est.CompletedRounds != 3 {
		t.Errorf("rounds = %d/%d, want 3/9", est.CompletedRounds, est.TotalRounds)
	}
	if est.UpdatedAt != asUnix(now.Add(-10*time.Second)) {
		t.Errorf("UpdatedAt = %d, want freshest child stamp", est.UpdatedAt)
	}
	if parent.Metrics.TaskCompletionEta == nil || *parent.Metrics.TaskCompletionEta != eta2 {
		t.Errorf("eta = %v, want max child eta %d", parent.Metrics.TaskCompletionEta, eta2)
	}
}

func TestApplySubagentTaskEstimate_FreshOwnWins(t *testing.T) {
	// The parent's own marker is fresh — the aggregate must not displace it,
	// and the adapter-computed eta stays untouched.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	ownEta := asUnix(now.Add(10 * time.Minute))
	own := &TaskEstimate{TotalRounds: 8, CompletedRounds: 5, UpdatedAt: asUnix(now.Add(-60 * time.Second)), Source: "marker"}
	parent := &SessionState{SessionID: "p", State: StateWorking,
		Metrics: &SessionMetrics{TaskEstimate: own, TaskCompletionEta: &ownEta}}
	childEta := asUnix(now.Add(1 * time.Minute))
	children := []*SessionState{
		estChild("p", StateWorking, &TaskEstimate{TotalRounds: 4, CompletedRounds: 4, UpdatedAt: asUnix(now), Source: "marker"}, &childEta),
	}

	ApplySubagentTaskEstimate(parent, children, now)

	if parent.Metrics.TaskEstimate != own {
		t.Errorf("TaskEstimate = %+v, want untouched own marker", parent.Metrics.TaskEstimate)
	}
	if *parent.Metrics.TaskCompletionEta != ownEta {
		t.Errorf("eta = %d, want untouched %d", *parent.Metrics.TaskCompletionEta, ownEta)
	}
}

func TestApplySubagentTaskEstimate_StaleOwnHandsOver(t *testing.T) {
	// The parent's marker went stale (>TaskEstimateGraceAge) while children
	// kept reporting — the fresher aggregate takes over.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	own := &TaskEstimate{TotalRounds: 8, CompletedRounds: 2, UpdatedAt: asUnix(now.Add(-10 * time.Minute)), Source: "marker"}
	parent := &SessionState{SessionID: "p", State: StateWorking,
		Metrics: &SessionMetrics{TaskEstimate: own}}
	children := []*SessionState{
		estChild("p", StateWorking, &TaskEstimate{TotalRounds: 6, CompletedRounds: 3, UpdatedAt: asUnix(now.Add(-20 * time.Second)), Source: "marker"}, nil),
	}

	ApplySubagentTaskEstimate(parent, children, now)

	est := parent.Metrics.TaskEstimate
	if est == nil || est.Source != SubagentEstimateSource {
		t.Fatalf("TaskEstimate = %+v, want subagents takeover from stale own", est)
	}
	if est.TotalRounds != 6 || est.CompletedRounds != 3 {
		t.Errorf("rounds = %d/%d, want 3/6", est.CompletedRounds, est.TotalRounds)
	}
	if parent.Metrics.TaskCompletionEta != nil {
		t.Errorf("eta = %v, want nil (no child eta)", parent.Metrics.TaskCompletionEta)
	}
}

func TestApplySubagentTaskEstimate_ClearsLingeringAggregate(t *testing.T) {
	// A previously-applied aggregate with no eligible children left must be
	// cleared, not displayed forever (children finished → wave over).
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	staleEta := asUnix(now.Add(-1 * time.Minute))
	parent := &SessionState{SessionID: "p", State: StateWorking,
		Metrics: &SessionMetrics{
			TaskEstimate:      &TaskEstimate{TotalRounds: 9, CompletedRounds: 3, Source: SubagentEstimateSource},
			TaskCompletionEta: &staleEta,
		}}
	children := []*SessionState{
		estChild("p", StateReady, &TaskEstimate{TotalRounds: 6, CompletedRounds: 6, Source: "marker"}, nil),
	}

	ApplySubagentTaskEstimate(parent, children, now)

	if parent.Metrics.TaskEstimate != nil || parent.Metrics.TaskCompletionEta != nil {
		t.Errorf("estimate = %+v eta = %v, want cleared", parent.Metrics.TaskEstimate, parent.Metrics.TaskCompletionEta)
	}
}

func TestApplySubagentTaskEstimate_IgnoresIneligibleChildren(t *testing.T) {
	// Non-working children, other parents' children, and children without
	// estimates contribute nothing; with no eligible child and no prior
	// aggregate the parent stays untouched.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	parent := &SessionState{SessionID: "p", State: StateWorking, Metrics: &SessionMetrics{}}
	children := []*SessionState{
		estChild("p", StateReady, &TaskEstimate{TotalRounds: 4, CompletedRounds: 4, Source: "marker"}, nil),
		estChild("other", StateWorking, &TaskEstimate{TotalRounds: 4, CompletedRounds: 1, Source: "marker"}, nil),
		estChild("p", StateWorking, nil, nil),
		nil,
	}

	ApplySubagentTaskEstimate(parent, children, now)

	if parent.Metrics.TaskEstimate != nil {
		t.Errorf("TaskEstimate = %+v, want nil", parent.Metrics.TaskEstimate)
	}
}

func TestApplySubagentTaskEstimate_NilParentOrMetricsNoop(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	ApplySubagentTaskEstimate(nil, nil, now)
	ApplySubagentTaskEstimate(&SessionState{SessionID: "p"}, nil, now) // Metrics nil
}
