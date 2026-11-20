package session

import (
	"encoding/json"
	"slices"
	"testing"

	"irrlicht/core/domain/orchestrator"
)

// Issue #2140: a named orchestrator worker that no session is attached to (a
// free Gas Town polecat, an idle pool slot) gets a row of its own under its
// rig, in `unattached_workers` — never in `agents`, because it is not a
// session and has no session state.
//
// Every assertion here reads the MARSHALLED payload rather than a Go field, so
// the file compiled against the tree before the field existed and its red run
// there is the evidence (go test ./core/domain/session/ -run
// TestBuildDashboard_Unattached -count=1 on 452333613).

// wireGroups marshals groups the way handleGetSessions does and decodes them
// back into plain maps, so tests assert on the JSON a client receives.
func wireGroups(t *testing.T, groups []*AgentGroup) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(groups)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// subGroup returns the nested group called name under g, failing the test
// unless exactly one sub-group has that name: a rig listed twice would draw
// its header and rows twice under one client key.
func subGroup(t *testing.T, g map[string]any, name string) map[string]any {
	t.Helper()
	subs, _ := g["groups"].([]any)
	var found []map[string]any
	for _, s := range subs {
		if m, ok := s.(map[string]any); ok && m["name"] == name {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("group %q has %d sub-groups named %q, want 1 (sub-groups: %v)", g["name"], len(found), name, subs)
	}
	return found[0]
}

// orchRig builds the dashboard for sessions and orch, requires the
// orchestrator group to be the only top-level group, and returns its rig
// sub-group called rigName.
func orchRig(t *testing.T, sessions []*SessionState, orch *orchestrator.State, rigName string) map[string]any {
	t.Helper()
	wire := wireGroups(t, BuildDashboard(sessions, orch))
	if len(wire) != 1 {
		t.Fatalf("got %d top-level groups, want the orchestrator group alone", len(wire))
	}
	return subGroup(t, wire[0], rigName)
}

// wantUnattached requires rigGroup's unattached worker names to be want, in
// order.
func wantUnattached(t *testing.T, rigGroup map[string]any, want ...string) {
	t.Helper()
	if got := unattachedNames(rigGroup); !slices.Equal(got, want) {
		t.Errorf("rig %v unattached = %v, want %v", rigGroup["name"], got, want)
	}
}

// fieldList returns the string field of every object in g's listKey array,
// in order ("" for an object without it).
func fieldList(g map[string]any, listKey, field string) []string {
	items, _ := g[listKey].([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		v, _ := m[field].(string)
		out = append(out, v)
	}
	return out
}

func unattachedNames(g map[string]any) []string {
	return fieldList(g, "unattached_workers", "worker_name")
}

func agentIDs(g map[string]any) []string { return fieldList(g, "agents", "session_id") }

// wantNoWorkerState requires every unattached worker in rigGroup to omit
// worker_state.
func wantNoWorkerState(t *testing.T, rigGroup map[string]any) {
	t.Helper()
	for _, st := range fieldList(rigGroup, "unattached_workers", "worker_state") {
		if st != "" {
			t.Errorf("rig %v: a worker whose session ended carries worker_state %q, want none", rigGroup["name"], st)
		}
	}
}

// gasTown builds a running Gas Town state rooted at /gt with the given rigs.
func gasTown(codebases ...orchestrator.Codebase) *orchestrator.State {
	return &orchestrator.State{
		Adapter:   "gastown",
		Running:   true,
		Root:      "/gt",
		Codebases: codebases,
	}
}

// rig builds a rig codebase with a main worktree holding mainWorkers and one
// polecat worktree per polecat.
func rig(name string, mainWorkers []orchestrator.Worker, polecats ...orchestrator.Worker) orchestrator.Codebase {
	wts := []orchestrator.Worktree{{Path: "/gt/" + name, IsMain: true, Workers: mainWorkers}}
	for _, pc := range polecats {
		wts = append(wts, orchestrator.Worktree{
			Path:    "/gt/" + name + "/polecats/" + pc.Name,
			Workers: []orchestrator.Worker{pc},
		})
	}
	return orchestrator.Codebase{Name: name, Status: "operational", Worktrees: wts}
}

func polecat(name, bead, sessionID, state string) orchestrator.Worker {
	return orchestrator.Worker{
		Role: "polecat", Icon: "\U0001F477", Description: "Worker agent",
		Name: name, ID: bead, SessionID: sessionID, State: state,
	}
}

func TestBuildDashboard_UnattachedWorkerOnWire(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "pole-1", State: StateWorking, ProjectName: "irrlicht"},
	}
	orch := gasTown(rig("irrlicht", nil,
		polecat("busy-1", "GH-1", "pole-1", "working"),
		polecat("free-2", "GH-2", "", "idle"),
	))

	rigGroup := orchRig(t, sessions, orch, "irrlicht")

	if got := agentIDs(rigGroup); !slices.Equal(got, []string{"pole-1"}) {
		t.Errorf("rig agents = %v, want [pole-1] — a free worker must never enter agents", got)
	}
	ws, _ := rigGroup["unattached_workers"].([]any)
	if len(ws) != 1 {
		t.Fatalf("rig unattached_workers = %v, want exactly the free polecat", rigGroup["unattached_workers"])
	}
	w := ws[0].(map[string]any)
	want := map[string]string{
		"worker_name":  "free-2",
		"worker_id":    "GH-2",
		"worker_state": "idle",
		"role":         "polecat",
		"icon":         "\U0001F477",
		"description":  "Worker agent",
	}
	for k, v := range want {
		if w[k] != v {
			t.Errorf("unattached worker %s = %v, want %q", k, w[k], v)
		}
	}
	// It is not a session: no session_id, and no session `state` field.
	for _, k := range []string{"session_id", "state"} {
		if _, ok := w[k]; ok {
			t.Errorf("unattached worker carries session field %q: %v", k, w)
		}
	}
}

