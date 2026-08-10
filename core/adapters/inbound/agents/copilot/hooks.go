// hooks.go provides the HTTP handler for receiving GitHub Copilot CLI hook
// events (issue #1378, epic #1355 Phase D).
//
// Two events are installed, and they are treated very differently. The
// asymmetry is the substance of this file, so it is stated up front:
//
//   - Stop fires once at true turn end and carries transcript_path. It holds
//     SignalTurnDone, a TierHook signal, giving an authoritative ready at turn
//     end instead of one inferred from the transcript tail. The hold is
//     consume-once (see session.signalPolicies), so it cannot bleed into the
//     next turn and cannot pin a session by construction.
//
//   - Notification/permission_prompt fires at prompt entry, and is DISPATCH
//     ONLY: it injects a synthetic activity event so the session is
//     re-classified immediately, but installs no hold. The waiting verdict
//     still comes from the transcript's own permission.requested →
//     TranscriptPermissionPending → transcript_permission_prompt rule (#1256),
//     at TierTranscript.
//
// Why the permission prompt does not hold a TierHook signal, which is the
// obvious thing to want and is what claudecode and codex both do:
//
// Copilot emits NOTHING when the user denies a prompt. Verified live on 1.0.78
// by driving an interactive session and refusing the prompt: no Stop, no
// postToolUse, no postToolUseFailure, no errorOccurred arrived in 42 seconds,
// while the UI had already returned to ready. The shared SignalPermissionPrompt
// row is released either by a PostToolUse-style Release (which only fires on
// approval) or by its stale predicate, Metrics.LastWasToolDenial — and this
// adapter's parser deliberately declines to set that flag, because it is Claude
// Code's turn-ENDING cancellation marker whereas Copilot carries on after a
// denial (see parsePermissionCompleted). With neither release available, a hold
// taken on a prompt the user then denies would stay held until the 12-hour
// ceiling (#1360) — the session would read waiting for the rest of the day.
//
// A dispatch-only notification keeps the whole benefit that is actually
// available here and none of that risk. Copilot writes permission.requested to
// its transcript roughly a millisecond before it fires the hook (measured:
// 22:58:27.940 vs 22:58:27.941), so the hook was never going to beat the
// transcript to the fact — it can only beat the daemon's own observation lag,
// which is exactly what the synthetic activity event removes.
package copilot

import (
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving Copilot hooks (issue #570).
//
// Aliased to the shared domain constant rather than restated: since #1383 two
// projections narrow on it — agents.HookConfigs (what --uninstall-hooks
// revokes) and the managed-user-file catalog — so a literal that drifted would
// silently drop this install out of both.
const PermissionKeyHooks = agent.HooksPermissionKey

// Hook event names, as they appear in the delivered envelope's
// hook_event_name field.
//
// Re-exports of the domain constants rather than independent literals, so a
// rename on this side cannot silently stop matching the shared table — the
// drift #1320 removed, one adapter over.
//
// Copilot's own event roster is camelCase (notification, agentStop). Both are
// registered here under their PascalCase Claude-compat aliases, which is not
// cosmetic: registered as Stop, the turn-end envelope arrives in Claude Code's
// exact shape (hook_event_name, session_id, transcript_path — all snake_case),
// whereas the native agentStop registration delivers camelCase keys and no
// hook_event_name at all. One field name to switch on, for both events.
const (
	HookNotification = session.HookNotification
	HookStop         = session.HookStop
)

// Notification types that mean "Copilot is blocked on the user". Copilot fires
// notification for other reasons too (idle turns, background task completion),
// and only these two open a state.
const (
	notificationPermissionPrompt  = "permission_prompt"
	notificationElicitationDialog = "elicitation_dialog"
)

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "copilot-hook-receiver"

// transcriptExt is the file extension the hook receiver confines
// caller-supplied paths to. It must be an EXTENSION, not the full basename:
// the confiner compares it with filepath.Ext, so the constant transcript
// filename would match nothing and refuse every legitimate path.
//
// Confining by extension is not the weaker check it looks like. The exact
// basename is still required immediately afterwards by sessionIDFromPath,
// which returns "" for anything that is not events.jsonl and makes the
// receiver drop the request — so a confined-but-wrong file in the tree
// (workspace.yaml, session.db) never reaches a dispatch.
const transcriptExt = ".jsonl"

// copilotHookPayload is the JSON body Copilot POSTs on a hook event.
//
// It decodes BOTH envelope shapes on purpose, because Copilot does not use one.
// Registered under a PascalCase alias, Stop arrives Claude-shaped
// (session_id / transcript_path). Notification ignores the compat mapping
// entirely — verified live by registering it under both spellings and
// receiving byte-identical bodies — and arrives with a camelCase sessionId,
// no transcript path of any spelling, plus hook_event_name and
// notification_type. Hence the paired fields and resolveTranscriptPath below.
type copilotHookPayload struct {
	HookEventName string `json:"hook_event_name"`

	// TranscriptPath is carried by Stop and absent from Notification.
	TranscriptPath string `json:"transcript_path"`

	// SessionID / SessionIDCamel are the two spellings of the same value.
	// Copilot's session id is the session-state directory name, which is what
	// sessionIDFromPath derives from a transcript path, so either one can
	// reconstruct the path the other omits.
	SessionID      string `json:"session_id"`
	SessionIDCamel string `json:"sessionId"`

	// NotificationType distinguishes a blocked-on-user notification from the
	// idle and background-task ones. Empty on Stop.
	NotificationType string `json:"notification_type"`
}

// sessionID returns whichever spelling the envelope used.
func (p copilotHookPayload) sessionID() string {
	if p.SessionID != "" {
		return p.SessionID
	}
	return p.SessionIDCamel
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the same
// agent-agnostic surface claudecode's and codex's hooks use.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates on
// (issue #570) — an alias, not a copy, so the three receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.HandlerFunc that receives Copilot hook events
// and dispatches them to the target. The returned hookjson.HookHandler is an
// http.Handler carrying the confiner it guards caller-supplied transcript paths
// with (issue #1390).
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so the installed hook stays quiet. A nil gate
// means no gating — used by tests.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		func(c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, gate, log, c, w, r)
		})
}

