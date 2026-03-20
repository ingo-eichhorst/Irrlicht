package gastown

import "time"

// Role constants for Gas Town agents.
const (
	RoleMayor    = "mayor"
	RoleDeacon   = "deacon"
	RoleWitness  = "witness"
	RoleRefinery = "refinery"
	RolePolecat  = "polecat"
	RoleCrew     = "crew"
)

// WorkUnit type discriminators.
const (
	WorkUnitConvoy   = "convoy"
	WorkUnitTaskList = "task_list"
)

// WorkUnit source identifiers.
const (
	SourceGasTown    = "gastown"
	SourceClaudeTasks = "claude_tasks"
)

// DaemonState represents the Gas Town daemon's runtime state
// as read from $GT_ROOT/daemon/state.json.
type DaemonState struct {
	Running        bool      `json:"running"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	HeartbeatCount int       `json:"heartbeat_count"`
}

// RigState represents the status of a single Gas Town rig,
// as returned by `gt rig list --json`.
type RigState struct {
	Name         string `json:"name"`
	BeadsPrefix  string `json:"beads_prefix"`
	Status       string `json:"status"`
	Witness      string `json:"witness"`
	Refinery     string `json:"refinery"`
	PolecatCount int    `json:"polecats"`
	CrewCount    int    `json:"crew"`
}

// PolecatState represents a single polecat worker,
// as returned by `gt polecat list --all --json`.
type PolecatState struct {
	Rig            string `json:"rig"`
	Name           string `json:"name"`
	State          string `json:"state"`
	Issue          string `json:"issue"`
	SessionRunning bool   `json:"session_running"`
}

// Snapshot is the flat Gas Town state (legacy format, kept for backward compat).
type Snapshot struct {
	Detected  bool           `json:"detected"`
	Daemon    *DaemonState   `json:"daemon,omitempty"`
	Rigs      []RigState     `json:"rigs"`
	Polecats  []PolecatState `json:"polecats"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// --- Enriched gastown_state model (per ADR) ---

// GlobalAgent represents a top-level Gas Town agent (mayor, deacon).
type GlobalAgent struct {
	Role      string `json:"role"`
	SessionID string `json:"session_id,omitempty"`
	State     string `json:"state"`
}

// Agent represents an agent within a worktree.
type Agent struct {
	Role      string `json:"role"`
	Name      string `json:"name,omitempty"`
	BeadID    string `json:"bead_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	State     string `json:"state"`
}

// Worktree represents a git worktree within a codebase.
type Worktree struct {
	Path   string  `json:"path"`
	Branch string  `json:"branch,omitempty"`
	IsMain bool    `json:"is_main"`
	Agents []Agent `json:"agents"`
}

// Codebase represents a rig/codebase with its worktrees and agents.
type Codebase struct {
	Rig       string     `json:"rig"`
	RepoURL   string     `json:"repo_url,omitempty"`
	Status    string     `json:"status,omitempty"`
	Worktrees []Worktree `json:"worktrees"`
}

// WorkUnit represents a convoy or task list with progress tracking.
type WorkUnit struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	SessionID string `json:"session_id,omitempty"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
}

// ConvoyState represents a convoy as returned by `gt convoy list --json`.
type ConvoyState struct {
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Done   int    `json:"done"`
}

// State is the enriched Gas Town state pushed via WebSocket.
type State struct {
	Type         string        `json:"type"`
	Running      bool          `json:"running"`
	GTRoot       string        `json:"gt_root"`
	GlobalAgents []GlobalAgent `json:"global_agents"`
	Codebases    []Codebase    `json:"codebases"`
	WorkUnits    []WorkUnit    `json:"work_units"`
	UpdatedAt    time.Time     `json:"updated_at"`
}
