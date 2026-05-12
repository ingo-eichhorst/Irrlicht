package inbound

import (
	"context"

	"irrlicht/core/domain/agent"
)

// Watcher is the inbound port the daemon consumes for transcript-file
// events. Identity() lets the daemon tag each event with its watcher's
// adapter identity instead of stamping the name on every event payload.
type Watcher interface {
	// Identity returns the metadata for the agent this watcher serves
	// (Name, DisplayName, IconSVGLight, IconSVGDark). Implementations
	// must return a non-zero Identity before being registered with the
	// SessionDetector (which panics on zero values).
	Identity() agent.Identity

	// Watch begins watching for transcript changes. Blocks until ctx is
	// cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context) error

	// Subscribe returns a channel that receives agent events.
	Subscribe() <-chan agent.Event

	// Unsubscribe removes a previously subscribed channel and closes it.
	Unsubscribe(ch <-chan agent.Event)
}
