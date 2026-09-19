package main

import (
	"testing"

	"irrlicht/core/domain/session"
)

func gtAgent(project string) *session.Agent {
	return &session.Agent{SessionState: &session.SessionState{ProjectName: project}}
}

// TestAttachGroupCosts_GastownSumsDistinctProjects verifies the orchestrator
// group's cost is the sum of the distinct project costs across its nested rig
// sessions (a shared project counted once), while a regular project group
// keeps its single-project cost.
func TestAttachGroupCosts_GastownSumsDistinctProjects(t *testing.T) {
	gastown := &session.AgentGroup{
		Name: "Gas Town",
		Type: "gastown",
		// Global agent on proj-a (also appears in a rig below → dedup).
		Agents: []*session.Agent{gtAgent("proj-a")},
		Groups: []*session.AgentGroup{
			{Name: "rig-1", Agents: []*session.Agent{gtAgent("proj-a"), gtAgent("proj-b")}},
			{Name: "rig-2", Agents: []*session.Agent{gtAgent("proj-b")}},
		},
	}
	regular := &session.AgentGroup{Name: "proj-c", Agents: []*session.Agent{gtAgent("proj-c")}}

	byTf := map[string]map[string]float64{
		"day":  {"proj-a": 1.00, "proj-b": 0.50, "proj-c": 9.00},
		"week": {"proj-a": 2.00, "proj-b": 0.50, "proj-c": 9.00},
	}

	attachGroupCosts([]*session.AgentGroup{gastown, regular}, byTf)

	// proj-a (1.00) + proj-b (0.50) counted once each; proj-c excluded.
	if got := gastown.Costs["day"]; got != 1.50 {
		t.Errorf("gastown.Costs[day]: want 1.50, got %v", got)
	}
	if got := gastown.Costs["week"]; got != 2.50 {
		t.Errorf("gastown.Costs[week]: want 2.50, got %v", got)
	}
	// Regular group unchanged: its single project's cost.
	if got := regular.Costs["day"]; got != 9.00 {
		t.Errorf("proj-c.Costs[day]: want 9.00, got %v", got)
	}
}

// TestProviderCostsByProvider_UnattributedBucketPreservesTotal is #1996's
// red-first proof: a cost row with no confirmed billing attribution must
// still surface in provider_costs — under the reserved "unattributed" key
// rather than being dropped — so the per-provider total agrees with what the
// project view already counts. Asserted against the literal wire string
// "unattributed" (the decided reserved key), not a shared constant, so this
// test also catches a future accidental rename of that constant.
//
// Before the fix, providerCostsByProvider's `if provider == "" { continue }`
// (handlers.go) drops the empty-provider bucket outright: the two sums
// below disagree and the "unattributed" key is absent. After, they agree.
func TestProviderCostsByProvider_UnattributedBucketPreservesTotal(t *testing.T) {
	byTf := map[string]map[string]float64{
		"day": {
			"anthropic": 1.00,
			"":          4.00, // no confirmed billing attribution
		},
	}
	// byProject (attachGroupCosts, driven by a separate project-keyed map)
	// counts every row regardless of provider — this is the ground truth
	// the per-provider view must agree with. Simulated directly here since
	// providerCostsByProvider only ever sees the provider-keyed map.
	const wantProjectTotalDay = 5.00

	got := providerCostsByProvider(byTf)

	var gotProviderTotalDay float64
	for _, byTf2 := range got {
		gotProviderTotalDay += byTf2["day"]
	}
	if gotProviderTotalDay != wantProjectTotalDay {
		t.Errorf("provider_costs day total: want %v (must equal the project total), got %v from %+v",
			wantProjectTotalDay, gotProviderTotalDay, got)
	}
	unattributed, ok := got["unattributed"]
	if !ok {
		t.Fatalf(`provider_costs missing the reserved "unattributed" key: %+v`, got)
	}
	if unattributed["day"] != 4.00 {
		t.Errorf(`provider_costs["unattributed"]["day"]: want 4.00, got %v`, unattributed["day"])
	}
	if _, hasRawEmpty := got[""]; hasRawEmpty {
		t.Errorf(`provider_costs must not carry a raw "" key, got %+v`, got)
	}
}
