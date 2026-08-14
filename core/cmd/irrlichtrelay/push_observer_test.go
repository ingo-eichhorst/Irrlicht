package main

// Observer + dispatcher + watchdog tests. Every test drives the observer's
// synchronous core (handle/tick) on an injected clock — no sleeps, no
// timing dependence — except the liveness test, which runs the real
// goroutine under a bounded poll, and the smoke test, which swaps in the
// real webpush.Sender against an httptest push service. Events enter
// through the same hook methods the hub calls, so the frame→event mapping
// is inside every test's blast radius.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/webpush"
	"irrlicht/core/ports/outbound"
)

// obsClock is the injected clock the observer, the push service and the
// tests share.
type obsClock struct {
	mu sync.Mutex
	t  time.Time
}

func newObsClock() *obsClock {
	return &obsClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *obsClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *obsClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// recordedSend is one fakeSender call.
type recordedSend struct {
	sub     webpush.Subscription
	payload []byte
	opts    webpush.Options
}

// fakeSender records every dispatch decision without encrypting anything;
// fail forces an error result per endpoint.
type fakeSender struct {
	mu    sync.Mutex
	calls []recordedSend
	fail  map[string]error // endpoint → forced Send result
}

func (f *fakeSender) Send(_ context.Context, sub webpush.Subscription, payload []byte, opts webpush.Options) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedSend{sub: sub, payload: append([]byte(nil), payload...), opts: opts})
	if f.fail != nil {
		if err := f.fail[sub.Endpoint]; err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSender) sends() []recordedSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedSend(nil), f.calls...)
}

// deviceSeed is one paired phone for an observerEnv: a device token in a
// workspace, optionally with a registered subscription.
type deviceSeed struct {
	label     string
	workspace string
	endpoint  string // "" = token without a subscription
}

// observerEnv assembles the full dispatch chain over a temp data dir: token
// store, push service, fake sender, observer — the same shapes runServe
// wires, minus goroutines.
type observerEnv struct {
	t          *testing.T
	obs        *pushObserver
	svc        *push.Service
	store      *authStore
	sender     *fakeSender
	clk        *obsClock
	ddir       string
	tokensPath string
	tokenIDs   map[string]string // label → device token id
	tokens     map[string]string // label → plaintext
}

func newObserverEnv(t *testing.T, seeds ...deviceSeed) *observerEnv {
	t.Helper()
	ddir := t.TempDir()
	tokensPath := filepath.Join(ddir, tokensFilename)
	tokenIDs := make(map[string]string, len(seeds))
	tokens := make(map[string]string, len(seeds))
	for _, s := range seeds {
		id, plaintext := mustIssueToken(t, tokensPath, s.label, s.workspace)
		tokenIDs[s.label] = id
		tokens[s.label] = plaintext
	}
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	clk := newObsClock()
	svc, err := push.NewService(ddir, clk.now)
	if err != nil {
		t.Fatalf("push.NewService: %v", err)
	}
	for _, s := range seeds {
		if s.endpoint == "" {
			continue
		}
		if err := svc.SetSubscription(tokenIDs[s.label], browserSubscription(t, s.endpoint)); err != nil {
			t.Fatalf("SetSubscription(%s): %v", s.label, err)
		}
	}
	sender := &fakeSender{fail: map[string]error{}}
	obs := newPushObserver(svc, store, sender, clk.now)
	return &observerEnv{
		t: t, obs: obs, svc: svc, store: store, sender: sender, clk: clk,
		ddir: ddir, tokensPath: tokensPath, tokenIDs: tokenIDs, tokens: tokens,
	}
}

// drain processes every queued hook event through the synchronous core and
// waits out the sends they spawned, so assertions never race a dispatch
// goroutine.
func (e *observerEnv) drain() {
	e.t.Helper()
	for {
		select {
		case qe := <-e.obs.events:
			e.obs.handle(qe.workspace, qe.ev, e.clk.now())
		default:
			e.obs.inflight.Wait()
			return
		}
	}
}

