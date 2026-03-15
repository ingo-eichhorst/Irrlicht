package event

import "fmt"

// MaxPayloadSize is the maximum allowed event payload size in bytes.
const MaxPayloadSize = 512 * 1024 // 512KB

// HookEvent represents a Claude Code hook event received via stdin.
type HookEvent struct {
	HookEventName   string                 `json:"hook_event_name"`
	SessionID       string                 `json:"session_id"`
	Timestamp       string                 `json:"timestamp"`
	Matcher         string                 `json:"matcher,omitempty"`
	Reason          string                 `json:"reason,omitempty"`
	Data            map[string]interface{} `json:"data"`
	TranscriptPath  string                 `json:"transcript_path,omitempty"`
	CWD             string                 `json:"cwd,omitempty"`
	Model           string                 `json:"model,omitempty"`
	PermissionMode  string                 `json:"permission_mode,omitempty"`
	Prompt          string                 `json:"prompt,omitempty"`
	Source          string                 `json:"source,omitempty"`
	ToolName        string                 `json:"tool_name,omitempty"`
	ParentSessionID string                 `json:"parent_session_id,omitempty"`

	// Adapter identifies the source of this event (e.g. "copilot").
	// Empty means the default Claude Code adapter.
	Adapter string `json:"adapter,omitempty"`
}

// validEventNames is the set of known valid hook event names.
var validEventNames = map[string]bool{
	"SessionStart":     true,
	"UserPromptSubmit": true,
	"Notification":     true,
	"PreToolUse":       true,
	"PostToolUse":      true,
	"PreCompact":       true,
	"Stop":             true,
	"SubagentStop":     true,
	"SessionEnd":       true,
}

// ResolveReason extracts the session-end reason from the event, checking both
// the direct Reason field and the legacy Data map.
func (e *HookEvent) ResolveReason() string {
	if e.Reason != "" {
		return e.Reason
	}
	if e.Data != nil {
		if r, ok := e.Data["reason"].(string); ok {
			return r
		}
	}
	return ""
}

// Validate checks that the event is structurally valid.
// pathValidator is called on any path fields; pass nil to skip path validation.
func (e *HookEvent) Validate(pathValidator func(string) error) error {
	if e.HookEventName == "" {
		return fmt.Errorf("missing hook_event_name")
	}
	if e.SessionID == "" {
		return fmt.Errorf("missing session_id")
	}
	if !validEventNames[e.HookEventName] {
		return fmt.Errorf("invalid event type: %s", e.HookEventName)
	}
	if pathValidator == nil {
		return nil
	}
	// Validate paths from legacy Data map.
	if e.Data != nil {
		if p, ok := e.Data["transcript_path"].(string); ok {
			if err := pathValidator(p); err != nil {
				return fmt.Errorf("invalid transcript_path: %w", err)
			}
		}
		if p, ok := e.Data["cwd"].(string); ok {
			if err := pathValidator(p); err != nil {
				return fmt.Errorf("invalid cwd: %w", err)
			}
		}
	}
	// Validate direct fields.
	if e.TranscriptPath != "" {
		if err := pathValidator(e.TranscriptPath); err != nil {
			return fmt.Errorf("invalid transcript_path: %w", err)
		}
	}
	if e.CWD != "" {
		if err := pathValidator(e.CWD); err != nil {
			return fmt.Errorf("invalid cwd: %w", err)
		}
	}
	return nil
}