// A running Gas Town whose polecats are all free used to render nothing at
// all: buildOrchGroup returned nil when no session matched.
func TestBuildDashboard_UnattachedAllIdleKeepsOrchestratorGroup(t *testing.T) {
	orch := gasTown(rig("irrlicht", nil, polecat("free-1", "GH-1", "", "idle")))

	wire := wireGroups(t, BuildDashboard(nil, orch))
	if len(wire) != 1 {
		t.Fatalf("got %d groups, want the orchestrator group alone", len(wire))
	}
	if wire[0]["type"] != "gastown" || wire[0]["name"] != "gt" {
		t.Errorf("top group = name %v type %v, want gt/gastown", wire[0]["name"], wire[0]["type"])
	}
	rigGroup := subGroup(t, wire[0], "irrlicht")
	wantUnattached(t, rigGroup, "free-1")
	if rigGroup["status"] != "operational" {
		t.Errorf("rig status = %v, want operational", rigGroup["status"])
	}
	if _, ok := rigGroup["agents"]; ok {
		t.Errorf("an all-idle rig must omit agents, got %v", rigGroup["agents"])
	}
}

// Skew, new session: the session list is read after the poller's state, so a
// session newer than the last poll is attached by the CWD fallback while its
// worker still has no SessionID. The worker must not ALSO get a free row.
// free-9 is the positive control: without it this test passes on a builder
// that emits no worker rows at all.
func TestBuildDashboard_UnattachedSkewNewSessionDrawnOnce(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "new-1", State: StateWorking, CWD: "/gt/irrlicht/polecats/fix-42", ProjectName: "irrlicht"},
	}
	orch := gasTown(rig("irrlicht", nil,
		polecat("fix-42", "GH-42", "", "idle"),
		polecat("free-9", "GH-9", "", "idle"),
	))

	rigGroup := orchRig(t, sessions, orch, "irrlicht")
	if got := agentIDs(rigGroup); !slices.Equal(got, []string{"new-1"}) {
		t.Errorf("rig agents = %v, want [new-1]", got)
	}
	// fix-42 already has its session row.
	wantUnattached(t, rigGroup, "free-9")
}

// Skew, ended session: a session that ended since the last poll leaves a
// stale SessionID on its worker. The worker must still get a row rather than
// appear in neither list.
func TestBuildDashboard_UnattachedSkewEndedSessionStillShown(t *testing.T) {
	orch := gasTown(rig("irrlicht", nil, polecat("fix-42", "GH-42", "gone-1", "working")))

	rigGroup := orchRig(t, nil, orch, "irrlicht")
	wantUnattached(t, rigGroup, "fix-42")
	// "working" is the vanished session's state, which the poller copied onto
	// the worker; it is not the orchestrator's own state for a free slot.
	wantNoWorkerState(t, rigGroup)
}

