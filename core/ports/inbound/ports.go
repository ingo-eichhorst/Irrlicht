// Package inbound defines inbound port interfaces — contracts that inbound
// adapters (file watchers, HTTP handlers, etc.) satisfy to drive the
// application core.
package inbound

import (
	"context"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/orchestrator"
)

// AgentWatcher watches a directory tree for agent transcript file changes,
// emitting events for new sessions, activity, and removals. Each
// implementation targets a specific agent (Claude Code, Codex, etc.).
type AgentWatcher interface {
	// Watch begins watching for transcript changes. It blocks until ctx is
	// cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context) error
	// Subscribe returns a channel that receives agent events.
	Subscribe() <-chan agent.Event
	// Unsubscribe removes a previously subscribed channel and closes it.
	Unsubscribe(ch <-chan agent.Event)
}

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
