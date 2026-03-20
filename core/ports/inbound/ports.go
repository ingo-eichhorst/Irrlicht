// Package inbound defines inbound port interfaces — contracts that inbound
// adapters (file watchers, HTTP handlers, etc.) satisfy to drive the
// application core.
package inbound

import (
	"context"

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