// transcriptConfiner returns the confiner this adapter's hook receiver guards
// caller-supplied transcript paths with, rooted in the adapter's own
// agent.Source declaration (issue #1361) rather than re-derived from the
// adapter's constants — so the tree the receiver confines to cannot drift from
// the one the daemon watches.
func transcriptConfiner() *hookjson.PathConfiner {
	return hookjson.ConfinerForSource(Source, runtime.GOOS, transcriptExt)
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth. The step order matches codex's receiver exactly and each step
// is load-bearing — see the contract assertions in hook*_test.go.
func serveHookRequest(target HookTarget, gate ConsentGranter, log outbound.Logger, confiner *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if gate != nil && !gate.Granted(AdapterName, PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The channel delivered (issue #1368). Counted against the "hooks" consent
	// only — the transcripts gate below authorizes READING the transcript, not
	// observing that a POST arrived, and a hooks-granted / transcripts-denied
	// install has a working channel this counter must not call dead.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the transcript, so it is gated
	// behind the "transcripts" consent, not merely the "hooks" write consent.
	// The check comes BEFORE the decode, and so before confinement, so a denied
	// session still yields a quiet 200 and the path is never resolved on that
	// branch. It used to sit between the decode and the confinement; #1389
	// welded those two into one call, and this moved above the pair rather than
	// being folded into it — see codex's receiver for the full argument, which
	// this one mirrors step for step.
	if gate != nil && !gate.Granted(AdapterName, PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The path arrives in an HTTP body on a local, unauthenticated endpoint, so
	// it is untrusted even when we assembled it ourselves from a caller-supplied
	// session id: any local process could otherwise steer the daemon into
	// opening a file of its choosing by sending "../../..". DecodeConfined
	// reads the body and confines in one step (issues #1361, #1389).
	//
	// This receiver is why DecodeConfined takes a get FUNCTION rather than a
	// field name: on Notification there is no transcript path in the envelope
	// at all, and resolveTranscriptPath synthesizes one from the session id.
	// Synthesized or not, it goes through the same confiner — and the write-back
	// then leaves payload.TranscriptPath holding the confined path on BOTH
	// branches, where before it kept the caller's raw string on Stop.
	var payload copilotHookPayload
	if !hookjson.DecodeConfined(w, r, log, logComponentHookReceiver, confiner, &payload,
		func(p *copilotHookPayload) string { return resolveTranscriptPath(*p) },
		func(p *copilotHookPayload, confined string) { p.TranscriptPath = confined },
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

	dispatchHookEvent(target, log, sessionID, transcriptPath, payload)
	w.WriteHeader(http.StatusOK)
}

// resolveTranscriptPath returns the transcript path to confine.
//
// Stop supplies one directly. Notification supplies none at all, so it is
// reconstructed from the session id against the adapter's OWN declared root —
// never from anything the caller sent. The result is still confined by the
// caller, which is what makes a hostile session id ("../../etc") harmless: it
// is the confiner, not this function, that decides the path is in-tree.
//
// The root MUST be absolutised. sessionsDir() returns the $HOME-relative
// literal ".copilot/session-state" whenever COPILOT_HOME is unset — the
// configuration every ordinary user is in — and PathConfiner.Confine refuses a
// non-absolute path outright (RejectRelativePath) before it ever consults the
// roots. Without AbsRoot the whole Notification branch is therefore dead in the
// default install: nothing dispatches, an error-level line is logged per
// prompt, and the confiner's rejection counter — whose job is to signal a local
// process probing the endpoint — accrues one false positive per legitimate
// permission prompt. Every hook test in this package relocates COPILOT_HOME to
// an absolute temp dir, which is exactly the spelling that hides it;
// hookdefaulthome_test.go is the one that does not.
//
// AbsRoot rather than copilotSessionsDir(): the confiner rebuilds the accepted
// path on the DECLARED root, deliberately un-symlink-resolved, so that the hook's
// key and the fswatcher's key are the same string (see confine.go). Handing it a
// pre-resolved root would reintroduce the two-spellings-one-session problem that
// comment exists to prevent.
func resolveTranscriptPath(payload copilotHookPayload) string {
	if payload.TranscriptPath != "" {
		return payload.TranscriptPath
	}
	id := payload.sessionID()
	if id == "" {
		return ""
	}
	// Derived from the SAME declaration the confiner guards, not from
	// sessionsDir() directly. Those agree today because Source() sets only Dir
	// — but the moment it grows DirByOS (the declared Windows seam) or DirFunc,
	// a second derivation would reconstruct against one root while the confiner
	// guarded another, and every Notification would come back RejectEscapesRoot
	// silently. That is exactly the drift #1361 removed from the receivers.
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
	return filepath.Join(root, id, transcriptFilename)
}

// dispatchHookEvent routes a decoded, consent-passed, confined, session-
// resolved payload to the right target method.
func dispatchHookEvent(target HookTarget, log outbound.Logger, sessionID, transcriptPath string, payload copilotHookPayload) {
	switch payload.HookEventName {
	case HookStop:
		// Turn end. Copilot's Stop envelope carries no last-assistant text
		// (verified live on 1.0.78: the body is hook_event_name, session_id,
		// timestamp, cwd, transcript_path, stop_reason, stop_hook_active), so
		// the empty text below is the honest value rather than a placeholder —
		// the transcript remains the source for what the turn actually said,
		// and the hook contributes only the authoritative turn boundary.
		log.LogInfo(logComponentHookReceiver, sessionID, "received Stop")
		target.HandleStopHook(sessionID, transcriptPath, "", false)
	case HookNotification:
		handleNotificationHook(target, log, sessionID, transcriptPath, payload)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once, in the one place that behaviour is decided for every
		// receiver (issue #1364). serveHookRequest's 200 is unaffected.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
}

// handleNotificationHook processes a Copilot notification.
//
// Only the two blocked-on-user types do anything, and what they do is dispatch,
// not hold — see this file's package comment for why a TierHook hold would pin
// a denied session for twelve hours. Passing HookNotification through to
// HandlePermissionHook is deliberate and is what makes this dispatch-only for
// free: session.HookSignal has no row for Notification, so the shared handler
// applies no signal and only injects the synthetic activity event.
//
// Any other notification type is ignored outright — it is a known event, not an
// unknown one, so it must not be counted as unrecognized.
func handleNotificationHook(target HookTarget, log outbound.Logger, sessionID, transcriptPath string, payload copilotHookPayload) {
	switch payload.NotificationType {
	case notificationPermissionPrompt, notificationElicitationDialog:
	default:
		return
	}

	log.LogInfo(logComponentHookReceiver, sessionID,
		fmt.Sprintf("received Notification (%s)", payload.NotificationType))
	target.HandlePermissionHook(sessionID, transcriptPath, HookNotification)
}
