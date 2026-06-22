package relay

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// waitForControllerState polls the controller's reported link state until it
// reaches want, failing the test if it doesn't within a generous deadline.
func waitForControllerState(t *testing.T, c *PublishController, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if enabled, st := c.Status(); enabled && st.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, st := c.Status()
	t.Fatalf("controller did not reach state %q (last state %q, lastError %q)", want, st.State, st.LastError)
}

// TestPublishControllerStartStop verifies Apply(true) starts a forwarder that
// connects, and Apply(false) stops it so Status() reports disabled again.
func TestPublishControllerStartStop(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	snap := func() ([]*session.SessionState, []AgentInfo) { return nil, nil }
	c := NewPublishController(t.Context(), Identity{DaemonID: "d1", DaemonLabel: "lap"}, bc, snap, nil, nil, nil)

	if enabled, _ := c.Status(); enabled {
		t.Fatal("a fresh controller must report disabled")
	}

	c.Apply(true, tr.url, "")
	waitForControllerState(t, c, PublishConnected)

	c.Apply(false, tr.url, "")
	if enabled, _ := c.Status(); enabled {
		t.Fatal("Apply(false) must stop publishing")
	}
}

// TestPublishControllerIdempotent verifies re-applying the config already in
// effect keeps the same forwarder instance — no needless relay reconnect.
func TestPublishControllerIdempotent(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	snap := func() ([]*session.SessionState, []AgentInfo) { return nil, nil }
	c := NewPublishController(t.Context(), Identity{DaemonID: "d1"}, bc, snap, nil, nil, nil)

	c.Apply(true, tr.url, "tok")
	c.mu.Lock()
	first := c.fwd
	c.mu.Unlock()

	c.Apply(true, tr.url, "tok")
	c.mu.Lock()
	second := c.fwd
	c.mu.Unlock()

	if first == nil || first != second {
		t.Fatalf("re-applying the same config must not restart the forwarder (first=%p second=%p)", first, second)
	}
}

// TestPublishControllerReconfigure verifies changing the URL tears down the old
// link and connects to the new relay without a daemon relaunch.
func TestPublishControllerReconfigure(t *testing.T) {
	tr1 := newTestRelay(t)
	tr2 := newTestRelay(t)
	bc := newFakeBroadcaster()
	snap := func() ([]*session.SessionState, []AgentInfo) { return nil, nil }
	c := NewPublishController(t.Context(), Identity{DaemonID: "d1"}, bc, snap, nil, nil, nil)

	c.Apply(true, tr1.url, "")
	select {
	case <-tr1.conns:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder did not connect to the first relay")
	}

	c.Apply(true, tr2.url, "")
	select {
	case <-tr2.conns:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder did not connect to the new relay after reconfigure")
	}
}

// TestPublishControllerEmptyURLActsAsOff verifies enabled=true with a blank URL
// does not start a forwarder (mirrors the macOS env builder's old semantics).
func TestPublishControllerEmptyURLActsAsOff(t *testing.T) {
	bc := newFakeBroadcaster()
	c := NewPublishController(t.Context(), Identity{DaemonID: "d1"}, bc, nil, nil, nil, nil)
	c.Apply(true, "   ", "tok")
	if enabled, _ := c.Status(); enabled {
		t.Fatal("enabled with a blank URL must not activate publishing")
	}
}
