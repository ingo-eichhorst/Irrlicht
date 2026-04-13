package session

import (
	"path/filepath"

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
	// 1. Build session-to-worker map.
	workerMap := map[string]*workerInfo{}
	if orch != nil && orch.Running {
		workerMap = buildWorkerMap(orch, sessions)
	}

	// 2. Index sessions and identify parent-child relationships.
	byID := make(map[string]*SessionState, len(sessions))
	for _, s := range sessions {
		byID[s.SessionID] = s
	}

	childIDs := make(map[string]bool)
	parentChildren := make(map[string][]*SessionState)
	for _, s := range sessions {
		if s.ParentSessionID != "" {
			if _, ok := byID[s.ParentSessionID]; ok {
				childIDs[s.SessionID] = true
				parentChildren[s.ParentSessionID] = append(parentChildren[s.ParentSessionID], s)
			}
		}
	}

	// 3. Partition top-level sessions into orchestrator vs regular.
	var orchAgents []*Agent          // global agents (no rig)
	rigAgentMap := map[string][]*Agent{} // rig name → agents
	var rigOrder []string

	regularGroupMap := map[string]*AgentGroup{}
	var regularOrder []string

	for _, s := range sessions {
		if childIDs[s.SessionID] {
			continue
		}
		agent := buildAgent(s, workerMap, parentChildren)

		wi, isOrch := workerMap[s.SessionID]
		if isOrch {
			if wi.Rig != "" {
				if _, exists := rigAgentMap[wi.Rig]; !exists {
					rigOrder = append(rigOrder, wi.Rig)
				}
				rigAgentMap[wi.Rig] = append(rigAgentMap[wi.Rig], agent)
			} else {
				orchAgents = append(orchAgents, agent)
			}
		} else {
			key := s.ProjectName
			if key == "" {
				key = "unknown"
			}
			g, ok := regularGroupMap[key]
			if !ok {
				g = &AgentGroup{Name: key}
				regularGroupMap[key] = g
				regularOrder = append(regularOrder, key)
			}
			g.Agents = append(g.Agents, agent)
		}
	}

	// 4. Assemble result.
	var result []*AgentGroup

	if orch != nil && orch.Running && (len(orchAgents) > 0 || len(rigAgentMap) > 0) {
		orchGroupName := "Gas Town"
		if orch.Root != "" {
			orchGroupName = filepath.Base(orch.Root)
		}

		orchGroup := &AgentGroup{
			Name:   orchGroupName,
			Type:   orch.Adapter,
			Agents: orchAgents,
		}

		// Build sub-groups for each rig.
		for _, rigName := range rigOrder {
			subGroup := &AgentGroup{
				Name:   rigName,
				Agents: rigAgentMap[rigName],
			}
			// Annotate with codebase status.
			for _, cb := range orch.Codebases {
				if cb.Name == rigName {
					subGroup.Status = cb.Status
					break
				}
			}
			orchGroup.Groups = append(orchGroup.Groups, subGroup)
		}

		result = append(result, orchGroup)
	}

	for _, key := range regularOrder {
		result = append(result, regularGroupMap[key])
	}

	return result
}

// buildAgent recursively creates an Agent tree from a session and its children.
func buildAgent(s *SessionState, workerMap map[string]*workerInfo, parentChildren map[string][]*SessionState) *Agent {
	agent := &Agent{SessionState: s}

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

	return agent
}

// unifySubagents recomputes the Subagents summary for an agent by merging
// the adapter-reported in-process count (metrics.OpenSubagents) with the
// file-based children attached to the agent tree.
func unifySubagents(a *Agent) {
	children := make([]*SessionState, 0, len(a.Children))
	for _, c := range a.Children {
		children = append(children, c.SessionState)
	}
	a.Subagents = ComputeSubagentSummary(a.SessionState, children)
}

// ComputeSubagentSummary returns the unified subagent summary for a parent
// session. The "in-process" component comes from the adapter via
// parent.Metrics.OpenSubagents (e.g. claudecode counts open Agent tool calls);
// the "file-based" component comes from child sessions in childSessions whose
// ParentSessionID matches the parent. Returns nil when there are no subagents.
func ComputeSubagentSummary(parent *SessionState, childSessions []*SessionState) *SubagentSummary {
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

	summary := &SubagentSummary{
		Total:   inProcess + len(fileChildren),
		Working: inProcess,
	}
	for _, c := range fileChildren {
		switch c.State {
		case StateWorking:
			summary.Working++
		case StateWaiting:
			summary.Waiting++
		case StateReady:
			summary.Ready++
		}
	}
	return summary
}

// buildWorkerMap creates a sessionID → workerInfo index from orchestrator state.
func buildWorkerMap(orch *orchestrator.State, sessions []*SessionState) map[string]*workerInfo {
	m := make(map[string]*workerInfo)

	for _, ga := range orch.GlobalAgents {
		if ga.SessionID != "" {
			m[ga.SessionID] = &workerInfo{
				Role:        ga.Role,
				Icon:        ga.Icon,
				Description: ga.Description,
			}
		}
	}

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

	// CWD-based fallback for sessions not matched above.
	if orch.Root != "" {
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

	return m
}
