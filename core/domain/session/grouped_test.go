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

	groups := BuildDashboard(sessions, nil)

	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}

	g0 := groups[0]
	if g0.Name != "proj-a" {
		t.Errorf("group 0 name = %q, want proj-a", g0.Name)
	}
	if len(g0.Agents) != 2 {
		t.Errorf("group 0 agents = %d, want 2", len(g0.Agents))
	}

	g1 := groups[1]
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
		Root:    "/gt",
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
	}

	groups := BuildDashboard(sessions, orch)

	// Two top-level groups: orchestrator ("gt") and regular ("my-app").
	if len(groups) != 2 {
		t.Fatalf("got %d top-level groups, want 2", len(groups))
	}

	// Orchestrator group.
	orchGroup := groups[0]
	if orchGroup.Name != "gt" {
		t.Errorf("orch group name = %q, want gt", orchGroup.Name)
	}
	if orchGroup.Type != "gastown" {
		t.Errorf("orch group type = %q, want gastown", orchGroup.Type)
	}

	// Global agents (mayor) on the orchestrator group.
	if len(orchGroup.Agents) != 1 {
		t.Fatalf("orch agents = %d, want 1", len(orchGroup.Agents))
	}
	if orchGroup.Agents[0].Role != "mayor" {
		t.Errorf("orch agent role = %q, want mayor", orchGroup.Agents[0].Role)
	}

	// Sub-groups (rigs).
	if len(orchGroup.Groups) != 1 {
		t.Fatalf("orch sub-groups = %d, want 1", len(orchGroup.Groups))
	}
	rigGroup := orchGroup.Groups[0]
	if rigGroup.Name != "irrlicht" {
		t.Errorf("rig name = %q, want irrlicht", rigGroup.Name)
	}
	if rigGroup.Status != "operational" {
		t.Errorf("rig status = %q, want operational", rigGroup.Status)
	}
	if len(rigGroup.Agents) != 2 {
		t.Fatalf("rig agents = %d, want 2", len(rigGroup.Agents))
	}
	if rigGroup.Agents[0].Role != "witness" {
		t.Errorf("rig agent 0 role = %q, want witness", rigGroup.Agents[0].Role)
	}
	pole := rigGroup.Agents[1]
	if pole.Role != "polecat" || pole.WorkerName != "fix-42" || pole.WorkerID != "GH-42" {
		t.Errorf("polecat = role=%q name=%q id=%q", pole.Role, pole.WorkerName, pole.WorkerID)
	}

	// Regular group.
	app := groups[1]
	if app.Name != "my-app" {
		t.Errorf("regular group name = %q, want my-app", app.Name)
	}
	if app.Type != "" {
		t.Errorf("regular group type = %q, want empty", app.Type)
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

	groups := BuildDashboard(sessions, nil)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if len(groups[0].Agents) != 1 {
		t.Fatalf("got %d agents, want 1 (parent only)", len(groups[0].Agents))
	}

	parent := groups[0].Agents[0]
	if len(parent.Children) != 2 {
		t.Fatalf("parent children = %d, want 2", len(parent.Children))
	}

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

	groups := BuildDashboard(sessions, nil)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if len(groups[0].Agents) != 1 {
		t.Fatalf("orphan should surface as top-level agent")
	}
	if groups[0].Agents[0].SessionID != "orphan" {
		t.Error("wrong agent")
	}
}

func TestBuildDashboard_SubagentsUnification(t *testing.T) {
	sessions := []*SessionState{
		{
			SessionID: "parent", State: StateWorking, ProjectName: "proj",
			Metrics: &SessionMetrics{OpenSubagents: 2},
		},
		{SessionID: "child1", State: StateWorking, ParentSessionID: "parent"},
	}

	groups := BuildDashboard(sessions, nil)

	parent := groups[0].Agents[0]
	if parent.Subagents == nil {
		t.Fatal("expected subagents")
	}
	if parent.Subagents.Total != 3 || parent.Subagents.Working != 3 {
		t.Errorf("subagents = %+v, want total=3 working=3", *parent.Subagents)
	}
}

func TestComputeSubagentSummary(t *testing.T) {
	t.Run("no subagents returns nil", func(t *testing.T) {
		parent := &SessionState{SessionID: "p", Metrics: &SessionMetrics{}}
		if got := ComputeSubagentSummary(parent, nil); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
	t.Run("only in-process", func(t *testing.T) {
		parent := &SessionState{SessionID: "p", Metrics: &SessionMetrics{OpenSubagents: 3}}
		got := ComputeSubagentSummary(parent, nil)
		if got == nil || got.Total != 3 || got.Working != 3 {
			t.Errorf("got %+v, want total=3 working=3", got)
		}
	})
	t.Run("only file-based with mixed states", func(t *testing.T) {
		parent := &SessionState{SessionID: "p"}
		children := []*SessionState{
			{SessionID: "c1", State: StateWorking, ParentSessionID: "p"},
			{SessionID: "c2", State: StateWaiting, ParentSessionID: "p"},
			{SessionID: "c3", State: StateReady, ParentSessionID: "p"},
			{SessionID: "unrelated", State: StateWorking, ParentSessionID: "other"},
		}
		got := ComputeSubagentSummary(parent, children)
		if got == nil {
			t.Fatal("got nil, want summary")
		}
		if got.Total != 3 || got.Working != 1 || got.Waiting != 1 || got.Ready != 1 {
			t.Errorf("got %+v, want total=3 working=1 waiting=1 ready=1", got)
		}
	})
	t.Run("merges in-process and file-based", func(t *testing.T) {
		parent := &SessionState{SessionID: "p", Metrics: &SessionMetrics{OpenSubagents: 2}}
		children := []*SessionState{
			{SessionID: "c1", State: StateWorking, ParentSessionID: "p"},
			{SessionID: "c2", State: StateWaiting, ParentSessionID: "p"},
		}
		got := ComputeSubagentSummary(parent, children)
		if got == nil {
			t.Fatal("got nil, want summary")
		}
		if got.Total != 4 || got.Working != 3 || got.Waiting != 1 {
			t.Errorf("got %+v, want total=4 working=3 waiting=1", got)
		}
	})
}

func TestBuildDashboard_Empty(t *testing.T) {
	groups := BuildDashboard(nil, nil)
	if len(groups) != 0 {
		t.Errorf("got %d groups, want 0", len(groups))
	}
}

func TestBuildDashboard_OrchestratorNotRunning(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "a", State: StateWorking, ProjectName: "proj"},
	}
	orch := &orchestrator.State{Adapter: "gastown", Running: false}

	groups := BuildDashboard(sessions, orch)

	if len(groups) != 1 || groups[0].Name != "proj" {
		t.Errorf("groups = %+v", groups)
	}
}

func TestBuildDashboard_RecursiveChildren(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "grandparent", State: StateWorking, ProjectName: "proj"},
		{SessionID: "parent", State: StateWorking, ParentSessionID: "grandparent"},
		{SessionID: "child", State: StateReady, ParentSessionID: "parent"},
	}

	groups := BuildDashboard(sessions, nil)

	gp := groups[0].Agents[0]
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
