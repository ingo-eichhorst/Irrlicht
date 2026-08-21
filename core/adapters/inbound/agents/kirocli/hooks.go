// hooks.go provides the HTTP handler for receiving Kiro CLI hook events
// (issue #1716, epic #1355 Phase D).
//
// Two events are installed (see hookinstaller.go's installedHookEvents), and
// they are treated differently:
//
//   - stop fires once at true turn end and carries assistant_response — the
//     #1716 audit verified this live, a direct match for HandleStopHook,
//     giving an authoritative ready at turn end instead of one inferred from
//     kiro-cli's own turn-end heuristic (a text-only AssistantMessage;
//     parser.go's doc comment). It holds SignalTurnDone, a TierHook signal,
//     consume-once (session.signalPolicies), so it cannot bleed into the next
//     turn.
//
//   - postToolUse fires after EVERY tool call and is dispatched as a broad
//     RELEASE only — never an assert. See dispatchHookEvent for why it is
//     forwarded under the EXISTING session.HookPostToolUse name rather than
//     under kiro-cli's own "postToolUse" spelling.
//
// preToolUse — the event that would seem to pair with postToolUse as an
// assert/release couple — is deliberately NOT installed at all. The audit
// found it fires for every tool call under trust-all with no discriminator
// (in the payload or the transcript) separating "waiting for approval" from
// "running now"; only duration tells them apart, live-measured, and this
// issue never calibrated a threshold for it. So there is no case in
// dispatchHookEvent for it, and it falls through to
// hookjson.IgnoreUnknownEvent like any other kiro-cli event we did not ask
// for (agentSpawn, userPromptSubmit) if one ever arrives.
package kirocli