// tick fires due policy timers at the injected clock's now and waits out
// the resulting sends.
func (e *observerEnv) tick() {
	e.t.Helper()
	e.obs.tick(e.clk.now())
	e.obs.inflight.Wait()
}

func (e *observerEnv) feed(workspace string, msg outbound.PushMessage) {
	e.t.Helper()
	e.obs.observePush(workspace, msg)
	e.drain()
}

func (e *observerEnv) connectDaemon(workspace, id, label string) {
	e.t.Helper()
	e.obs.observeDaemonConnected(workspace, id, label)
	e.drain()
}

func (e *observerEnv) disconnectDaemon(workspace, id, label string) {
	e.t.Helper()
	e.obs.observeDaemonDisconnected(workspace, id, label)
	e.drain()
}

// enterWaiting walks one session through first sighting (silent) into
// waiting — the shortest path to a dispatch.
func (e *observerEnv) enterWaiting(workspace, sessionID string) {
	e.t.Helper()
	e.feed(workspace, sessionFrame(outbound.PushTypeCreated, sessionID, "working", ""))
	e.feed(workspace, sessionFrame(outbound.PushTypeUpdated, sessionID, "waiting", ""))
}

// sessionFrame builds the client-bound frame shape the hub fans out.
func sessionFrame(typ, sessionID, state, parent string) outbound.PushMessage {
	return outbound.PushMessage{Type: typ, Session: &session.SessionState{
		SessionID:       sessionID,
		State:           state,
		ParentSessionID: parent,
		Adapter:         "claude-code",
		ProjectName:     "irrlicht",
	}}
}

// decodePayload unmarshals a fake-sent payload — what the phone would see
// after decrypting.
func decodePayload(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("payload is not JSON: %v (%s)", err, data)
	}
	return m
}

// TestWaitingTransitionDispatchesOnePush pins the daily path end to end
// minus encryption: working → waiting yields exactly one send carrying the
// §8.4 waiting knobs and the structured payload the service worker
// composes from.
func TestWaitingTransitionDispatchesOnePush(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	env.enterWaiting("acme", "sess-1")

	sends := env.sender.sends()
	if len(sends) != 1 {
		t.Fatalf("got %d sends, want exactly 1", len(sends))
	}
	s := sends[0]
	if s.sub.Endpoint != "https://push.example/v2/abc" {
		t.Fatalf("sent to %q, want the workspace's subscription", s.sub.Endpoint)
	}
	if s.opts.Topic != "sess-1" {
		t.Fatalf("Options.Topic = %q, want the logical collapse key (the session id)", s.opts.Topic)
	}
	if s.opts.TTL != time.Hour {
		t.Fatalf("Options.TTL = %v, want 1h for waiting", s.opts.TTL)
	}
	if s.opts.Urgency != webpush.UrgencyHigh {
		t.Fatalf("Options.Urgency = %q, want high for waiting", s.opts.Urgency)
	}
	m := decodePayload(t, s.payload)
	for field, want := range map[string]any{
		"v":          float64(1),
		"kind":       "session",
		"session_id": "sess-1",
		"label":      "claude-code",
		"project":    "irrlicht",
		"state":      "waiting",
		"renotify":   true,
	} {
		if got := m[field]; got != want {
			t.Errorf("payload %s = %v, want %v", field, got, want)
		}
	}
}

// TestDispatchIsWorkspaceScoped pins the isolation boundary: an event in
// workspace A must reach A's phone and no one else's.
func TestDispatchIsWorkspaceScoped(t *testing.T) {
	env := newObserverEnv(t,
		deviceSeed{label: "phone-a", workspace: "ws-a", endpoint: "https://push.example/v2/a"},
		deviceSeed{label: "phone-b", workspace: "ws-b", endpoint: "https://push.example/v2/b"},
	)
	env.enterWaiting("ws-a", "sess-a")

	sends := env.sender.sends()
	if len(sends) != 1 {
		t.Fatalf("got %d sends, want exactly 1", len(sends))
	}
	if got := sends[0].sub.Endpoint; got != "https://push.example/v2/a" {
		t.Fatalf("event in ws-a delivered to %q", got)
	}
}

