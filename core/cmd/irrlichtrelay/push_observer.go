package main

// The transition observer and dispatcher: the bridge from the hub's existing
// event flow to phones (docs/mobile-notifications-arc42.md §5.1). The hub
// calls the hook methods at its three seams; the hook only maps the frame
// onto a notify.Event and enqueues it; one goroutine drains the queue,
// drives the per-workspace policy engines, and hands every decided push to
// the dispatcher, which encrypts-and-POSTs via webpush to each subscription
// in the same workspace. Policy — whether, when, how coalesced — lives in
// core/domain/notify and nowhere here.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/domain/notify"
	"irrlicht/core/pkg/webpush"
	"irrlicht/core/ports/outbound"
)

// observerQueueSize bounds the hook → observer channel. The hook may run
// under the hub's lock, so it must never block: past this depth events are
// dropped (and counted — see enqueue).
const observerQueueSize = 1024

// dispatchConcurrency bounds concurrent push POSTs. The semaphore is
// acquired inside each send goroutine, never by the observer loop, so eight
// slow push-service responses queue sends without stalling observation.
const dispatchConcurrency = 8

// deliveryDetailLimit caps the failure reason recorded in delivery health —
// it is destined for a status line, not for parsing.
const deliveryDetailLimit = 200

// pushSender is the dispatcher's delivery seam. Production is
// *webpush.Sender (its nil Client falls back to a shared 10 s-timeout
// default); tests substitute a recorder so dispatch decisions are graded
// without decrypting.
type pushSender interface {
	Send(ctx context.Context, sub webpush.Subscription, payload []byte, opts webpush.Options) error
}

// pushPayload is the encrypted-side wire shape: the notify payload plus the
// Renotify flag, which the service worker needs to decide between a buzzing
// banner and a silent replacement. It rides inside the encrypted payload —
// there is no unencrypted channel to the device to carry it.
type pushPayload struct {
	notify.Payload
	Renotify bool `json:"renotify"`
}

// queuedEvent carries one mapped event plus the workspace it belongs to —
// the isolation unit every engine and every dispatch is scoped by.
type queuedEvent struct {
	workspace string
	ev        notify.Event
}

// pushObserver owns one notify.Engine per workspace (lazily created —
// engine-per-workspace is the isolation model: one tenant's burst can never
// coalesce into another's summary). The engines are not safe for concurrent
// use, so every engine access goes through the observer mutex; the run
// goroutine and the synchronous entry points (handle, tick) share that
// discipline, which is what lets tests drive the core directly with an
// injected clock and no goroutine.
type pushObserver struct {
	svc    *push.Service
	auth   *authStore
	sender pushSender
	now    func() time.Time

	events  chan queuedEvent
	dropped atomic.Uint64 // hook-side drops (queue full)

	// inflight tracks spawned send goroutines; sem bounds how many POST
	// concurrently.
	inflight sync.WaitGroup
	sem      chan struct{}

	mu      sync.Mutex
	engines map[string]*notify.Engine // workspace → policy engine

	done chan struct{} // closed when run exits
}

// newPushObserver builds the observer and seeds the watchdog from the
// persisted daemon roster. now is the injected clock; nil means time.Now.
// The auth store is required: dispatch resolves every subscription's
// workspace live through it (docs/mobile-notifications-arc42.md §8.1), and
// an observer without one would have no isolation boundary to scope by.
func newPushObserver(svc *push.Service, auth *authStore, sender pushSender, now func() time.Time) *pushObserver {
	if auth == nil {
		panic("push observer requires an auth store (docs/mobile-notifications-arc42.md §8.1)")
	}
	if now == nil {
		now = time.Now
	}
	o := &pushObserver{
		svc:     svc,
		auth:    auth,
		sender:  sender,
		now:     now,
		events:  make(chan queuedEvent, observerQueueSize),
		sem:     make(chan struct{}, dispatchConcurrency),
		engines: make(map[string]*notify.Engine),
		done:    make(chan struct{}),
	}
	o.seedFromRoster(o.now())
	return o
}

// seedFromRoster feeds every rostered daemon into its workspace's engine as
// up-then-down, arming the disconnect grace — so a daemon that died while
// the relay was down is still reported by the §6.4 watchdog instead of
// silently forgotten. A daemon that reconnects within the grace cancels
// silently, which is the common relay-restart case. The stated cost: a relay
// restart while a daemon stays offline re-sends one Topic-replaced
// disconnect banner. Seeding drives the engines directly — it is a replay of
// what the roster already knows, not a fresh sighting, so it must not
// advance any LastSeen.
func (o *pushObserver) seedFromRoster(now time.Time) {
	for _, e := range o.svc.Roster() {
		o.drive(e.Workspace, notify.Event{Kind: notify.EventDaemonUp, DaemonID: e.DaemonID, DaemonLabel: e.Label}, now)
		o.drive(e.Workspace, notify.Event{Kind: notify.EventDaemonDown, DaemonID: e.DaemonID, DaemonLabel: e.Label}, now)
	}
}

