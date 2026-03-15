package cursor

// CursorEvent represents a Cursor IDE hook event received via stdin.
// Field names match the Cursor hook payload format; they differ from
// Claude Code's HookEvent (e.g., conversation_id vs session_id).
type CursorEvent struct {
	HookEventName  string   `json:"hook_event_name"`
	ConversationID string   `json:"conversation_id"`
	GenerationID   string   `json:"generation_id,omitempty"`
	Model          string   `json:"model,omitempty"`
	CursorVersion  string   `json:"cursor_version,omitempty"`
	WorkspaceRoots []string `json:"workspace_roots,omitempty"`
	UserEmail      string   `json:"user_email,omitempty"`
	TranscriptPath string   `json:"transcript_path,omitempty"`

	// sessionStart additional fields
	Source         string `json:"source,omitempty"`          // "new" or "resume"
	PermissionMode string `json:"permission_mode,omitempty"` // e.g. "default", "strict"

	// sessionEnd additional field
	Reason string `json:"reason,omitempty"` // "user_exit", "timeout", "clear", "logout"

	// stop additional field
	StopReason string `json:"stop_reason,omitempty"` // "end_turn", "max_tokens", "tool_use"

	// subagent events
	SubagentID            string `json:"subagent_id,omitempty"`
	ParentConversationID  string `json:"parent_conversation_id,omitempty"`

	// tool-use events
	ToolName     string      `json:"tool_name,omitempty"`
	ToolInput    interface{} `json:"tool_input,omitempty"`
	ToolResponse interface{} `json:"tool_response,omitempty"`
	Error        string      `json:"error,omitempty"`

	// beforeSubmitPrompt
	Prompt string `json:"prompt,omitempty"`

	// shell execution events
	Command    string `json:"command,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`

	// preCompact
	CompactType string `json:"compact_type,omitempty"` // "auto" or "manual"

	// afterAgentThought
	Thought string `json:"thought,omitempty"`
}

// FirstWorkspaceRoot returns the first entry in WorkspaceRoots, or "" if empty.
// Used to populate the CWD field in the normalized HookEvent.
func (e *CursorEvent) FirstWorkspaceRoot() string {
	if len(e.WorkspaceRoots) > 0 {
		return e.WorkspaceRoots[0]
	}
	return ""
}

// SessionPrefix is prepended to ConversationID to form the unified session ID.
// This ensures Cursor sessions are distinct from Claude Code sessions in the
// file system (e.g., cursor_conv_abc123.json vs session_abc123.json).
const SessionPrefix = "cursor_"
