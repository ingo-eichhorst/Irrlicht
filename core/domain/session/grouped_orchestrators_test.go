package session

import (
	"encoding/json"
	"slices"
	"testing"

	"irrlicht/core/domain/orchestrator"
)

// Issue #2148: BuildDashboard takes every orchestrator the daemon monitors, so
// a worker-list pool renders beside Gas Town. These assert on the marshalled
// payload (wireGroups). The fields they read — State.Name, Worker.Stale,
// Worker.LastExit — did not exist before #2148, so they are mutation tests:
// each names, at its assertion, the change that was run to turn it red.

func intp(v int) *int { return &v }

// workerPool builds a running worker-list state: no Root, one codebase per
// team holding one path-less worktree of slots.
func workerPool(name string, teams ...orchestrator.Codebase) *orchestrator.State {
	return &orchestrator.State{Adapter: "workerlist", Running: true, Name: name, Codebases: teams}
}

func poolTeam(name, status string, slots ...orchestrator.Worker) orchestrator.Codebase {
	return orchestrator.Codebase{Name: name, Status: status, Worktrees: []orchestrator.Worktree{{Workers: slots}}}
}

func slot(name, workItem, state string) orchestrator.Worker {
	return orchestrator.Worker{Role: "worker", Name: name, ID: workItem, State: state}
}

