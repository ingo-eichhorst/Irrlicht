// Package orchestrator defines the standardised data model that all
// multi-agent orchestration system adapters map their native state to.
package orchestrator

import "time"

// State is the standardised snapshot all orchestrator adapters produce.
type State struct {
	// Adapter identifies the orchestrator (e.g. "gastown").
	Adapter string `json:"adapter"`
	// Running indicates whether the orchestrator daemon/process is active.
	Running bool `json:"running"`
	// Root is the filesystem root of the orchestrator workspace.
	Root string `json:"root,omitempty"`
	// GlobalAgents are orchestrator-level agents not scoped to a codebase
	// (e.g. Gas Town's mayor, deacon).
	GlobalAgents []GlobalAgent `json:"global_agents,omitempty"`
	// Codebases are the repositories/projects managed by the orchestrator.
	Codebases []Codebase `json:"codebases,omitempty"`
	// WorkUnits are trackable units of work (convoys, task lists, etc.).
	WorkUnits []workUnit `json:"work_units,omitempty"`
	// Health is the orchestrator's daemon/watchdog health, if reported.
	Health *Health `json:"health,omitempty"`
	// UpdatedAt is when this state snapshot was produced.
	UpdatedAt time.Time `json:"updated_at"`
	// RoleIcons maps role names to display emojis. Set by the adapter,
	// used by the domain for CWD-based role derivation fallback.
	RoleIcons map[string]string `json:"-"`
}

// Health reports the orchestrator daemon and watchdog liveness. All fields are
// optional so adapters that can't observe a signal simply omit it.
type Health struct {
	DaemonRunning  bool      `json:"daemon_running"`
	PID            int       `json:"pid,omitempty"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	HeartbeatCount int       `json:"heartbeat_count,omitempty"`
	BootRunning    bool      `json:"boot_running,omitempty"`
	BootDegraded   bool      `json:"boot_degraded,omitempty"`
	SessionAlive   bool      `json:"session_alive,omitempty"`
}

// GlobalAgent represents an orchestrator-level agent not scoped to a codebase.
type GlobalAgent struct {
	Role        string `json:"role"`
	Icon        string `json:"icon,omitempty"`        // Display icon (e.g. "🎩"), set by each adapter.
	Description string `json:"description,omitempty"` // Human-readable role description, set by each adapter.
	SessionID   string `json:"session_id,omitempty"`
	State       string `json:"state"`
}

// Codebase represents a repository managed by the orchestrator.
type Codebase struct {
	Name      string     `json:"name"`
	RepoURL   string     `json:"repo_url,omitempty"`
	Status    string     `json:"status,omitempty"`
	Worktrees []Worktree `json:"worktrees,omitempty"`
}

// Worktree represents a git worktree within a codebase.
type Worktree struct {
	Path    string   `json:"path"`
	Branch  string   `json:"branch,omitempty"`
	IsMain  bool     `json:"is_main"`
	Workers []Worker `json:"workers,omitempty"`
}

// Worker represents a coding agent operating within a worktree.
type Worker struct {
	Role        string `json:"role"`
	Icon        string `json:"icon,omitempty"`        // Display icon, set by each adapter.
	Description string `json:"description,omitempty"` // Human-readable role description, set by each adapter.
	Name        string `json:"name,omitempty"`
	ID          string `json:"id,omitempty"` // bead ID, task ID, etc.
	SessionID   string `json:"session_id,omitempty"`
	State       string `json:"state"`
}

// workUnit represents a trackable unit of work with progress.
type workUnit struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // "convoy", "task_list"
	Name   string `json:"name"`
	Source string `json:"source"` // "gastown", "claude_tasks"
	Total  int    `json:"total"`
	Done   int    `json:"done"`
}
