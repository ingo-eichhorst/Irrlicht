// Package cursor provides normalization from Cursor IDE hook events to
// irrlicht's unified HookEvent domain type.
package cursor

import (
	"strings"

	cursorev "irrlicht/core/domain/cursor"
	"irrlicht/core/domain/event"
)

// eventNameMap translates Cursor hook_event_name values to irrlicht HookEvent names.
// Events with no direct analog are mapped to the nearest equivalent.
var eventNameMap = map[string]string{
	"sessionStart":         "SessionStart",
	"sessionEnd":           "SessionEnd",
	"stop":                 "Stop",
	"subagentStart":        "SessionStart",    // treated as a sub-session start; keeps session working
	"subagentStop":         "SubagentStop",
	"preToolUse":           "PreToolUse",
	"postToolUse":          "PostToolUse",
	"postToolUseFailure":   "PostToolUse",     // map to same event; failure carried in context
	"beforeSubmitPrompt":   "UserPromptSubmit",
	"beforeShellExecution": "PreToolUse",      // triggers speculative-wait path
	"afterShellExecution":  "PostToolUse",
	"preCompact":           "PreCompact",
	"afterAgentThought":    "PreToolUse",      // keeps session in working during inference gap
}

// approvalProneKeywords: tool names containing these strings trigger speculative waiting.
var approvalProneKeywords = []string{
	"shell", "bash", "exec", "run",
	"write", "edit", "create", "delete",
}

// NormalizeEvent converts a CursorEvent into the unified HookEvent used by
// the irrlicht domain. Field name translation and session ID prefixing happen here.
func NormalizeEvent(c *cursorev.CursorEvent) *event.HookEvent {
	mapped := eventNameMap[c.HookEventName]
	if mapped == "" {
		// Unknown Cursor event: keep session in working state.
		mapped = "PreToolUse"
	}

	sessionID := cursorev.SessionPrefix + c.ConversationID

	normalized := &event.HookEvent{
		HookEventName:  mapped,
		SessionID:      sessionID,
		TranscriptPath: c.TranscriptPath,
		CWD:            c.FirstWorkspaceRoot(),
		Model:          c.Model,
		Source:         c.Source,
	}

	// Map stop_reason / reason for SessionEnd.
	if c.HookEventName == "sessionEnd" {
		normalized.Reason = c.Reason
	}

	// Map sessionStart source field for matcher semantics.
	// Cursor uses source="new"/"resume"; irrlicht state machine checks matcher.
	if c.HookEventName == "sessionStart" || c.HookEventName == "subagentStart" {
		normalized.Matcher = mapSessionStartMatcher(c.Source)
	}

	// Map preCompact compact_type to matcher (auto/manual).
	if c.HookEventName == "preCompact" {
		normalized.Matcher = c.CompactType
	}

	// Map tool name for preToolUse, postToolUse, and shell events.
	switch c.HookEventName {
	case "preToolUse", "postToolUse", "postToolUseFailure":
		normalized.ToolName = c.ToolName
	case "beforeShellExecution", "afterShellExecution":
		// Synthesize a tool name so speculative-wait logic fires correctly.
		normalized.ToolName = "shell"
	}

	// Map subagent parent linkage.
	if c.ParentConversationID != "" {
		normalized.ParentSessionID = cursorev.SessionPrefix + c.ParentConversationID
	}

	return normalized
}

// IsApprovalProne reports whether a tool name is likely to require user approval.
// Used by the caller to decide whether to schedule speculative waiting.
func IsApprovalProne(toolName string) bool {
	lower := strings.ToLower(toolName)
	for _, keyword := range approvalProneKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// mapSessionStartMatcher converts Cursor's source field into the matcher string
// expected by irrlicht's SmartStateTransition.
//
//   - "new" → "startup" (fresh session, no prior task)
//   - "resume" → "resume" (session resumed mid-task)
//   - anything else → "startup" (safe default)
func mapSessionStartMatcher(source string) string {
	switch source {
	case "resume":
		return "resume"
	default:
		return "startup"
	}
}
