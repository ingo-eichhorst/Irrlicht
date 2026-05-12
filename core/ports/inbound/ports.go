// Package inbound defines inbound port interfaces — contracts that
// inbound adapters (file watchers, HTTP handlers, etc.) satisfy to
// drive the application core.
//
// The agent-side port is Watcher (see watcher.go); the orchestrator
// side is OrchestratorWatcher (below).
package inbound

import (
	"context"

	"irrlicht/core/domain/orchestrator"
)

// OrchestratorWatcher monitors a multi-agent orchestration system and
// produces standardised state snapshots. Each implementation targets a
// specific orchestrator (Gas Town, etc.).
type OrchestratorWatcher interface {
	// Name returns the orchestrator identifier (e.g. "gastown").
	Name() string
	// Detected returns true if the orchestrator is installed/available.
	Detected() bool
	// Watch begins monitoring and blocks until ctx is cancelled.
	Watch(ctx context.Context) error
	// Subscribe returns a channel of orchestrator state snapshots.
	Subscribe() <-chan orchestrator.State
	// Unsubscribe removes a subscriber channel.
	Unsubscribe(ch <-chan orchestrator.State)
	// State returns the latest state snapshot, or nil if unavailable.
	State() *orchestrator.State
}
