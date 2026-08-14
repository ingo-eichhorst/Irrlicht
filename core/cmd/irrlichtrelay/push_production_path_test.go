package main

// The production-path test. Every other observer test drives the
// synchronous core directly (handle/tick on an injected clock), which is
// the right shape for grading policy but says nothing about the three
// pieces of wiring that stand between a hub event and a phone: the hook the
// hub calls, the run goroutine that drains the queue, and the timer run
// arms from nextWake. All three can be dead with the rest of the suite
// green — so nothing in this file calls handle, drive or tick, and the only
// clock is the real one.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/domain/notify"
	"irrlicht/core/pkg/webpush"
	"irrlicht/core/ports/outbound"
)

// productionPathPolicy shortens the two timers this test waits on so the
// real time.Timer inside run is exercised in milliseconds instead of the
// §8.4 defaults' seven and sixty seconds. Every other knob stays at its
// default — this is the shipped policy with the clock turned down, not a
// different policy.
var productionPathPolicy = notify.Config{
	HoldDown:    50 * time.Millisecond,
	DaemonGrace: 100 * time.Millisecond,
}

// wireDeadline bounds every wait in this file. Generous on purpose: the
// failure it must never produce is a flake on a loaded CI runner, and the
// failure it exists to catch (a seam that never fires) does not get less
// wrong with more patience.
const wireDeadline = 10 * time.Second

// productionPath is the relay's real dispatch chain: a hub built by
// newHubWithAuth with an observer attached through setPushHook and its run
// goroutine started, over a real token store and push service.
type productionPath struct {
	t        *testing.T
	hub      *hub
	obs      *pushObserver
	svc      *push.Service
	sender   *fakeSender // the recording sender, nil when the caller supplied its own
	ddir     string
	tokenIDs map[string]string // seed label → device token id
	stop     chan struct{}
	stopOnce sync.Once
}

// newProductionPath is the single-phone shape most of these tests want.
func newProductionPath(t *testing.T, workspace, endpoint string) *productionPath {
	t.Helper()
	return newProductionPathWith(t, nil, deviceSeed{label: "phone", workspace: workspace, endpoint: endpoint})
}

// newProductionPathWith builds the chain over seeds, with sender nil meaning
// the recording fakeSender. The observer's run goroutine is started here and
// stopped by cleanup, so no test has to remember either.
func newProductionPathWith(t *testing.T, sender pushSender, seeds ...deviceSeed) *productionPath {
	t.Helper()
	ddir := t.TempDir()
	tokensPath := filepath.Join(ddir, tokensFilename)
	tokenIDs := make(map[string]string, len(seeds))
	for _, s := range seeds {
		id, _ := mustIssueToken(t, tokensPath, s.label, s.workspace)
		tokenIDs[s.label] = id
	}
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	svc, err := push.NewService(ddir, nil)
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
	p := &productionPath{t: t, svc: svc, ddir: ddir, tokenIDs: tokenIDs, stop: make(chan struct{})}
	if sender == nil {
		p.sender = &fakeSender{}
		sender = p.sender
	}
	p.obs = newPushObserver(svc, store, sender, productionPathPolicy, nil)

	p.hub = newHubWithAuth(store, nil, defaultLimits())
	p.hub.setPushHook(p.obs)

	go p.obs.run(p.stop)
	t.Cleanup(func() { p.shutdown(true) })
	return p
}

// shutdown closes the stop channel once and waits for run to return.
// mustExit is false for the caller that is testing the exit itself.
func (p *productionPath) shutdown(mustExit bool) {
	p.stopOnce.Do(func() { close(p.stop) })
	if !mustExit {
		return
	}
	select {
	case <-p.obs.done:
	case <-time.After(wireDeadline):
		p.t.Error("observer loop did not exit after stop closed")
	}
}