import (
	"net/http"
	"path/filepath"
	"runtime"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// Hook event names, exactly as kiro-cli puts them on the wire
// (hook_event_name) — verified live against kiro-cli 2.6.0. Unlike
// claudecode's PascalCase and copilot's Claude-compat aliasing, kiro-cli's
// own spelling is used as-is: "stop" is lowercase and "postToolUse" is
// camelCase, and neither matches an existing session.HookXxx constant's
// VALUE (session.HookStop == "Stop"), so these are local rather than
// re-exported.
const (
	HookStop        = "stop"
	HookPostToolUse = "postToolUse"
)

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "kirocli-hook-receiver"

// transcriptExt is the file extension the hook receiver confines
// caller-supplied paths to.
const transcriptExt = ".jsonl"

// kiroHookPayload is the JSON body kiro-cli pipes on stdin for a hook event —
// forwarded verbatim as the beacon's POST body (core/pkg/hookbeacon).
//
// Verified live (#1716's audit): the envelope carries NO transcript_path of
// any spelling on any event — {"hook_event_name":"stop","cwd":...,
// "assistant_response":"ok"} is the complete stop body. session_id IS the
// <uuid>.jsonl transcript filename the fswatcher already tails (verified
// byte-for-byte), so every event's path is reconstructed the same way
// copilot's Notification-only reconstruction works, applied uniformly here
// because NO kiro-cli event carries a path directly.
//
// Headless sessions fire hooks but omit session_id and write no transcript
// at all (the audit's own finding, extending the already-recorded "headless
// is invisible" result to hook correlation) — there is nothing to correlate
// to, so sessionIDFromPayload below returns "" and the receiver drops the
// request.
type kiroHookPayload struct {
	HookEventName string `json:"hook_event_name"`

	// SessionID is what every hook event correlates on. Never carried
	// redundantly via KIRO_SESSION_ID here: the beacon (core/pkg/hookbeacon)
	// forwards stdin verbatim and does not read or forward the child's
	// environment, so the payload's own field is the only channel available
	// to this receiver.
	SessionID string `json:"session_id"`

	// AssistantResponse is stop's final turn text. Empty on every other
	// event.
	AssistantResponse string `json:"assistant_response"`

	// TranscriptPath is never present on the wire (see the type doc). It
	// exists so DecodeConfined has a field to write the confined,
	// session-id-derived path back into (issue #1389's postcondition).
	TranscriptPath string `json:"-"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the
// same agent-agnostic surface claudecode's, codex's, copilot's and
// gemini-cli's hooks use.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates
// on (issue #570) — an alias, not a copy, so the receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.HandlerFunc that receives kiro-cli hook
// events and dispatches them to the target. The returned hookjson.HookHandler
// is an http.Handler carrying the confiner it guards caller-supplied
// transcript paths with (issue #1390).
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so the beacon-delivered hook stays quiet. A
// nil gate means no gating — used by tests.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyTranscripts),
		func(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, consent, log, c, w, r)
		})
}

// transcriptConfiner returns the confiner this adapter's hook receiver
// guards caller-supplied transcript paths with, rooted in the adapter's own
// agent.Source declaration (issue #1361) rather than re-derived from the
// adapter's constants.
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

	if !consent.Granted(PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The channel delivered (issue #1368). Counted against the "hooks"
	// consent only, ahead of the transcripts check below — see every sibling
	// receiver's identical comment for why the order is load-bearing in both
	// directions.
	hookjson.ObserveHookReceipt(AdapterName)

	if !consent.Granted(PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Every kiro-cli event's path is synthesized from session_id (see the
	// payload type doc — none carries transcript_path), and even so it is
	// untrusted: session_id arrives in an HTTP body on a local,
	// unauthenticated endpoint, so a hostile "../../etc" id must be refused
	// exactly as a caller-supplied path would be. DecodeConfined reads the
	// body and confines in one step (issues #1361, #1389).
	//
	// A HEADLESS session's stop fires with no session_id at all (the audit's
	// own finding — headless runs write no transcript either, so there is
	// nothing to correlate to). resolveTranscriptPath then returns "", and
	// Confine's own RejectEmptyPath answers that with a 400 — the "missing
	// transcript_path" exception hookjson.RejectPath's doc carries, since an
	// empty candidate reads as a malformed body rather than a real path
	// decision. DecodeConfined returns false and this function's own return
	// below is unreached for that case, exactly as it is for a genuinely
	// malformed body; there is no separate "drop quietly" branch to write,
	// because DecodeConfined already answered before sessionID could be
	// inspected here.
	var payload kiroHookPayload
	if !hookjson.DecodeConfined(w, r, log, logComponentHookReceiver, consent, confiner, &payload,
		func(p *kiroHookPayload) string { return resolveTranscriptPath(*p) },
		func(p *kiroHookPayload, confined string) { p.TranscriptPath = confined },
	) {
		return
	}

	dispatchHookEvent(target, log, payload.SessionID, payload.TranscriptPath, payload)
	w.WriteHeader(http.StatusOK)
}

// resolveTranscriptPath rebuilds the transcript path every kiro-cli event
// implies but never carries, from the caller-supplied session id against the
// adapter's OWN declared root — never from anything else in the payload. The
// result is still confined by the caller (Confine), which is what makes a
// hostile session id harmless: it is the confiner, not this function, that
// decides the path is in-tree.
//
// Kiro CLI's transcripts are flat — <uuid>.jsonl directly under
// sessions/cli/, unlike copilot's <session-id>/events.jsonl nesting — so no
// sub-path is joined beyond the filename.
//
// The root MUST be absolutised: sessionsDir() returns the $HOME-relative
// literal "sessions/cli" whenever KIRO_HOME is unset, and
// PathConfiner.Confine refuses a non-absolute path outright before it ever
// consults the roots. Every hook test in this package relocates KIRO_HOME to
// an absolute temp dir, which is exactly the spelling that would hide a
// regression here (see copilot's resolveTranscriptPath for the same warning).
func resolveTranscriptPath(payload kiroHookPayload) string {
	id := payload.SessionID
	if id == "" {
		return ""
	}
	src, ok := Source().(agent.FilesUnderRoot)
	if !ok {
		return ""
	}
	root, err := agentpaths.AbsRoot(src.RootDirFor(runtime.GOOS))
	if err != nil {
		// No resolvable root — fail closed. An empty path is refused as
		// RejectEmptyPath, which is the honest reason.
		return ""
	}
	return filepath.Join(root, id+transcriptExt)
}

// dispatchHookEvent routes a decoded, consent-passed, confined,
// session-resolved payload to the right target method.
func dispatchHookEvent(target HookTarget, log outbound.Logger, sessionID, transcriptPath string, payload kiroHookPayload) {
	switch payload.HookEventName {
	case HookStop:
		log.LogInfo(logComponentHookReceiver, sessionID, "received stop")
		text := tailer.TruncateAssistantText(payload.AssistantResponse)
		cue := session.ProseIndicatesWaiting(tailer.WaitingScanWindow(payload.AssistantResponse))
		target.HandleStopHook(sessionID, transcriptPath, text, cue)
	case HookPostToolUse:
		// Broad release, mirroring exactly what gemini-cli's AfterTool and
		// claudecode's own (narrowly-matchered) PostToolUse both do: a
		// Release on an unheld signal is a no-op, so dispatching broadly is
		// safe even though no event this adapter installs ever ASSERTS
		// SignalPermissionPrompt (preToolUse is not installed — see the
		// package comment).
		//
		// Forwarded under session.HookPostToolUse — Claude Code's own
		// "PostToolUse" spelling — rather than under kiro-cli's own
		// "postToolUse" wire name. session.hookSignalEffects is a flat map
		// keyed by the literal dispatched string with, by its own doc
		// comment, "no adapter dimension": copilot already reuses
		// session.HookStop and session.HookNotification as dispatch keys for
		// exactly this reason, rather than adding an adapter-specific row to
		// that shared, cross-cutting table for an effect ("release
		// SignalPermissionPrompt broadly") an existing row already means.
		// Adding a new "postToolUse" row there would be the shared-code
		// change AGENTS.md asks to flag before making; reusing the existing
		// one needs none.
		log.LogInfo(logComponentHookReceiver, sessionID, "received postToolUse")
		target.HandlePermissionHook(sessionID, transcriptPath, session.HookPostToolUse)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once (issue #1364). serveHookRequest's 200 is unaffected.
		// This is also where preToolUse, agentSpawn and userPromptSubmit land
		// if kiro-cli ever sends one of them despite this adapter installing
		// none of the three.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
}