// --- hub-facing hook (may run under the hub's lock: map + non-blocking
// send, nothing else) ---

// observePush is the fanoutPush seam: every client-bound PushMessage passes
// through here, session-shaped or not.
func (o *pushObserver) observePush(workspace string, msg outbound.PushMessage) {
	ev, ok := sessionEvent(msg)
	if !ok {
		return
	}
	o.enqueue(workspace, ev)
}

// observeDaemonConnected is the daemonConnected seam. Fired per connection,
// including flap-reconnects — a repeated up on an already-up daemon is a
// no-op in the engine.
func (o *pushObserver) observeDaemonConnected(workspace, daemonID, label string) {
	o.enqueue(workspace, notify.Event{Kind: notify.EventDaemonUp, DaemonID: daemonID, DaemonLabel: label})
}

// observeDaemonDisconnected is the daemonDisconnected seam, fired only when
// the daemon's last live connection is gone (the hub's own broadcast rule).
func (o *pushObserver) observeDaemonDisconnected(workspace, daemonID, label string) {
	o.enqueue(workspace, notify.Event{Kind: notify.EventDaemonDown, DaemonID: daemonID, DaemonLabel: label})
}

// sessionEvent maps a client-bound PushMessage onto the policy engine's
// input. Only the session-shaped frames map; history, permissions, input and
// focus frames — and any frame without a Session — are not transitions and
// return ok=false.
func sessionEvent(msg outbound.PushMessage) (notify.Event, bool) {
	s := msg.Session
	if s == nil {
		return notify.Event{}, false
	}
	switch msg.Type {
	case outbound.PushTypeCreated, outbound.PushTypeUpdated:
		return notify.Event{
			Kind:      notify.EventSessionUpdate,
			SessionID: s.SessionID,
			ParentID:  s.ParentSessionID,
			State:     notify.State(s.State),
			Label:     s.Adapter,
			Project:   s.ProjectName,
		}, true
	case outbound.PushTypeDeleted:
		return notify.Event{Kind: notify.EventSessionDelete, SessionID: s.SessionID}, true
	}
	return notify.Event{}, false
}

// enqueue hands one mapped event to the observer goroutine without ever
// blocking the caller (which may hold the hub's lock). A full queue DROPS
// the event, counted and logged with the workspace so inability to observe
// is never silent (docs/mobile-notifications-arc42.md §8.3). The drop is
// safe to take: the engine is diff-driven, so a lost session update
// self-heals at the session's next update or snapshot reconcile.
func (o *pushObserver) enqueue(workspace string, ev notify.Event) {
	select {
	case o.events <- queuedEvent{workspace: workspace, ev: ev}:
	default:
		o.dropped.Add(1)
		log.Printf("push: observer queue full — dropping %s event for workspace %q (self-heals at the next update or snapshot)", ev.Kind, workspace)
	}
}

// --- observer goroutine + synchronous core ---

// run drains the queue and fires due policy timers until stop closes —
// started beside the token watch in runServe, on the same stop channel.
func (o *pushObserver) run(stop <-chan struct{}) {
	defer close(o.done)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		// Rearm from the earliest pending engine timer. Stop-and-drain
		// first so a stale expiry from a previous arm can't double-fire.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if wake, ok := o.nextWake(); ok {
			d := wake.Sub(o.now())
			if d < 0 {
				d = 0
			}
			timer.Reset(d)
		}
		select {
		case qe := <-o.events:
			o.handle(qe.workspace, qe.ev, o.now())
		case <-timer.C:
			o.tick(o.now())
		case <-stop:
			return
		}
	}
}

// handle folds one observed event into policy and delivery — the
// synchronous core the run goroutine and the tests share. Daemon events
// also upsert the persisted roster (connect and disconnect both prove the
// link existed just now), which is why the roster write lives here and not
// in the hook: it is file I/O, and the hook may hold the hub's lock.
func (o *pushObserver) handle(workspace string, ev notify.Event, now time.Time) {
	switch ev.Kind {
	case notify.EventDaemonUp, notify.EventDaemonDown:
		o.svc.RosterUpsert(workspace, ev.DaemonID, ev.DaemonLabel, now.Unix())
	}
	o.drive(workspace, ev, now)
}

// drive runs one event through the workspace's engine and dispatches
// whatever it decides. Roster-free on purpose — startup seeding replays
// through here without faking a sighting.
func (o *pushObserver) drive(workspace string, ev notify.Event, now time.Time) {
	o.mu.Lock()
	eng := o.engines[workspace]
	if eng == nil {
		eng = notify.New(notify.Config{})
		o.engines[workspace] = eng
	}
	pushes := eng.Handle(ev, now)
	o.mu.Unlock()
	o.dispatch(workspace, pushes)
}

