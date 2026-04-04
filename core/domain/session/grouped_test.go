package session

import (
	"irrlicht/core/domain/orchestrator"
	"testing"
)

func TestBuildDashboard_NoOrchestrator(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "a1", State: StateWorking, ProjectName: "proj-a"},
		{SessionID: "a2", State: StateReady, ProjectName: "proj-a"},
		{SessionID: "b1", State: StateWaiting, ProjectName: "proj-b"},
	}

	resp := BuildDashboard(sessions, nil)

	if resp.Orchestrator != nil {
		t.Fatal("expected nil orchestrator")
	}
	if len(resp.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(resp.Groups))
	}

	// Groups should be in input order (proj-a first).
	g0 := resp.Groups[0]
	if g0.Name != "proj-a" {
		t.Errorf("group 0 name = %q, want proj-a", g0.Name)
	}
	if len(g0.Agents) != 2 {
		t.Errorf("group 0 agents = %d, want 2", len(g0.Agents))
	}

	g1 := resp.Groups[1]
	if g1.Name != "proj-b" {
		t.Errorf("group 1 name = %q, want proj-b", g1.Name)
	}
	if len(g1.Agents) != 1 {
		t.Errorf("group 1 agents = %d, want 1", len(g1.Agents))
	}
}

func TestBuildDashboard_WithOrchestrator(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "wit-1", State: StateWorking, ProjectName: "witness"},
		{SessionID: "pole-1", State: StateReady, ProjectName: "fix-42"},
		{SessionID: "mayor-1", State: StateWorking, ProjectName: "mayor"},
		{SessionID: "other-1", State: StateWorking, ProjectName: "my-app"},
	}

	orch := &orchestrator.State{
		Adapter: "gastown",
		Running: true,
		GlobalAgents: []orchestrator.GlobalAgent{
			{Role: "mayor", SessionID: "mayor-1", State: "working"},
		},
		Codebases: []orchestrator.Codebase{
			{
				Name:   "irrlicht",
				Status: "operational",
				Worktrees: []orchestrator.Worktree{
					{
						Path:   "/gt/irrlicht",
						IsMain: true,
						Workers: []orchestrator.Worker{
							{Role: "witness", SessionID: "wit-1", State: "working"},
						},
					},
					{
						Path:   "/gt/irrlicht/polecats/fix-42",
						IsMain: false,
						Workers: []orchestrator.Worker{
							{Role: "polecat", Name: "fix-42", ID: "GH-42", SessionID: "pole-1", State: "ready"},
						},
					},
				},
			},
		},
		WorkUnits: []orchestrator.WorkUnit{
			{ID: "c1", Type: "convoy", Name: "Feature X", Source: "gastown", Total: 5, Done: 3},
		},
	}

	resp := BuildDashboard(sessions, orch)

	// Orchestrator summary.
	if resp.Orchestrator == nil {
		t.Fatal("expected orchestrator summary")
	}
	if resp.Orchestrator.Adapter != "gastown" {
		t.Errorf("adapter = %q, want gastown", resp.Orchestrator.Adapter)
	}
	if len(resp.Orchestrator.WorkUnits) != 1 {
		t.Errorf("work units = %d, want 1", len(resp.Orchestrator.WorkUnits))
	}

	// Groups: irrlicht (witness + polecat), mayor, my-app.
	if len(resp.Groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(resp.Groups))
	}

	// Find groups by name.
	groupByName := map[string]*AgentGroup{}
	for _, g := range resp.Groups {
		groupByName[g.Name] = g
	}

	// irrlicht group: witness + polecat, status from codebase.
	rig := groupByName["irrlicht"]
	if rig == nil {
		t.Fatal("missing irrlicht group")
	}
	if rig.Status != "operational" {
		t.Errorf("irrlicht status = %q, want operational", rig.Status)
	}
	if len(rig.Agents) != 2 {
		t.Fatalf("irrlicht agents = %d, want 2", len(rig.Agents))
	}

	wit := rig.Agents[0]
	if wit.Role != "witness" {
		t.Errorf("agent 0 role = %q, want witness", wit.Role)
	}

	pole := rig.Agents[1]
	if pole.Role != "polecat" || pole.WorkerName != "fix-42" || pole.WorkerID != "GH-42" {
		t.Errorf("polecat = role=%q name=%q id=%q", pole.Role, pole.WorkerName, pole.WorkerID)
	}

	// mayor group: global agent, no rig status.
	mayorGroup := groupByName["mayor"]
	if mayorGroup == nil {
		t.Fatal("missing mayor group")
	}
	if len(mayorGroup.Agents) != 1 {
		t.Fatalf("mayor agents = %d, want 1", len(mayorGroup.Agents))
	}
	if mayorGroup.Agents[0].Role != "mayor" {
		t.Errorf("mayor role = %q", mayorGroup.Agents[0].Role)
	}

	// my-app group: regular session, no role.
	app := groupByName["my-app"]
	if app == nil {
		t.Fatal("missing my-app group")
	}
	if app.Agents[0].Role != "" {
		t.Errorf("regular session has role = %q", app.Agents[0].Role)
	}
}

