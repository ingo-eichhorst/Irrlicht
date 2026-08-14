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
	"strconv"
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

// dropLogInterval throttles the queue-full log line, the way
// rejectLogInterval throttles the hub's over-cap one: the queue fills only
// when the relay is already behind, so a line per dropped event makes the
// overload its own amplifier. The first drop speaks immediately and at most
// one line per interval follows.
const dropLogInterval = time.Second

// sendDrainTimeout bounds how long shutdown waits for cancelled sends to
// unwind. It sits well inside runServe's 5 s srv.Shutdown window, because
// the two run in sequence: a longer bound here would spend the HTTP
// server's grace period rather than the sends'.
const sendDrainTimeout = 2 * time.Second

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

	// cfg is handed to every engine this observer creates. The zero value
	// is §8.4's policy table (notify.New fills each zero field), which is
	// what production passes; a caller shortens individual knobs to drive
	// the timer path without waiting out a 7 s hold-down.
	cfg notify.Config

	events  chan queuedEvent
	dropped atomic.Uint64 // hook-side drops (queue full), for this relay's lifetime
	// lastDropLog is the unix-nanosecond stamp of the last queue-full log
	// line, held as an atomic rather than under a mutex because the hook
	// that writes it may be running under the hub's lock.
	lastDropLog atomic.Int64

	// inflight tracks spawned send goroutines; sem bounds how many POST
	// concurrently. ctx is the parent of every POST, cancelled by shutdown
	// so a relay stopping mid-delivery does not leave requests open past
	// the process's own grace window.
	inflight sync.WaitGroup
	sem      chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc

	mu      sync.Mutex
	engines map[string]*notify.Engine // workspace → policy engine

	done chan struct{} // closed when run exits
}

// newPushObserver builds the observer and seeds the watchdog from the
// persisted daemon roster. cfg is the policy config every engine gets (the
// zero value is §8.4's defaults); now is the injected clock, nil means
// time.Now. The auth store is required: dispatch resolves every
// subscription's workspace live through it
// (docs/mobile-notifications-arc42.md §8.1), and an observer without one
// would have no isolation boundary to scope by.
func newPushObserver(svc *push.Service, auth *authStore, sender pushSender, cfg notify.Config, now func() time.Time) *pushObserver {
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
		cfg:     cfg,
		events:  make(chan queuedEvent, observerQueueSize),
		sem:     make(chan struct{}, dispatchConcurrency),
		engines: make(map[string]*notify.Engine),
		done:    make(chan struct{}),
	}
	o.ctx, o.cancel = context.WithCancel(context.Background())
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
// the event, counted always and logged at most once per dropLogInterval so
// inability to observe is never silent (docs/mobile-notifications-arc42.md
// §8.3) and never becomes the load itself. Every line carries the lifetime
// total, and shutdown reports the final one, so the counter is read by a
// human rather than only incremented. The drop is safe to take: the engine
// is diff-driven, so a lost session update self-heals at the session's next
// update or snapshot reconcile.
func (o *pushObserver) enqueue(workspace string, ev notify.Event) {
	select {
	case o.events <- queuedEvent{workspace: workspace, ev: ev}:
		return
	default:
	}
	total := o.dropped.Add(1)
	if !o.claimDropLog() {
		return
	}
	log.Printf("push: observer queue full — dropping %s event for workspace %q (%d dropped since start; further drop lines throttled to one per %s; each self-heals at the next update or snapshot)",
		ev.Kind, workspace, total, dropLogInterval)
}

// claimDropLog reports whether this drop is the one allowed to speak in the
// current interval. Compare-and-swap rather than a mutex: the caller may be
// holding the hub's lock, and blocking on a contended mutex here is exactly
// what the hook contract forbids.
func (o *pushObserver) claimDropLog() bool {
	now := o.now().UnixNano()
	for {
		last := o.lastDropLog.Load()
		if last != 0 && now-last < int64(dropLogInterval) {
			return false
		}
		if o.lastDropLog.CompareAndSwap(last, now) {
			return true
		}
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
			o.shutdown()
			return
		}
	}
}

