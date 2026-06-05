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
// these four and ignores everything else.
const (
	HookPermissionRequest  = "PermissionRequest"
	HookPreToolUse         = "PreToolUse"
	HookPostToolUse        = "PostToolUse"
	HookPostToolUseFailure = "PostToolUseFailure"
)

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
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
}

// MarkerTarget is the narrow interface for hook-carried task-estimate
// markers (#604) — the same method shape as the MetricsCollector port, so
// the metrics adapter satisfies it directly (mirrors RateLimitTarget in
// statusline.go). Nil disables the scan (tests).
type MarkerTarget interface {
	IngestTaskEstimate(transcriptPath string, est *session.TaskEstimate)
}

// scanToolInputForMarker walks the decoded tool_input and scans its string
// values for a task-estimate marker. The raw JSON can't be scanned directly:
// inside a JSON string the marker's quotes are escaped (\"marker\") and the
// captured comment body would not unmarshal. Tool inputs are small, shallow
// objects — the walk recurses into nested objects/arrays for completeness.
func scanToolInputForMarker(raw json.RawMessage, observedAt time.Time) *tailer.TaskEstimate {
	if len(raw) == 0 {
		return nil
	}
	// Fast reject before decoding. The comment opener survives JSON string
	// escaping (a backslash-quote doesn't touch "<!--"), but HTML-escaping
	// encoders write "<" as a unicode escape — Go's json.Marshal does,
	// Claude Code's JSON.stringify doesn't. Accept both encodings; the
	// raw-string needle below is the escaped opener byte-for-byte.
	htmlEscapedOpener := `\u003c!--`
	if s := string(raw); !strings.Contains(s, "<!--") && !strings.Contains(s, htmlEscapedOpener) {
		return nil
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	var est *tailer.TaskEstimate
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch val := v.(type) {
		case string:
			if found := tailer.ScanTaskEstimate(val, observedAt); found != nil {
				est = found // latest valid wins, matching the transcript scan
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
	walk(decoded)
	return est
}

// ConsentGate reports whether the user has granted a permission (issue
// #570). Satisfied by *services.PermissionService. Hooks installed by a
// pre-consent daemon version keep firing until answered, so the receivers
// drop payloads while their permission is pending or denied.
type ConsentGate interface {
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
func NewHookHandler(target HookTarget, markers MarkerTarget, gate ConsentGate, log outbound.Logger) http.HandlerFunc {
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
			log.LogInfo("hook-receiver", sessionID,
				fmt.Sprintf("received %s (tool=%s)", payload.HookEventName, payload.ToolName))
			target.HandlePermissionHook(sessionID, payload.TranscriptPath, payload.HookEventName)
		}

		switch payload.HookEventName {
		case HookPermissionRequest, HookPostToolUse, HookPostToolUseFailure:
			dispatch()
		case HookPreToolUse:
			// Marker scan first, for ALL tools (#604): the rules block lets
			// the agent carry its progress marker in a tool input (e.g. the
			// Bash description), and the payload reaches the daemon even
			// when the transcript drops the surrounding prose.
			if markers != nil {
				if est := scanToolInputForMarker(payload.ToolInput, time.Now()); est != nil {
					log.LogInfo("hook-receiver", sessionID,
						fmt.Sprintf("task-estimate marker via %s tool input: %d/%d", payload.ToolName, est.CompletedRounds, est.TotalRounds))
					markers.IngestTaskEstimate(payload.TranscriptPath, &session.TaskEstimate{
						TotalRounds:     est.TotalRounds,
						CompletedRounds: est.CompletedRounds,
						Risk:            est.Risk,
						Confidence:      est.Confidence,
						UpdatedAt:       est.ObservedAt,
					})
				}
			}
			// Only dispatch for user-input tools; reject anything else even
			// if the settings.json matcher was misconfigured to be broader.
			if payload.ToolName == toolAskUserQuestion || payload.ToolName == toolExitPlanMode {
				dispatch()
			} else {
				log.LogInfo("hook-receiver", sessionID,
					fmt.Sprintf("ignored PreToolUse for unexpected tool %q", payload.ToolName))
			}
		default:
			// Unrecognized hook event — accept but ignore.
			log.LogInfo("hook-receiver", sessionID,
				fmt.Sprintf("ignored unrecognized hook event %q", payload.HookEventName))
		}

		w.WriteHeader(http.StatusOK)
	}
}
