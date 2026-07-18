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

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// Hook event names. Codex fires these (among others); the daemon installs and
// recognizes only these three and ignores everything else.
const (
	HookPermissionRequest = "PermissionRequest"
	HookPostToolUse       = "PostToolUse"
	// HookStop fires once at true turn end, carrying last_assistant_message.
	HookStop = "Stop"
)

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "codex-hook-receiver"

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

// ConsentGranter reports whether the user has granted a permission (issue
// #570). Satisfied by *services.PermissionService. Hooks installed by a
// pre-consent daemon keep firing until answered, so the receiver drops
// payloads while its permission is pending or denied.
type ConsentGranter interface {
	Granted(agentName, key string) bool
}

// NewHookHandler returns an http.HandlerFunc that receives Codex hook events
// (PermissionRequest, PostToolUse, Stop) and dispatches them to the target.
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
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveHookRequest(target, gate, log, w, r)
	}
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth.
func serveHookRequest(target HookTarget, gate ConsentGranter, log outbound.Logger, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if gate != nil && !gate.Granted(AdapterName, PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload codexHookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return
	}
	if payload.TranscriptPath == "" {
		http.Error(w, "bad request: missing transcript_path", http.StatusBadRequest)
		return
	}

	// Resolving the session id opens and parses the Codex transcript file — a
	// transcript read, so it must be gated behind the "transcripts" consent,
	// not merely the "hooks" write consent (issue #1174). A hooks-granted /
	// transcripts-denied session is not monitored anyway, so dropping the hook
	// here is both consent-correct and behaviourally harmless.
	if gate != nil && !gate.Granted(AdapterName, PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}
	sessionID := sessionIDFromPath(payload.TranscriptPath)
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
		// Unrecognized hook event — accept but ignore.
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored unrecognized hook event %q", payload.HookEventName))
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
		waitingCueInTail(payload.LastAssistantMessage))
}

// waitingCueInTail reports whether the bounded tail window of an assistant
// message carries a trailing question or an imperative waiting cue. Bounded,
// not full text: ExtractWaitingCue over-fires on very long turns. Mirrors the
// same window+OR rule parser.go uses for PendingWaitingCue and claudecode's
// hooks.go uses for its Stop hook (issue #1171 — see the DRY note in the PR).
func waitingCueInTail(full string) bool {
	win := tailer.WaitingScanWindow(full)
	return win != "" &&
		(session.ExtractQuestionSnippet(win) != "" || session.ExtractWaitingCue(win) != "")
}
