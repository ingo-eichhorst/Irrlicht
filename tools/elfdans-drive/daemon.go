package main

// The synthetic daemon: one relay link, a table of made-up sessions, and the
// transitions a scenario drives them through. Everything on the wire comes
// from irrlichd's own relay.Forwarder — hello, daemon_snapshot, and one push
// envelope per transition (docs/relay-protocol.md) — so this file only has
// to supply what the forwarder asks of a daemon: an identity, a snapshot
// function, and a push broadcaster.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

const (
	// connectTimeout bounds the wait for the relay to accept our daemon
	// hello. The forwarder retries with backoff forever, so without a
	// deadline a driver pointed at a dead port, or holding a token the relay
	// rejects, would sit there looking busy — and a driver that cannot
	// connect must fail loudly rather than skip, exactly like the checks in
	// tools/elfdans-rig.sh.
	connectTimeout = 15 * time.Second

	// broadcastTimeout bounds handing one frame to the forwarder. Reaching
	// it means the forwarder stopped draining, which makes every later
	// expectation meaningless — so it is an error, never a dropped frame.
	broadcastTimeout = 5 * time.Second

	// cleanupDrain gives the deletes we emit on the way out time to reach
	// the relay before the socket closes under them.
	cleanupDrain = 300 * time.Millisecond

	// syntheticAdapter is the adapter name every session this driver invents
	// carries. It is what core/domain/notify uses as the human label in a
	// burst summary, so a summary produced here reads "4 agents need
	// attention · elfdans-drive ×4" — the same shape four real sessions of
	// one agent kind produce.
	syntheticAdapter = "elfdans-drive"
)

// bus is the daemon-side PushBroadcaster the forwarder subscribes to. A real
// daemon's push service drops frames for a slow subscriber; this one refuses
// to, because a driver whose evidence quietly went missing is worse than a
// driver that stops: every expectation downstream would then be graded
// against a transition that never left the process.
type bus struct {
	mu   sync.Mutex
	subs map[chan outbound.PushMessage]struct{}
	err  error
}

func newBus() *bus { return &bus{subs: make(map[chan outbound.PushMessage]struct{})} }

func (b *bus) Subscribe() chan outbound.PushMessage {
	ch := make(chan outbound.PushMessage, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *bus) Unsubscribe(ch chan outbound.PushMessage) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Broadcast hands one frame to every subscriber. No subscriber at all means
// the forwarder is not attached — the frame has nowhere to go, which is a
// failure of the run and not a quiet no-op.
func (b *bus) Broadcast(msg outbound.PushMessage) {
	b.mu.Lock()
	subs := make([]chan outbound.PushMessage, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	if len(subs) == 0 {
		b.setErr(fmt.Errorf("a %s frame had no subscriber — the relay link is down, so it went nowhere", msg.Type))
		return
	}
	for _, ch := range subs {
		select {
		case ch <- msg:
		case <-time.After(broadcastTimeout):
			b.setErr(fmt.Errorf("the relay forwarder stopped draining after %s — a %s frame was not sent", broadcastTimeout, msg.Type))
		}
	}
}

// setErr keeps the FIRST failure: it is the one that explains the rest.
func (b *bus) setErr(err error) {
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.mu.Unlock()
}

// take returns and clears the pending failure, so a caller reports it once,
// against the transition that produced it.
func (b *bus) take() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.err
	b.err = nil
	return err
}

// daemon is one synthetic irrlichd: an identity, the sessions it claims, and
// the relay link it claims them over.
type daemon struct {
	url   string
	token string
	id    string
	label string

	bus *bus
	fwd *relay.Forwarder

	// connected is the driver's own record of the link, not the forwarder's:
	// Forwarder.Run returns without restating its state when the context is
	// what ended it, so Status() still reads "connected" after a deliberate
	// drop — and cleanup would then emit deletes into a closed socket and
	// report a failure the run never had.
	connected bool
	cancel    context.CancelFunc
	done      chan struct{}

	mu       sync.Mutex
	order    []string // creation order, so the snapshot and cleanup are deterministic
	sessions map[string]*session.SessionState
}

func newDaemon(url, token, id, label string) *daemon {
	return &daemon{
		url:      url,
		token:    token,
		id:       id,
		label:    label,
		bus:      newBus(),
		sessions: make(map[string]*session.SessionState),
	}
}

// add invents one session in the given state. Sessions added before connect
// ride in the daemon_snapshot, where §8.4 makes them a silent first
// sighting — which is what every scenario needs before it can drive an edge
// that notifies.
func (d *daemon) add(id, state, parent string) {
	now := time.Now().Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.sessions[id]; !dup {
		d.order = append(d.order, id)
	}
	d.sessions[id] = &session.SessionState{
		Version:         1,
		SessionID:       id,
		State:           state,
		Adapter:         syntheticAdapter,
		Model:           "synthetic",
		CWD:             "/tmp/elfdans-drive",
		ProjectName:     syntheticAdapter,
		ParentSessionID: parent,
		FirstSeen:       now,
		UpdatedAt:       now,
		Confidence:      "high",
		EventCount:      1,
		LastEvent:       "elfdans-drive",
	}
}

// snapshot is the forwarder's SnapshotFunc: the daemon_snapshot sent once per
// (re)connect. Copies are handed out because the forwarder marshals them on
// its own goroutine, and the next transition mutates the originals.
func (d *daemon) snapshot() ([]*session.SessionState, []relay.AgentInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*session.SessionState, 0, len(d.order))
	for _, id := range d.order {
		cp := *d.sessions[id]
		out = append(out, &cp)
	}
	return out, []relay.AgentInfo{{Name: syntheticAdapter, DisplayName: "elfdans-drive (synthetic)"}}
}

