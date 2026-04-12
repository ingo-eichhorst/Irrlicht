// hooks.go provides the HTTP handler for receiving Claude Code hook events.
// Claude Code fires hooks on PermissionRequest, PostToolUse, and
// PostToolUseFailure — the daemon uses these to surface permission-pending
// state in the classifier (issue #108).
package claudecode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"irrlicht/core/ports/outbound"
)

// HookPayload is the JSON body sent by Claude Code hook events.
// Only the fields used by the handler are decoded; the rest is ignored.
type HookPayload struct {
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

// SessionIDFromTranscriptPath extracts irrlicht's session ID (the UUID
// filename stem) from a Claude Code transcript path. The hook payload's
// session_id may differ from the transcript filename, so we always derive
// from the path — matching how fswatcher assigns session IDs.
func SessionIDFromTranscriptPath(p string) string {
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
func NewHookHandler(target HookTarget, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload HookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
			return
		}

		sessionID := SessionIDFromTranscriptPath(payload.TranscriptPath)
		if sessionID == "" {
			http.Error(w, "bad request: missing transcript_path", http.StatusBadRequest)
			return
		}

		switch payload.HookEventName {
		case "PermissionRequest", "PostToolUse", "PostToolUseFailure":
			log.LogInfo("hook-receiver", sessionID,
				fmt.Sprintf("received %s (tool=%s)", payload.HookEventName, payload.ToolName))
			target.HandlePermissionHook(sessionID, payload.TranscriptPath, payload.HookEventName)
		default:
			// Unrecognized hook event — accept but ignore.
			log.LogInfo("hook-receiver", sessionID,
				fmt.Sprintf("ignored unrecognized hook event %q", payload.HookEventName))
		}

		w.WriteHeader(http.StatusOK)
	}
}
