package session

import (
	"path/filepath"
	"time"

	"irrlicht/core/domain/orchestrator"
)

// AgentGroup is a recursive container for agents and sub-groups.
// A group may contain direct agents and/or nested sub-groups.
type AgentGroup struct {
	Name   string        `json:"name"`
	Type   string        `json:"type,omitempty"`   // "gastown" for orchestrator groups
	Status string        `json:"status,omitempty"` // codebase status (e.g. "operational")
	Agents []*Agent      `json:"agents,omitempty"`
	Groups []*AgentGroup `json:"groups,omitempty"` // nested sub-groups (e.g. rigs)

	// Costs holds per-time-frame cost totals (USD) accrued within the
	// trailing window for each key. Keys: "day", "week", "month", "year".
	// Populated by the HTTP handler from the CostTracker; zero values are
	// omitted by the tracker but present keys may still be 0 when no data
	// exists in the window.
	Costs map[string]float64 `json:"costs,omitempty"`

	// UnattachedWorkers are the named orchestrator workers in this group
	// that no session is attached to — a free polecat, an idle pool slot
	// (#2140). They are kept out of Agents on purpose: an older macOS app
	// decodes every Agents entry as a session and requires session_id and
	// state, so one session-less entry there would fail its whole hydrate.
	UnattachedWorkers []*UnattachedWorker `json:"unattached_workers,omitempty"`
}

// UnattachedWorker is an orchestrator worker with no attached session. It is
// not a session: it carries no session_id and no session `state` field.
// WorkerState is the orchestrator's own Worker.State, set only when the
// orchestrator matched no session to the worker either. A worker still
// holding a SessionID from the last poll (its session ended since) gets none,
// because an adapter may have copied that session's state onto it — the Gas
// Town poller does (buildPolecatWorktrees, appendDogWorkers).
type UnattachedWorker struct {
	Role        string `json:"role,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Description string `json:"description,omitempty"`
	WorkerName  string `json:"worker_name"`
	WorkerID    string `json:"worker_id,omitempty"`
	WorkerState string `json:"worker_state,omitempty"`
}

// Agent is a session with optional orchestrator role and nested children.
type Agent struct {
	*SessionState
	Role        string   `json:"role,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Description string   `json:"description,omitempty"`
	WorkerName  string   `json:"worker_name,omitempty"`
	WorkerID    string   `json:"worker_id,omitempty"`
	Children    []*Agent `json:"children,omitempty"`
}

// workerInfo holds orchestrator metadata for a matched session.
type workerInfo struct {
	Role        string
	Icon        string
	Description string
	Rig         string
	Name        string
	ID          string
}

// BuildDashboard creates a recursive group hierarchy from a flat list of
// sessions and optional orchestrator state.
//
// With an orchestrator: returns [orchGroup, ...regularGroups] where orchGroup
// contains global agents directly and rig agents in nested sub-groups.
//
// Without an orchestrator: returns [...projectGroups] grouped by ProjectName.
func BuildDashboard(sessions []*SessionState, orch *orchestrator.State) []*AgentGroup {
	// 1. Build session-to-worker map, and list the workers no session in
	// this call is attached to.
	workerMap := map[string]*workerInfo{}
	var unattached map[string][]*UnattachedWorker
	if orch != nil && orch.Running {
		workerMap = buildWorkerMap(orch, sessions)
		unattached = unattachedWorkersByRig(orch, sessions, workerMap)
	}

	// 2. Index sessions and identify parent-child relationships.
	childIDs, parentChildren := indexParentChildren(sessions)

	// 3. Partition top-level sessions into orchestrator vs regular.
	partition := partitionSessions(sessions, workerMap, parentChildren, childIDs)

	// 4. Assemble result.
	var result []*AgentGroup
	if orchGroup := buildOrchGroup(orch, partition, unattached); orchGroup != nil {
		result = append(result, orchGroup)
	}
	for _, key := range partition.regularOrder {
		result = append(result, partition.regularGroupMap[key])
	}

	return result
}

// indexParentChildren identifies parent-child relationships among sessions:
// childIDs marks sessions whose ParentSessionID resolves to another session
// in the input, and parentChildren maps each such parent's SessionID to its
// children (in input order).
func indexParentChildren(sessions []*SessionState) (childIDs map[string]bool, parentChildren map[string][]*SessionState) {
	byID := make(map[string]*SessionState, len(sessions))
	for _, s := range sessions {
		byID[s.SessionID] = s
	}

	childIDs = make(map[string]bool)
	parentChildren = make(map[string][]*SessionState)
	for _, s := range sessions {
		if s.ParentSessionID != "" {
			if _, ok := byID[s.ParentSessionID]; ok {
				childIDs[s.SessionID] = true
				parentChildren[s.ParentSessionID] = append(parentChildren[s.ParentSessionID], s)
			}
		}
	}
	return childIDs, parentChildren
}

