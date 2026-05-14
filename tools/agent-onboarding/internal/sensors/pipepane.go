package sensors

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"time"
)

// PipePane tails the file written by `tmux pipe-pane`. This captures the
// FULL scrollback history of a pane — what crossed the bottom of the
// terminal — whereas Pane captures only what is currently visible.
// PR #269 surfaced the value of having both: some signals (codex's
// "Booting MCP" gate) are visible only on the live pane before tmux
// clears them; others (long tool-call outputs) only land in the pipe-pane
// log after they scroll past.
//
// The recorder is responsible for setting up the tmux pipe-pane redirect
// before the sensor starts (e.g. `tmux pipe-pane -o -t <target> 'cat >> <Path>'`).
// PipePane just tails the file.
//
// Kind: "line". Payload:
//
//	{"line": "<raw text without trailing \\n>"}
type PipePane struct {
	// Path is the file `tmux pipe-pane` writes to.
	Path string
	// PollInterval defaults to 100ms if zero.
	PollInterval time.Duration
}

const pipePaneName = "pipepane"

// Name implements Sensor.
func (p *PipePane) Name() string { return pipePaneName }

// Run implements Sensor.
func (p *PipePane) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 32)
	poll := p.PollInterval
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	go func() {
		defer close(out)

		var f *os.File
		for f == nil {
			var err error
			f, err = os.Open(p.Path)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
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
			for {
				line, err := r.ReadString('\n')
				if len(line) > 0 {
					trim := trimEOL(line)
					payload, _ := MarshalPayload(struct {
						Line string `json:"line"`
					}{Line: trim})
					select {
					case out <- Signal{
						Ts:      time.Now().UTC(),
						Sensor:  pipePaneName,
						Kind:    "line",
						Payload: json.RawMessage(payload),
					}:
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
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

// trimEOL strips a single trailing \n and any preceding \r.
func trimEOL(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\n' {
		s = s[:n-1]
	}
	if n := len(s); n > 0 && s[n-1] == '\r' {
		s = s[:n-1]
	}
	return s
}
