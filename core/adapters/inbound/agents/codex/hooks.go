// hooks.go provides the HTTP handler for receiving Codex CLI hook events.
// Codex shipped a Claude-Code-shaped hooks system (experimental in
// rust-v0.114.0, ~March 2026); the daemon uses it to observe Codex's UI state
// from a structured push channel instead of inferring it from transcript prose
// (issue #1171, epic #1129).
//
// Two events carry live state:
//   - PermissionRequest fires *while* Codex is blocked on an approval overlay
//     (shell escalation, network access) — the real win, retiring the
//     waiting_cue prose regex that never reliably caught TUI overlays.
//   - Stop fires once at true turn end, carrying last_assistant_message, which
//     feeds IsWaitingForUserInput directly (turn-end is already covered by the
//     transcript's task_complete/turn_aborted → turn_done, so Stop is marginal
//     but its final-message payload is authoritative).
//
// PostToolUse clears the permission-pending overlay once an approved tool runs
// (Codex has no PostToolUseFailure event). A denied approval that aborts the
// turn without a following PostToolUse is a known gap — the overlay then clears
// on the next tool call (issue #1174). Clearing pending on Stop was considered
// and deliberately dropped: Codex delivers hooks fire-and-forget, so a stale,
// reordered Stop from a prior turn could race a newer turn's genuine
// approval-pending overlay and hide a real waiting state — the exact failure
// this tier exists to prevent.
package codex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// Hook event names. Codex fires these (among others); the daemon installs and
// recognizes only these three and ignores everything else.
//
// Re-exports of the domain constants, like claudecode's: PermissionRequest and
// PostToolUse route into the same SessionDetector.HandlePermissionHook, which
// resolves them through session.HookSignal. Declaring them as independent
// literals here would let a rename on this side silently stop matching the
// shared table — the exact drift #1320 removed, one adapter over.
const (
	HookPermissionRequest = session.HookPermissionRequest
	HookPostToolUse       = session.HookPostToolUse
	// HookStop fires once at true turn end, carrying last_assistant_message.
	HookStop = session.HookStop
)

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "codex-hook-receiver"

// transcriptExt is the file extension of a Codex rollout. The hook receiver
// confines caller-supplied paths to this extension.
const transcriptExt = ".jsonl"

// codexHookPayload is the JSON body Codex sends on a hook event (stdin →
// POSTed to the daemon by the installed curl command). Only the fields the
// handler uses are decoded; the rest (session_id, cwd, model, turn_id, …) is
// ignored. The payload's own session_id is deliberately NOT decoded: it is
// shared by a session's parent and every child (session_meta.go), so keying an
// overlay on it would mis-attribute a child's state to its parent — the id is
// resolved from the transcript path instead.
type codexHookPayload struct {
	TranscriptPath string `json:"transcript_path"`
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
	// LastAssistantMessage is the turn's final assistant text, carried by the
	// Stop hook. Empty on other events.
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the same
// agent-agnostic surface claudecode's hooks use.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates on
// (issue #570) — an alias, not a copy, so this adapter and claudecode cannot
// drift on it (issue #1179). See hookjson for why the HookTarget below is NOT
// shared the same way.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns the hook receiver for Codex hook events
// (PermissionRequest, PostToolUse, Stop), which dispatches them to the target.
// The returned hookjson.HookHandler is an http.Handler carrying the confiner it
// guards caller-supplied transcript paths with (issue #1390).
//
// The handler returns 200 with an empty body for recognized events. For
// PermissionRequest, an empty response means Codex shows its normal approval
// prompt (no auto-approve/deny).
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200 (so the installed hook stays quiet). Resolving
// the session id reads the transcript file, so that read is additionally gated
// behind the "transcripts" permission. A nil gate means no gating — used by
// tests.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		func(c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, gate, log, c, w, r)
		})
}