// Crew is named too, so the rule covers it: a crew worker whose session ended
// since the last poll shows as a worker row until the next poll drops it
// (buildMainWorkers derives crew from sessions). Pinned so a later change to
// that choice is a visible decision rather than a silent one.
func TestBuildDashboard_UnattachedStaleCrewFollowsTheRule(t *testing.T) {
	crew := orchestrator.Worker{Role: "crew", Name: "alice", SessionID: "gone-crew", State: "ready"}
	orch := gasTown(rig("irrlicht", []orchestrator.Worker{crew}))

	rigGroup := orchRig(t, nil, orch, "irrlicht")
	wantUnattached(t, rigGroup, "alice")
	wantNoWorkerState(t, rigGroup)
}

// Rigs with sessions come first (session order), then rigs whose only workers
// are free, in orch.Codebases order.
func TestBuildDashboard_UnattachedOnlyRigsFollowSessionRigs(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "pole-b", State: StateWorking},
	}
	orch := gasTown(
		rig("alpha", nil, polecat("a-free", "A-1", "", "idle")),
		rig("beta", nil, polecat("b-busy", "B-1", "pole-b", "working")),
		rig("gamma", nil, polecat("g-free", "G-1", "", "idle")),
	)

	wire := wireGroups(t, BuildDashboard(sessions, orch))
	order := fieldList(wire[0], "groups", "name")
	if !slices.Equal(order, []string{"beta", "alpha", "gamma"}) {
		t.Errorf("rig order = %v, want [beta alpha gamma]", order)
	}
}

// The web keys a worker row by group path + worker name, so one rig must not
// list the same name twice (crew derived from two sessions in one directory,
// both since ended).
func TestBuildDashboard_UnattachedNamesUniquePerRig(t *testing.T) {
	mainWorkers := []orchestrator.Worker{
		{Role: "crew", Name: "alice", SessionID: "gone-a", State: "ready"},
		{Role: "crew", Name: "alice", SessionID: "gone-b", State: "ready"},
	}
	orch := gasTown(rig("irrlicht", mainWorkers))

	wantUnattached(t, orchRig(t, nil, orch, "irrlicht"), "alice")
}

// LOCK (passes before #2140 by construction): unnamed role singletons
// (witness, refinery) and global agents (mayor, deacon, boot) are emitted by
// the poller whether or not a session matched (see unattachedWorkersByRig).
// Rows for them would be permanent placeholders, so they never become worker
// rows.
func TestBuildDashboard_UnattachedExcludesUnnamedAndGlobalAgents(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "pole-1", State: StateWorking},
	}
	orch := gasTown(rig("irrlicht",
		[]orchestrator.Worker{
			{Role: "witness", State: "idle"},
			{Role: "refinery", State: "idle"},
		},
		polecat("busy-1", "GH-1", "pole-1", "working"),
	))
	orch.GlobalAgents = []orchestrator.GlobalAgent{
		{Role: "mayor", State: "idle"},
		{Role: "deacon", State: "idle"},
		{Role: "boot", State: "idle"},
	}

	wire := wireGroups(t, BuildDashboard(sessions, orch))
	if len(wire) != 1 {
		t.Fatalf("got %d groups, want 1", len(wire))
	}
	if _, ok := wire[0]["unattached_workers"]; ok {
		t.Errorf("orchestrator group carries unattached_workers: %v", wire[0]["unattached_workers"])
	}
	if _, ok := subGroup(t, wire[0], "irrlicht")["unattached_workers"]; ok {
		t.Errorf("rig carries unattached_workers for unnamed workers")
	}
}

// LOCK: no running orchestrator, no orchestrator group — free workers or not.
func TestBuildDashboard_UnattachedNeedsRunningOrchestrator(t *testing.T) {
	stopped := gasTown(rig("irrlicht", nil, polecat("free-1", "GH-1", "", "idle")))
	stopped.Running = false

	if got := BuildDashboard(nil, stopped); len(got) != 0 {
		t.Errorf("stopped orchestrator: got %d groups, want 0", len(got))
	}
	if got := BuildDashboard(nil, nil); len(got) != 0 {
		t.Errorf("nil orchestrator: got %d groups, want 0", len(got))
	}
}
