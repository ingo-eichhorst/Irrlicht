package relay

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

const forwardWriteTimeout = 10 * time.Second

// helloAckTimeout bounds the wait for the relay's reply to our hello. The relay
// answers immediately (a hello_ack on success, a CloseRevoked close on a bad
// token), so this only guards against a hung or incompatible relay.
const helloAckTimeout = 10 * time.Second

// Publish link states reported by Status() and surfaced over the daemon's
// /api/v1/relay/publish endpoint so the macOS app can show a publish-connection
// indicator (issue #718). PublishAuthFailed is kept distinct from
// PublishDisconnected so the app can tell "relay rejected the token" apart from
// a transient network drop.
const (
	PublishConnecting   = "connecting"
	PublishConnected    = "connected"
	PublishAuthFailed   = "auth_failed"
	PublishDisconnected = "disconnected"
)

// maxRelayFrameBytes caps a single inbound frame from the relay so a malicious
// or buggy relay can't exhaust daemon memory. The daemon only reads hello_ack
// and control frames, so this is generous headroom.
const maxRelayFrameBytes = 1 << 20

// streamPath is the relay's WebSocket endpoint, shared with the daemon and
// every client.
const streamPath = "/api/v1/sessions/stream"

// normalizeRelayURL turns IRRLICHT_RELAY_URL into a dialable WebSocket URL:
// it supplies a ws:// scheme (rewriting http(s)://) and appends the stream
// path when absent, so the env var can be a bare base like ws://host:7839.
func normalizeRelayURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	switch {
	case strings.HasPrefix(s, "http://"):
		s = "ws://" + strings.TrimPrefix(s, "http://")
	case strings.HasPrefix(s, "https://"):
		s = "wss://" + strings.TrimPrefix(s, "https://")
	case !strings.HasPrefix(s, "ws://") && !strings.HasPrefix(s, "wss://"):
		s = "ws://" + s
	}
	s = strings.TrimRight(s, "/")
	if !strings.HasSuffix(s, streamPath) {
		s += streamPath
	}
	return s
}

// SnapshotFunc returns the daemon's current sessions and adapter registry,
// captured to build the daemon_snapshot sent on each (re)connect so the relay
// reconciles its cache without waiting for the next per-session delta.
type SnapshotFunc func() ([]*session.SessionState, []AgentInfo)

// Forwarder subscribes to the daemon's push broadcaster and pushes every
// session event out to a relay server over a WebSocket, reconnecting with
// exponential backoff. Pushing out (rather than accepting inbound) means the
// daemon needs no reachable address — it works behind NAT.
type Forwarder struct {
	url      string
	identity Identity
	token    string
	push     outbound.PushBroadcaster
	snapshot SnapshotFunc
	logger   outbound.Logger

	dialer     *websocket.Dialer
	minBackoff time.Duration
	maxBackoff time.Duration

	mu      sync.Mutex
	state   string // one of the Publish* constants
	lastErr string
}

// NewForwarder builds a Forwarder targeting relayURL. push and snapshot are
// required; logger may be nil. token is sent in the hello for an auth-enabled
// relay and may be empty (a no-auth relay ignores it).
func NewForwarder(relayURL string, id Identity, token string, push outbound.PushBroadcaster, snapshot SnapshotFunc, logger outbound.Logger) *Forwarder {
	return &Forwarder{
		url:        normalizeRelayURL(relayURL),
		identity:   id,
		token:      token,
		push:       push,
		snapshot:   snapshot,
		logger:     logger,
		dialer:     websocket.DefaultDialer,
		minBackoff: time.Second,
		maxBackoff: 30 * time.Second,
		// The forwarder is created only when publishing is enabled and Run()
		// dials immediately, so "connecting" is the truthful initial state for a
		// status read that races ahead of the first dial.
		state: PublishConnecting,
	}
}

// Status type for the daemon's publish-status endpoint.
type Status struct {
	URL         string `json:"url"`
	State       string `json:"state"`
	LastError   string `json:"lastError,omitempty"`
	DaemonID    string `json:"daemonId"`
	DaemonLabel string `json:"daemonLabel"`
}

// Status returns the forwarder's current link state for /api/v1/relay/publish.
func (f *Forwarder) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{
		URL:         f.url,
		State:       f.state,
		LastError:   f.lastErr,
		DaemonID:    f.identity.DaemonID,
		DaemonLabel: f.identity.DaemonLabel,
	}
}

func (f *Forwarder) setState(state, lastErr string) {
	f.mu.Lock()
	f.state = state
	f.lastErr = lastErr
	f.mu.Unlock()
}

