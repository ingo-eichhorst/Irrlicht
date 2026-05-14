// Package sensors defines the contract every recording probe satisfies and
// the central writer that fans signals into signals.jsonl.
//
// Each sensor (transcript, pane, pipepane, pty, proc, fs, net) runs in its
// own goroutine, owns its underlying resource (file handle, subprocess,
// poller), and emits Signal records over an output channel. The recorder
// merges every sensor's channel and serializes the merged stream as JSONL.
//
// Phase 3 of #268 will prune which sensors are enabled per agent based on
// the synthesis run's sensor_relevance.json; until then, the recorder runs
// every sensor that doesn't error on construction.
package sensors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Signal is one observation emitted by a sensor at a point in time.
// Serialized to one JSONL line in signals.jsonl.
type Signal struct {
	Ts      time.Time       `json:"ts"`
	Sensor  string          `json:"sensor"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Sensor is one observation source.
type Sensor interface {
	// Name is the value placed in Signal.Sensor for every signal this
	// source emits.
	Name() string

	// Run starts the sensor and returns a channel of Signals it owns.
	// The channel closes when the sensor finishes (either because the
	// underlying source ended or ctx was cancelled). Sensors must
	// observe ctx.Done() to avoid leaking goroutines on shutdown.
	Run(ctx context.Context) <-chan Signal
}

// WriteSignals drains a channel of Signals into the given writer as JSONL
// (one Signal per line). Returns nil when in is closed, ctx.Err() if ctx
// is cancelled first, or an encoding error.
func WriteSignals(ctx context.Context, w io.Writer, in <-chan Signal) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s, ok := <-in:
			if !ok {
				return nil
			}
			if err := enc.Encode(s); err != nil {
				return fmt.Errorf("encode signal: %w", err)
			}
		}
	}
}

// Merge fans signals from multiple sensor channels into a single channel.
// The returned channel closes when every input channel has closed.
func Merge(in ...<-chan Signal) <-chan Signal {
	out := make(chan Signal, 64)
	var wg sync.WaitGroup
	wg.Add(len(in))
	for _, c := range in {
		go func(c <-chan Signal) {
			defer wg.Done()
			for s := range c {
				out <- s
			}
		}(c)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// MarshalPayload is a convenience for sensor implementations: marshal a
// payload struct into json.RawMessage so the Signal stays cheap to copy.
func MarshalPayload(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return b, nil
}
