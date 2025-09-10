package inbound

import (
	"irrlicht/hook/domain/event"
)

// EventHandler defines the inbound port for handling Claude Code hook events
type EventHandler interface {
	// HandleEvent processes a single hook event
	HandleEvent(event *event.HookEvent) error
	
	// HandleEventStream processes a stream of events
	HandleEventStream(events <-chan *event.HookEvent, errors chan<- error)
	
	// Stop gracefully stops the event handler
	Stop() error
}

// EventReceiver defines the interface for receiving events from external sources
type EventReceiver interface {
	// ReceiveEvents starts receiving events and sends them to the provided channel
	ReceiveEvents(events chan<- *event.HookEvent, errors chan<- error) error
	
	// Stop stops the event receiver
	Stop() error
}

// EventStreamProcessor processes multiple events concurrently
type EventStreamProcessor interface {
	// ProcessEvents processes a batch of events
	ProcessEvents(events []*event.HookEvent) []ProcessingResult
	
	// ProcessEventsConcurrently processes events with controlled concurrency
	ProcessEventsConcurrently(events []*event.HookEvent, maxConcurrency int) []ProcessingResult
}

// ProcessingResult holds the result of processing an event
type ProcessingResult struct {
	Event   *event.HookEvent
	Success bool
	Error   error
}

// CommandHandler defines the interface for handling commands (future extension)
type CommandHandler interface {
	// HandleCommand processes a command
	HandleCommand(command Command) (CommandResult, error)
}

// Command represents a command to be processed
type Command interface {
	GetType() string
	GetPayload() interface{}
}

// CommandResult represents the result of command processing
type CommandResult interface {
	IsSuccess() bool
	GetData() interface{}
	GetError() error
}