// Run connects to the relay and forwards push events until ctx is cancelled.
// Each connection failure backs off exponentially (with jitter to decorrelate
// reconnect storms); a connection that stayed up past maxBackoff is treated as
// healthy and resets the delay so a long-lived link reconnects promptly.
func (f *Forwarder) Run(ctx context.Context) {
	backoff := f.minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := f.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		// Classify the just-ended link for Status(): a CloseRevoked (4401) close
		// — whether seen while reading the hello reply or mid-stream after a
		// revoke — is auth_failed; anything else is a transient disconnect.
		switch {
		case websocket.IsCloseError(err, CloseRevoked):
			f.setState(PublishAuthFailed, "relay rejected the token")
			f.logError(fmt.Sprintf("relay link to %s rejected the token (auth failed)", f.url))
		case err != nil:
			f.setState(PublishDisconnected, err.Error())
			f.logError(fmt.Sprintf("relay link to %s ended: %v", f.url, err))
		default:
			f.setState(PublishDisconnected, "")
		}
		if time.Since(start) > f.maxBackoff {
			backoff = f.minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter(backoff)):
		}
		if backoff *= 2; backoff > f.maxBackoff {
			backoff = f.maxBackoff
		}
	}
}

// runOnce establishes one relay connection: hello → (await hello_ack) →
// daemon_snapshot → forward the push stream until the relay drops, ctx cancels,
// or a write fails.
func (f *Forwarder) runOnce(ctx context.Context) error {
	f.setState(PublishConnecting, "")
	conn, _, err := f.dialer.DialContext(ctx, f.url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.WriteJSON(Hello{
		Type:            MsgHello,
		ProtocolVersion: ProtocolVersion,
		Role:            RoleDaemon,
		DaemonID:        f.identity.DaemonID,
		DaemonLabel:     f.identity.DaemonLabel,
		Token:           f.token,
	}); err != nil {
		return err
	}

	// Wait for the relay's reply before declaring the link up. The relay sends
	// a hello_ack once it has accepted our token; an auth-enabled relay that
	// rejects the token closes here with CloseRevoked (4401) instead. Reading
	// this reply before sending the snapshot makes the auth verdict race-free:
	// Run classifies a CloseRevoked close as auth_failed regardless of whether
	// it arrives now or mid-stream. We don't act on the ack's contents (v1 has
	// nothing to negotiate), only on whether the relay accepted us.
	conn.SetReadLimit(maxRelayFrameBytes)
	conn.SetReadDeadline(time.Now().Add(helloAckTimeout))
	if _, _, err := conn.ReadMessage(); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Time{}) // clear; the read pump reads with no deadline

	// Subscribe BEFORE capturing the snapshot. Otherwise a broadcast that
	// fires between snapshotState() and Subscribe() is in neither the snapshot
	// (captured earlier) nor the delta stream (subscribed later) and is lost
	// until that session's next change. With this order a change reflected in
	// both the snapshot and the stream is just an idempotent upsert on the relay.
	ch := f.push.Subscribe()
	defer f.push.Unsubscribe(ch)

	sessions, agentInfos := f.snapshotState()
	if err := conn.WriteJSON(DaemonSnapshot{
		Type:     MsgDaemonSnapshot,
		Sessions: sessions,
		Agents:   agentInfos,
	}); err != nil {
		return err
	}
	f.setState(PublishConnected, "")
	f.logInfo(fmt.Sprintf("connected to relay %s as %q (%s)", f.url, f.identity.DaemonLabel, f.identity.DaemonID))

	// Read pump: surface the relay closing the socket (and drain control
	// frames / a mid-stream revoke close).
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				readErr <- err
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			if !shouldForward(msg) {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(forwardWriteTimeout))
			if err := conn.WriteJSON(Push{
				Type:   MsgPush,
				Source: f.identity.DaemonID,
				TS:     time.Now().Unix(),
				Msg:    msg,
			}); err != nil {
				return err
			}
		}
	}
}

func (f *Forwarder) snapshotState() ([]*session.SessionState, []AgentInfo) {
	if f.snapshot == nil {
		return nil, nil
	}
	return f.snapshot()
}

// shouldForward drops messages meaningless across hosts. focus_requested asks a
// client to raise the local terminal/IDE window of a session — nonsensical for
// a session on a different machine, so the forwarder filters it (wiki §5.4).
// permissions_updated is likewise host-local (#570): consent is managed on the
// daemon's own machine, and a forwarded copy would make remote dashboards
// re-fetch (and potentially re-open) their LOCAL wizard on this host's churn.
func shouldForward(msg outbound.PushMessage) bool {
	return msg.Type != outbound.PushTypeFocusRequested &&
		msg.Type != outbound.PushTypePermissionsUpdated
}

// jitter returns a random duration in [0, d/2] to spread reconnect attempts.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d/2) + 1))
}

func (f *Forwarder) logInfo(msg string) {
	if f.logger != nil {
		f.logger.LogInfo("relay-forwarder", "", msg)
	}
}

func (f *Forwarder) logError(msg string) {
	if f.logger != nil {
		f.logger.LogError("relay-forwarder", "", msg)
	}
}
