package gastown

import "time"

// DaemonState represents the Gas Town daemon's runtime state
// as read from $GT_ROOT/daemon/state.json.
type DaemonState struct {
	Running        bool      `json:"running"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	HeartbeatCount int       `json:"heartbeat_count"`
}
