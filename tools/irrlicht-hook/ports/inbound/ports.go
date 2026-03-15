package inbound

import "irrlicht/hook/domain/event"

// EventHandler processes a hook event.
type EventHandler interface {
	HandleEvent(evt *event.HookEvent) error
}