// dashboardPartition accumulates BuildDashboard's split of top-level
// sessions into orchestrator-owned agents (global + per-rig) and regular
// project groups, preserving first-seen order for deterministic output.
type dashboardPartition struct {
	orchAgents      []*Agent            // global agents (no rig)
	rigAgentMap     map[string][]*Agent // rig name → agents
	rigOrder        []string
	regularGroupMap map[string]*AgentGroup
	regularOrder    []string
}

// partitionSessions builds an Agent tree for each top-level (non-child)
// session and buckets it into the orchestrator or regular partition.
func partitionSessions(sessions []*SessionState, workerMap map[string]*workerInfo, parentChildren map[string][]*SessionState, childIDs map[string]bool) *dashboardPartition {
	p := &dashboardPartition{
		rigAgentMap:     map[string][]*Agent{},
		regularGroupMap: map[string]*AgentGroup{},
	}
	for _, s := range sessions {
		if childIDs[s.SessionID] {
			continue
		}
		agent := buildAgent(s, workerMap, parentChildren)

		if wi, isOrch := workerMap[s.SessionID]; isOrch {
			p.addOrchAgent(wi, agent)
		} else {
			p.addRegularAgent(s, agent)
		}
	}
	return p
}

// addOrchAgent files agent under its rig's bucket, or as a global agent when
// wi has no rig.
func (p *dashboardPartition) addOrchAgent(wi *workerInfo, agent *Agent) {
	if wi.Rig != "" {
		if _, exists := p.rigAgentMap[wi.Rig]; !exists {
			p.rigOrder = append(p.rigOrder, wi.Rig)
		}
		p.rigAgentMap[wi.Rig] = append(p.rigAgentMap[wi.Rig], agent)
		return
	}
	p.orchAgents = append(p.orchAgents, agent)
}

// addRegularAgent files agent under its project's group, creating the group
// on first sight ("unknown" when the session has no project name).
func (p *dashboardPartition) addRegularAgent(s *SessionState, agent *Agent) {
	key := s.ProjectName
	if key == "" {
		key = "unknown"
	}
	g, ok := p.regularGroupMap[key]
	if !ok {
		g = &AgentGroup{Name: key}
		p.regularGroupMap[key] = g
		p.regularOrder = append(p.regularOrder, key)
	}
	g.Agents = append(g.Agents, agent)
}

// buildOrchGroup assembles the top-level "Gas Town" group (global agents
// plus a nested sub-group per rig) from partition and the rigs' unattached
// workers, or returns nil when there is no running orchestrator or it
// contributed neither agents nor unattached workers.
func buildOrchGroup(orch *orchestrator.State, partition *dashboardPartition, unattached map[string][]*UnattachedWorker) *AgentGroup {
	if orch == nil || !orch.Running {
		return nil
	}
	rigGroups := buildRigGroups(orch, partition, unattached)
	if len(partition.orchAgents) == 0 && len(rigGroups) == 0 {
		return nil
	}

	orchGroupName := "Gas Town"
	if orch.Root != "" {
		orchGroupName = filepath.Base(orch.Root)
	}

	return &AgentGroup{
		Name:   orchGroupName,
		Type:   orch.Adapter,
		Agents: partition.orchAgents,
		Groups: rigGroups,
	}
}

// buildRigGroups returns one sub-group per rig that has sessions or
// unattached workers: rigs with sessions first, in partition order, then rigs
// whose only rows are unattached workers, in orch.Codebases order.
func buildRigGroups(orch *orchestrator.State, partition *dashboardPartition, unattached map[string][]*UnattachedWorker) []*AgentGroup {
	order := append([]string(nil), partition.rigOrder...)
	for _, cb := range orch.Codebases {
		if _, hasSessions := partition.rigAgentMap[cb.Name]; !hasSessions && len(unattached[cb.Name]) > 0 {
			order = append(order, cb.Name)
		}
	}

	groups := make([]*AgentGroup, 0, len(order))
	for _, rigName := range order {
		groups = append(groups, &AgentGroup{
			Name:              rigName,
			Status:            codebaseStatus(orch, rigName),
			Agents:            partition.rigAgentMap[rigName],
			UnattachedWorkers: unattached[rigName],
		})
	}
	return groups
}

// codebaseStatus returns the status of the first codebase called rigName, or
// "" when the orchestrator reports none by that name.
func codebaseStatus(orch *orchestrator.State, rigName string) string {
	for _, cb := range orch.Codebases {
		if cb.Name == rigName {
			return cb.Status
		}
	}
	return ""
}

