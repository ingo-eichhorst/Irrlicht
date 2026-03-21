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

// ConvoyState represents a convoy as returned by `gt convoy list --json`.
type ConvoyState struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
}