// connect dials the relay and waits until it has accepted the daemon hello.
// The forwarder reports auth_failed distinctly from a transient drop, so a
// token the relay rejects is named as such instead of timing out.
func (d *daemon) connect(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)
	d.fwd = relay.NewForwarder(d.url, relay.Identity{DaemonID: d.id, DaemonLabel: d.label}, relay.ForwarderDeps{
		Token:    d.token,
		Push:     d.bus,
		Snapshot: d.snapshot,
	})
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		d.fwd.Run(ctx)
	}()

	deadline := time.Now().Add(connectTimeout)
	for {
		st := d.fwd.Status()
		switch {
		case st.State == relay.PublishConnected:
			d.connected = true
			ok("connected to %s as daemon %s", st.URL, d.id)
			return nil
		case st.State == relay.PublishAuthFailed:
			return fmt.Errorf("relay %s rejected the token — issue one with `irrlichtrelay token issue`, or pass -token", st.URL)
		case time.Now().After(deadline):
			return fmt.Errorf("relay %s did not accept a daemon hello within %s (state %q: %s)",
				st.URL, connectTimeout, st.State, orNone(st.LastError))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// transition moves one session to a new state and forwards the
// session_updated the relay's observer reads as an edge.
func (d *daemon) transition(id, state string) error {
	d.mu.Lock()
	s, known := d.sessions[id]
	if !known {
		d.mu.Unlock()
		return fmt.Errorf("no session %q to transition", id)
	}
	s.State = state
	s.UpdatedAt = time.Now().Unix()
	s.EventCount++
	cp := *s
	d.mu.Unlock()

	d.bus.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: &cp})
	if err := d.bus.take(); err != nil {
		return fmt.Errorf("%s → %s: %w", id, state, err)
	}
	return nil
}

// cleanup deletes every session this run invented and closes the link, so a
// device test does not leave phantom rows in the dashboard the tester is
// reading. A link that is already down needs neither: the hub drops a
// disconnected daemon's cached sessions itself.
func (d *daemon) cleanup() error {
	if !d.connected {
		d.disconnect()
		return nil
	}
	d.mu.Lock()
	gone := make([]*session.SessionState, 0, len(d.order))
	for _, id := range d.order {
		cp := *d.sessions[id]
		gone = append(gone, &cp)
	}
	d.mu.Unlock()
	for _, s := range gone {
		d.bus.Broadcast(outbound.PushMessage{Type: outbound.PushTypeDeleted, Session: s})
	}
	time.Sleep(cleanupDrain)
	info("deleted %d synthetic session(s) from the relay", len(gone))
	d.disconnect()
	return d.bus.take()
}

// disconnect drops the relay link and waits for the forwarder to unwind.
// This is also the disconnect scenario's whole payload: the §6.4 watchdog
// starts its grace the moment the hub sees the socket go.
func (d *daemon) disconnect() {
	if d.cancel == nil {
		return
	}
	d.cancel()
	select {
	case <-d.done:
	case <-time.After(connectTimeout):
	}
	d.cancel = nil
	d.connected = false
}

func orNone(s string) string {
	if s == "" {
		return "no error reported"
	}
	return s
}
