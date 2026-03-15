package stdin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"irrlicht/core/domain/event"
	"irrlicht/core/ports/inbound"
)

// Adapter reads a single JSON hook event from stdin and passes it to an EventHandler.
type Adapter struct {
	handler inbound.EventHandler
}

// New returns a new stdin Adapter that delegates to handler.
func New(handler inbound.EventHandler) *Adapter {
	return &Adapter{handler: handler}
}

// ReadAndHandle reads one event from stdin, validates the payload size, parses the
// JSON, and calls the handler. It returns the raw payload size alongside any error.
func (a *Adapter) ReadAndHandle() (payloadSize int, err error) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0, fmt.Errorf("failed to read stdin: %w", err)
	}
	payloadSize = len(input)
	if payloadSize > event.MaxPayloadSize {
		return payloadSize, fmt.Errorf("payload size %d exceeds maximum %d", payloadSize, event.MaxPayloadSize)
	}
	var evt event.HookEvent
	if err := json.Unmarshal(input, &evt); err != nil {
		return payloadSize, fmt.Errorf("failed to parse JSON: %w", err)
	}
	if err := a.handler.HandleEvent(&evt); err != nil {
		return payloadSize, err
	}
	return payloadSize, nil
}