func TestBuildDashboard_ChildrenNesting(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "parent", State: StateWorking, ProjectName: "proj"},
		{SessionID: "child1", State: StateWorking, ParentSessionID: "parent"},
		{SessionID: "child2", State: StateReady, ParentSessionID: "parent"},
	}

	resp := BuildDashboard(sessions, nil)

	if len(resp.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(resp.Groups))
	}
	if len(resp.Groups[0].Agents) != 1 {
		t.Fatalf("got %d agents, want 1 (parent only)", len(resp.Groups[0].Agents))
	}

	parent := resp.Groups[0].Agents[0]
	if len(parent.Children) != 2 {
		t.Fatalf("parent children = %d, want 2", len(parent.Children))
	}

	// Subagents summary should reflect children.
	if parent.Subagents == nil {
		t.Fatal("expected subagents summary")
	}
	if parent.Subagents.Total != 2 || parent.Subagents.Working != 1 || parent.Subagents.Ready != 1 {
		t.Errorf("subagents = %+v, want total=2 working=1 ready=1", *parent.Subagents)
	}
}

func TestBuildDashboard_OrphanChildren(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "orphan", State: StateWaiting, ParentSessionID: "missing", ProjectName: "proj"},
	}

	resp := BuildDashboard(sessions, nil)

	if len(resp.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(resp.Groups))
	}
	if len(resp.Groups[0].Agents) != 1 {
		t.Fatalf("orphan should surface as top-level agent")
	}
	if resp.Groups[0].Agents[0].SessionID != "orphan" {
		t.Error("wrong agent")
	}
}

func TestBuildDashboard_SubagentsUnification(t *testing.T) {
	sessions := []*SessionState{
		{
			SessionID: "parent", State: StateWorking, ProjectName: "proj",
			Subagents: &SubagentSummary{Total: 2, Working: 2}, // in-process agents
		},
		{SessionID: "child1", State: StateWorking, ParentSessionID: "parent"},
	}

	resp := BuildDashboard(sessions, nil)

	parent := resp.Groups[0].Agents[0]
	if parent.Subagents == nil {
		t.Fatal("expected subagents")
	}
	// 2 in-process + 1 file-based (working) = 3 total, 3 working.
	if parent.Subagents.Total != 3 || parent.Subagents.Working != 3 {
		t.Errorf("subagents = %+v, want total=3 working=3", *parent.Subagents)
	}
}

func TestBuildDashboard_Empty(t *testing.T) {
	resp := BuildDashboard(nil, nil)
	if resp.Orchestrator != nil {
		t.Error("expected nil orchestrator")
	}
	if len(resp.Groups) != 0 {
		t.Errorf("got %d groups, want 0", len(resp.Groups))
	}
}

func TestBuildDashboard_OrchestratorNotRunning(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "a", State: StateWorking, ProjectName: "proj"},
	}
	orch := &orchestrator.State{Adapter: "gastown", Running: false}

	resp := BuildDashboard(sessions, orch)

	if resp.Orchestrator != nil {
		t.Error("expected nil orchestrator when not running")
	}
	// Session should still be grouped by project name.
	if len(resp.Groups) != 1 || resp.Groups[0].Name != "proj" {
		t.Errorf("groups = %+v", resp.Groups)
	}
}

func TestBuildDashboard_RecursiveChildren(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "grandparent", State: StateWorking, ProjectName: "proj"},
		{SessionID: "parent", State: StateWorking, ParentSessionID: "grandparent"},
		{SessionID: "child", State: StateReady, ParentSessionID: "parent"},
	}

	resp := BuildDashboard(sessions, nil)

	gp := resp.Groups[0].Agents[0]
	if len(gp.Children) != 1 {
		t.Fatalf("grandparent children = %d, want 1", len(gp.Children))
	}
	p := gp.Children[0]
	if len(p.Children) != 1 {
		t.Fatalf("parent children = %d, want 1", len(p.Children))
	}
	if p.Children[0].SessionID != "child" {
		t.Error("wrong grandchild")
	}
}
