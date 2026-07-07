// hooks.go provides the HTTP handler for receiving Claude Code hook events.
// Claude Code fires hooks on PermissionRequest, PreToolUse, PostToolUse, and
// PostToolUseFailure — the daemon uses these to surface user-blocking state
// in the classifier. PermissionRequest covers permission gates (issue #108);
// PreToolUse on AskUserQuestion / ExitPlanMode covers user-input overlays
// that block the agent before the transcript is flushed (issue #307).
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
// these five and ignores everything else.
const (
	HookPermissionRequest  = "PermissionRequest"
	HookPreToolUse         = "PreToolUse"
	HookPostToolUse        = "PostToolUse"
	HookPostToolUseFailure = "PostToolUseFailure"
	HookPreCompact         = "PreCompact"
)

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
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	// HandleCompactHook forces a session to working for the duration of a
	// manual /compact, whose compaction window writes nothing to the transcript
	// (#657). trigger is the PreCompact cause ("manual" / "auto").
	HandleCompactHook(sessionID, transcriptPath, trigger string)
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
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch val := v.(type) {
		case string:
			if found := tailer.ScanTaskEstimate(val, observedAt); found != nil {
				est = found // latest valid wins, matching the transcript scan
			}
			if found := tailer.ScanTaskSummary(val, observedAt); found != nil {
				sum = found
			}
			if found := tailer.ScanTaskQuestion(val, observedAt); found != nil {
				q = found
			}
		case map[string]interface{}:
			for _, child := range val {
				walk(child)
			}
		case []interface{}:
			for _, child := range val {
				walk(child)
			}
		}
	}
	walk(v)
	return est, sum, q
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
			// A manual /compact replaces the context; the compaction window
			// (tens of seconds to minutes) writes nothing to the transcript, so
			// without this hook the session stays frozen in its pre-compact
			// state instead of showing working (#657). Force working now; the
			// compact_boundary then releases it back to ready (#656). The
			// installer matches "manual"; the trigger check here is
			// defense-in-depth so an auto-compaction (already working, fires
			// mid-turn) never gets a spurious working blip.
			if payload.Trigger == compactTriggerManual {
				log.LogInfo(logComponentHookReceiver, sessionID,
					fmt.Sprintf("received %s (trigger=%s)", payload.HookEventName, payload.Trigger))
				target.HandleCompactHook(sessionID, payload.TranscriptPath, payload.Trigger)
			} else {
				log.LogInfo(logComponentHookReceiver, sessionID,
					fmt.Sprintf("ignored %s (trigger=%q, not manual)", payload.HookEventName, payload.Trigger))
			}
		case HookPreToolUse:
			// Marker scan first, for ALL tools (#604): the rules block lets
			// the agent carry its progress marker in a tool input (e.g. the
			// Bash description), and the payload reaches the daemon even
			// when the transcript drops the surrounding prose.
			if markers != nil {
				// The question marker rides end-of-turn assistant prose, not a
				// tool-input carrier, so the hook path (tool inputs only) drops it
				// (#759): the transcript text-block scan delivers it, and the
				// deterministic compactor covers the no-marker case regardless.
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
				// Task-summary marker (#738) — same carrier, same drop-bypass.
				if sum != nil {
					log.LogInfo(logComponentHookReceiver, sessionID,
						fmt.Sprintf("task-summary marker via %s tool input", payload.ToolName))
					markers.IngestTaskSummary(payload.TranscriptPath, sum.Text, sum.ObservedAt)
				}
			}
			// Only dispatch for user-input tools; reject anything else even
			// if the settings.json matcher was misconfigured to be broader.
			if payload.ToolName == toolAskUserQuestion || payload.ToolName == toolExitPlanMode {
				dispatch()
			} else {
				log.LogInfo(logComponentHookReceiver, sessionID,
					fmt.Sprintf("ignored PreToolUse for unexpected tool %q", payload.ToolName))
			}
		default:
			// Unrecognized hook event — accept but ignore.
			log.LogInfo(logComponentHookReceiver, sessionID,
				fmt.Sprintf("ignored unrecognized hook event %q", payload.HookEventName))
		}

		w.WriteHeader(http.StatusOK)
	}
}