// tick fires due timers (ready hold-downs, daemon graces) across every
// engine and dispatches the results.
func (o *pushObserver) tick(now time.Time) {
	type due struct {
		workspace string
		pushes    []notify.Push
	}
	o.mu.Lock()
	var fired []due
	for ws, eng := range o.engines {
		if p := eng.Tick(now); len(p) > 0 {
			fired = append(fired, due{workspace: ws, pushes: p})
		}
	}
	o.mu.Unlock()
	for _, d := range fired {
		o.dispatch(d.workspace, d.pushes)
	}
}

// nextWake returns the earliest pending timer across all engines.
func (o *pushObserver) nextWake() (time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var wake time.Time
	ok := false
	for _, eng := range o.engines {
		if w, has := eng.NextWake(); has && (!ok || w.Before(wake)) {
			wake, ok = w, true
		}
	}
	return wake, ok
}

// --- dispatcher ---

// dispatch fans each decided push out to every subscription whose device
// token resolves to the deciding workspace. The workspace is resolved LIVE
// from the token store per send — the registry persists no workspace copy,
// so a revoked token has no workspace at all and simply matches nothing
// (docs/mobile-notifications-arc42.md §8.1).
func (o *pushObserver) dispatch(workspace string, pushes []notify.Push) {
	for _, p := range pushes {
		payload, err := json.Marshal(pushPayload{Payload: p.Payload, Renotify: p.Renotify})
		if err != nil {
			log.Printf("push: marshaling %s payload: %v", p.Payload.Kind, err)
			continue
		}
		opts := webpush.Options{TTL: p.TTL, Topic: p.Topic, Urgency: webpushUrgency(p.Urgency)}
		for _, entry := range o.svc.Subscriptions() {
			ident, ok := o.auth.identityOf(entry.TokenID)
			if !ok || ident.workspace != workspace {
				continue
			}
			o.sendAsync(entry, payload, opts)
		}
	}
}

// sendAsync performs one delivery in its own goroutine, bounded by the
// dispatch semaphore, and records the outcome — success or failure, with a
// short reason — as the token's delivery health (§8.3: every attempt leaves
// a visible verdict). A gone subscription (404/410) is pruned rather than
// retried: the phone re-subscribes with its stored device token on its next
// open.
func (o *pushObserver) sendAsync(entry push.Entry, payload []byte, opts webpush.Options) {
	o.inflight.Add(1)
	go func() {
		defer o.inflight.Done()
		o.sem <- struct{}{}
		defer func() { <-o.sem }()
		err := o.sender.Send(context.Background(), entry.Subscription, payload, opts)
		at := o.now().Unix()
		switch {
		case err == nil:
			o.svc.SetDeliveryStatus(entry.TokenID, push.DeliveryStatus{At: at, OK: true})
		case errors.Is(err, webpush.ErrSubscriptionGone):
			// The webpush error already names only the endpoint's origin,
			// never the path (the subscription's capability secret).
			log.Printf("push: subscription for token %s is gone — pruning: %s", entry.TokenID, sendFailureReason(err))
			if derr := o.svc.DeleteSubscription(entry.TokenID); derr != nil {
				log.Printf("push: pruning subscription for token %s: %v", entry.TokenID, derr)
			}
			o.svc.SetDeliveryStatus(entry.TokenID, push.DeliveryStatus{At: at, OK: false, Detail: sendFailureReason(err)})
		default:
			log.Printf("push: delivery to token %s failed: %s", entry.TokenID, sendFailureReason(err))
			o.svc.SetDeliveryStatus(entry.TokenID, push.DeliveryStatus{At: at, OK: false, Detail: sendFailureReason(err)})
		}
	}()
}

// webpushUrgency maps the policy's urgency onto the RFC 8030 header value.
func webpushUrgency(u notify.Urgency) webpush.Urgency {
	if u == notify.UrgencyHigh {
		return webpush.UrgencyHigh
	}
	return webpush.UrgencyNormal
}

// sendFailureReason renders a failed send for the log and the delivery-health
// detail — the only two places a send error is ever formatted, so the
// redaction below covers both exits at once.
//
// A transport failure surfaces as a *url.Error carrying the whole request
// URL, and http.Client wraps it again on the way out, so the endpoint's PATH
// — the subscription's capability secret (RFC 8030 §8.3) — ends up in the
// rendered message twice over. Substituting the origin for the URL keeps the
// host, which is what makes an outage diagnosable, and drops the half that
// would let a log reader push to the phone.
func sendFailureReason(err error) string {
	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) && ue.URL != "" {
		msg = strings.ReplaceAll(msg, ue.URL, redactedEndpoint(ue.URL))
	}
	if len(msg) > deliveryDetailLimit {
		msg = msg[:deliveryDetailLimit]
	}
	return msg
}

// redactedEndpoint reduces a push endpoint to its origin. An endpoint that
// will not parse is replaced wholesale rather than partially — an
// unparseable URL is the case where we know least about which part is the
// secret.
func redactedEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "[endpoint]"
	}
	return u.Scheme + "://" + u.Host
}
