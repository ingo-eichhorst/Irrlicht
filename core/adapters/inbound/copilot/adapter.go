// Package copilot implements the inbound adapter for GitHub Copilot CLI hook events.
//
// Copilot CLI uses the same stdin-JSON hook convention as Claude Code but differs in:
//   - Event names use camelCase (sessionStart) vs PascalCase (SessionStart)
//   - No sessionId in shell hook payloads — session key is derived from sha256(cwd)[0:16]
//   - Tool names are lowercase (bash, edit) vs TitleCase (Bash, Edit)
//   - Hook event name is passed via --event flag, not embedded in the JSON payload
package copilot

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"irrlicht/core/domain/event"
	"irrlicht/core/ports/inbound"
)

// copilotPayload represents the JSON payload Copilot CLI sends to shell hooks via stdin.
type copilotPayload struct {
	Timestamp int64  `json:"timestamp"`
	CWD       string `json:"cwd"`

	// sessionStart
	Source        string `json:"source"`
	InitialPrompt string `json:"initialPrompt"`

	// sessionEnd
	Reason string `json:"reason"`

	// userPromptSubmitted
	Prompt string `json:"prompt"`

	// preToolUse / postToolUse
	ToolName   string              `json:"toolName"`
	ToolArgs   string              `json:"toolArgs"`
	ToolResult *copilotToolResult  `json:"toolResult"`

	// errorOccurred
	Error *copilotError `json:"error"`
}

type copilotToolResult struct {
	ResultType      string `json:"resultType"`
	TextResultForLLM string `json:"textResultForLlm"`
}

type copilotError struct {
	Message string `json:"message"`
	Name    string `json:"name"`
}

// copilotEventMap maps Copilot CLI hook event names to Claude Code canonical names.
var copilotEventMap = map[string]string{
	"sessionStart":        "SessionStart",
	"userPromptSubmitted": "UserPromptSubmit",
	"preToolUse":          "PreToolUse",
	"postToolUse":         "PostToolUse",
	"agentStop":           "Stop",
	"subagentStop":        "SubagentStop",
	"errorOccurred":       "Stop",  // no direct equivalent; treat as session-ready
	"sessionEnd":          "SessionEnd",
}

// DeriveSessionKey returns "copilot-" + sha256(cwd) first 8 bytes as hex (16 hex chars total suffix).
// This is the stable session identity for a given working directory.
func DeriveSessionKey(cwd string) string {
	h := sha256.Sum256([]byte(cwd))
	return fmt.Sprintf("copilot-%x", h[:8])
}

// Adapter translates Copilot CLI hook payloads to HookEvents and calls the handler.
type Adapter struct {
	handler   inbound.EventHandler
	eventName string // the Copilot event name provided via --event flag
}

// New returns a new Copilot Adapter that normalises events and delegates to handler.
// eventName is the Copilot event name (e.g. "sessionStart"), set via --event flag.
func New(handler inbound.EventHandler, eventName string) *Adapter {
	return &Adapter{handler: handler, eventName: eventName}
}

// ReadAndHandle reads one Copilot hook payload from stdin, translates it to a HookEvent,
// and calls the handler. Returns payload size and any error.
func (a *Adapter) ReadAndHandle() (payloadSize int, err error) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0, fmt.Errorf("failed to read stdin: %w", err)
	}
	payloadSize = len(input)

	if payloadSize > event.MaxPayloadSize {
		return payloadSize, fmt.Errorf("payload size %d exceeds maximum %d", payloadSize, event.MaxPayloadSize)
	}

	var payload copilotPayload
	if err := json.Unmarshal(input, &payload); err != nil {
		return payloadSize, fmt.Errorf("failed to parse Copilot JSON: %w", err)
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

// translate converts a Copilot payload to a HookEvent using the canonical Claude Code event names.
func (a *Adapter) translate(p *copilotPayload) (*event.HookEvent, error) {
	if p.CWD == "" {
		return nil, fmt.Errorf("copilot event missing cwd field")
	}

	canonicalName, ok := copilotEventMap[a.eventName]
	if !ok {
		return nil, fmt.Errorf("unknown copilot event name: %q", a.eventName)
	}

	sessionKey := DeriveSessionKey(p.CWD)

	evt := &event.HookEvent{
		HookEventName: canonicalName,
		SessionID:     sessionKey,
		CWD:           p.CWD,
		Adapter:       "copilot",
	}

	switch canonicalName {
	case "SessionStart":
		evt.Source = p.Source
		// Map Copilot source values to matcher values the state machine understands.
		switch p.Source {
		case "resume":
			evt.Matcher = "resume"
		case "startup":
			evt.Matcher = "startup"
		default:
			// "new" or empty → new session (no matcher needed)
		}

	case "SessionEnd":
		// Map Copilot session-end reasons to the Claude Code reason vocabulary.
		// "user_exit" → StateCancelledByUser; anything else → StateDeleteSession.
		switch p.Reason {
		case "user_exit":
			evt.Reason = "prompt_input_exit"
		default:
			evt.Reason = p.Reason
		}

	case "PreToolUse", "PostToolUse":
		// Normalise Copilot lowercase tool names to TitleCase for approvalProneTools lookup.
		evt.ToolName = titleCase(p.ToolName)
	}

	return evt, nil
}

// titleCase upper-cases the first letter of s (e.g. "bash" → "Bash").
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
