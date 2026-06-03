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
