package agent

import "time"

// StatePolicy controls adapter-specific session state behavior.
//
// SessionDetector owns the shared state machine, while each adapter can
// provide policy overrides for heuristics that differ by transcript format.
// Today this is used for stale-tool-call waiting detection.
type StatePolicy struct {
	// EnableStaleToolTimer enables the stale open-tool heuristic that
	// transitions working → waiting after a timeout with no activity.
	EnableStaleToolTimer bool

	// StaleToolTimeout overrides the detector default timeout when > 0.
	StaleToolTimeout time.Duration
}

// DefaultStatePolicy returns the fallback behavior when an adapter doesn't
// provide an explicit policy.
func DefaultStatePolicy() StatePolicy {
	return StatePolicy{EnableStaleToolTimer: true}
}
