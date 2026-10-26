// hooks.go provides the HTTP handler for receiving opencode bus events
// (issue #1719, epic #1355).
//
// Three events are installed and dispatched: permission.asked asserts the
// blocked-on-user signal, permission.replied releases it, and session.idle
// releases it AND reports the authoritative turn end.
//
// opencode 2.x (#2108) ends a turn with session.execution.succeeded, .failed or
// .interrupted instead — it never publishes session.idle (#2104 item 8) — and
// each of those is dispatched exactly as session.idle is. Its plugin runs once
// per project directory inside 2.x's shared service, every instance sees the
// same event stream, and each event therefore arrives once per instance (#2104
// item 12); the 2.x plugin forwards the event's own id, and the receiver drops
// an id it has already dispatched (recentEventIDs). A 1.x payload carries no id
// and is dispatched every time, as before.
//
// # opencode DOES have a blocked-on-user state, and it is invisible in the store
//
// #1719 asked whether opencode has any genuine blocked-on-user state and told
// this issue not to assume the answer either way. It does, and it is the whole
// reason this adapter is worth a hook channel at all. Measured against
// opencode 1.14.50's own bundle:
//
//   - Permission.ask evaluates each requested pattern against the ruleset and
//     returns EARLY, publishing nothing, when every one of them already
//     resolves to "allow". It publishes permission.asked only when something
//     genuinely has to be asked. So this is a narrow assert, not the
//     fires-for-every-tool event #1719 warned against reusing
//     session.HookPreToolUse's row for — and note that no row is reused: each
//     event below goes to a dedicated SessionDetector entry point, so
//     session.hookSignalEffects (a flat map with no adapter dimension) is never
//     consulted on this path.
//
//   - **That pending request is held in a plain in-memory Map**
//     (`{pending: new Map, approved: <rows from the permission table>}`). Only
//     the APPROVED patterns are persisted; a request awaiting an answer is
//     written nowhere. opencode's store therefore cannot express "the user is
//     being asked right now", which is exactly what this adapter's Source —
//     an agent.ProcessOwnedStore over opencode.db — reads. Before this hook
//     channel, opencode had no `waiting` at all: neither the watcher nor the
//     parser touches the permission table, because there is nothing there to
//     touch until after the user has answered.
//
//   - Permission.reply publishes permission.replied for EVERY reply INCLUDING
//     "reject", and cascades a rejection to that session's other pending
//     requests, publishing one per request. That is what puts opencode out of
//     copilot's position — copilot emits nothing at all on a denial, so a hold
//     with no release would pin the session `waiting` until
//     permissionPromptHoldTimeout's 12-hour ceiling. Here the release really
//     does arrive on the deny path.
//
//   - session.idle is published by SessionStatus.set from the session runner's
//     onIdle, and directly by SessionRunState.cancel. It therefore covers the
//     turn ending, being cancelled and being interrupted. This receiver
//     releases the permission hold on it as well as reporting turn end — the
//     same belt-and-braces gemini-cli's AfterAgent handling uses, closing the
//     one case no reply covers: the process being torn down with a request
//     still pending.
//
// # Why the payload carries no path (contracttesting.PathDaemonDerived)
//
// Every other hook receiver in this tree confines a caller-supplied
// transcript_path to its adapter's declared roots. This one cannot, and must
// not pretend to: opencode has no file per session. Its Source is an
// agent.ProcessOwnedStore, session state lives in one SQLite database, and the
// "transcript path" every downstream consumer keys an opencode session on is a
// string the DAEMON composes — dbPath + "-wal?session=" + id, the same spelling
// watcher.go emits on every event (transcriptPathFor is the one function both
// use, so the hook's key and the watcher's key cannot drift into two sessions).
//
// So the honest payload is a session id, and the receiver reads it through
// hookjson.DecodeSealed. That is not a relaxation of the #1361 guard, it is the
// guard's premise being absent: there is no caller-supplied path on this
// receiver's dispatch path for a traversal, a symlink or an escape to be
// expressed in. The obligation that replaces "confine what the caller named" is
// "prove nothing the caller names travels", and it is graded from outside by
// AssertHookPathConfined's PathDaemonDerived route, whose obligations
// contradict the from-body route's directly — so declaring the wrong one fails
// rather than passing quietly. See hookpath_test.go.
//
// What DecodeSealed keeps, unchanged from DecodeConfined: the #1488 consent
// backstop before the body is read, the 1 MiB bound on an unauthenticated local
// endpoint, and the single body-reading function core/architecture_hookbody_test.go
// pins.
package opencode

