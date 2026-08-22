// hooks.go provides the HTTP handler for receiving Hermes Agent lifecycle
// events (issue #1722, epic #1355).
//
// Three events are installed and dispatched: pre_approval_request asserts the
// blocked-on-user signal, post_approval_response releases it, and
// on_session_end releases it AND reports the authoritative turn end.
//
// # hermes DOES have a blocked-on-user state, and its store cannot express it
//
// #1722 asked whether hermes has a blocked-on-user state and told this issue
// not to assume the answer. It does, and it is the whole reason a hook channel
// is worth anything here. Measured against Hermes Agent v0.19.0's own source:
//
//   - `tools/approval.py` asks the user before running a command that matched
//     a danger pattern, and it fires `pre_approval_request` immediately
//     before the prompt on every surface. It is NARROW: the guards run first
//     and the hook is reached only when something genuinely has to be asked,
//     so this is not the fires-on-every-tool event #1722 warned against
//     reusing `session.HookPreToolUse`'s row for. No row is reused at all —
//     each event below goes to a dedicated SessionDetector entry point, so
//     session.hookSignalEffects (a flat map with no adapter dimension) is
//     never consulted on this path.
//
//   - **The pending request is held in process memory only.** The CLI prompt
//     blocks inside `prompt_dangerous_approval`; the gateway path parks an
//     `_ApprovalEntry` in a module-level `_gateway_queues` map keyed by a
//     contextvar. `state.db` has `sessions`, `messages`,
//     `session_model_usage`, `state_meta`, `gateway_routing`,
//     `compression_locks` and `async_delegations` — and no approval table.
//     Nothing about "the user is being asked right now" is ever written, so
//     the ProcessOwnedStore this adapter reads cannot express it and never
//     could. Before this channel hermes had no `waiting` state at all.
//
//   - `post_approval_response` fires for EVERY outcome — `once`, `session`,
//     `always`, `deny`, `timeout`, `smart_approve`, `smart_deny` — so the
//     release really does arrive on the deny path, which is what puts hermes
//     out of copilot's position (copilot emits nothing on a denial, so a hold
//     with no release would pin the session `waiting`).
//
//   - `on_session_end` fires at the end of every `run_conversation` call
//     (agent/turn_finalizer.py), which is once per TURN despite the name, and
//     its extras carry `completed` and `interrupted`. This receiver releases
//     the permission hold on it as well as reporting turn end — the same
//     belt-and-braces gemini-cli's AfterAgent and opencode's session.idle use,
//     closing the one case no response covers: the process torn down with a
//     prompt still open.
//
// # Where the session id comes from, and why it is two fields
//
// hermes' shell-hook envelope has a top-level `session_id`, and
// `_serialize_payload` fills it from the firing site's `session_id` kwarg. The
// approval hooks do not pass one — they pass `session_key`, which lands in
// `extra`. So `on_session_end` arrives with a top-level id and the two
// approval events arrive with an empty one and the id in `extra.session_key`.
// For the CLI that key IS the store's session id (`cli.py` binds
// `set_current_session_key(self.session_id or "default")` before every turn),
// which is what makes the correlation work at all — but it is a DIFFERENT
// FIELD, and a receiver reading only the documented top-level one would
// silently never see an approval.
//
// Both spellings are run through sessionIDShape before anything downstream is
// touched. hermes mints `YYYYMMDD_HHMMSS_<6 hex>` (cli.py, agent_init.py,
// gateway/slash_commands.py all use the same expression, and the recorded
// fixtures agree), so a gateway platform's routing key, the literal "default"
// the contextvar falls back to, and anything else a caller might post at a
// local unauthenticated endpoint are dropped rather than turned into a phantom
// session.
//
// # Why the payload carries no path (contracttesting.PathDaemonDerived)
//
// hermes writes no transcript files — one shared state.db — so there is no
// per-session file for a caller to name. The "transcript path" every
// downstream consumer keys a hermes session on is a string the DAEMON
// composes, `<storePath>?session=<id>`, and transcriptPathFor is the one
// function both this receiver and the watcher use so the two keys cannot
// drift into two sessions.
//
// The receiver therefore reads its body through hookjson.DecodeSealed. That is
// not a relaxation of the #1361 guard, it is the guard's premise being absent:
// there is no caller-supplied path on this dispatch path for a traversal, a
// symlink or an escape to be expressed in. The obligation that replaces
// "confine what the caller named" is "prove nothing the caller names travels",
// graded from outside by AssertHookPathConfined's PathDaemonDerived route,
// whose obligations contradict the from-body route's directly — so declaring
// the wrong one fails rather than passing quietly. See hookpath_test.go.
package hermes