// transcriptConfiner returns the confiner this adapter's hook receiver guards
// caller-supplied transcript paths with, rooted in the adapter's own
// agent.Source declaration (issue #1361). It replaces the adapter-local
// confineToSessionsDir, which re-derived $CODEX_HOME/sessions from its own
// constants and so could guard a different tree than the one being watched.
func transcriptConfiner() *hookjson.PathConfiner {
	return hookjson.ConfinerForSource(Source, runtime.GOOS, transcriptExt)
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth.
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
	// install has a working channel that this counter must not call dead.
	hookjson.ObserveHookReceipt(AdapterName)

	var payload codexHookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return
	}

	// Resolving the session id opens and parses the Codex transcript file — a
	// transcript read, so it must be gated behind the "transcripts" consent,
	// not merely the "hooks" write consent (issue #1174). A hooks-granted /
	// transcripts-denied session is not monitored anyway, so dropping the hook
	// here is both consent-correct and behaviourally harmless.
	//
	// The consent check comes BEFORE confinement so a denied session still
	// yields a quiet 200: a user who has not granted transcript access should
	// not have hooks failing at them, and the path is never resolved on that
	// branch anyway.
	if gate != nil && !gate.Granted(AdapterName, PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}
	// transcript_path arrives in an HTTP body on a local, unauthenticated
	// endpoint, so it is untrusted: any local process could otherwise steer the
	// daemon into opening a file of its choosing. Confine it to the declared
	// sessions tree before any read, and carry the confined path downstream so
	// the target reads that and not the caller's string (issue #1361).
	transcriptPath, reason := confiner.Confine(payload.TranscriptPath)
	if reason != hookjson.RejectNone {
		hookjson.RejectPath(w, log, logComponentHookReceiver, payload.TranscriptPath, reason)
		return
	}
	payload.TranscriptPath = transcriptPath

	sessionID := sessionIDFromPath(transcriptPath)
	if sessionID == "" {
		// Header not yet written or transcript unreadable — drop rather than
		// guess an id. The transcript tailer covers the state once it lands.
		w.WriteHeader(http.StatusOK)
		return
	}

	dispatchHookEvent(target, log, sessionID, payload)
	w.WriteHeader(http.StatusOK)
}

// dispatchHookEvent routes a decoded, consent-passed, session-resolved payload
// to the right target method.
func dispatchHookEvent(target HookTarget, log outbound.Logger, sessionID string, payload codexHookPayload) {
	switch payload.HookEventName {
	case HookPermissionRequest, HookPostToolUse:
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("received %s (tool=%s)", payload.HookEventName, payload.ToolName))
		target.HandlePermissionHook(sessionID, payload.TranscriptPath, payload.HookEventName)
	case HookStop:
		handleStopHook(target, log, sessionID, payload)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once, in the one place that behaviour is decided for every
		// receiver (issue #1364). serveHookRequest's 200 is unaffected.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
}

// handleStopHook processes a Codex Stop hook — the authoritative turn-done push
// delivered at true turn end, carrying the turn's final assistant text. It
// forwards a turn-done signal plus the final assistant text so the classifier
// decides ready-vs-waiting from the same message IsWaitingForUserInput reads.
//
// The forwarded text is display-truncated; the waiting-cue verdict is computed
// from a bounded tail window of the FULL message (mirroring parser.go's
// PendingWaitingCue) so a question sitting before the display tail still routes
// the turn to waiting, not ready.
func handleStopHook(target HookTarget, log outbound.Logger, sessionID string, payload codexHookPayload) {
	log.LogInfo(logComponentHookReceiver, sessionID,
		fmt.Sprintf("received %s (%d chars of assistant text)", payload.HookEventName, len(payload.LastAssistantMessage)))

	target.HandleStopHook(sessionID, payload.TranscriptPath,
		tailer.TruncateAssistantText(payload.LastAssistantMessage),
		session.ProseIndicatesWaiting(tailer.WaitingScanWindow(payload.LastAssistantMessage)))
}
