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
// JSON, and calls the handler. It returns the raw payload bytes, size, and any error.
func (a *Adapter) ReadAndHandle() (payloadSize int, rawInput []byte, err error) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read stdin: %w", err)
	}
	payloadSize = len(input)
	rawInput = input
	if payloadSize > event.MaxPayloadSize {
		return payloadSize, rawInput, fmt.Errorf("payload size %d exceeds maximum %d", payloadSize, event.MaxPayloadSize)
	}
	var evt event.HookEvent
	if err := json.Unmarshal(input, &evt); err != nil {
		return payloadSize, rawInput, fmt.Errorf("failed to parse JSON: %w", err)
	}
	if err := a.handler.HandleEvent(&evt); err != nil {
		return payloadSize, rawInput, err
	}
	return payloadSize, rawInput, nil
}
