// hooks.go provides the HTTP handler for receiving Claude Code hook events.
// Claude Code fires hooks on PermissionRequest, PreToolUse, PostToolUse,
// PostToolUseFailure, PreCompact and Stop — the daemon uses these to surface
// user-blocking and turn-done state in the classifier. PermissionRequest covers
// permission gates (issue #108); PreToolUse on AskUserQuestion / ExitPlanMode
// covers user-input overlays that block the agent before the transcript is
// flushed (issue #307); Stop is the authoritative per-turn done signal
// delivered at true turn end, carrying the final assistant text (issue #1161);
// Notification/idle_prompt is the authoritative "idle at the prompt waiting for
// the user" signal for turns that end with no prose waiting-cue (issue #1173).
package claudecode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// Hook event names. Claude Code fires these; the daemon recognizes only
// these six and ignores everything else.
const (
	HookPermissionRequest  = "PermissionRequest"
	HookPreToolUse         = "PreToolUse"
	HookPostToolUse        = "PostToolUse"
	HookPostToolUseFailure = "PostToolUseFailure"
	HookPreCompact         = "PreCompact"
	// HookStop fires once at true turn end, carrying last_assistant_message.
	// It is the authoritative turn-done signal for claudecode (issue #1161).
	HookStop = "Stop"
	// HookNotification fires for Claude Code UI notifications, carrying a
	// notification_type discriminator. The daemon acts only on the idle_prompt
	// type — the agent finished its turn and is idle at the prompt waiting for
	// the user — as an authoritative waiting signal (issue #1173).
	HookNotification = "Notification"
)

// notificationTypeIdlePrompt is the Notification hook's notification_type value
// for "the agent is idle at the prompt waiting for the user" — the only
// notification the daemon acts on (issue #1173). Claude Code's Notification
// matcher filters on notification_type; the handler re-checks it as
// defense-in-depth so a broadened matcher can't dispatch other notification
// types (auth_success, permission_prompt, …).
const notificationTypeIdlePrompt = "idle_prompt"

// compactTriggerManual is the PreCompact trigger value for a user-invoked
// /compact (as opposed to "auto"). Only manual compaction forces working — an
// auto-compaction fires mid-turn while the session is already working (#657).
const compactTriggerManual = "manual"

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "hook-receiver"

// Tool names that suspend the agent waiting for user input. PreToolUse hooks
// must match one of these — anything else is rejected by the handler, even
// if the matcher in settings.json was edited to be broader. Defense-in-depth
// against the matcher being the sole filter.
const (
	toolAskUserQuestion = "AskUserQuestion"
	toolExitPlanMode    = "ExitPlanMode"
)

// hookPayload is the JSON body sent by Claude Code hook events.
// Only the fields used by the handler are decoded; the rest is ignored.
type hookPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	IsInterrupt    bool            `json:"is_interrupt,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	// Trigger is "manual" or "auto" on PreCompact events (the compaction
	// cause). Empty on other hook events.
	Trigger string `json:"trigger,omitempty"`
	// LastAssistantMessage is the full text of the turn's final assistant
	// message, carried by the Stop hook (issue #1161). Empty on other events.
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
	// NotificationType is the Notification hook's discriminator (e.g.
	// "idle_prompt", "permission_prompt"). Empty on other events (issue #1173).
	NotificationType string `json:"notification_type,omitempty"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	// HandleCompactHook forces a session to working for the duration of a
	// manual /compact, whose compaction window writes nothing to the transcript
	// (#657). trigger is the PreCompact cause ("manual" / "auto").
	HandleCompactHook(sessionID, transcriptPath, trigger string)
	// HandleStopHook records the authoritative turn-done signal from Claude
	// Code's Stop hook (#1161). lastAssistantText is the turn's final assistant
	// text, already display-truncated; waitingCue reports whether that message
	// carried a question or imperative cue (computed from the full text so a cue
	// beyond the display tail still routes the turn to waiting, not ready).
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
	// HandleIdlePromptHook records the Notification/idle_prompt signal — the
	// agent finished its turn and is idle at the prompt waiting for the user
	// (issue #1173). An authoritative waiting signal for the case the prose
	// waiting-cue heuristic can't detect (a turn that ended on a plain statement).
	HandleIdlePromptHook(sessionID, transcriptPath string)
}

