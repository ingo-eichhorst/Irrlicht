package sensors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Pane periodically snapshots the visible state of a tmux pane via
// `tmux capture-pane -p -t <Target>`. Emits a Signal each time the
// snapshot text changes; suppresses no-op ticks so signals.jsonl stays
// small. A tmux outage (binary missing, target gone) closes the channel
// cleanly so the recorder can continue with the remaining sensors.
//
// Kind: "snapshot". Payload:
//
//	{"snapshot": "<full pane text>", "sha256_prefix": "<8 hex chars>"}
//
// The sha256_prefix lets the Phase 3 synth dedupe and compare snapshots
// without re-hashing.
type Pane struct {
	// Target identifies the tmux pane, e.g. "recorder:0.0".
	Target string
	// PollInterval defaults to 250ms (per #268 Phase 1 spec).
	PollInterval time.Duration
	// Lines limits how many lines back to capture (-S). 0 means tmux default.
	Lines int
}

const paneName = "pane"

// Name implements Sensor.
func (p *Pane) Name() string { return paneName }

// Run implements Sensor.
func (p *Pane) Run(ctx context.Context) <-chan Signal {
	out := make(chan Signal, 8)
	poll := p.PollInterval
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	go func() {
		defer close(out)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()

		var prevHash string
		for {
			snap, err := capturePane(ctx, p.Target, p.Lines)
			if err != nil {
				// Most likely: tmux not installed or target gone.
				// Exit cleanly so the recorder logs once and continues.
				return
			}
			h := sha256.Sum256([]byte(snap))
			hashPrefix := hex.EncodeToString(h[:4])
			if hashPrefix != prevHash {
				payload, _ := MarshalPayload(struct {
					Snapshot     string `json:"snapshot"`
					SHA256Prefix string `json:"sha256_prefix"`
				}{Snapshot: snap, SHA256Prefix: hashPrefix})
				select {
				case out <- Signal{
					Ts:      time.Now().UTC(),
					Sensor:  paneName,
					Kind:    "snapshot",
					Payload: json.RawMessage(payload),
				}:
				case <-ctx.Done():
					return
				}
				prevHash = hashPrefix
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

// capturePane shells out to `tmux capture-pane -p -t <target>`. Returns the
// pane text or an error if tmux fails (target missing, binary not installed).
func capturePane(ctx context.Context, target string, lines int) (string, error) {
	args := []string{"capture-pane", "-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Strip trailing whitespace lines that tmux pads.
	return strings.TrimRight(string(out), "\n"), nil
}

