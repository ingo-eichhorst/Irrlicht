package sensors

import (
	"context"
	"testing"
	"time"
)

// TestPane_invalidTargetClosesCleanly drives the sensor with a definitely-
// nonexistent tmux target. tmux returns nonzero, the sensor closes the
// channel cleanly, and the recorder can continue without it. This is the
// "tmux outage" path the production code handles silently.
func TestPane_invalidTargetClosesCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p := &Pane{Target: "definitely-not-a-real-tmux-session-12345:0.0", PollInterval: 20 * time.Millisecond}
	ch := p.Run(ctx)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("got a signal from an invalid target")
		}
	case <-time.After(time.Second):
		t.Fatal("sensor didn't close on invalid target within 1s")
	}
}
