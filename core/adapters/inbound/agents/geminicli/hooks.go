// hooks.go provides the HTTP handler for receiving Gemini CLI hook events
// (issue #1717, epic #1355 Phase D).
//
// Three events are installed, out of the eleven Gemini CLI's hook system
// fires (BeforeTool, AfterTool, BeforeAgent, Notification, AfterAgent,
// SessionStart, SessionEnd, PreCompress, BeforeModel, AfterModel,
// BeforeToolSelection — source-read from the installed CLI's own bundle, not
// from rendered docs). Each installed event is treated differently, and the
// asymmetry is the substance of this file:
//
//   - AfterAgent fires once at true turn end, gated by the CLI itself on
//     "every pending tool call in this turn has resolved" — so it holds
//     SignalTurnDone through HandleStopHook exactly like Claude Code's Stop,
//     giving an authoritative ready at turn end instead of one inferred from
//     the (nonexistent, for this adapter) turn_done transcript marker. It ALSO
//     unconditionally releases any held permission-prompt signal — see the
//     ReleasePermissionPromptHold call below and its own doc comment for why.
//
//   - AfterTool fires after EVERY tool call — source-confirmed: its payload
//     carries no discriminator distinguishing "ran silently" from "was
//     confirmed first". It is installed anyway, but ONLY as a broad release:
//     dispatched through the generic HandlePermissionHook under the
//     "AfterTool" name, which session.HookSignal maps to a Release, mirroring
//     exactly how claudecode's own broad PostToolUse releases the same
//     signal. Releasing broadly is safe (a Release on an unheld signal is a
//     no-op); it is only ASSERTING broadly that would be wrong, which is why
//     BeforeTool — Gemini's equally-broad pre-tool event — is not installed
//     at all. See hookinstaller.go's installedHookEvents comment.
//
//   - Notification, gated on notification_type == "ToolPermission" (today the
//     ONLY member of Gemini's NotificationType enum), is the narrow assert.
//     Source-traced: Gemini's scheduler fires this exactly when a tool
//     genuinely requires user confirmation — shouldConfirmExecute returned
//     non-nil — and fires it BEFORE blocking on the user's answer, regardless
//     of the eventual outcome. That is a real, content-based discriminator
//     BeforeTool cannot offer.
//
// Why the permission prompt gets its own dedicated method
// (HandlePermissionPromptHook) rather than going through the same generic
// dispatch AfterTool uses, the way claudecode's PreToolUse does:
//
// Gemini's wire name for this event is literally "Notification" — the same
// string Claude Code uses for ITS OWN Notification hook, and the same one
// copilot's Notification/permission_prompt dispatches through. session.
// HookSignal is one flat map keyed by that literal string with no adapter
// dimension: giving "Notification" a row there to make gemini's dispatch hold
// SignalPermissionPrompt would silently change what claudecode's and (more
// dangerously) copilot's OWN "Notification" dispatches do. Copilot's package
// comment explains why that would be a real regression for a live adapter:
// copilot emits nothing at all when the user denies a prompt, so a hold with
// no release would pin a denied session waiting for
// permissionPromptHoldTimeout (12h). Gemini is not in that position — its
// AfterAgent reliably fires after a cancelled confirmation too, and this
// adapter's ReleasePermissionPromptHold call closes exactly that gap — but the
// shared table has no way to express "holds for gemini, dispatch-only for
// copilot" against the same literal string, so the assert has to live outside
// it. HandlePermissionPromptHook is that outside-the-table path — the same
// shape session.SessionDetector already uses for HandleIdlePromptHook and
// HandleCompactHook, both of which also need a gate the generic table cannot
// express.
package geminicli

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving Gemini CLI hooks (issue
// #570).
//
// Aliased to the shared domain constant rather than restated: since #1383 two
// projections narrow on it — agents.HookConfigs (what --uninstall-hooks
// revokes) and the managed-user-file catalog — so a literal that drifted
// would silently drop this install out of both.
const PermissionKeyHooks = agent.HooksPermissionKey

// Hook event names, as they appear in the delivered envelope's
// hook_event_name field. Source-read from the installed CLI's bundle
// (HookEventName enum), not from rendered docs.
//
// HookAfterTool re-exports the domain constant added for this adapter
// (session.HookAfterTool); HookAfterAgent and HookNotification are local —
// AfterAgent has no cross-adapter meaning to share (it is dispatched to
// HandleStopHook directly, never through the name-keyed table), and
// "Notification" already has a domain constant (session.HookNotification)
// but this adapter deliberately does NOT reuse it as a dispatch key — see the
// package comment.
const (
	HookAfterTool    = session.HookAfterTool
	HookAfterAgent   = "AfterAgent"
	HookNotification = "Notification"
)

// notificationToolPermission is the one NotificationType value Gemini CLI's
// hook system defines today (source-read: the enum has exactly this member).
// Any OTHER value is future-proofing on Gemini's part, not ours — treated as
// a known-but-inert notification, never as an unrecognized event.
const notificationToolPermission = "ToolPermission"

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "geminicli-hook-receiver"

// transcriptExt is the file extension the hook receiver confines
// caller-supplied paths to. Gemini CLI's transcript files end .jsonl, the
// same as claudecode's and copilot's.
const transcriptExt = ".jsonl"

// geminiHookPayload is the JSON body Gemini CLI POSTs on a hook event.
//
// Unlike copilot, every event carries transcript_path directly — Gemini's own
// createBaseInput builds {session_id, transcript_path, cwd, hook_event_name,
// timestamp} for every fire site, so there is no Notification-must-
// reconstruct-the-path case here. SessionID is deliberately NOT read for
// session identity (see sessionIDFromPath below) — it is a full UUID
// (`config.getSessionId()`) that does not match the filename-stem session ID
// the fswatcher and every other adapter surface uses; it is decoded only so
// it can be logged.
type geminiHookPayload struct {
	HookEventName string `json:"hook_event_name"`

	// TranscriptPath is confined and then overwritten with the confined
	// spelling by DecodeConfined — see NewHookHandler.
	TranscriptPath string `json:"transcript_path"`

	// SessionID is Gemini's own session UUID, logged only. Never used to
	// derive the id this adapter dispatches under.
	SessionID string `json:"session_id"`

	// NotificationType distinguishes a blocked-on-user notification
	// (ToolPermission) from any other kind Gemini may add. Empty on every
	// other event.
	NotificationType string `json:"notification_type"`

	// PromptResponse is AfterAgent's final assistant text for the turn. Empty
	// on every other event.
	PromptResponse string `json:"prompt_response"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the
// same agent-agnostic surface claudecode's, codex's and copilot's hooks use,
// extended by the two methods this adapter's design needs (see the package
// comment).
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
	HandlePermissionPromptHook(sessionID, transcriptPath, hookName string)
	ReleasePermissionPromptHold(sessionID string)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates on
// (issue #570) — an alias, not a copy, so the receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.HandlerFunc that receives Gemini CLI hook
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

// transcriptConfiner returns the confiner this adapter's hook receiver guards
// caller-supplied transcript paths with, rooted in the adapter's own
// agent.Source declaration (issue #1361) rather than re-derived from the
// adapter's constants — so the tree the receiver confines to cannot drift
// from the one the daemon watches.
func transcriptConfiner() *hookjson.PathConfiner {
	return hookjson.ConfinerForSource(Source, runtime.GOOS, transcriptExt)
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth. The step order matches claudecode's, codex's and copilot's
// receivers exactly, and each step is load-bearing — see the contract
// assertions in hook*_test.go.
func serveHookRequest(target HookTarget, consent hookjson.Consent, log outbound.Logger, confiner *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Both keys come off the receiver's OWN hookjson.Consent (issue #1488), so
	// the set this sequence is written for is the set the handler publishes
	// and the contract derives. A key this receiver did not declare answers
	// false, so a drift between the two fails closed.
	if !consent.Granted(PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The channel delivered (issue #1368). Counted against the "hooks"
	// consent only — the transcripts gate below authorizes READING the
	// transcript, not observing that a POST arrived, and a hooks-granted /
	// transcripts-denied install has a working channel this counter must not
	// call dead.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the transcript, so it is gated
	// behind the "transcripts" consent, not merely the "hooks" write consent.
	// The check comes BEFORE the decode, and so before confinement, so a
	// denied session still yields a quiet 200 and the path is never resolved
	// on that branch.
	if !consent.Granted(PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The path arrives in an HTTP body on a local, unauthenticated endpoint,
	// so it is untrusted even though it came from our own beacon's payload
	// pass-through: the beacon forwards whatever Gemini CLI wrote to stdin
	// verbatim (core/pkg/hookbeacon's send doc), so a caller-supplied path is
	// exactly what this decodes. DecodeConfined reads the body and confines
	// in one step (issues #1361, #1389).
	var payload geminiHookPayload
	if !hookjson.DecodeConfined(w, r, log, logComponentHookReceiver, consent, confiner, &payload,
		func(p *geminiHookPayload) string { return p.TranscriptPath },
		func(p *geminiHookPayload, confined string) { p.TranscriptPath = confined },
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

	dispatchHookEvent(target, log, sessionID, transcriptPath, payload)
	w.WriteHeader(http.StatusOK)
}

// sessionIDFromTranscriptPath derives the session ID the SAME way
// agent.FilesUnderRoot's default does when no SessionIDFromPath override is
// declared (this adapter declares none — see agent.go) — the filename stem.
//
// Deliberately NOT payload.SessionID: that field carries Gemini's own
// config.getSessionId() UUID, which does not equal the filename-stem id every
// other part of this adapter (and the fswatcher itself) keys sessions on.
// Measured live: a session whose transcript is
// chats/session-2026-08-20T16-44-16031e41.jsonl reported
// session_id "16031e41-8a6d-4a8c-9479-ef742ed12f9c" in this same hook's
// payload — a different, longer string. Dispatching under the payload's
// spelling would silently mint a second, hook-only "session" the rest of the
// daemon never recognizes.
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

// dispatchHookEvent routes a decoded, consent-passed, confined, session-
// resolved payload to the right target method.
func dispatchHookEvent(target HookTarget, log outbound.Logger, sessionID, transcriptPath string, payload geminiHookPayload) {
	switch payload.HookEventName {
	case HookAfterTool:
		// Broad release. See the package comment on why broad is safe here
		// (a Release on an unheld signal is a no-op) where it would not be
		// for an assert.
		log.LogInfo(logComponentHookReceiver, sessionID, "received AfterTool")
		target.HandlePermissionHook(sessionID, transcriptPath, HookAfterTool)
	case HookAfterAgent:
		handleAfterAgentHook(target, log, sessionID, transcriptPath, payload)
	case HookNotification:
		handleNotificationHook(target, log, sessionID, transcriptPath, payload)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once, in the one place that behaviour is decided for
		// every receiver (issue #1364). serveHookRequest's 200 is
		// unaffected.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
}

// handleAfterAgentHook processes Gemini CLI's AfterAgent hook — the
// authoritative turn-done push, fired once at true turn end (issue #1717).
//
// The forwarded text is display-truncated the same way claudecode's and
// codex's Stop handling truncates theirs; the waiting-cue verdict is computed
// from a bounded tail window (WaitingScanWindow) of the FULL message — the
// same window parser.go would use for PendingWaitingCue — so a question or
// cue sitting before the display tail still routes the turn to waiting.
//
// ReleasePermissionPromptHold is called unconditionally alongside
// HandleStopHook — see that method's own doc comment (session_detector_
// lifecycle.go) for the source-verified reasoning: AfterTool's broad release
// never fires when a confirmation was CANCELLED (Gemini's scheduler returns
// before reaching tool execution), but AfterAgent still fires for that turn,
// making it the correct backstop rather than a silent reliance on the
// permission-prompt hold's 12-hour ceiling.
func handleAfterAgentHook(target HookTarget, log outbound.Logger, sessionID, transcriptPath string, payload geminiHookPayload) {
	log.LogInfo(logComponentHookReceiver, sessionID,
		"received AfterAgent")

	target.HandleStopHook(sessionID, transcriptPath,
		tailer.TruncateAssistantText(payload.PromptResponse),
		session.ProseIndicatesWaiting(tailer.WaitingScanWindow(payload.PromptResponse)))
	target.ReleasePermissionPromptHold(sessionID)
}

// handleNotificationHook processes a Gemini CLI Notification. Only
// notification_type == "ToolPermission" opens a hold — see the package
// comment for why this is a real, narrow discriminator and why it dispatches
// through a dedicated method rather than the generic name-keyed one.
//
// Any other notification type is ignored outright — it is a known event, not
// an unknown one, so it must not be counted as unrecognized.
func handleNotificationHook(target HookTarget, log outbound.Logger, sessionID, transcriptPath string, payload geminiHookPayload) {
	if payload.NotificationType != notificationToolPermission {
		return
	}

	log.LogInfo(logComponentHookReceiver, sessionID,
		"received Notification (ToolPermission)")
	target.HandlePermissionPromptHook(sessionID, transcriptPath, HookNotification)
}