// TestSameStateAndSubagentUpdatesAreSilent: the daemon emits
// session_updated on every metrics tick, and subagent sessions never
// notify — the parent covers them (§8.4). The initial waiting send doubles
// as the vacuity guard.
func TestSameStateAndSubagentUpdatesAreSilent(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	env.enterWaiting("acme", "sess-1")
	if n := len(env.sender.sends()); n != 1 {
		t.Fatalf("got %d sends after entering waiting, want 1", n)
	}

	// Same-state metrics tick: no new send.
	env.feed("acme", sessionFrame(outbound.PushTypeUpdated, "sess-1", "waiting", ""))
	if n := len(env.sender.sends()); n != 1 {
		t.Fatalf("same-state update dispatched: %d sends, want still 1", n)
	}

	// Subagent enters waiting: no send.
	env.feed("acme", sessionFrame(outbound.PushTypeCreated, "sub-1", "working", "sess-1"))
	env.feed("acme", sessionFrame(outbound.PushTypeUpdated, "sub-1", "waiting", "sess-1"))
	if n := len(env.sender.sends()); n != 1 {
		t.Fatalf("subagent transition dispatched: %d sends, want still 1", n)
	}
}

// TestDeleteCancelsPendingHoldDown: a session deleted inside the ready
// hold-down must not push when the hold-down would have elapsed (§8.4
// "cancels pending hold-downs").
func TestDeleteCancelsPendingHoldDown(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	env.feed("acme", sessionFrame(outbound.PushTypeCreated, "sess-1", "working", ""))
	env.feed("acme", sessionFrame(outbound.PushTypeUpdated, "sess-1", "ready", ""))
	env.feed("acme", sessionFrame(outbound.PushTypeDeleted, "sess-1", "ready", ""))
	env.clk.advance(8 * time.Second) // past the 7s hold-down
	env.tick()
	if n := len(env.sender.sends()); n != 0 {
		t.Fatalf("deleted session's hold-down fired: %d sends, want 0", n)
	}

	// Vacuity guard: the pipeline was live — an undeleted session dispatches.
	env.enterWaiting("acme", "sess-2")
	sends := env.sender.sends()
	if len(sends) != 1 || sends[0].opts.Topic != "sess-2" {
		t.Fatalf("control send missing or wrong: %+v", sends)
	}
}

// TestSendOutcomesRecordedAndGonePruned covers §8.3's outcome table: a gone
// subscription (404/410) is pruned and its failure recorded, a plain error
// keeps the entry, and a success records ok — all as RAM delivery health
// per token.
func TestSendOutcomesRecordedAndGonePruned(t *testing.T) {
	t.Run("gone prunes and records", func(t *testing.T) {
		env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/gone"})
		env.sender.fail["https://push.example/v2/gone"] = fmt.Errorf("https://push.example answered 410: %w", webpush.ErrSubscriptionGone)
		env.enterWaiting("acme", "sess-1")

		if entries := env.svc.Subscriptions(); len(entries) != 0 {
			t.Fatalf("gone subscription not pruned: %+v", entries)
		}
		st, ok := env.svc.DeliveryStatus(env.tokenIDs["phone"])
		if !ok {
			t.Fatal("no delivery health recorded for the pruned token")
		}
		if st.OK || st.Detail == "" || st.At != env.clk.now().Unix() {
			t.Fatalf("delivery health = %+v, want ok=false with a reason and the send time", st)
		}
	})

	t.Run("plain error keeps the entry", func(t *testing.T) {
		env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/err"})
		env.sender.fail["https://push.example/v2/err"] = &webpush.StatusError{Code: 500, Body: "upstream sad"}
		env.enterWaiting("acme", "sess-1")

		if entries := env.svc.Subscriptions(); len(entries) != 1 {
			t.Fatalf("transient failure pruned the subscription: %+v", entries)
		}
		st, ok := env.svc.DeliveryStatus(env.tokenIDs["phone"])
		if !ok || st.OK || st.Detail == "" {
			t.Fatalf("delivery health = %+v ok=%v, want a recorded failure", st, ok)
		}
	})

	t.Run("success records ok", func(t *testing.T) {
		env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/fine"})
		env.enterWaiting("acme", "sess-1")
		st, ok := env.svc.DeliveryStatus(env.tokenIDs["phone"])
		if !ok || !st.OK || st.At != env.clk.now().Unix() {
			t.Fatalf("delivery health = %+v ok=%v, want ok=true with the send time", st, ok)
		}
	})
}