// MarkerTarget is the narrow interface for hook-carried task-estimate
// markers (#604) — the same method shape as the MetricsCollector port, so
// the metrics adapter satisfies it directly (mirrors RateLimitIngester in
// statusline.go). Nil disables the scan (tests).
type MarkerTarget interface {
	IngestTaskEstimate(transcriptPath string, est *session.TaskEstimate)
	IngestTaskSummary(transcriptPath, text string, observedAt int64)
}

// scanToolInput walks the decoded tool_input once, scanning every string value
// for both an in-band task-estimate marker (#604) and a task-summary marker
// (#738) — they share the same carrier (e.g. a Bash description), so a single
// walk picks up both, and they reach the daemon even when the transcript drops
// the surrounding prose. The raw JSON can't be scanned directly: inside a JSON
// string the marker's quotes are escaped (\"marker\") and the captured comment
// body would not unmarshal. Tool inputs are small, shallow objects — the walk
// recurses into nested objects/arrays for completeness. Latest valid of each
// wins, matching the transcript scan.
func scanToolInput(raw json.RawMessage, observedAt time.Time) (*tailer.TaskEstimate, *tailer.TaskSummary, *tailer.TaskQuestion) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	// Fast reject before decoding. The comment opener survives JSON string
	// escaping (a backslash-quote doesn't touch "<!--"), but HTML-escaping
	// encoders write "<" as a unicode escape — Go's json.Marshal does,
	// Claude Code's JSON.stringify doesn't. Accept both encodings; the
	// raw-string needle below is the escaped opener byte-for-byte.
	htmlEscapedOpener := `\u003c!--`
	if s := string(raw); !strings.Contains(s, "<!--") && !strings.Contains(s, htmlEscapedOpener) {
		return nil, nil, nil
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, nil, nil
	}
	return scanValueForMarkers(decoded, observedAt)
}

// scanValueForMarkers walks a decoded JSON value, scanning every string for
// task-estimate, task-summary and task-question markers; latest valid of each
// wins. Shared by the PostToolUse hook (scanToolInput) and the transcript
// parser so a marker emitted inside a tool input — e.g. the Bash `description`
// carrier (#617) — is found by both the live hook and the transcript/replay
// path. The per-string fast-reject lives inside the Scan* functions.
func scanValueForMarkers(v interface{}, observedAt time.Time) (*tailer.TaskEstimate, *tailer.TaskSummary, *tailer.TaskQuestion) {
	var est *tailer.TaskEstimate
	var sum *tailer.TaskSummary
	var q *tailer.TaskQuestion
	walkMarkerValue(v, observedAt, &est, &sum, &q)
	return est, sum, q
}

// walkMarkerValue is the recursive walk behind scanValueForMarkers, pulled out
// of the closure it used to be so the switch/loops below aren't scored as
// nested inside an enclosing function literal. est/sum/q are accumulated by
// pointer since the walk must propagate a find back up through recursive
// map/slice traversal; latest valid of each wins, matching the transcript scan.
func walkMarkerValue(v interface{}, observedAt time.Time, est **tailer.TaskEstimate, sum **tailer.TaskSummary, q **tailer.TaskQuestion) {
	switch val := v.(type) {
	case string:
		if found := tailer.ScanTaskEstimate(val, observedAt); found != nil {
			*est = found // latest valid wins, matching the transcript scan
		}
		if found := tailer.ScanTaskSummary(val, observedAt); found != nil {
			*sum = found
		}
		if found := tailer.ScanTaskQuestion(val, observedAt); found != nil {
			*q = found
		}
	case map[string]interface{}:
		for _, child := range val {
			walkMarkerValue(child, observedAt, est, sum, q)
		}
	case []interface{}:
		for _, child := range val {
			walkMarkerValue(child, observedAt, est, sum, q)
		}
	}
}

// ConsentGranter reports whether the user has granted a permission (issue
// #570). Satisfied by *services.PermissionService. Hooks installed by a
// pre-consent daemon version keep firing until answered, so the receivers
// drop payloads while their permission is pending or denied.
type ConsentGranter interface {
	Granted(agentName, key string) bool
}

