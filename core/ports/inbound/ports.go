// Package inbound defines inbound port interfaces — contracts that inbound
// adapters (file watchers, HTTP handlers, etc.) satisfy to drive the
// application core.
package inbound

import (
	"context"

	"irrlicht/core/domain/gastown"
	"irrlicht/core/domain/transcript"
)

// AgentWatcher watches a directory tree for agent transcript file changes,
// emitting events for new sessions, activity, and removals. Each
// implementation targets a specific agent (Claude Code, Codex, etc.).
type AgentWatcher interface {
	// Watch begins watching for transcript changes. It blocks until ctx is
	// cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context) error
	// Subscribe returns a channel that receives transcript events.
	Subscribe() <-chan transcript.TranscriptEvent
	// Unsubscribe removes a previously subscribed channel and closes it.
	Unsubscribe(ch <-chan transcript.TranscriptEvent)
}

// GasTownCollector detects Gas Town presence, resolves GT_ROOT, and watches
// the daemon state file for changes.
type GasTownCollector interface {
	// Detected returns true if a valid Gas Town installation was found.
	Detected() bool
	// Root returns the resolved GT_ROOT path, or "" if not detected.
	Root() string
	// DaemonState returns the latest parsed daemon state, or nil if unavailable.
	DaemonState() *gastown.DaemonState
	// Watch begins watching daemon/state.json for changes. It blocks until
	// ctx is cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context) error
	// Subscribe returns a channel that receives daemon state updates whenever
	// the watched file changes on disk.
	Subscribe() <-chan gastown.DaemonState
	// Unsubscribe removes a previously subscribed channel.
	Unsubscribe(ch <-chan gastown.DaemonState)
}