import (
	"net/http"
	"regexp"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving hermes lifecycle events
// (issue #570). Aliased to the shared domain constant rather than restated.
const PermissionKeyHooks = agent.HooksPermissionKey

// logComponentHookReceiver is the Logger component tag for every log line this
// receiver emits.
const logComponentHookReceiver = "hermes-hook-receiver"

// sessionIDShape is the id hermes mints for a session:
// `<YYYYMMDD>_<HHMMSS>_<6 lowercase hex>`. Every producer in the package
// builds it from the same expression —
// `f"{self.session_start.strftime('%Y%m%d_%H%M%S')}_{uuid.uuid4().hex[:6]}"` —
// and every id in this adapter's recorded fixtures matches.
//
// It is checked for the reason opencode checks its `ses` prefix: the endpoint
// is local and unauthenticated, and an id this adapter could never have issued
// must be dropped rather than turned into a session that does not exist. Here
// it does a second job the prefix check does not — the approval events'
// session key falls back to the literal "default" when a turn runs without one
// (`set_current_session_key(self.session_id or "default")`), and gateway
// surfaces put a platform routing key in the same field.
//
// # Gateway sessions, and why hex length is the whole guard (#1773)
//
// Gateway sessions are minted the same date/time shape but with 8 hex
// characters instead of 6 — `uuid.uuid4().hex[:8]`, gateway/session.py:2831
// and :3354, unchanged since v0.19.0 — against the CLI/TUI mint's
// `uuid.uuid4().hex[:6]` (agent/agent_init.py:1647, cli.py:5437,
// tui_gateway/server.py:7359) this regex actually accepts. At v0.19.0 a
// gateway approval's top-level session_id was empty, so it was rejected
// before this regex was ever consulted; hermes 0.20.5's
// tools/approval.py:126-133 now forwards a well-formed session_id for
// gateway turns too, so the hex-length distinction here is the ONLY thing
// standing between a gateway approval and a dispatch. See
// TestSessionIDShape_RejectsGatewayMintedIDs and
// TestHookReceiver_GatewaySurfaceApprovalIsQuiet (hooks_test.go), and the
// mutation check in hooks_mutations_test.go that proves the regex — not
// something else — is what rejects them.
var sessionIDShape = regexp.MustCompile(`^[0-9]{8}_[0-9]{6}_[0-9a-f]{6}$`)

// hermesHookPayload is the JSON body hermes pipes to a shell hook's stdin,
// forwarded verbatim by `irrlichd hook-post hermes`.
//
// The shape is agent/shell_hooks._serialize_payload's, captured live from
// `hermes hooks test`:
//
//	{"hook_event_name": "...", "tool_name": null, "tool_input": null,
//	 "session_id": "...", "cwd": "...", "extra": {...}}
//
// Only the three fields this receiver acts on are modelled. `cwd` is
// deliberately NOT one of them: it is the hermes process's own working
// directory at fire time and is the one thing the store genuinely cannot
// supply for a `source='cli'` session — but adopting it here would mean a
// caller-supplied path reaching project binding, which is precisely what the
// PathDaemonDerived declaration below says does not happen. Using it is a
// separate change with a confinement obligation of its own.
type hermesHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Extra         struct {
		// SessionKey is where the approval hooks put the session id; see this
		// file's header.
		SessionKey string `json:"session_key"`
	} `json:"extra"`
}

