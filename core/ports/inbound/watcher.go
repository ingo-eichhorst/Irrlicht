package inbound

import (
	"context"

	"irrlicht/core/domain/agent"
)

// Watcher is the new inbound port introduced in #159 Phase A.4. It mirrors
// AgentWatcher's shape (lifecycle + event stream) and adds Identity() so
// the daemon can tag events with their watcher's identity without
// bouncing through the redundant agent.Event.Adapter field. In PR5 the
// daemon switches to consuming this port exclusively, AgentWatcher is
// deleted, and agent.Event.Adapter is dropped.
//
// PR4 (this PR) leaves AgentWatcher intact and has every existing
// watcher implementation satisfy both interfaces by gaining an
// Identity() method.
type Watcher interface {
	// Identity returns the metadata for the agent this watcher serves
	// (Name, DisplayName, IconSVGLight, IconSVGDark). Stored on the
	// watcher at construction time via the new WithIdentity() builder
	// method.
	Identity() agent.Identity

	// Watch begins watching for transcript changes. Blocks until ctx is
	// cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context) error

	// Subscribe returns a channel that receives agent events.
	Subscribe() <-chan agent.Event

	// Unsubscribe removes a previously subscribed channel and closes it.
	Unsubscribe(ch <-chan agent.Event)
}