// TestDaemonWatchdogCycle drives §6.4: a reconnect within the 60s grace is
// silent; one that never comes yields exactly one daemon_down push
// (Topic-collapsed per daemon, renotify true); a reconnect after that push
// replaces the banner silently (renotify false).
func TestDaemonWatchdogCycle(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	env.connectDaemon("acme", "mac-1", "laptop")

	// Roaming blip: down, back within grace, then well past it — nothing.
	env.disconnectDaemon("acme", "mac-1", "laptop")
	env.clk.advance(30 * time.Second)
	env.connectDaemon("acme", "mac-1", "laptop")
	env.clk.advance(2 * time.Minute)
	env.tick()
	if n := len(env.sender.sends()); n != 0 {
		t.Fatalf("within-grace reconnect pushed: %d sends, want 0", n)
	}

	// Real outage: grace elapses → one daemon_down.
	env.disconnectDaemon("acme", "mac-1", "laptop")
	env.clk.advance(61 * time.Second)
	env.tick()
	sends := env.sender.sends()
	if len(sends) != 1 {
		t.Fatalf("got %d sends after grace, want exactly 1", len(sends))
	}
	if got := sends[0].opts.Topic; got != "daemon:mac-1" {
		t.Fatalf("watchdog Topic = %q, want daemon:mac-1", got)
	}
	if got := sends[0].opts.Urgency; got != webpush.UrgencyNormal {
		t.Fatalf("watchdog Urgency = %q, want normal", got)
	}
	m := decodePayload(t, sends[0].payload)
	if m["kind"] != "daemon_down" || m["daemon_label"] != "laptop" || m["renotify"] != true {
		t.Fatalf("watchdog payload = %v, want daemon_down/laptop/renotify=true", m)
	}

	// Reconnect after the banner: silent replacement.
	env.connectDaemon("acme", "mac-1", "laptop")
	sends = env.sender.sends()
	if len(sends) != 2 {
		t.Fatalf("got %d sends after reconnect, want 2", len(sends))
	}
	if got := sends[1].opts.Topic; got != "daemon:mac-1" {
		t.Fatalf("daemon_up Topic = %q, want daemon:mac-1 (replaces the banner)", got)
	}
	m = decodePayload(t, sends[1].payload)
	if m["kind"] != "daemon_up" || m["renotify"] != false {
		t.Fatalf("daemon_up payload = %v, want kind=daemon_up renotify=false", m)
	}
}

