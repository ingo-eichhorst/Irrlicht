// hooks.go provides the HTTP handler for receiving pi lifecycle events
// (issue #1721, epic #1355).
//
// Exactly one event is installed and dispatched: agent_settled, routed to
// HandleStopHook with an empty text and a false waiting cue. See
// hookinstaller.go's installedHookEvents for what is deliberately NOT
// installed and why.
//
// # The payload-free floor is deliberate, and it is the cheaper half of a
// # supply-chain trade
//
// pi's agent_settled event carries no message content (docs/extensions.md:
// the handler signature is `(_event, ctx)` and the event object has no
// message field), and the extension COULD read the last assistant message
// out of ctx.sessionManager and forward it. It does not, on purpose: every
// additional line in extension.js is a line of executable code irrlicht
// ships into a user's agent, and this is exactly the "honest floor"
// hook_signal.go already documents for a payload-free HookStop —
// HandleStopHook asserts SignalTurnDone unconditionally and only
// CONDITIONALLY overlays the text and cue, so the authoritative "the turn
// truly ended" signal is real without them. What it gives up is a waiting
// cue sitting outside the transcript tail the parser already scans; it can
// never clear or mask a cue the transcript found on its own.
//
// # pi's one blocked-on-user state, and why it is not here
//
// #1721 asked whether pi has any blocked-on-user state at all and told this
// issue not to assume the answer. Measured against the installed package
// (0.83.0):
//
//   - There is no tool-approval prompt. pi has no sandbox (its own
//     docs/security.md: tools run with the process's permissions), so
//     nothing blocks before a tool runs.
//   - `ask_question` — which pi's README, docs/usage.md and docs/sdk.md all
//     name as an --exclude-tools example — is NOT in 0.83.0. The string
//     "ask_question" does not appear as a quoted identifier anywhere under
//     dist/ (0 files, against 18 for "bash" and 14 for "read"). #1721's own
//     measurement, reproduced independently here. It is docs drift.
//   - pi DOES have one genuine built-in blocking prompt: the project trust
//     prompt (docs/settings.md: "On interactive startup, pi asks before
//     trusting a project folder..."), surfaced to extensions as the
//     `project_trust` event.
//
// project_trust is nonetheless unusable as a `waiting` signal, and the
// reason is structural rather than a matter of effort: it fires BEFORE a
// session exists, and its context is explicitly a reduced one —
// docs/extensions.md, "ctx has a limited trust context: cwd, mode, hasUI,
// and select/confirm/input/notify UI helpers". There is no sessionManager
// and therefore no transcript path, which is the only thing this adapter
// correlates a session on. A hook irrlicht cannot attribute to a session is
// a hook irrlicht cannot act on. Live-confirmed by the event walk this issue
// ran against pi 0.83.0: session_start is the FIRST event carrying a session
// file at all.
//
// So pi is, through hooks, a two-state adapter — working/ready — and that is
// a measured result rather than a gap left open. `waiting` remains reachable
// for pi exactly where it already was: the transcript-tail prose cue.
package pi

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving pi lifecycle events
// (issue #570). Aliased to the shared domain constant rather than restated —
// see agent.HooksPermissionKey's own doc for why two external projections
// narrow on it.
const PermissionKeyHooks = agent.HooksPermissionKey

// logComponentHookReceiver is the Logger component tag for every log line
// this receiver emits.
const logComponentHookReceiver = "pi-hook-receiver"

// transcriptExt is the file extension the hook receiver confines
// caller-supplied paths to.
const transcriptExt = ".jsonl"

