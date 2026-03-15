package inbound

import "irrlicht/core/domain/event"

// EventHandler processes a hook event.
type EventHandler interface {
	HandleEvent(evt *event.HookEvent) error
}