// TestDaemonRosterPersistsAcrossRestart pins §8.6's roster file — 0600,
// stable order, rewritten only on change — and the relay-restart case: a
// NEW observer over the same dir seeds the watchdog, so a daemon that died
// while the relay was down still gets its down push.
func TestDaemonRosterPersistsAcrossRestart(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	env.connectDaemon("acme", "mac-1", "laptop")

	path := filepath.Join(env.ddir, "daemon-roster.json")
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("roster not written on connect: %v", err)
	}
	if perm := fi1.Mode().Perm(); perm != 0o600 {
		t.Fatalf("roster mode = %o, want 600", perm)
	}
	readRoster := func() (v struct {
		Version int `json:"version"`
		Daemons []struct {
			Workspace string `json:"workspace"`
			DaemonID  string `json:"daemon_id"`
			Label     string `json:"label"`
			LastSeen  int64  `json:"last_seen"`
		} `json:"daemons"`
	}) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatalf("roster is not JSON: %v (%s)", err, data)
		}
		return v
	}
	f := readRoster()
	if f.Version != 1 || len(f.Daemons) != 1 {
		t.Fatalf("roster = %+v, want version 1 with one daemon", f)
	}
	d := f.Daemons[0]
	if d.Workspace != "acme" || d.DaemonID != "mac-1" || d.Label != "laptop" || d.LastSeen != env.clk.now().Unix() {
		t.Fatalf("roster entry = %+v", d)
	}

	// A no-op upsert (same values, same clock) must not rewrite the file —
	// the atomic temp+rename would give it a new inode.
	env.connectDaemon("acme", "mac-1", "laptop")
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fi1, fi2) {
		t.Fatal("no-op upsert rewrote the roster file")
	}

	// A changing upsert rewrites; a second daemon lands in sorted order.
	env.clk.advance(5 * time.Second)
	env.connectDaemon("acme", "alpha", "desk")
	env.disconnectDaemon("acme", "mac-1", "laptop")
	f = readRoster()
	if len(f.Daemons) != 2 || f.Daemons[0].DaemonID != "alpha" || f.Daemons[1].DaemonID != "mac-1" {
		t.Fatalf("roster order = %+v, want alpha before mac-1", f.Daemons)
	}
	if f.Daemons[1].LastSeen != env.clk.now().Unix() {
		t.Fatalf("disconnect did not advance last_seen: %+v", f.Daemons[1])
	}

	// Relay restart: fresh service + observer over the same dir, neither
	// daemon comes back, grace elapses → a down push per rostered daemon.
	svc2, err := push.NewService(env.ddir, env.clk.now)
	if err != nil {
		t.Fatalf("push.NewService (restart): %v", err)
	}
	sender2 := &fakeSender{}
	obs2 := newPushObserver(svc2, env.store, sender2, env.clk.now)
	env.clk.advance(61 * time.Second)
	obs2.tick(env.clk.now())
	obs2.inflight.Wait()

	sends := sender2.sends()
	if len(sends) != 2 {
		t.Fatalf("got %d sends after restart+grace, want one per rostered daemon (2)", len(sends))
	}
	topics := map[string]bool{}
	for _, s := range sends {
		topics[s.opts.Topic] = true
		if m := decodePayload(t, s.payload); m["kind"] != "daemon_down" {
			t.Fatalf("restart watchdog payload = %v, want daemon_down", m)
		}
	}
	if !topics["daemon:mac-1"] || !topics["daemon:alpha"] {
		t.Fatalf("restart watchdog topics = %v", topics)
	}
}

// TestBurstCoalescesIntoSummary: four sessions entering waiting inside the
// 20s window — the first three push individually, the fourth becomes the
// "N agents need attention" summary (§8.4).
func TestBurstCoalescesIntoSummary(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	for i := 1; i <= 4; i++ {
		env.enterWaiting("acme", fmt.Sprintf("sess-%d", i))
	}
	sends := env.sender.sends()
	if len(sends) != 4 {
		t.Fatalf("got %d sends, want 4 (three individual + one summary)", len(sends))
	}
	for i := 0; i < 3; i++ {
		if want := fmt.Sprintf("sess-%d", i+1); sends[i].opts.Topic != want {
			t.Fatalf("send %d Topic = %q, want %q", i, sends[i].opts.Topic, want)
		}
	}
	last := sends[3]
	if last.opts.Topic != "summary" {
		t.Fatalf("4th send Topic = %q, want summary", last.opts.Topic)
	}
	m := decodePayload(t, last.payload)
	if m["kind"] != "summary" || m["count"] != float64(4) {
		t.Fatalf("summary payload = %v, want kind=summary count=4", m)
	}
}