import (
	"net/http"
	"strings"
	"sync"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving opencode bus events
// (issue #570). Aliased to the shared domain constant rather than restated —
// see agent.HooksPermissionKey's own doc for why two external projections
// narrow on it.
const PermissionKeyHooks = agent.HooksPermissionKey

// logComponentHookReceiver is the Logger component tag for every log line this
// receiver emits.
const logComponentHookReceiver = "opencode-hook-receiver"

// sessionIDPrefix is what opencode's own PermissionID/SessionID schema
// enforces for a session id — `Schema.String.check(isStartsWith("ses"))`. It is
// checked here for the same reason pi checks its transcript's suffix: the
// endpoint is local and unauthenticated, and an id that is not one this adapter
// could ever have seen is dropped rather than turned into a phantom session.
const sessionIDPrefix = "ses"

// openCodeHookPayload is the JSON body irrlicht's own opencode plugin writes to
// the beacon's stdin (plugin.js), forwarded verbatim by
// `irrlichd hook-post opencode`.
//
// Deliberately minimal, and deliberately carrying NO path — see this file's
// header. SessionID is opencode's own `ses_...` id, which is the id the watcher
// keys every session on (it reads it straight out of the `session` table), so
// there is exactly one spelling of session identity on this adapter and no
// second one to disagree with it.
type openCodeHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	// EventID is opencode 2.x's own id for the bus event (#2108), forwarded
	// so the receiver can drop the copy each further plugin instance delivers.
	// Empty from the 1.x plugin, which forwards none, and omitted then, so a
	// 1.x payload's bytes are what they were.
	EventID string `json:"event_id,omitempty"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the same
// agent-agnostic surface every other receiver uses, narrowed to the three
// methods this adapter's installed events need.
type HookTarget interface {
	HandlePermissionPromptHook(sessionID, transcriptPath, hookName string)
	ReleasePermissionPromptHold(sessionID string)
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates on
// (issue #570) — an alias, not a copy, so the receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.Handler that receives opencode bus events and
// dispatches them to the target.
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so an install left behind by a pre-consent
// daemon stays quiet. A nil gate means no gating — used by tests.
//
// The returned hookjson.HookHandler carries a NIL Confiner, and that is the
// declaration rather than an omission: this receiver has no caller-supplied
// path to confine, and a nil confiner is what makes hookjson.DecodeConfined
// fail CLOSED if a later edit switched this receiver back to the confining
// decode without giving the adapter roots. AssertHookPathConfined's
// PathDaemonDerived route asserts it.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	seen := newRecentEventIDs(recentEventIDCapacity)
	return hookjson.NewHandler(nil,
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyDatabase),
		func(consent hookjson.Consent, _ *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, seen, consent, log, w, r)
		})
}

// recentEventIDCapacity bounds how many 2.x event ids the receiver remembers.
// A repeat arrives from a sibling plugin instance spawning its own beacon for
// the same event, so it lands within moments of the first; the bound only has
// to outlast the events published in that window, across every session the
// service hosts, and the five event types the 2.x plugin forwards are a few
// per turn. It is a memory bound, not a tuning knob.
const recentEventIDCapacity = 1024

// maxEventIDLen bounds an event id the receiver will remember. opencode 2.1.4's
// ids are 30 bytes ("evt_175fc0687002Gb1i3eKhC48Ken", from the sandbox daemon
// log of #2108's live run); 128 leaves room for a longer scheme without letting
// the dedupe set grow with the request body.
const maxEventIDLen = 128

// recentEventIDs is a bounded set of the event ids most recently dispatched:
// a map for the lookup and a ring for the eviction order, behind a mutex
// because the HTTP server runs handlers concurrently.
type recentEventIDs struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	ring []string
	next int
}

func newRecentEventIDs(capacity int) *recentEventIDs {
	return &recentEventIDs{ids: make(map[string]struct{}, capacity), ring: make([]string, capacity)}
}

// firstSighting records id and reports whether it is new. When the set is
// full, the oldest id is forgotten to make room.
func (r *recentEventIDs) firstSighting(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.ids[id]; dup {
		return false
	}
	if old := r.ring[r.next]; old != "" {
		delete(r.ids, old)
	}
	r.ring[r.next] = id
	r.next = (r.next + 1) % len(r.ring)
	r.ids[id] = struct{}{}
	return true
}

// serveHookRequest is NewHookHandler's request logic. The step order matches
// every sibling receiver's exactly, and each step is load-bearing — see the
// contract assertions in hook*_test.go.
func serveHookRequest(target HookTarget, seen *recentEventIDs, consent hookjson.Consent, log outbound.Logger, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Both keys come off the receiver's OWN hookjson.Consent (issue #1488), so
	// the set this sequence is written for is the set the handler publishes and
	// the contract derives.
	if !consent.Granted(PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The channel delivered (issue #1368). Counted against the "hooks" consent
	// only, ahead of the database check below.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the session store, so it is gated
	// behind "database" — this adapter's read permission — not merely the
	// "hooks" write consent. The check comes BEFORE the decode, so a denied
	// session still yields a quiet 200 and the body is never read.
	if !consent.Granted(PermissionKeyDatabase) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload openCodeHookPayload
	if !hookjson.DecodeSealed(w, r, log, logComponentHookReceiver, consent, &payload) {
		return
	}

	sessionID := payload.SessionID
	if !strings.HasPrefix(sessionID, sessionIDPrefix) {
		// Not an id this adapter could have issued — drop rather than invent a
		// session. The store watcher covers the state regardless.
		w.WriteHeader(http.StatusOK)
		return
	}

	// An event id longer than any opencode issues is not one our plugin
	// forwarded. It is dropped rather than remembered: the set below holds
	// every id it is handed until evicted, and a caller on this
	// unauthenticated local endpoint could otherwise park up to the body
	// bound (1 MiB) in each of its slots.
	if len(payload.EventID) > maxEventIDLen {
		w.WriteHeader(http.StatusOK)
		return
	}

	// A 2.x event this receiver already dispatched, delivered again by
	// another plugin instance (#2104 item 12). Dropped AFTER the session-id
	// check, so an id from a payload that was itself dropped is never
	// remembered, and before anything is dispatched — a repeated
	// permission.asked that arrives after its permission.replied must not
	// hold the session waiting again.
	if payload.EventID != "" && !seen.firstSighting(payload.EventID) {
		log.LogInfo(logComponentHookReceiver, sessionID, "dropped a repeat of "+payload.HookEventName+" (event "+payload.EventID+")")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Composed by the daemon from its own store resolver, never from the body.
	transcriptPath := transcriptPathFor(sessionID)

	switch payload.HookEventName {
	case HookEventPermissionAsked:
		log.LogInfo(logComponentHookReceiver, sessionID, "received permission.asked")
		target.HandlePermissionPromptHook(sessionID, transcriptPath, HookEventPermissionAsked)
	case HookEventPermissionReplied:
		log.LogInfo(logComponentHookReceiver, sessionID, "received permission.replied")
		target.ReleasePermissionPromptHold(sessionID)
	case HookEventSessionIdle, HookEventExecutionSucceeded, HookEventExecutionFailed, HookEventExecutionInterrupted:
		// session.idle is 1.x's turn end; the three session.execution.* are
		// 2.x's (#2108), which publishes no session.idle. Each is the same
		// authoritative claim, so each is handled the same way.
		log.LogInfo(logComponentHookReceiver, sessionID, "received "+payload.HookEventName)
		// Released before the turn-end report, and unconditionally: a release
		// on an unheld signal is a no-op, and this is the only thing that
		// closes a prompt torn down without a reply (a killed process, a
		// cancelled run). Same pairing gemini-cli's AfterAgent uses.
		target.ReleasePermissionPromptHold(sessionID)
		// Payload-free, exactly as pi's and vibe's single events are:
		// HandleStopHook asserts SignalTurnDone unconditionally and only
		// CONDITIONALLY overlays the text and cue, so the authoritative "the
		// turn truly ended" claim is real without them, and it can never clear
		// or mask a cue found elsewhere. opencode's bus event carries no
		// message content to forward.
		target.HandleStopHook(sessionID, transcriptPath, "", false)
	default:
		// Unrecognized event — accept but ignore, counted per name and reported
		// once, in the one place that behaviour is decided for every receiver
		// (issue #1364). This is where an opencode event rename, or an event a
		// future plugin version forwards that this daemon does not know,
		// becomes visible instead of silent.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
	w.WriteHeader(http.StatusOK)
}