// piHookPayload is the JSON body irrlicht's own pi extension writes to the
// beacon's stdin (extension.js), forwarded verbatim by
// `irrlichd hook-post pi`.
//
// Deliberately minimal, and deliberately carrying no session_id: pi's
// extension API hands the extension the session's transcript file directly
// (ctx.sessionManager.getSessionFile(), live-verified to be the absolute
// path of the .jsonl the fswatcher already tails), and the filename stem of
// that path IS the id every other part of this adapter keys sessions on. A
// separate session_id field would be a second spelling of the same fact that
// could disagree with it — which is not hypothetical: geminicli's hooks.go
// records a measured case where the agent's own session_id and the
// filename-stem id were different strings.
//
// It arrives on a local, unauthenticated endpoint, so "our own extension
// wrote it" buys nothing: the path is confined before anything reads it.
type piHookPayload struct {
	HookEventName string `json:"hook_event_name"`

	// TranscriptPath is confined and then overwritten with the confined
	// spelling by DecodeConfined — see NewHookHandler.
	TranscriptPath string `json:"transcript_path"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the
// same agent-agnostic surface every other receiver uses, narrowed to the one
// method this adapter's single installed event needs.
type HookTarget interface {
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates
// on (issue #570) — an alias, not a copy, so the receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.Handler that receives pi lifecycle events
// and dispatches them to the target. The returned hookjson.HookHandler is
// the handler together with the confiner it guards caller-supplied
// transcript paths with (issue #1390).
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so an install left behind by a pre-consent
// daemon stays quiet. A nil gate means no gating — used by tests.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyTranscripts),
		func(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, consent, log, c, w, r)
		})
}

// transcriptConfiner returns the confiner this adapter's hook receiver
// guards caller-supplied transcript paths with, rooted in the adapter's own
// Source declaration (issue #1361) rather than re-derived from the adapter's
// constants — so the tree the receiver confines to cannot drift from the one
// the daemon watches.
func transcriptConfiner() *hookjson.PathConfiner {
	return hookjson.ConfinerForSource(Source, runtime.GOOS, transcriptExt)
}

// serveHookRequest is NewHookHandler's request logic. The step order matches
// every sibling receiver's exactly, and each step is load-bearing — see the
// contract assertions in hook*_test.go.
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
	// consent only, ahead of the transcripts check below.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the transcript, so it is gated
	// behind "transcripts", not merely the "hooks" write consent. The check
	// comes BEFORE the decode, and so before confinement, so a denied
	// session still yields a quiet 200 and the path is never resolved.
	if !consent.Granted(PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload piHookPayload
	if !hookjson.DecodeConfined(w, r, log, logComponentHookReceiver, consent, confiner, &payload,
		func(p *piHookPayload) string { return p.TranscriptPath },
		func(p *piHookPayload, confined string) { p.TranscriptPath = confined },
	) {
		return
	}
	transcriptPath := payload.TranscriptPath

	sessionID := sessionIDFromTranscriptPath(transcriptPath)
	if sessionID == "" {
		// Not a path this adapter owns — drop rather than guess an id. The
		// transcript tailer covers the state regardless.
		w.WriteHeader(http.StatusOK)
		return
	}

	switch payload.HookEventName {
	case HookEventAgentSettled:
		log.LogInfo(logComponentHookReceiver, sessionID, "received agent_settled")
		target.HandleStopHook(sessionID, transcriptPath, "", false)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once, in the one place that behaviour is decided for
		// every receiver (issue #1364). This is where a pi event rename, or
		// an event a future extension version subscribes to that this daemon
		// does not know, becomes visible instead of silent.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
	w.WriteHeader(http.StatusOK)
}

// sessionIDFromTranscriptPath derives the session ID the SAME way
// agent.FilesUnderRoot's default does when no SessionIDFromPath override is
// declared (this adapter declares none — see agent.go): the filename stem.
//
// pi's transcripts are <timestamp>_<uuid>.jsonl under a per-cwd directory
// (sessions/--<cwd-with-dashes>--/), so the stem is the whole
// "2026-08-22T03-42-52-286Z_01a02790-..." id — live-verified against pi
// 0.83.0 during this issue's audit.
func sessionIDFromTranscriptPath(path string) string {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, transcriptExt) {
		return ""
	}
	id := strings.TrimSuffix(base, transcriptExt)
	if id == "" {
		return ""
	}
	return id
}