// sessionIDFromTranscriptPath extracts irrlicht's session ID (the UUID
// filename stem) from a Claude Code transcript path. The hook payload's
// session_id may differ from the transcript filename, so we always derive
// from the path — matching how fswatcher assigns session IDs.
func sessionIDFromTranscriptPath(p string) string {
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(p), ".jsonl")
}

// NewHookHandler returns an http.HandlerFunc that receives Claude Code
// hook events (PermissionRequest, PostToolUse, PostToolUseFailure) and
// dispatches them to the target.
//
// The handler returns 200 with an empty body for recognized events. For
// PermissionRequest, an empty response means Claude Code shows its normal
// permission prompt (no auto-approve/deny).
//
// markers receives task-estimate markers found in PreToolUse tool inputs
// (#604) — a transport that bypasses the transcript writer, which drops
// mid-task assistant text on claude ≥2.1.162. The marker scan runs for ALL
// tools; the permission dispatch below stays strictly gated to the
// user-input tools — two independent paths, not a relaxation of the gate.
//
// gate is the consent check for the "hooks" permission; while not granted
// the payload is dropped with 200 (so the curl hook stays quiet). A nil
// gate means no gating — used by tests.
func NewHookHandler(target HookTarget, markers MarkerTarget, gate ConsentGranter, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveHookRequest(target, markers, gate, log, w, r)
	}
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth (go:S3776 — this dropped the reported complexity from 31 to
// within the 15-point budget without changing any behavior).
func serveHookRequest(target HookTarget, markers MarkerTarget, gate ConsentGranter, log outbound.Logger, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if gate != nil && !gate.Granted(AdapterName, PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload hookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return
	}

	sessionID := sessionIDFromTranscriptPath(payload.TranscriptPath)
	if sessionID == "" {
		http.Error(w, "bad request: missing transcript_path", http.StatusBadRequest)
		return
	}

	dispatch := func() {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("received %s (tool=%s)", payload.HookEventName, payload.ToolName))
		target.HandlePermissionHook(sessionID, payload.TranscriptPath, payload.HookEventName)
	}

	switch payload.HookEventName {
	case HookPermissionRequest, HookPostToolUse, HookPostToolUseFailure:
		dispatch()
	case HookPreCompact:
		handlePreCompactHook(target, log, sessionID, payload)
	case HookPreToolUse:
		handlePreToolUseHook(markers, log, sessionID, payload, dispatch)
	case HookStop:
		handleStopHook(target, log, sessionID, payload)
	case HookNotification:
		handleNotificationHook(target, log, sessionID, payload)
	default:
		// Unrecognized hook event — accept but ignore.
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored unrecognized hook event %q", payload.HookEventName))
	}

	w.WriteHeader(http.StatusOK)
}

// handlePreCompactHook processes a PreCompact hook event. A manual /compact
// replaces the context; the compaction window (tens of seconds to minutes)
// writes nothing to the transcript, so without this hook the session stays
// frozen in its pre-compact state instead of showing working (#657). Force
// working now; the compact_boundary then releases it back to ready (#656).
// The installer matches "manual"; the trigger check here is defense-in-depth
// so an auto-compaction (already working, fires mid-turn) never gets a
// spurious working blip.
func handlePreCompactHook(target HookTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	if payload.Trigger == compactTriggerManual {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("received %s (trigger=%s)", payload.HookEventName, payload.Trigger))
		target.HandleCompactHook(sessionID, payload.TranscriptPath, payload.Trigger)
	} else {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored %s (trigger=%q, not manual)", payload.HookEventName, payload.Trigger))
	}
}

// handleStopHook processes a Claude Code Stop hook — the authoritative
// turn-done push delivered at true turn end (#1161). It forwards a turn-done
// signal plus the turn's final assistant text so the classifier decides
// ready-vs-waiting from the same message IsWaitingForUserInput reads, without
// depending on the transcript-tail heuristic (and its codex carve-out).
//
// The forwarded text is display-truncated; the waiting-cue verdict is computed
// from a bounded tail window (WaitingScanWindow) of the FULL message — the same
// window parser.go uses for PendingWaitingCue — so a question or cue sitting
// before the display tail still routes the turn to waiting, while ExtractWaitingCue
// is not fed the whole (possibly very long) turn, where it over-fires.
func handleStopHook(target HookTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	log.LogInfo(logComponentHookReceiver, sessionID,
		fmt.Sprintf("received %s (%d chars of assistant text)", payload.HookEventName, len(payload.LastAssistantMessage)))

	target.HandleStopHook(sessionID, payload.TranscriptPath,
		tailer.TruncateAssistantText(payload.LastAssistantMessage),
		waitingCueInTail(payload.LastAssistantMessage))
}