// rigWorkerKey identifies a codebase worker by rig and name — the pair both
// the explicit SessionID match and the CWD fallback produce.
type rigWorkerKey struct{ rig, name string }

// unattachedWorkersByRig returns, per rig, the named codebase workers that no
// session in sessions is attached to. Only named workers qualify; see
// orchestrator.Worker.Name for that contract.
//
// Attachment is read from the sessions actually in this call, through
// workerMap, and not from a worker's own SessionID: the orchestrator state
// and the session list are read at different times (handleGetSessions), so a
// session newer than the last poll is attached by the CWD fallback while its
// worker has no SessionID yet, and a session that ended since then leaves a
// stale SessionID behind. workerMap's keys alone would count that stale id.
func unattachedWorkersByRig(orch *orchestrator.State, sessions []*SessionState, workerMap map[string]*workerInfo) map[string][]*UnattachedWorker {
	attached := attachedWorkerKeys(sessions, workerMap)
	out := make(map[string][]*UnattachedWorker)
	for _, cb := range orch.Codebases {
		out[cb.Name] = append(out[cb.Name], rigUnattachedWorkers(cb, attached)...)
	}
	return out
}

// attachedWorkerKeys returns the rig workers some session in sessions is
// attached to, by either workerMap source. Global agents and unnamed roles
// produce keys no named codebase worker looks up.
func attachedWorkerKeys(sessions []*SessionState, workerMap map[string]*workerInfo) map[rigWorkerKey]bool {
	attached := make(map[rigWorkerKey]bool)
	for _, s := range sessions {
		if wi := workerMap[s.SessionID]; wi != nil {
			attached[rigWorkerKey{wi.Rig, wi.Name}] = true
		}
	}
	return attached
}

// rigUnattachedWorkers lists cb's named workers that are not in attached, once
// per name: clients key a worker row by group and name, so a name listed here
// is added to attached and its later duplicates are skipped like attached ones.
func rigUnattachedWorkers(cb orchestrator.Codebase, attached map[rigWorkerKey]bool) []*UnattachedWorker {
	var out []*UnattachedWorker
	for _, wt := range cb.Worktrees {
		for _, w := range wt.Workers {
			key := rigWorkerKey{cb.Name, w.Name}
			if w.Name == "" || attached[key] {
				continue
			}
			attached[key] = true
			out = append(out, &UnattachedWorker{
				Role:        w.Role,
				Icon:        w.Icon,
				Description: w.Description,
				WorkerName:  w.Name,
				WorkerID:    w.ID,
				WorkerState: ownWorkerState(w),
			})
		}
	}
	return out
}

// ownWorkerState is w.State when the orchestrator matched no session to w,
// and "" otherwise (see UnattachedWorker).
func ownWorkerState(w orchestrator.Worker) string {
	if w.SessionID != "" {
		return ""
	}
	return w.State
}

// buildAgent recursively creates an Agent tree from a session and its children.
//
// The Agent embeds a shallow COPY of the session, not the caller's pointer:
// unifySubagents writes Subagents through the embedded SessionState, and the
// input sessions are routinely shared with concurrent JSON marshals (the
// daemon's websocket fan-out, the relay hub's push encode) — writing in place
// is a data race (#572: TSan flagged hub.go's encode racing this write via
// the relay's /api/v1/sessions handler). The group tree owning its own
// structs makes BuildAgentGroups non-mutating; nothing reads the input's
// Subagents afterwards (the detector populates session.Subagents itself
// before save/broadcast — session_detector_subagent.go).
func buildAgent(s *SessionState, workerMap map[string]*workerInfo, parentChildren map[string][]*SessionState) *Agent {
	cp := *s.ClientCopy()
	agent := &Agent{SessionState: &cp}

	// Annotate with orchestrator role.
	if wi, ok := workerMap[s.SessionID]; ok {
		agent.Role = wi.Role
		agent.Icon = wi.Icon
		agent.Description = wi.Description
		agent.WorkerName = wi.Name
		agent.WorkerID = wi.ID
	}

	// Attach children recursively.
	if children, ok := parentChildren[s.SessionID]; ok {
		agent.Children = make([]*Agent, 0, len(children))
		for _, c := range children {
			agent.Children = append(agent.Children, buildAgent(c, workerMap, parentChildren))
		}
	}

	// Unify subagents summary: merge in-process agents with file-based children.
	unifySubagents(agent)
	// Subagent enrichment can add a task estimate to a ready parent. Project
	// after enrichment so the complete client response follows the ready-state
	// metric contract without changing the source session.
	agent.SessionState = agent.SessionState.ClientCopy()

	return agent
}

