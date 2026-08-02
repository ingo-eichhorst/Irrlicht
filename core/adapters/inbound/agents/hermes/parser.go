package hermes

import (
	"encoding/json"
	"time"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for Hermes Agent sessions.
//
// Hermes has no transcript file: each "line" here is one row of the
// `messages` table, handed over as a map by ComputeMetrics (see rowToRaw).
// The rows are OpenAI-shaped, which makes the mapping to irrlicht's event
// vocabulary direct rather than heuristic:
//
//	role="user"                            → user_message (clears open tools)
//	role="assistant" finish_reason="stop"  → turn_done
//	role="assistant" finish_reason="tool_calls" → assistant_message + ToolUses
//	role="tool"                            → tool_result (closes by tool_call_id)
//
// finish_reason is the load-bearing field: it is an explicit turn boundary,
// so unlike the adapters that infer one from idle time, Hermes needs no
// IdleFlush seam to settle a session to ready.
//
// Distribution of the four shapes across the sample store used to build this
// (11 sessions, 91 message rows): assistant/tool_calls 28, tool 28,
// assistant/stop 17, user 17, user/model_switch 1.
type Parser struct{}

// Synthetic keys injected by ComputeMetrics onto each row map. They carry
// per-session context that lives on the `sessions` row rather than the
// message row.
const (
	keyRole        = "role"
	keyContent     = "content"
	keyToolCalls   = "tool_calls"
	keyToolCallID  = "tool_call_id"
	keyToolName    = "tool_name"
	keyFinish      = "finish_reason"
	keyDisplayKind = "display_kind"

	keyTimestamp = "_ts"    // epoch seconds (REAL) from messages.timestamp
	keyModel     = "_model" // sessions.model
	keyCWD       = "_cwd"   // sessions.cwd (empty for source='cli')
)

// maxAssistantText bounds the waiting-state display string, matching the
// tailer's own truncation budget.
const maxAssistantText = 200

// ParseLine converts one `messages` row into a normalized ParsedEvent.
// Returns nil for rows that carry no usable signal.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	role, _ := raw[keyRole].(string)
	if role == "" {
		return nil
	}

	ev := &tailer.ParsedEvent{}
	if ts, ok := raw[keyTimestamp].(float64); ok && ts > 0 {
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * float64(time.Second))
		ev.Timestamp = time.Unix(sec, nsec)
	}
	if cwd, ok := raw[keyCWD].(string); ok {
		ev.CWD = cwd
	}
	if model, ok := raw[keyModel].(string); ok {
		ev.ModelName = model
	}

	switch role {
	case "user":
		// A model_switch row is bookkeeping Hermes writes when the user
		// changes model mid-session, not a prompt. Treating it as a user
		// message would reset the open-tool set and restart the turn.
		if kind, _ := raw[keyDisplayKind].(string); kind == "model_switch" {
			ev.Skip = true
			return ev
		}
		ev.EventType = "user_message"
		ev.ClearToolNames = true
	case "assistant":
		ev.EventType = "assistant_message"
		if text, ok := raw[keyContent].(string); ok && text != "" {
			ev.AssistantText = truncateRunes(text, maxAssistantText)
		}
		ev.ToolUses = parseToolCalls(raw[keyToolCalls])
		if finish, _ := raw[keyFinish].(string); finish == "stop" {
			ev.EventType = "turn_done"
		}
	case "tool":
		ev.EventType = "tool_result"
		if id, ok := raw[keyToolCallID].(string); ok && id != "" {
			ev.ToolResultIDs = []string{id}
		}
	default:
		// "system" and any future role: no state signal to contribute.
		ev.Skip = true
	}

	return ev
}

// parseToolCalls extracts tool invocations from a `messages.tool_calls`
// column. The column holds a JSON array of OpenAI-style tool calls:
//
//	[{"id":"888148639","call_id":"888148639","type":"function",
//	  "function":{"name":"terminal","arguments":"{...}"}}]
//
// Both `id` and `call_id` were identical in every observed row; `id` is
// preferred and `call_id` is the fallback, because the matching `tool` row
// closes the call by `tool_call_id`.
func parseToolCalls(v interface{}) []tailer.ToolUse {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	var calls []struct {
		ID       string `json:"id"`
		CallID   string `json:"call_id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(s), &calls); err != nil {
		return nil
	}
	var uses []tailer.ToolUse
	for _, c := range calls {
		id := c.ID
		if id == "" {
			id = c.CallID
		}
		if id == "" {
			continue
		}
		uses = append(uses, tailer.ToolUse{ID: id, Name: c.Function.Name})
	}
	return uses
}

// truncateRunes keeps the LAST n runes, matching the tailer's tail-truncation
// of assistant text (the trailing prose is what carries a waiting cue).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
