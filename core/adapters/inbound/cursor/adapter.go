// Package cursor implements the inbound adapter for Cursor IDE hook events.
//
// Cursor IDE uses the same stdin-JSON hook convention as Claude Code but differs in:
//   - Event names use camelCase (sessionStart) vs PascalCase (SessionStart)
//   - Session identity comes from conversation_id (not session_id)
//   - Working directory is workspace_roots[0] (not cwd)
//   - No Notification event — speculative waiting used for approval-prone tools
//   - hook_event_name is embedded in the JSON payload (no --event CLI flag needed)
package cursor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"irrlicht/core/domain/event"
	"irrlicht/core/ports/inbound"
)

// cursorPayload represents the JSON payload Cursor IDE sends to hooks via stdin.
type cursorPayload struct {
	HookEventName string   `json:"hook_event_name"`
	ConversationID string  `json:"conversation_id"`
	GenerationID   string  `json:"generation_id"`
	Model          string  `json:"model"`
	CursorVersion  string  `json:"cursor_version"`
	WorkspaceRoots []string `json:"workspace_roots"`
	UserEmail      string  `json:"user_email"`
	TranscriptPath string  `json:"transcript_path"`

	// sessionStart
	Source         string `json:"source"`
	PermissionMode string `json:"permission_mode"`

	// sessionEnd
	Reason string `json:"reason"`

	// stop
	StopReason string `json:"stop_reason"`

	// subagentStart / subagentStop
	SubagentID            string `json:"subagent_id"`
	ParentConversationID  string `json:"parent_conversation_id"`

	// preToolUse / postToolUse / postToolUseFailure
	ToolName  string `json:"tool_name"`
	ToolInput any    `json:"tool_input"`

	// postToolUse
	ToolResponse any `json:"tool_response"`

	// postToolUseFailure
	Error string `json:"error"`

	// beforeSubmitPrompt
	Prompt string `json:"prompt"`

	// beforeShellExecution / afterShellExecution
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`

	// preCompact
	CompactType string `json:"compact_type"`

	// afterAgentThought
	Thought string `json:"thought"`
}

// cursorEventMap maps Cursor IDE hook event names to Irrlicht canonical (Claude Code) names.
var cursorEventMap = map[string]string{
	"sessionStart":         "SessionStart",
	"sessionEnd":           "SessionEnd",
	"stop":                 "Stop",
	"subagentStart":        "SessionStart",   // treated as sub-session start
	"subagentStop":         "SubagentStop",
	"preToolUse":           "PreToolUse",
	"postToolUse":          "PostToolUse",
	"postToolUseFailure":   "PostToolUse",    // same handler; error carried in response
	"beforeSubmitPrompt":   "UserPromptSubmit",
	"beforeShellExecution": "PreToolUse",     // speculative wait trigger
	"afterShellExecution":  "PostToolUse",
	"preCompact":           "PreCompact",
	"afterAgentThought":    "PreToolUse",     // keeps session in "working" during reasoning
}

// approvalProneKeywords are substrings that identify tools requiring user approval in Cursor.
var approvalProneKeywords = []string{
	"shell", "bash", "exec", "run",
	"write", "edit", "create", "delete",
}

// SessionIDPrefix is the prefix added to Cursor conversation IDs in session files.
const SessionIDPrefix = "cursor_"

// Adapter translates Cursor IDE hook payloads to HookEvents and calls the handler.
type Adapter struct {
	handler inbound.EventHandler
}

// New returns a new Cursor Adapter that normalises events and delegates to handler.
func New(handler inbound.EventHandler) *Adapter {
	return &Adapter{handler: handler}
}

// ReadAndHandle reads one Cursor hook payload from stdin, translates it to a HookEvent,
// and calls the handler. Returns payload size and any error.
func (a *Adapter) ReadAndHandle() (payloadSize int, err error) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0, fmt.Errorf("failed to read stdin: %w", err)
	}
	return a.readAndHandleBytes(input)
}

// readAndHandleBytes processes a raw payload byte slice. Exported for testing.
func (a *Adapter) readAndHandleBytes(input []byte) (int, error) {
	payloadSize := len(input)

	if payloadSize > event.MaxPayloadSize {
		return payloadSize, fmt.Errorf("payload size %d exceeds maximum %d", payloadSize, event.MaxPayloadSize)
	}

	var payload cursorPayload
	if err := json.NewDecoder(bytes.NewReader(input)).Decode(&payload); err != nil {
		return payloadSize, fmt.Errorf("failed to parse Cursor JSON: %w", err)
	}

	evt, err := a.translate(&payload)
	if err != nil {
		return payloadSize, err
	}

	if err := a.handler.HandleEvent(evt); err != nil {
		return payloadSize, err
	}
	return payloadSize, nil
}

// translate converts a Cursor payload to a HookEvent using canonical Irrlicht event names.
func (a *Adapter) translate(p *cursorPayload) (*event.HookEvent, error) {
	if p.ConversationID == "" {
		return nil, fmt.Errorf("cursor event missing conversation_id field")
	}

	if p.HookEventName == "" {
		return nil, fmt.Errorf("cursor event missing hook_event_name field")
	}

	canonicalName, ok := cursorEventMap[p.HookEventName]
	if !ok {
		return nil, fmt.Errorf("unknown cursor event name: %q", p.HookEventName)
	}

	// workspace_roots[0] is the primary working directory.
	cwd := ""
	if len(p.WorkspaceRoots) > 0 {
		cwd = p.WorkspaceRoots[0]
	}

	sessionID := SessionIDPrefix + p.ConversationID

	evt := &event.HookEvent{
		HookEventName:  canonicalName,
		SessionID:      sessionID,
		CWD:            cwd,
		Model:          p.Model,
		TranscriptPath: p.TranscriptPath,
		Adapter:        "cursor",
	}

	switch p.HookEventName {
	case "sessionStart":
		evt.Source = p.Source
		evt.PermissionMode = p.PermissionMode
		switch p.Source {
		case "resume":
			evt.Matcher = "resume"
		case "startup":
			evt.Matcher = "startup"
		}

	case "subagentStart":
		// Subagent start — use subagent_id as session ID to track it separately.
		if p.SubagentID != "" {
			evt.SessionID = SessionIDPrefix + p.SubagentID
		}
		evt.ParentSessionID = SessionIDPrefix + p.ConversationID

	case "subagentStop":
		// Route to parent conversation after subagent completes.
		if p.SubagentID != "" {
			evt.SessionID = SessionIDPrefix + p.SubagentID
		}
		evt.ParentSessionID = SessionIDPrefix + p.ConversationID

	case "sessionEnd":
		// All Cursor session-end reasons map to delete (no cancelled_by_user equivalent).
		evt.Reason = p.Reason

	case "stop":
		// No special mapping needed; canonicalName is "Stop".

	case "preToolUse":
		evt.ToolName = p.ToolName

	case "postToolUse", "postToolUseFailure":
		evt.ToolName = p.ToolName

	case "beforeShellExecution":
		// Treat as PreToolUse with a synthetic tool name for speculative waiting.
		evt.ToolName = "Bash"

	case "afterShellExecution":
		// Treat as PostToolUse completing the shell tool.
		evt.ToolName = "Bash"

	case "beforeSubmitPrompt":
		evt.Prompt = p.Prompt

	case "preCompact":
		evt.Matcher = p.CompactType

	case "afterAgentThought":
		// Maps to PreToolUse to keep session in "working" during reasoning.
		// No tool name needed.
	}

	return evt, nil
}

// IsApprovalProne returns true if the tool name suggests it may require user approval.
// Used by the speculative-wait trigger for Cursor sessions.
func IsApprovalProne(toolName string) bool {
	lower := strings.ToLower(toolName)
	for _, keyword := range approvalProneKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