// unifySubagents recomputes the Subagents summary for an agent by merging
// the adapter-reported in-process count (metrics.OpenSubagents) with the
// file-based children attached to the agent tree. It also refreshes the
// parent's subagent-derived task estimate (#622) so the REST-hydration
// path shows the same chip as the push path (refreshSubagentSummary).
func unifySubagents(a *Agent) {
	children := make([]*SessionState, 0, len(a.Children))
	for _, c := range a.Children {
		children = append(children, c.SessionState)
	}
	a.Subagents = ComputeSubagentSummary(a.SessionState, children)
	ApplySubagentTaskEstimate(a.SessionState, children, time.Now())
}

// ComputeSubagentSummary returns the unified subagent summary for a parent
// session. The "in-process" component comes from the adapter via
// parent.Metrics.OpenSubagents (e.g. claudecode counts open Agent tool calls);
// the "file-based" component comes from child sessions in childSessions whose
// ParentSessionID matches the parent. Returns nil when there are no subagents.
func ComputeSubagentSummary(parent *SessionState, childSessions []*SessionState) *subagentSummary {
	inProcess := 0
	if parent != nil && parent.Metrics != nil {
		inProcess = parent.Metrics.OpenSubagents
	}

	var fileChildren []*SessionState
	if parent != nil {
		for _, c := range childSessions {
			if c != nil && c.ParentSessionID == parent.SessionID {
				fileChildren = append(fileChildren, c)
			}
		}
	}

	if inProcess == 0 && len(fileChildren) == 0 {
		return nil
	}

	summary := &subagentSummary{
		Total:   inProcess + len(fileChildren),
		Working: inProcess,
	}
	// Tally by key, not by switch (#1801): Total already counts every file
	// child, so any state missing from the buckets breaks the invariant that
	// they sum to it. A map cannot go silently incomplete; the readout below
	// is the only place states are named, and a canonical state absent from it
	// is caught by TestComputeSubagentSummary_BucketsSumToTotal.
	byState := map[string]int{}
	for _, c := range fileChildren {
		byState[c.State]++
	}
	summary.Working += byState[StateWorking]
	summary.Waiting = byState[StateWaiting]
	summary.Ready = byState[StateReady]
	summary.Error = byState[StateError]
	return summary
}

// buildWorkerMap creates a sessionID → workerInfo index from orchestrator state.
func buildWorkerMap(orch *orchestrator.State, sessions []*SessionState) map[string]*workerInfo {
	m := make(map[string]*workerInfo)
	addGlobalAgentWorkers(m, orch)
	addCodebaseWorkers(m, orch)
	addCWDFallbackWorkers(m, orch, sessions)
	return m
}

// addGlobalAgentWorkers indexes orchestrator-level global agents (no rig)
// into m.
func addGlobalAgentWorkers(m map[string]*workerInfo, orch *orchestrator.State) {
	for _, ga := range orch.GlobalAgents {
		if ga.SessionID != "" {
			m[ga.SessionID] = &workerInfo{
				Role:        ga.Role,
				Icon:        ga.Icon,
				Description: ga.Description,
			}
		}
	}
}

// addCodebaseWorkers indexes per-rig, per-worktree workers into m.
func addCodebaseWorkers(m map[string]*workerInfo, orch *orchestrator.State) {
	for _, cb := range orch.Codebases {
		for _, wt := range cb.Worktrees {
			for _, w := range wt.Workers {
				if w.SessionID != "" {
					m[w.SessionID] = &workerInfo{
						Role:        w.Role,
						Icon:        w.Icon,
						Description: w.Description,
						Rig:         cb.Name,
						Name:        w.Name,
						ID:          w.ID,
					}
				}
			}
		}
	}
}

// addCWDFallbackWorkers fills in workerInfo for sessions not matched by an
// explicit orchestrator/worker SessionID above, by deriving a role from CWD.
func addCWDFallbackWorkers(m map[string]*workerInfo, orch *orchestrator.State, sessions []*SessionState) {
	if orch.Root == "" {
		return
	}
	for _, s := range sessions {
		if _, ok := m[s.SessionID]; ok {
			continue
		}
		iconFn := orchestrator.IconLookup(func(role string) string {
			return orch.RoleIcons[role]
		})
		ri := orchestrator.DeriveGasTownRole(s.CWD, orch.Root, iconFn)
		if ri != nil {
			m[s.SessionID] = &workerInfo{
				Role: ri.Role,
				Icon: ri.Icon,
				Rig:  ri.Rig,
				Name: ri.Name,
			}
		}
	}
}