// waitingCueInTail reports whether the bounded tail window of an assistant
// message carries a trailing question or an imperative waiting cue (issue
// #1150). Bounded, not full text: ExtractWaitingCue over-fires on very long
// turns (see tailer.MaxWaitingScanRunes). Shared by the transcript parser
// (PendingWaitingCue) and the Stop-hook handler (#1161) so the window size and
// the OR-of-two-detectors rule can't drift between the two paths.
func waitingCueInTail(full string) bool {
	win := tailer.WaitingScanWindow(full)
	return win != "" &&
		(session.ExtractQuestionSnippet(win) != "" || session.ExtractWaitingCue(win) != "")
}

// handleNotificationHook processes a Claude Code Notification hook. It acts only
// on the idle_prompt type — the agent finished its turn and is idle at the
// prompt waiting for the user (issue #1173) — forwarding it as an authoritative
// waiting signal. Every other notification_type (permission_prompt is already
// covered by the blocking PermissionRequest hook; auth_success and friends are
// irrelevant to state) is accepted and ignored, mirroring handlePreToolUseHook's
// defense-in-depth reject: the installer matches only idle_prompt, but the
// handler re-checks so a broadened settings.json matcher can't dispatch others.
func handleNotificationHook(target HookTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	if payload.NotificationType != notificationTypeIdlePrompt {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored Notification of type %q (only %s drives state)", payload.NotificationType, notificationTypeIdlePrompt))
		return
	}
	log.LogInfo(logComponentHookReceiver, sessionID,
		fmt.Sprintf("received %s (type=%s)", payload.HookEventName, payload.NotificationType))
	target.HandleIdlePromptHook(sessionID, payload.TranscriptPath)
}

// handlePreToolUseHook processes a PreToolUse hook event: scans the tool
// input for task-estimate/task-summary markers for ALL tools (#604), then
// dispatches (via dispatch) only for the user-input tools (AskUserQuestion /
// ExitPlanMode) — rejecting anything else even if the settings.json matcher
// was misconfigured to be broader.
func handlePreToolUseHook(markers MarkerTarget, log outbound.Logger, sessionID string, payload hookPayload, dispatch func()) {
	// Marker scan first, for ALL tools (#604): the rules block lets
	// the agent carry its progress marker in a tool input (e.g. the
	// Bash description), and the payload reaches the daemon even
	// when the transcript drops the surrounding prose.
	if markers != nil {
		scanAndIngestMarkers(markers, log, sessionID, payload)
	}
	if payload.ToolName == toolAskUserQuestion || payload.ToolName == toolExitPlanMode {
		dispatch()
	} else {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored PreToolUse for unexpected tool %q", payload.ToolName))
	}
}

// scanAndIngestMarkers scans a PreToolUse payload's tool input for
// task-estimate (#604) and task-summary (#738) markers and forwards any found
// to markers. The question marker rides end-of-turn assistant prose, not a
// tool-input carrier, so the hook path (tool inputs only) drops it (#759):
// the transcript text-block scan delivers it, and the deterministic
// compactor covers the no-marker case regardless.
func scanAndIngestMarkers(markers MarkerTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	est, sum, _ := scanToolInput(payload.ToolInput, time.Now())
	if est != nil {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("task-estimate marker via %s tool input: %d/%d", payload.ToolName, est.CompletedRounds, est.TotalRounds))
		markers.IngestTaskEstimate(payload.TranscriptPath, &session.TaskEstimate{
			TotalRounds:     est.TotalRounds,
			CompletedRounds: est.CompletedRounds,
			Risk:            est.Risk,
			Confidence:      est.Confidence,
			UpdatedAt:       est.ObservedAt,
		})
	}
	if sum != nil {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("task-summary marker via %s tool input", payload.ToolName))
		markers.IngestTaskSummary(payload.TranscriptPath, sum.Text, sum.ObservedAt)
	}
}