// TestSubscriptionHealthEndpoint covers the §8.3 surface: the caller's own
// registration + last delivery outcome, with the endpoint reduced to its
// host (the path is the subscription's capability secret, RFC 8030 §8.3).
func TestSubscriptionHealthEndpoint(t *testing.T) {
	env := newObserverEnv(t,
		deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/secret-path"},
		deviceSeed{label: "bare", workspace: "acme"},
	)
	srv := httptest.NewServer(buildMux(newHubWithAuth(env.store, nil, defaultLimits()), env.store, env.svc))
	t.Cleanup(srv.Close)

	env.enterWaiting("acme", "sess-1")

	type healthResp struct {
		Registered   bool   `json:"registered"`
		Created      int64  `json:"created"`
		EndpointHost string `json:"endpoint_host"`
		LastDelivery *struct {
			At     int64  `json:"at"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"last_delivery"`
	}

	status, body := doPush(t, "GET", srv.URL+"/api/v1/push/subscriptions", env.tokens["phone"], nil)
	if status != http.StatusOK {
		t.Fatalf("health = %d: %s", status, body)
	}
	var hr healthResp
	if err := json.Unmarshal(body, &hr); err != nil {
		t.Fatal(err)
	}
	if !hr.Registered || hr.Created != env.clk.now().Unix() {
		t.Fatalf("health = %+v, want registered with the pairing time", hr)
	}
	if hr.EndpointHost != "push.example" {
		t.Fatalf("endpoint_host = %q, want the bare host", hr.EndpointHost)
	}
	if strings.Contains(string(body), "secret-path") {
		t.Fatalf("health response leaks the endpoint path: %s", body)
	}
	if hr.LastDelivery == nil || !hr.LastDelivery.OK || hr.LastDelivery.At != env.clk.now().Unix() {
		t.Fatalf("last_delivery = %+v, want the recorded ok outcome", hr.LastDelivery)
	}

	// No bearer → 401, like every other subscription route.
	if status, body := doPush(t, "GET", srv.URL+"/api/v1/push/subscriptions", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health = %d, want 401: %s", status, body)
	}

	// A token that never subscribed: registered false, delivery unknown.
	status, body = doPush(t, "GET", srv.URL+"/api/v1/push/subscriptions", env.tokens["bare"], nil)
	if status != http.StatusOK {
		t.Fatalf("health (no subscription) = %d: %s", status, body)
	}
	hr = healthResp{}
	if err := json.Unmarshal(body, &hr); err != nil {
		t.Fatal(err)
	}
	if hr.Registered || hr.EndpointHost != "" || hr.LastDelivery != nil {
		t.Fatalf("health for unsubscribed token = %+v, want registered:false", hr)
	}
}

// TestObserverGoroutineLiveness is the one test that runs the real
// goroutine: an event entering through the hook must come out as a send,
// and closing stop must terminate the loop. Bounded polling, no fixed
// sleeps.
func TestObserverGoroutineLiveness(t *testing.T) {
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: "https://push.example/v2/abc"})
	stop := make(chan struct{})
	go env.obs.run(stop)

	env.obs.observePush("acme", sessionFrame(outbound.PushTypeCreated, "sess-1", "working", ""))
	env.obs.observePush("acme", sessionFrame(outbound.PushTypeUpdated, "sess-1", "waiting", ""))

	deadline := time.Now().Add(10 * time.Second)
	for len(env.sender.sends()) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("no send within 10s of the hook call")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(stop)
	select {
	case <-env.obs.done:
	case <-time.After(10 * time.Second):
		t.Fatal("observer loop did not exit after stop closed")
	}
}

