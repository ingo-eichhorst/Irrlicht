package sensors

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"time"
)

// Transcript tails a single transcript file and emits one Signal per
// appended line. The file may not exist when Run starts; Transcript polls
// until it appears, then reads each newline-terminated chunk as it lands.
//
// Kind is always "line"; Payload carries the raw line text:
//
//	{"line": "<raw text without trailing \\n>"}
//
// Phase 3 synthesis runs richer parsers on the raw text; the recorder is
// intentionally format-agnostic.
type Transcript struct {
	// Path is the transcript file to tail (e.g. an agent's JSONL log).
	Path string
	// PollInterval defaults to 100ms if zero.
	PollInterval time.Duration
}

// transcriptName is the value reported in Signal.Sensor.
const transcriptName = "transcript"

// Name implements Sensor.
func (t *Transcript) Name() string { return transcriptName }

// Run implements Sensor.
func (t *Transcript) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 32)
	poll := t.PollInterval
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	go func() {
		defer close(out)

		// Wait for the file to appear.
		var f *os.File
		for f == nil {
			var err error
			f, err = os.Open(t.Path)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
				// Other open errors aren't recoverable by polling; bail.
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(poll):
			}
		}
		defer f.Close()

		r := bufio.NewReader(f)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()

		for {
			// Drain whatever is currently available.
			for {
				line, err := r.ReadString('\n')
				if len(line) > 0 {
					// Strip trailing newline (and any trailing CR) before payload.
					trim := line
					if n := len(trim); n > 0 && trim[n-1] == '\n' {
						trim = trim[:n-1]
					}
					if n := len(trim); n > 0 && trim[n-1] == '\r' {
						trim = trim[:n-1]
					}
					payload, _ := MarshalPayload(struct {
						Line string `json:"line"`
					}{Line: trim})
					sig := Signal{
						Ts:      time.Now().UTC(),
						Sensor:  transcriptName,
						Kind:    "line",
						Payload: json.RawMessage(payload),
					}
					select {
					case out <- sig:
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
					// EOF or partial-line at end-of-file — wait for more.
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}
