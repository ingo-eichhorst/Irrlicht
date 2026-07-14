package main

import (
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// hasUnknownContributor reports whether resp's top-contributors list surfaces
// the "unknown" label, the same check TestHandleGetHistory_UnknownBucketRule
// uses for the cost chart's equivalent rule.
func hasUnknownContributor(resp historyResponse) bool {
	for _, c := range resp.TopContributors {
		if c.Label == "unknown" {
			return true
		}
	}
	return false
}

// TestBuildAgentsResponse_UnknownShareRule: chart=agents' "unknown" project
// (a session whose recordings never carried a CWD) follows the same ≥10%-
// share-or-drop rule the cost chart already uses for its own unknown bucket
// (#1046) — surfaced only when it's a meaningful share of total concurrency,
// dropped as a misleading sliver otherwise.
func TestBuildAgentsResponse_UnknownShareRule(t *testing.T) {
	// Kept: unknown peak 3 / grand 12 = 25%, well above 10%.
	kept := buildAgentsResponse("day", "", &outbound.ConcurrencyResult{
		BucketStarts: []int64{},
		ByKey:        map[string][]float64{"projA": {9}, "": {3}},
		PeakByKey:    map[string]float64{"projA": 9, "": 3},
	})
	if !hasUnknownContributor(kept) {
		t.Errorf("≥10%% unknown should be surfaced, got %+v", kept.TopContributors)
	}
	for _, c := range kept.TopContributors {
		if c.Label == "" {
			t.Errorf("raw \"\" key should never reach the response, got %+v", kept.TopContributors)
		}
	}

	// Dropped: unknown peak 1 / grand 100 = 1%, well below 10%.
	dropped := buildAgentsResponse("day", "", &outbound.ConcurrencyResult{
		BucketStarts: []int64{},
		ByKey:        map[string][]float64{"projA": {99}, "": {1}},
		PeakByKey:    map[string]float64{"projA": 99, "": 1},
	})
	if hasUnknownContributor(dropped) {
		t.Errorf("<10%% unknown should be dropped, got %+v", dropped.TopContributors)
	}
	for _, c := range dropped.TopContributors {
		if c.Label == "" {
			t.Errorf("raw \"\" key should never reach the response, got %+v", dropped.TopContributors)
		}
	}
}

// TestBuildStateResponse_UnknownShareRule is the chart=state counterpart:
// same ≥10% rule, applied to the project row list and every per-state
// sub-map in ByState.
func TestBuildStateResponse_UnknownShareRule(t *testing.T) {
	hasProject := func(projects []string, name string) bool {
		for _, p := range projects {
			if p == name {
				return true
			}
		}
		return false
	}

	// Kept: unknown total 3 / grand 12 = 25%.
	kept := buildStateResponse("24h", "", &outbound.StateSeriesResult{
		BucketStarts: []int64{},
		ByState: map[string]map[string][]float64{
			session.StateWorking: {"projA": {9}, "": {3}},
			session.StateWaiting: {},
			session.StateReady:   {},
		},
	})
	if !hasProject(kept.Projects, "unknown") {
		t.Errorf("≥10%% unknown should be surfaced as a project row, got %+v", kept.Projects)
	}
	if hasProject(kept.Projects, "") {
		t.Errorf("raw \"\" should never reach Projects, got %+v", kept.Projects)
	}
	if _, ok := kept.ByState[session.StateWorking]["unknown"]; !ok {
		t.Errorf("ByState[working] should carry the relabeled \"unknown\" key, got %+v", kept.ByState[session.StateWorking])
	}
	if _, ok := kept.ByState[session.StateWorking][""]; ok {
		t.Errorf("ByState[working] should not carry the raw \"\" key once resolved, got %+v", kept.ByState[session.StateWorking])
	}

	// Dropped: unknown total 1 / grand 100 = 1%.
	dropped := buildStateResponse("24h", "", &outbound.StateSeriesResult{
		BucketStarts: []int64{},
		ByState: map[string]map[string][]float64{
			session.StateWorking: {"projA": {99}, "": {1}},
			session.StateWaiting: {},
			session.StateReady:   {},
		},
	})
	if hasProject(dropped.Projects, "unknown") || hasProject(dropped.Projects, "") {
		t.Errorf("<10%% unknown should be dropped entirely, got %+v", dropped.Projects)
	}
	if _, ok := dropped.ByState[session.StateWorking]["unknown"]; ok {
		t.Errorf("dropped unknown should not appear in ByState, got %+v", dropped.ByState[session.StateWorking])
	}
	if _, ok := dropped.ByState[session.StateWorking][""]; ok {
		t.Errorf("dropped unknown should not leave the raw \"\" key in ByState either, got %+v", dropped.ByState[session.StateWorking])
	}
}

// TestBuildStateResponse_Top8Cap: chart=state's project rows are capped to
// the 8 busiest, ranked by total activity across every state/bucket — years
// of one-off stale projects (e.g. long-removed ir:exec worktree dirs) must
// not blow out the row list (#1046).
func TestBuildStateResponse_Top8Cap(t *testing.T) {
	byProject := map[string][]float64{}
	for i := 1; i <= 10; i++ {
		byProject[projectName(i)] = []float64{float64(11 - i)} // project1=10 ... project10=1
	}
	resp := buildStateResponse("1y", "", &outbound.StateSeriesResult{
		BucketStarts: []int64{},
		ByState: map[string]map[string][]float64{
			session.StateWorking: byProject,
			session.StateWaiting: {},
			session.StateReady:   {},
		},
	})

	if len(resp.Projects) != 8 {
		t.Fatalf("want exactly 8 projects, got %d: %+v", len(resp.Projects), resp.Projects)
	}
	for i := 1; i <= 8; i++ {
		if resp.Projects[i-1] != projectName(i) {
			t.Errorf("projects[%d]: want %s (busiest-first), got %s", i-1, projectName(i), resp.Projects[i-1])
		}
	}
	for i := 9; i <= 10; i++ {
		if _, ok := resp.ByState[session.StateWorking][projectName(i)]; ok {
			t.Errorf("ByState[working] should be pruned to the top 8, still has %s", projectName(i))
		}
	}
}

func projectName(i int) string {
	return "project" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}