// awaitSends blocks until the fake sender has seen at least n sends,
// failing with what was being waited on. Polling, because the thing under
// test is whether a send ever happens at all.
func (p *productionPath) awaitSends(n int, what string) []recordedSend {
	p.t.Helper()
	deadline := time.Now().Add(wireDeadline)
	for {
		if sends := p.sender.sends(); len(sends) >= n {
			return sends
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("waiting for %s: %d send(s) after %s, want %d", what, len(p.sender.sends()), wireDeadline, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// awaitRoster blocks until the persisted daemon roster names daemonID. The
// roster write is the one observable that only handle performs — drive
// (what run would call if the two were swapped) is roster-free by design.
func (p *productionPath) awaitRoster(daemonID string) {
	p.t.Helper()
	path := filepath.Join(p.ddir, "daemon-roster.json")
	deadline := time.Now().Add(wireDeadline)
	for {
		var f struct {
			Daemons []push.RosterEntry `json:"daemons"`
		}
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &f) == nil {
			for _, e := range f.Daemons {
				if e.DaemonID == daemonID {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("daemon %q never reached %s within %s — the run goroutine is not folding events through handle", daemonID, path, wireDeadline)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestPushReachesThePhoneThroughTheHubAndTheObserversOwnTimer walks the
// three seams end to end on the production objects: a session frame fanned
// out by the hub, a ready hold-down fired by run's own timer, and the §6.4
// watchdog fired by the same timer after the hub reports the daemon link
// gone. Every event enters through a hub method; nothing here touches the
// observer's synchronous core.
func TestPushReachesThePhoneThroughTheHubAndTheObserversOwnTimer(t *testing.T) {
	const (
		workspace = "acme"
		endpoint  = "https://push.example/v2/abc"
		daemonID  = "mac-1"
	)
	p := newProductionPath(t, workspace, endpoint)

	// 1. The hook: a waiting transition fanned out by the hub reaches the
	//    phone. fanoutPush is the hub's own seam, not the observer's.
	p.hub.fanoutPush(workspace, daemonID, sessionFrame(outbound.PushTypeCreated, "sess-waiting", "working", ""))
	p.hub.fanoutPush(workspace, daemonID, sessionFrame(outbound.PushTypeUpdated, "sess-waiting", "waiting", ""))
	sends := p.awaitSends(1, "a waiting transition fanned out by the hub")
	if got := sends[0].sub.Endpoint; got != endpoint {
		t.Fatalf("first send went to %q, want the workspace's subscription %q", got, endpoint)
	}
	if got := sends[0].opts.Topic; got != "sess-waiting" {
		t.Fatalf("first send Topic = %q, want the session id", got)
	}

	// 2. The timer: a ready edge arms the hold-down and nothing else
	//    arrives, so only run's own time.Timer can deliver it.
	p.hub.fanoutPush(workspace, daemonID, sessionFrame(outbound.PushTypeCreated, "sess-ready", "working", ""))
	p.hub.fanoutPush(workspace, daemonID, sessionFrame(outbound.PushTypeUpdated, "sess-ready", "ready", ""))
	sends = p.awaitSends(2, "the ready hold-down firing off the observer's own timer")
	if got := sends[1].opts.Topic; got != "sess-ready" {
		t.Fatalf("second send Topic = %q, want sess-ready off the hold-down timer", got)
	}
	if m := decodePayload(t, sends[1].payload); m["state"] != "ready" {
		t.Fatalf("hold-down payload = %v, want state=ready", m)
	}

	// 3. The daemon seams: connect persists the roster (only handle does
	//    that), and the disconnect grace fires off the same timer.
	daemonSend := make(chan []byte, 1)
	p.hub.daemonConnected(workspace, daemonID, "laptop", daemonSend)
	p.awaitRoster(daemonID)

	p.hub.daemonDisconnected(workspace, daemonID, daemonSend)
	sends = p.awaitSends(3, "the §6.4 watchdog firing off the observer's own timer")
	if got := sends[2].opts.Topic; got != "daemon:"+daemonID {
		t.Fatalf("third send Topic = %q, want daemon:%s", got, daemonID)
	}
	if m := decodePayload(t, sends[2].payload); m["kind"] != "daemon_down" || m["daemon_label"] != "laptop" {
		t.Fatalf("watchdog payload = %v, want daemon_down/laptop", m)
	}
}

// parkingSender parks every Send until the test releases it, so the number
// simultaneously inside the sender is exactly the number of POSTs the
// dispatcher has admitted.
type parkingSender struct {
	entered chan struct{}
	release chan struct{}
}

func (s *parkingSender) Send(_ context.Context, _ webpush.Subscription, _ []byte, _ webpush.Options) error {
	s.entered <- struct{}{}
	<-s.release
	return nil
}

// TestDispatchNeverExceedsTheConcurrencyBound pins dispatchConcurrency.
// One workspace transition fans out to every phone in it, and a relay with
// more paired phones than the bound must not open a POST per phone at once
// — a push service that has stopped answering would otherwise park one
// goroutine per subscription per transition.
func TestDispatchNeverExceedsTheConcurrencyBound(t *testing.T) {
	const workspace = "acme"
	sender := &parkingSender{
		// Buffered past the phone count so a parked Send never blocks on
		// reporting itself, which would serialize what we are measuring.
		entered: make(chan struct{}, 4*dispatchConcurrency),
		release: make(chan struct{}),
	}
	seeds := make([]deviceSeed, 0, dispatchConcurrency+4)
	for i := range dispatchConcurrency + 4 {
		seeds = append(seeds, deviceSeed{
			label:     fmt.Sprintf("phone-%d", i),
			workspace: workspace,
			endpoint:  fmt.Sprintf("https://push.example/v2/phone-%d", i),
		})
	}
	p := newProductionPathWith(t, sender, seeds...)
	t.Cleanup(func() { close(sender.release) })

	p.hub.fanoutPush(workspace, "mac-1", sessionFrame(outbound.PushTypeCreated, "sess-1", "working", ""))
	p.hub.fanoutPush(workspace, "mac-1", sessionFrame(outbound.PushTypeUpdated, "sess-1", "waiting", ""))

	// Vacuity guard: the bound must actually be reached, or the assertion
	// below would pass against a dispatcher that sends nothing at all.
	for i := range dispatchConcurrency {
		select {
		case <-sender.entered:
		case <-time.After(wireDeadline):
			t.Fatalf("only %d of %d POSTs started within %s", i, dispatchConcurrency, wireDeadline)
		}
	}
	select {
	case <-sender.entered:
		t.Fatalf("a %dth POST started while %d were still in flight — dispatch is not bounded by the semaphore", dispatchConcurrency+1, dispatchConcurrency)
	case <-time.After(250 * time.Millisecond):
		// Patience for the broken case only: a bounded dispatcher can
		// never exceed the bound however long we watch.
	}
}

// cancelSender reports whether shutdown reached its POST's context, then
// waits for the test before returning — so "run returned while a send was
// still in flight" is a deterministic observation rather than a race.
type cancelSender struct {
	entered   chan struct{}
	cancelled chan struct{}
	finish    chan struct{}
}

func (s *cancelSender) Send(ctx context.Context, _ webpush.Subscription, _ []byte, _ webpush.Options) error {
	s.entered <- struct{}{}
	<-ctx.Done()
	close(s.cancelled)
	<-s.finish
	return ctx.Err()
}

// TestShutdownCancelsInFlightSendsAndWaitsForThem: a push POST outlives the
// relay's stop signal by default — sendAsync's goroutine owns the request
// and nothing joins it. srv.Shutdown then spends its 5 s window around
// goroutines parked mid-POST, and the process exits with the request still
// open. Shutdown must cancel those requests and drain them.
func TestShutdownCancelsInFlightSendsAndWaitsForThem(t *testing.T) {
	const workspace = "acme"
	sender := &cancelSender{
		entered:   make(chan struct{}, 1),
		cancelled: make(chan struct{}),
		finish:    make(chan struct{}),
	}
	p := newProductionPathWith(t, sender, deviceSeed{label: "phone", workspace: workspace, endpoint: "https://push.example/v2/abc"})

	p.hub.fanoutPush(workspace, "mac-1", sessionFrame(outbound.PushTypeCreated, "sess-1", "working", ""))
	p.hub.fanoutPush(workspace, "mac-1", sessionFrame(outbound.PushTypeUpdated, "sess-1", "waiting", ""))
	select {
	case <-sender.entered:
	case <-time.After(wireDeadline):
		t.Fatalf("no POST started within %s", wireDeadline)
	}

	p.shutdown(false)
	select {
	case <-sender.cancelled:
	case <-time.After(wireDeadline):
		close(sender.finish)
		t.Fatalf("stop closed but the in-flight POST's context was never cancelled after %s — the send carries a context shutdown cannot reach", wireDeadline)
	}
	select {
	case <-p.obs.done:
		close(sender.finish)
		t.Fatal("run returned while a send goroutine was still in flight — close(stop) hands srv.Shutdown a POST it cannot account for")
	case <-time.After(250 * time.Millisecond):
	}
	close(sender.finish)
	select {
	case <-p.obs.done:
	case <-time.After(wireDeadline):
		t.Fatalf("run did not return within %s of its sends draining", wireDeadline)
	}

	// A cancelled POST is not a verdict about the subscription, so it must
	// not overwrite (or invent) the token's last delivery outcome.
	if st, ok := p.svc.DeliveryStatus(p.tokenIDs["phone"]); ok {
		t.Fatalf("shutdown recorded delivery health %+v — a cancelled send is not a delivery attempt's outcome", st)
	}
}