// sessionID returns the store session id this payload refers to, or "" when
// neither field carries one hermes could have minted.
func (p hermesHookPayload) sessionID() string {
	for _, candidate := range []string{p.SessionID, p.Extra.SessionKey} {
		if sessionIDShape.MatchString(candidate) {
			return candidate
		}
	}
	return ""
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

// NewHookHandler returns an http.Handler that receives hermes lifecycle events
// and dispatches them to the target.
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so an install left behind by a pre-consent
// daemon stays quiet. A nil gate means no gating — used by tests.
//
// The returned hookjson.HookHandler carries a NIL Confiner, and that is the
// declaration rather than an omission: this receiver has no caller-supplied
// path to confine, and a nil confiner is what makes hookjson.DecodeConfined
// fail CLOSED if a later edit switched this receiver back to the confining
// decode without giving the adapter roots.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(nil,
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyStore),
		func(consent hookjson.Consent, _ *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, consent, log, w, r)
		})
}

// serveHookRequest is NewHookHandler's request logic. The step order matches
// every sibling receiver's exactly, and each step is load-bearing — see the
// contract assertions in hook*_test.go.
func serveHookRequest(target HookTarget, consent hookjson.Consent, log outbound.Logger, w http.ResponseWriter, r *http.Request) {
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
	// only, ahead of the store check below.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the session store, so it is gated
	// behind "store" — this adapter's read permission — not merely the "hooks"
	// write consent. The check comes BEFORE the decode, so a denied session
	// still yields a quiet 200 and the body is never read.
	if !consent.Granted(PermissionKeyStore) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload hermesHookPayload
	if !hookjson.DecodeSealed(w, r, log, logComponentHookReceiver, consent, &payload) {
		return
	}

	sessionID := payload.sessionID()
	if sessionID == "" {
		// Not an id hermes could have minted — a gateway routing key, the
		// "default" fallback, or a caller inventing one. Dropped rather than
		// turned into a phantom session; the store watcher covers the state
		// regardless.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Composed by the daemon from its own store resolver, never from the body.
	transcriptPath := transcriptPathFor(sessionID)

	switch payload.HookEventName {
	case HookEventPreApprovalRequest:
		log.LogInfo(logComponentHookReceiver, sessionID, "received pre_approval_request")
		target.HandlePermissionPromptHook(sessionID, transcriptPath, HookEventPreApprovalRequest)
	case HookEventPostApprovalResponse:
		log.LogInfo(logComponentHookReceiver, sessionID, "received post_approval_response")
		target.ReleasePermissionPromptHold(sessionID)
	case HookEventOnSessionEnd:
		log.LogInfo(logComponentHookReceiver, sessionID, "received on_session_end")
		// Released before the turn-end report, and unconditionally: a release
		// on an unheld signal is a no-op, and this is the only thing that
		// closes a prompt torn down without a response (an interrupted turn, a
		// killed process).
		target.ReleasePermissionPromptHold(sessionID)
		// Payload-free, exactly as opencode's, pi's and vibe's single events
		// are: HandleStopHook asserts SignalTurnDone unconditionally and only
		// CONDITIONALLY overlays the text and cue, so the authoritative "the
		// turn truly ended" claim is real without them, and it can never clear
		// or mask a cue found elsewhere. hermes' envelope carries no message
		// content to forward.
		target.HandleStopHook(sessionID, transcriptPath, "", false)
	default:
		// Unrecognized event — accept but ignore, counted per name and reported
		// once, in the one place that behaviour is decided for every receiver
		// (issue #1364). This is where a hermes event rename, or an event a
		// future config forwards that this daemon does not know, becomes
		// visible instead of silent.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
	w.WriteHeader(http.StatusOK)
}
