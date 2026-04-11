package session

import "irrlicht/core/domain/orchestrator"

// DashboardResponse is the unified API response containing the full
// Orchestrator → Group → Agent → Children hierarchy.
type DashboardResponse struct {
	Orchestrator *OrchestratorSummary `json:"orchestrator,omitempty"`
	Groups       []*AgentGroup        `json:"groups"`
}

// OrchestratorSummary holds structural orchestrator info (adapter, status,
// work units). Individual worker states are represented as Agents, not here.
type OrchestratorSummary struct {
	Adapter   string             `json:"adapter"`
	Running   bool               `json:"running"`
	WorkUnits []WorkUnitSummary  `json:"work_units,omitempty"`
}

// WorkUnitSummary represents a trackable unit of work (convoy, task list).
type WorkUnitSummary struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Total  int    `json:"total"`
	Done   int    `json:"done"`
}

// AgentGroup is a collection of agents working on the same project or rig.
type AgentGroup struct {
	Name   string   `json:"name"`
	Status string   `json:"status,omitempty"`
	Agents []*Agent `json:"agents"`
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

// BuildDashboard creates a hierarchical DashboardResponse from a flat list
// of sessions and optional orchestrator state. Sessions are grouped by rig
// (if matched to an orchestrator worker) or by ProjectName.
func BuildDashboard(sessions []*SessionState, orch *orchestrator.State) *DashboardResponse {
	resp := &DashboardResponse{}

	// 1. Build orchestrator summary + session-to-worker map.
	workerMap := map[string]*workerInfo{}
	if orch != nil && orch.Running {
		resp.Orchestrator = buildOrchestratorSummary(orch)
		workerMap = buildWorkerMap(orch)
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

	// 3. Build agent trees from top-level sessions and collect into groups.
	groupMap := make(map[string]*AgentGroup)
	var groupOrder []string

	for _, s := range sessions {
		if childIDs[s.SessionID] {
			continue
		}

		agent := buildAgent(s, workerMap, parentChildren)

		// Determine group key: rig name from orchestrator, or project name.
		groupKey := s.ProjectName
		if wi, ok := workerMap[s.SessionID]; ok && wi.Rig != "" {
			groupKey = wi.Rig
		}
		if groupKey == "" {
			groupKey = "unknown"
		}

		g, ok := groupMap[groupKey]
		if !ok {
			g = &AgentGroup{Name: groupKey}
			groupMap[groupKey] = g
			groupOrder = append(groupOrder, groupKey)
		}
		g.Agents = append(g.Agents, agent)
	}

	// 4. Annotate groups with orchestrator codebase status.
	if orch != nil {
		for _, cb := range orch.Codebases {
			if g, ok := groupMap[cb.Name]; ok {
				g.Status = cb.Status
			}
		}
	}

	// 5. Assemble ordered groups.
	resp.Groups = make([]*AgentGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		resp.Groups = append(resp.Groups, groupMap[key])
	}

	return resp
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
// file-based children attached to the agent tree. Delegates to
// ComputeSubagentSummary so the detector and REST path share one formula.
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
// ParentSessionID matches the parent. Passing nil or an empty slice for
// childSessions skips the file-based contribution. Returns nil when there are
// no subagents at all, so callers can leave Subagents unset on the wire.
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
		Working: inProcess, // in-process agents are always working
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

// buildOrchestratorSummary creates a lightweight summary from orchestrator state.
func buildOrchestratorSummary(orch *orchestrator.State) *OrchestratorSummary {
	summary := &OrchestratorSummary{
		Adapter: orch.Adapter,
		Running: orch.Running,
	}
	for _, wu := range orch.WorkUnits {
		summary.WorkUnits = append(summary.WorkUnits, WorkUnitSummary{
			ID:     wu.ID,
			Type:   wu.Type,
			Name:   wu.Name,
			Source: wu.Source,
			Total:  wu.Total,
			Done:   wu.Done,
		})
	}
	return summary
}

// buildWorkerMap creates a sessionID → workerInfo index from orchestrator state.
func buildWorkerMap(orch *orchestrator.State) map[string]*workerInfo {
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

	return m
}