// shutdown cancels every in-flight POST and waits a bounded time for the
// send goroutines to unwind, so run does not return while a request is
// still open: runServe closes stop and then gives srv.Shutdown five
// seconds, and an abandoned dispatch goroutine spends that window without
// anyone accounting for it. Sends that outlast the bound are reported and
// left — the process is going away regardless, and a shutdown that hangs on
// an unresponsive push service is worse than one that says what it dropped.
func (o *pushObserver) shutdown() {
	o.cancel()
	drained := make(chan struct{})
	go func() {
		o.inflight.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(sendDrainTimeout):
		log.Printf("push: %s elapsed with push deliveries still in flight — abandoning them", sendDrainTimeout)
	}
	if n := o.dropped.Load(); n > 0 {
		log.Printf("push: %d observed event(s) were dropped by a full observer queue over this relay's lifetime (docs/mobile-notifications-arc42.md §8.3)", n)
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
		eng = notify.New(o.cfg)
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
//
// Two pushes sharing a Topic are NOT ordered against each other, and the
// window is stated here rather than closed. Each goes to its own goroutine,
// they queue on the semaphore independently, and RFC 8030 §5.4 collapse
// happens in ARRIVAL order at the push service — so the older banner wins
// whenever its POST is accepted last, and it then sits on the phone until
// that Topic's next push. The window is one scheduling delay plus one round
// trip, bounded by the sender's 10 s client timeout.
//
// What it takes to reach: two pushes on one Topic, less than a round trip
// apart. §8.4's per-(session, edge) cooldown and the 7 s ready hold-down
// keep two SESSION pushes that far apart under any healthy round trip, so
// the exposed pair is the daemon Topic — a down fired by tick and an up
// folded by handle have no minimum separation at all, and a link that flaps
// inside a slow POST can leave "disconnected" showing for a daemon that is
// back.
//
// It is left open on purpose: closing it means serializing the send
// goroutines per Topic, and a keyed lock held across a POST while the
// semaphore is saturated trades a display-ordering bug for a deadlock
// shape. The honest bound is the paragraph above, not a partial fix.
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
//
// Both waits select on the observer's context: a goroutine queued behind
// the semaphore when the relay stops never opens its request at all, and
// one already POSTing has that request cancelled. Neither is a delivery
// verdict, so neither writes delivery health — overwriting a real outcome
// with "context canceled" at every shutdown would make the §8.3 panel
// answer "the last attempt failed" for a subscription that is fine.
func (o *pushObserver) sendAsync(entry push.Entry, payload []byte, opts webpush.Options) {
	o.inflight.Add(1)
	go func() {
		defer o.inflight.Done()
		select {
		case o.sem <- struct{}{}:
		case <-o.ctx.Done():
			return
		}
		defer func() { <-o.sem }()
		err := o.sender.Send(o.ctx, entry.Subscription, payload, opts)
		if err != nil && o.ctx.Err() != nil {
			return
		}
		o.recordSendOutcome(entry, err)
	}()
}

// sendVerdict is what one delivery attempt amounted to: when it happened,
// whether it landed, whether the push service declared the subscription gone
// (in which case it has just been pruned), and the redacted reason if it
// failed. The dispatcher discards it — delivery health is its only
// consumer — while the synchronous test notification answers with it.
type sendVerdict struct {
	at     int64
	ok     bool
	gone   bool
	reason string
}

// recordSendOutcome turns one Send result into the §8.3 delivery-health
// record, pruning a subscription the push service reports gone (404/410)
// rather than retrying it — the phone re-subscribes with its stored device
// token on its next open.
//
// The dispatcher's async sends and the synchronous test notification share
// it, so what an outcome MEANS cannot differ between the path a user's
// notifications take and the path the diagnostic reports on. A diagnostic
// with its own idea of "delivered" is a diagnostic that can disagree with
// the thing it exists to describe.
func (o *pushObserver) recordSendOutcome(entry push.Entry, err error) sendVerdict {
	v := sendVerdict{at: o.now().Unix(), ok: err == nil}
	if err == nil {
		o.svc.SetDeliveryStatus(entry.TokenID, push.DeliveryStatus{At: v.at, OK: true})
		return v
	}
	v.reason = sendFailureReason(entry.Subscription.Endpoint, err)
	if errors.Is(err, webpush.ErrSubscriptionGone) {
		v.gone = true
		// The webpush error already names only the endpoint's origin,
		// never the path (the subscription's capability secret).
		log.Printf("push: subscription for token %s is gone — pruning: %s", entry.TokenID, v.reason)
		if derr := o.svc.DeleteSubscription(entry.TokenID); derr != nil {
			log.Printf("push: pruning subscription for token %s: %v", entry.TokenID, derr)
		}
	} else {
		log.Printf("push: delivery to token %s failed: %s", entry.TokenID, v.reason)
	}
	o.svc.SetDeliveryStatus(entry.TokenID, push.DeliveryStatus{At: v.at, OK: false, Detail: v.reason})
	return v
}

// --- the test notification (docs/mobile-notifications-arc42.md §8.3) ---

// pushKindTest names the diagnostic payload the "send a test notification"
// button produces. It is spelled here rather than in core/domain/notify
// because it is not a policy decision: no engine emits it, and no §8.4 rule
// applies to it. An installed worker that predates it falls through to its
// unknown-kind branch (genericNotification in platforms/web/sw.js), which
// shows a content-free "update the app page" banner under a tag of its own:
// enough to prove delivery on an older phone, and unable to collapse with a
// real session's notification. Precisely: the first one is a banner, and a
// repeat replaces it silently, because that fallback sets renotify false.
const pushKindTest = notify.PushKind("test")

// testPushTopic is both the RFC 8030 §5.4 collapse key and the device-side
// `tag` sw.js mirrors it with. It can be neither a session id (a UUID) nor a
// daemon topic ("daemon:"+id) nor the summary topic, so a test notification
// replaces only an earlier test notification — never a real one, and never
// the other way round.
const testPushTopic = "beacon-test"

// testPushTTL bounds how long a push service may hold an undelivered test
// notification. It matches §8.4's `ready` TTL: long enough for a phone that
// is asleep or briefly off the network, which is a state worth being able to
// diagnose, and short enough that a banner never surfaces so late that it
// reads as a real event.
const testPushTTL = 10 * time.Minute

// testPayloadVersion mirrors the unexported payloadVersion the notify engine
// stamps into every Payload — the number sw.js checks before composing
// anything (`payload.v !== 1`). It is not importable, so the two copies are
// pinned together by TestTestPayloadVersionMatchesTheEngines.
const testPayloadVersion = 1

// testNotificationPayload composes the diagnostic payload. It names no
// session on purpose: the service worker's ledger fold takes session-kind
// payloads only (arc42 §8.5), so a test notification cannot leave a phantom
// row in the ledger or a phantom count on the badge.
func testNotificationPayload(now time.Time) pushPayload {
	return pushPayload{
		Payload: notify.Payload{Version: testPayloadVersion, Kind: pushKindTest, At: now.Unix()},
		// The point of the exercise is a banner that buzzes: a silent
		// replacement is indistinguishable from nothing arriving, which is
		// the state the button is being pressed to resolve.
		Renotify: true,
	}
}

// sendTestNotification delivers one diagnostic push to entry and reports
// what happened — the §8.3 "Doubt" row. It bypasses core/domain/notify
// entirely: this is a direct dispatch, not a transition, so no hold-down, no
// cooldown and no burst window stands between the button and the push
// service. Everything downstream is the production path, sender included.
//
// Deliberately NOT behind the dispatch semaphore. That bound exists to keep
// the observer's fan-out from opening unbounded connections; a single
// request-scoped send is already bounded by the HTTP server and by the
// sender's own client timeout, and queueing the diagnostic behind eight slow
// deliveries would make it answer "cancelled" precisely when the relay is in
// the state somebody is pressing the button to investigate.
func (o *pushObserver) sendTestNotification(ctx context.Context, entry push.Entry) sendVerdict {
	payload, err := json.Marshal(testNotificationPayload(o.now()))
	if err != nil {
		// Unreachable for a fixed struct of scalars, and still not silent:
		// this function's contract is that its caller can render the answer.
		return sendVerdict{at: o.now().Unix(), reason: "encoding the test payload: " + err.Error()}
	}
	opts := webpush.Options{TTL: testPushTTL, Topic: testPushTopic, Urgency: webpush.UrgencyHigh}
	err = o.sender.Send(ctx, entry.Subscription, payload, opts)
	if err != nil && ctx.Err() != nil {
		// The caller hung up (closed the app, lost the network) and took the
		// request context with it. Same rule as the dispatcher's async sends:
		// a cancellation is not a delivery VERDICT, so it must not overwrite
		// §8.3 delivery health — the panel would then report "the last
		// attempt failed" about a subscription that is fine, on the strength
		// of the user closing a tab. The verdict below is returned for the
		// record; nobody is left to read it.
		return sendVerdict{at: o.now().Unix(), reason: sendFailureReason(entry.Subscription.Endpoint, err)}
	}
	return o.recordSendOutcome(entry, err)
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
// The endpoint's PATH is the subscription's capability secret (RFC 8030
// §8.3) and must reach neither. Which string a failure carries it in is not
// derivable from the error's type: a transport failure surfaces as a
// *url.Error rendering the whole request URL, while a push service answering
// non-2xx comes back as a *webpush.StatusError whose body is the RFC 7807
// problem+json RFC 8030 §8.2 asks for — and problem+json's `instance` member
// names that same URL. Keying on *url.Error therefore redacts one shape and
// waves the other through. The redaction is driven instead by the endpoint
// the send was ADDRESSED to, which the dispatcher knows whatever the failure
// looks like; the *url.Error's own URL is redacted on top, for the cases
// where the two differ. Substituting the origin keeps the host, which is
// what makes an outage diagnosable, and drops the half that would let a log
// reader push to the phone.
func sendFailureReason(endpoint string, err error) string {
	msg := redactEndpointIn(err.Error(), endpoint)
	var ue *url.Error
	if errors.As(err, &ue) && ue.URL != "" {
		msg = redactEndpointIn(msg, ue.URL)
	}
	if len(msg) > deliveryDetailLimit {
		msg = msg[:deliveryDetailLimit]
	}
	return msg
}

// redactEndpointIn substitutes the origin for EVERY occurrence of endpoint
// in msg, in both spellings it can appear in.
//
// Every occurrence, because a single failure names it more than once —
// problem+json repeats the resource in `instance` and again in the
// human-readable detail. Both spellings, because net/url.Error and fmt
// render a URL with %q, which escapes what strconv.Quote escapes: an
// endpoint carrying such a byte (a registry restored from backup or edited
// by hand holds whatever it holds — Subscription.Validate only gates the
// door) appears escaped in the message and the raw substitution misses it.
func redactEndpointIn(msg, endpoint string) string {
	if endpoint == "" {
		return msg
	}
	redacted := redactedEndpoint(endpoint)
	msg = strings.ReplaceAll(msg, endpoint, redacted)
	if quoted := strconv.Quote(endpoint); quoted != `"`+endpoint+`"` {
		msg = strings.ReplaceAll(msg, quoted[1:len(quoted)-1], redacted)
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