// TestRealSenderEndToEndSmoke swaps in the real webpush.Sender against an
// httptest push service: one waiting event must arrive as an encrypted,
// fixed-size POST with the §8.4 headers.
func TestRealSenderEndToEndSmoke(t *testing.T) {
	type recordedPost struct {
		header http.Header
		body   []byte
	}
	var mu sync.Mutex
	var posts []recordedPost
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, recordedPost{header: r.Header.Clone(), body: body})
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)

	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: ts.URL + "/wpush/v1/device"})
	env.obs.sender = &webpush.Sender{Key: env.svc.VAPIDKey()}

	const sid = "0b5c2f6e-9c1d-4a8e-b3f7-2d4e6a8c0e12"
	env.enterWaiting("acme", sid)

	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 1 {
		t.Fatalf("push service saw %d POSTs, want 1", len(posts))
	}
	p := posts[0]
	if got := p.header.Get("Content-Encoding"); got != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q, want aes128gcm", got)
	}
	if got := p.header.Get("TTL"); got != "3600" {
		t.Fatalf("TTL header = %q, want 3600 (waiting)", got)
	}
	if got, want := p.header.Get("Topic"), webpush.TopicHeader(sid); got != want {
		t.Fatalf("Topic header = %q, want %q", got, want)
	}
	// The fixed padded record: 2048 plaintext + 16 GCM tag + 86 header.
	if len(p.body) != 2150 {
		t.Fatalf("body is %d bytes, want the constant 2150", len(p.body))
	}
}

// TestHookDropsWhenQueueFull: with the loop stopped and the queue at
// capacity, further hook calls must return immediately and count their
// drops — absence and inability must differ (§8.3). The engine being
// diff-driven is what makes the drop safe: the next update or snapshot
// replay carries the same state.
func TestHookDropsWhenQueueFull(t *testing.T) {
	env := newObserverEnv(t) // no goroutine, nothing drains
	for i := 0; i < observerQueueSize; i++ {
		env.obs.observePush("acme", sessionFrame(outbound.PushTypeUpdated, fmt.Sprintf("sess-%d", i), "waiting", ""))
	}
	if got := env.obs.dropped.Load(); got != 0 {
		t.Fatalf("dropped %d while filling to capacity, want 0", got)
	}
	for i := 0; i < 3; i++ {
		env.obs.observePush("acme", sessionFrame(outbound.PushTypeUpdated, "sess-extra", "waiting", ""))
	}
	if got := env.obs.dropped.Load(); got != 3 {
		t.Fatalf("dropped = %d, want 3", got)
	}
	if got := len(env.obs.events); got != observerQueueSize {
		t.Fatalf("queue holds %d events, want the full %d", got, observerQueueSize)
	}
}

func TestSendFailureNeverRepeatsTheEndpointPath(t *testing.T) {
	// A transport failure (DNS, refused connection, client timeout) comes back
	// as a *url.Error whose message embeds the full request URL — including
	// the subscription's path, which RFC 8030 §8.3 makes a capability secret.
	// It must not reach the operator's log or the health detail; the host is
	// what makes the failure diagnosable, and the host is all we may keep.
	const endpoint = "https://push.example/v2/secret-capability-path"
	env := newObserverEnv(t, deviceSeed{label: "phone", workspace: "acme", endpoint: endpoint})
	env.sender.fail[endpoint] = &url.Error{
		Op:  "Post",
		URL: endpoint,
		Err: errors.New("dial tcp 127.0.0.1:443: connect: connection refused"),
	}
	env.enterWaiting("acme", "sess-1")

	st, ok := env.svc.DeliveryStatus(env.tokenIDs["phone"])
	if !ok {
		t.Fatal("no delivery health recorded for the failed send")
	}
	if strings.Contains(st.Detail, "secret-capability-path") {
		t.Fatalf("delivery detail repeats the subscription's capability path: %q", st.Detail)
	}
	if !strings.Contains(st.Detail, "push.example") {
		t.Fatalf("delivery detail %q dropped the host too — the failure must stay diagnosable", st.Detail)
	}
	// The same sanitized string is what the log line carries, so one guard
	// covers both exits.
	if got := sendFailureReason(env.sender.fail[endpoint]); strings.Contains(got, "secret-capability-path") {
		t.Fatalf("sendFailureReason leaks the path: %q", got)
	}
}
