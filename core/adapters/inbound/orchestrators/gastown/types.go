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
	RoleBoot     = "boot"
	RoleDog      = "dog"
)

// roleMeta provides display metadata for each Gas Town role.
// Keeping this in the adapter (not the domain model) ensures the domain
// stays orchestrator-agnostic; any future orchestrator defines its own metadata.
var roleMeta = map[string]struct{ Icon, Desc string }{
	RoleMayor:    {"\U0001F3A9", "Coordinates all rigs and global state"},
	RoleDeacon:   {"\U0001F4CB", "Assigns tasks to polecats, manages the queue"},
	RoleWitness:  {"\U0001F989", "Reviews polecat work before merging"},
	RoleRefinery: {"\U0001F3ED", "Merges accepted work into the main branch"},
	RolePolecat:  {"\U0001F477", "Executes a single task in an isolated worktree"},
	RoleCrew:     {"\U0001F9D1\u200D\U0001F4BB", "Supports a polecat with research or sub-tasks"},
	RoleBoot:     {"\U0001F97E", "Watchdog for the Deacon"},
	RoleDog:      {"\U0001F415", "Cross-rig infrastructure worker"},
}


// daemonState represents the Gas Town daemon's runtime state
// as read from $GT_ROOT/daemon/state.json.
type daemonState struct {
	Running        bool      `json:"running"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	HeartbeatCount int       `json:"heartbeat_count"`
}

// rigState represents the status of a single Gas Town rig,
// as returned by `gt rig list --json`.
type rigState struct {
	Name         string `json:"name"`
	BeadsPrefix  string `json:"beads_prefix"`
	Status       string `json:"status"`
	Witness      string `json:"witness"`
	Refinery     string `json:"refinery"`
	PolecatCount int    `json:"polecats"`
	CrewCount    int    `json:"crew"`
}

// polecatState represents a single polecat worker,
// as returned by `gt polecat list --all --json`.
type polecatState struct {
	Rig            string `json:"rig"`
	Name           string `json:"name"`
	State          string `json:"state"`
	Issue          string `json:"issue"`
	SessionRunning bool   `json:"session_running"`
}

// dogState represents a dog worker as returned by `gt dog list --json`.
type dogState struct {
	Name       string            `json:"name"`
	State      string            `json:"state"`
	LastActive string            `json:"last_active"`
	Worktrees  map[string]string `json:"worktrees"` // rig name → worktree path
}

// bootStatus represents boot status as returned by `gt boot status --json`.
type bootStatus struct {
	BootDir      string `json:"boot_dir"`
	Degraded     bool   `json:"degraded"`
	Running      bool   `json:"running"`
	SessionAlive bool   `json:"session_alive"`
}