// topGroup returns the single top-level group of type typ ("" for a project
// group, which omits the key).
func topGroup(t *testing.T, wire []map[string]any, typ string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, g := range wire {
		if got, _ := g["type"].(string); got == typ {
			found = append(found, g)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d top-level groups of type %q, want 1 (groups: %v)", len(found), typ, wire)
	}
	return found[0]
}

// unattachedEntry returns the unattached worker called name in rigGroup.
func unattachedEntry(t *testing.T, rigGroup map[string]any, name string) map[string]any {
	t.Helper()
	items, _ := rigGroup["unattached_workers"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m["worker_name"] == name {
			return m
		}
	}
	t.Fatalf("rig %v has no unattached worker %q", rigGroup["name"], name)
	return nil
}

// LOCK + mutation: passing a worker list beside Gas Town leaves Gas Town's
// group byte-for-byte as it was alone, and puts the pool after it, ahead of
// the project groups. Seen red by building the pool's group into the first
// orchestrator's slot (partition.orchs[0] for every i) — the pool's slots then
// joined Gas Town's group.
func TestBuildDashboard_PoolBesideGasTownLeavesGasTownUnchanged(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "pole-1", State: StateWorking, ProjectName: "irrlicht", CWD: "/gt/irrlicht/polecats/busy-1"},
		{SessionID: "other-1", State: StateReady, ProjectName: "my-app"},
	}
	gt := func() *orchestrator.State {
		return gasTown(rig("irrlicht", nil,
			polecat("busy-1", "GH-1", "pole-1", "working"),
			polecat("free-1", "GH-2", "", "idle")))
	}
	pool := workerPool("Build pool", poolTeam("team-a", "operational", slot("team-a-m1-1", "proj-a1b2", "busy")))

	alone, err := json.Marshal(BuildDashboard(sessions, gt())[0])
	if err != nil {
		t.Fatal(err)
	}
	both := BuildDashboard(sessions, gt(), pool)
	beside, err := json.Marshal(both[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(alone) != string(beside) {
		t.Errorf("Gas Town group changed when a pool was passed beside it\nalone:  %s\nbeside: %s", alone, beside)
	}

	var names []string
	for _, g := range both {
		names = append(names, g.Name+"/"+g.Type)
	}
	if want := []string{"gt/gastown", "Build pool/workerlist", "my-app/"}; !slices.Equal(names, want) {
		t.Errorf("top-level groups = %q, want %q", names, want)
	}
}

// The pool's group is named from State.Name and has one sub-group per team,
// each slot — busy or idle — an unattached worker carrying stale and
// last_exit. Seen red by: ignoring State.Name in orchGroupName (the group was
// named "Gas Town"); dropping the Stale copy, and separately the LastExit
// copy, in rigUnattachedWorkers.
func TestBuildDashboard_PoolListsEverySlotWithItsIndications(t *testing.T) {
	busyStaleFailed := slot("team-a-m1-1", "proj-a1b2", "busy")
	busyStaleFailed.Stale = true
	busyStaleFailed.LastExit = intp(2)
	idleOK := slot("team-a-m1-2", "", "idle")
	idleOK.LastExit = intp(0)
	pool := workerPool("Build pool",
		poolTeam("team-a", "operational", busyStaleFailed, idleOK),
		poolTeam("team-b", "stale", slot("team-b-m2-1", "", "idle")))

	wire := wireGroups(t, BuildDashboard(nil, pool))
	g := topGroup(t, wire, "workerlist")
	if g["name"] != "Build pool" {
		t.Errorf("pool group name = %v, want %q (State.Name)", g["name"], "Build pool")
	}
	if got := fieldList(g, "groups", "name"); !slices.Equal(got, []string{"team-a", "team-b"}) {
		t.Errorf("team sub-groups = %v, want [team-a team-b]", got)
	}
	teamA := subGroup(t, g, "team-a")
	wantUnattached(t, teamA, "team-a-m1-1", "team-a-m1-2")
	if st := subGroup(t, g, "team-b")["status"]; st != "stale" {
		t.Errorf("team-b status = %v, want stale", st)
	}

	busy := unattachedEntry(t, teamA, "team-a-m1-1")
	if busy["stale"] != true {
		t.Errorf("busy stale slot: stale = %v, want true", busy["stale"])
	}
	if busy["last_exit"] != float64(2) {
		t.Errorf("busy slot: last_exit = %v, want 2", busy["last_exit"])
	}
	if busy["worker_state"] != "busy" || busy["worker_id"] != "proj-a1b2" {
		t.Errorf("busy slot: worker_state=%v worker_id=%v, want busy/proj-a1b2", busy["worker_state"], busy["worker_id"])
	}

	idle := unattachedEntry(t, teamA, "team-a-m1-2")
	if _, ok := idle["stale"]; ok {
		t.Errorf("fresh idle slot carries stale: %v", idle["stale"])
	}
	// A completed run with exit 0 is on the wire as 0, not omitted: the
	// client tells "no run yet" (absent) from "last run passed" (0).
	if v, ok := idle["last_exit"]; !ok || v != float64(0) {
		t.Errorf("idle slot: last_exit = %v (present %v), want 0 present", v, ok)
	}
	if _, ok := unattachedEntry(t, subGroup(t, g, "team-b"), "team-b-m2-1")["last_exit"]; ok {
		t.Error("slot with no completed run carries last_exit")
	}
}

// A pool has no Root, so it claims no session by path: a session under a Gas
// Town polecat path stays a project session when only the pool is running.
// LOCK — addCWDFallbackWorkers already returned early on an empty Root before
// #2148; this pins that the pool keeps Root empty in the dashboard's eyes.
func TestBuildDashboard_PoolClaimsNoSessionByPath(t *testing.T) {
	sessions := []*SessionState{
		{SessionID: "pole-1", State: StateWorking, ProjectName: "irrlicht", CWD: "/gt/irrlicht/polecats/busy-1"},
	}
	pool := workerPool("Build pool", poolTeam("team-a", "operational", slot("team-a-m1-1", "", "idle")))

	wire := wireGroups(t, BuildDashboard(sessions, pool))
	g := topGroup(t, wire, "workerlist")
	if ids := agentIDs(subGroup(t, g, "team-a")); len(ids) != 0 {
		t.Errorf("pool team claimed sessions %v by path", ids)
	}
	proj := topGroup(t, wire, "")
	if ids := agentIDs(proj); !slices.Equal(ids, []string{"pole-1"}) {
		t.Errorf("project group agents = %v, want [pole-1]", ids)
	}
}

// A session two orchestrators both claim goes to the first passed, once.
// Seen red by letting a later claim overwrite an earlier one in
// buildWorkerMaps (dropping the `!claimed` check): the session moved under
// the pool, and Gas Town's busy polecat turned into a free-worker row.
func TestBuildDashboard_FirstOrchestratorKeepsASharedSession(t *testing.T) {
	sessions := []*SessionState{{SessionID: "pole-1", State: StateWorking, ProjectName: "irrlicht"}}
	gt := gasTown(rig("irrlicht", nil, polecat("busy-1", "GH-1", "pole-1", "working")))
	claimed := slot("team-a-m1-1", "proj-a1b2", "busy")
	claimed.SessionID = "pole-1"
	pool := workerPool("Build pool", poolTeam("team-a", "operational", claimed))

	wire := wireGroups(t, BuildDashboard(sessions, gt, pool))
	if ids := agentIDs(subGroup(t, topGroup(t, wire, "gastown"), "irrlicht")); !slices.Equal(ids, []string{"pole-1"}) {
		t.Errorf("Gas Town rig agents = %v, want [pole-1]", ids)
	}
	if _, ok := subGroup(t, topGroup(t, wire, "gastown"), "irrlicht")["unattached_workers"]; ok {
		t.Error("Gas Town's busy polecat listed as unattached")
	}
	team := subGroup(t, topGroup(t, wire, "workerlist"), "team-a")
	if ids := agentIDs(team); len(ids) != 0 {
		t.Errorf("pool team also holds sessions %v", ids)
	}
	wantUnattached(t, team, "team-a-m1-1")
}

// LOCK: BuildDashboard(sessions, nil) — a single nil orchestrator, as every
// non-daemon caller passes — and a mix of nil and stopped states yield the
// project groups alone.
func TestBuildDashboard_NilAndStoppedOrchestratorsAreSkipped(t *testing.T) {
	sessions := []*SessionState{{SessionID: "a", State: StateWorking, ProjectName: "proj"}}
	stopped := workerPool("Build pool", poolTeam("team-a", "operational", slot("s1", "", "idle")))
	stopped.Running = false
	for _, orchs := range [][]*orchestrator.State{nil, {nil}, {nil, stopped}} {
		groups := BuildDashboard(sessions, orchs...)
		if len(groups) != 1 || groups[0].Name != "proj" || groups[0].Type != "" {
			t.Errorf("orchs=%v: groups = %+v, want the proj group alone", orchs, groups)
		}
	}
}
