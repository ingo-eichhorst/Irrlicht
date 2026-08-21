// hooks.go provides the HTTP handler for receiving Mistral Vibe hook events
// (issue #1718, epic #1355).
//
// Exactly one event is installed: post_agent_turn, dispatched to
// HandleStopHook with an empty text and a false waitingCue. Vibe's own
// payload for this event carries no message content at all — source-read
// (vibe/core/hooks/models.go: PostAgentTurnInvocation adds nothing to the
// shared HookSessionContext) AND live-fired against the installed 2.19.1
// CLI during this issue's audit, which confirmed the delivered body is
// exactly {session_id, transcript_path, cwd, parent_session_id,
// hook_event_name}. This is the SAME "honest floor" hook_signal.go already
// documents for a payload-free HookStop row: HandleStopHook still asserts
// SignalTurnDone/HookTurnDone unconditionally and only conditionally
// overlays the text/cue fields, so the authoritative "turn truly ended"
// signal is real even without message content — it just cannot correct a
// waiting-cue verdict the transcript tail already computed, which is the
// same limit every other adapter's payload-free path accepts.
//
// before_tool and after_tool are deliberately NOT installed, and this is
// the ticket's central negative finding (see the audit comment on #1718).
// before_tool fires unconditionally for EVERY tool call, before Vibe's own
// internal permission check even runs — source-traced in
// vibe/core/agent_loop/_loop.py's _execute_tool_call:
// _run_before_tool_pipeline runs, THEN _should_execute_tool/_ask_approval —
// and confirmed by Vibe's own bundled, LLM-facing documentation ("before_tool
// | Per tool call, before the user permission prompt"). Its payload carries
// no field indicating whether THIS call will reach an interactive prompt
// (that depends on per-session ALWAYS/NEVER/ASK runtime state the hook never
// sees), so asserting SignalPermissionPrompt on it would hold `waiting` on
// every single tool call, approved or not. after_tool was considered as a
// broad release (the pattern gemini-cli's AfterTool uses) but that pattern
// is only safe paired with a narrow assert on the SAME signal from the SAME
// adapter — Vibe has none, and repo-wide session.SignalPermissionPrompt is
// held only by hook-derived calls (HandlePermissionHook/
// HandlePermissionPromptHook — nothing transcript-heuristic touches it), so
// a release with nothing to ever release would be pure surface area with
// zero behavioral value.
package vibe

import (
	"net/http"
	"runtime"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving Mistral Vibe hooks
// (issue #570). Aliased to the shared domain constant rather than restated —
// see agent.HooksPermissionKey's own doc for why two external projections
// narrow on it.
const PermissionKeyHooks = agent.HooksPermissionKey

// logComponentHookReceiver is the Logger component tag for every log line
// this receiver emits.
const logComponentHookReceiver = "vibe-hook-receiver"

// transcriptExt is the file extension the hook receiver confines
// caller-supplied paths to — messages.jsonl, the constant filename every
// Vibe session transcript uses (see adapter.go's transcriptFilename).
const transcriptExt = ".jsonl"

// vibeHookPayload is the JSON body Vibe POSTs on a hook event — live-fired
// and confirmed against the installed 2.19.1 CLI. SessionID is decoded only
// to be available for logging; session identity is derived from the
// transcript path (sessionIDFromTranscriptPath below), the same choice
// every other JSON hook receiver in this codebase makes and for the same
// reason: it is the id the fswatcher and the rest of the daemon already key
// sessions on, where a hook's own session_id is not guaranteed to agree
// (gemini-cli's hooks.go documents a measured case where it did not).
type vibeHookPayload struct {
	HookEventName string `json:"hook_event_name"`

	// TranscriptPath is confined and then overwritten with the confined
	// spelling by DecodeConfined — see NewHookHandler.
	TranscriptPath string `json:"transcript_path"`

	// SessionID is Vibe's own session id, logged only. Never used to derive
	// the id this adapter dispatches under.
	SessionID string `json:"session_id"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the
// same agent-agnostic surface claudecode's, codex's, copilot's and
// geminicli's hooks use, narrowed to the one method this adapter's single
// installed event needs.
type HookTarget interface {
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates
// on (issue #570) — an alias, not a copy, so the receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.HandlerFunc that receives Mistral Vibe hook
// events and dispatches them to the target. The returned hookjson.HookHandler
// is an http.Handler carrying the confiner it guards caller-supplied
// transcript paths with (issue #1390).
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so the installed hook stays quiet. A nil gate
// means no gating — used by tests.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyTranscripts),
		func(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, consent, log, c, w, r)
		})
}

// transcriptConfiner returns the confiner this adapter's hook receiver
// guards caller-supplied transcript paths with, rooted in the adapter's own
// Source declaration (issue #1361) rather than re-derived from the
// adapter's constants — so the tree the receiver confines to cannot drift
// from the one the daemon watches.
func transcriptConfiner() *hookjson.PathConfiner {
	return hookjson.ConfinerForSource(Source, runtime.GOOS, transcriptExt)
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth. The step order matches every other JSON hook receiver's
// exactly — see the contract assertions in hook*_test.go.
func serveHookRequest(target HookTarget, consent hookjson.Consent, log outbound.Logger, confiner *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Both keys come off the receiver's OWN hookjson.Consent (issue #1488),
	// so the set this sequence is written for is the set the handler
	// publishes and the contract derives.
	if !consent.Granted(PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The channel delivered (issue #1368). Counted against the "hooks"
	// consent only.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the transcript, so it is gated
	// behind "transcripts", not merely the "hooks" write consent. The check
	// comes BEFORE the decode, and so before confinement, so a denied
	// session still yields a quiet 200 and the path is never resolved.
	if !consent.Granted(PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The path arrives in an HTTP body on a local, unauthenticated endpoint,
	// so it is untrusted even though it comes from our own beacon's payload
	// pass-through. DecodeConfined reads the body and confines in one step
	// (issues #1361, #1389).
	var payload vibeHookPayload
	if !hookjson.DecodeConfined(w, r, log, logComponentHookReceiver, consent, confiner, &payload,
		func(p *vibeHookPayload) string { return p.TranscriptPath },
		func(p *vibeHookPayload, confined string) { p.TranscriptPath = confined },
	) {
		return
	}
	transcriptPath := payload.TranscriptPath

	sessionID := sessionIDFromPath(transcriptPath)
	if sessionID == "" {
		// Not a path this adapter owns — drop rather than guess an id. The
		// transcript tailer covers the state regardless.
		w.WriteHeader(http.StatusOK)
		return
	}

	switch payload.HookEventName {
	case hookEventPostAgentTurn:
		log.LogInfo(logComponentHookReceiver, sessionID, "received post_agent_turn")
		target.HandleStopHook(sessionID, transcriptPath, "", false)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once, in the one place that behaviour is decided for
		// every receiver (issue #1364).
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
	w.WriteHeader(http.StatusOK)
}
