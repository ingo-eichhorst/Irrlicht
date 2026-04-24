package claudecode

import (
	"strings"

	"irrlicht/core/pkg/tailer"
	"irrlicht/core/pkg/transcript"
)

// eventTypeAssistantStreaming is emitted for intermediate Claude Code assistant
// messages (thinking blocks, partial text) that should not trigger IsAgentDone().
const eventTypeAssistantStreaming = "assistant_streaming"

// Parser implements tailer.TranscriptParser for Claude Code transcripts.
// Claude Code events use top-level "type" fields ("user", "assistant", "system")
// and embed tool calls inside message.content[] arrays.
//
// The parser is stateful: it tracks the last requestId to deduplicate streaming
// events within one API turn and expose the pending contribution to the tailer.
type Parser struct {
	// Cost deduplication state: Claude Code emits multiple streaming events with
	// the same requestId per turn (partial output, then final with full tokens).
	// We keep the latest usage for the current requestId as pendingContrib;
	// when the requestId changes we emit it as a completed Contribution.
	lastRequestID  string
	pendingContrib *tailer.PerTurnContribution
}

// ParseLine parses a Claude Code JSONL line into a normalized ParsedEvent.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{
		Timestamp: tailer.ParseTimestamp(raw),
	}
	if cwd := transcript.ExtractCWDFromLine(raw); cwd != "" {
		ev.CWD = cwd
	}

	eventType := resolveEventType(raw)

	if handleEarlyReturn(eventType, raw, ev) {
		return ev
	}

	ev.ModelName, ev.ContextWindow = extractClaudeCodeModel(raw)
	ev.Tokens = extractClaudeCodeTokens(raw)
	p.applyRequestIDContribution(raw, ev)
	ev.ContentChars = tailer.ExtractContentChars(raw)

	eventType = resolveAssistantStreaming(raw, eventType)

	if !isClaudeCodeMessageEvent(eventType) {
		ev.Skip = true
		return ev
	}
	ev.EventType = eventType

	askUserQuestion := scanMessageContent(raw, ev)
	applyAssistantText(raw, ev, eventType, askUserQuestion)
	return ev
}

// resolveEventType reads the event discriminator, falling back to field shape
// for legacy transcript flavours that don't set "type" explicitly.
func resolveEventType(raw map[string]interface{}) string {
	if et, ok := raw["type"].(string); ok {
		return et
	}
	if _, ok := raw["user_input"]; ok {
		return "user_message"
	}
	if _, ok := raw["assistant_output"]; ok {
		return "assistant_message"
	}
	if _, ok := raw["tool_call"]; ok {
		return "tool_call"
	}
	return "unknown"
}

// handleEarlyReturn processes event types that never reach the message-content
// pipeline (system/turn_done, user meta/interrupts, permission-mode). Returns
// true when the caller should return ev immediately.
func handleEarlyReturn(eventType string, raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	switch eventType {
	case "system":
		handleSystemEvent(raw, ev)
		return true
	case "user":
		return handleUserEvent(raw, ev)
	case "permission-mode":
		if mode, ok := raw["permissionMode"].(string); ok {
			ev.PermissionMode = mode
		}
		ev.Skip = true
		return true
	}
	return false
}

// handleSystemEvent maps turn_duration / stop_hook_summary subtypes to
// turn_done; everything else is skipped.
func handleSystemEvent(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	if subtype, _ := raw["subtype"].(string); subtype == "turn_duration" || subtype == "stop_hook_summary" {
		ev.EventType = "turn_done"
		return
	}
	ev.Skip = true
}

// handleUserEvent handles the user event variants that should short-circuit
// the parser (isMeta, task-notification, local-command wrappers). Returns
// true when caller should return ev immediately. Interrupts are recorded on
// ev but do NOT short-circuit.
func handleUserEvent(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	if isMeta, ok := raw["isMeta"].(bool); ok && isMeta {
		ev.Skip = true
		return true
	}
	if handleTaskNotification(raw, ev) {
		return true
	}
	msg, ok := raw["message"].(map[string]interface{})
	if !ok {
		return false
	}
	if isLocalCommandContent(msg) {
		ev.Skip = true
		return true
	}
	recordUserInterruptFlags(msg, ev)
	return false
}

// handleTaskNotification captures subagent-completion signals from the parent
// transcript's task-notification events. Returns true when the event was a
// task-notification and the caller should skip it. See issue #134.
func handleTaskNotification(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	origin, ok := raw["origin"].(map[string]interface{})
	if !ok {
		return false
	}
	if kind, _ := origin["kind"].(string); kind != "task-notification" {
		return false
	}
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if content, ok := msg["content"].(string); ok {
			ev.SubagentCompletions = append(ev.SubagentCompletions, tailer.SubagentCompletion{
				AgentID:   extractXMLField(content, "task-id"),
				ToolUseID: extractXMLField(content, "tool-use-id"),
				Status:    extractXMLField(content, "status"),
			})
		}
	}
	ev.Skip = true
	return true
}

// isLocalCommandContent returns true when the user message is one of the
// shell-escape / command wrappers that Claude Code writes for /context,
// <bash-input>, etc. These don't represent real user turns.
func isLocalCommandContent(msg map[string]interface{}) bool {
	content, ok := msg["content"].(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(content, "<local-command") ||
		strings.HasPrefix(content, "<command-name>") ||
		strings.HasPrefix(content, "<bash-input>") ||
		strings.HasPrefix(content, "<bash-stdout>")
}

// recordUserInterruptFlags scans a user message's content blocks for the two
// interrupt flavours (ESC cancel vs tool-use denial) and sets the matching
// flag on ev. Neither sets IsError — that's reserved for tool_result.is_error.
// See issue #102 Bug B and the follow-up split for the denial flicker.
func recordUserInterruptFlags(msg map[string]interface{}, ev *tailer.ParsedEvent) {
	contentArr, ok := msg["content"].([]interface{})
	if !ok {
		return
	}
	for _, item := range contentArr {
		block, ok := item.(map[string]interface{})
		if !ok || block["type"] != "text" {
			continue
		}
		text, ok := block["text"].(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(text, "[Request interrupted by user for tool use") {
			ev.IsToolDenial = true
			return
		}
		if strings.HasPrefix(text, "[Request interrupted by user") {
			ev.IsUserInterrupt = true
			return
		}
	}
}

// applyRequestIDContribution implements the request-ID-scoped deduplication.
// Claude Code streams multiple events per API turn with the same requestId;
// only the final event carries authoritative token counts. We emit the prior
// turn's contribution once a new requestId arrives.
func (p *Parser) applyRequestIDContribution(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	reqID, ok := raw["requestId"].(string)
	if !ok || reqID == "" {
		return
	}
	ev.RequestID = reqID
	if reqID != p.lastRequestID {
		if p.lastRequestID != "" && p.pendingContrib != nil {
			ev.Contribution = p.pendingContrib
		}
		p.lastRequestID = reqID
		p.pendingContrib = nil
	}
	if ev.Tokens != nil {
		p.pendingContrib = &tailer.PerTurnContribution{
			Model: ev.ModelName,
			Usage: extractAnthropicUsageBreakdown(raw),
		}
	}
}

// resolveAssistantStreaming downgrades an assistant event to the streaming
// marker when its stop_reason is not known-terminal, preventing IsAgentDone()
// from firing between tool calls. Uses an allow-list so unknown future stop
// reasons default to "assume streaming" — the safe side of Bug D (#102).
//
// Terminal stop_reasons: end_turn, stop_sequence, refusal, tool_use.
// Everything else (nil, max_tokens, pause_turn, unknown) maps to streaming.
func resolveAssistantStreaming(raw map[string]interface{}, eventType string) string {
	if eventType != "assistant" {
		return eventType
	}
	msg, ok := raw["message"].(map[string]interface{})
	if !ok {
		return eventType
	}
	stopReason, _ := msg["stop_reason"].(string)
	switch stopReason {
	case "end_turn", "stop_sequence", "refusal", "tool_use":
		return eventType
	default:
		return eventTypeAssistantStreaming
	}
}

// scanMessageContent walks message.content[] collecting tool_use / tool_result
// deltas onto ev. Returns the latest AskUserQuestion prompt text so the caller
// can surface it as the waiting-state header when no text block is present.
func scanMessageContent(raw map[string]interface{}, ev *tailer.ParsedEvent) string {
	msg, ok := raw["message"].(map[string]interface{})
	if !ok {
		return ""
	}
	contentArr, ok := msg["content"].([]interface{})
	if !ok {
		return ""
	}
	var askUserQuestion string
	for _, item := range contentArr {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		switch block["type"] {
		case "tool_use":
			if q := collectToolUse(block, ev); q != "" {
				askUserQuestion = q
			}
		case "tool_result":
			collectToolResult(block, ev)
		}
	}
	return askUserQuestion
}

// collectToolUse records a tool_use block onto ev and extracts task/question
// metadata for the known Claude Code tool kinds. Returns the AskUserQuestion
// prompt text when this block was one.
func collectToolUse(block map[string]interface{}, ev *tailer.ParsedEvent) string {
	id, _ := block["id"].(string)
	name, _ := block["name"].(string)
	if name != "" {
		ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
	}
	input, _ := block["input"].(map[string]interface{})
	if input == nil {
		return ""
	}
	switch name {
	case "TaskCreate":
		subject, _ := input["subject"].(string)
		desc, _ := input["description"].(string)
		activeForm, _ := input["activeForm"].(string)
		ev.TaskDeltas = append(ev.TaskDeltas, tailer.TaskDelta{
			Op:          tailer.TaskOpCreate,
			Subject:     subject,
			Description: desc,
			ActiveForm:  activeForm,
		})
	case "TaskUpdate":
		taskID, _ := input["taskId"].(string)
		status, _ := input["status"].(string)
		ev.TaskDeltas = append(ev.TaskDeltas, tailer.TaskDelta{
			Op:     tailer.TaskOpUpdate,
			ID:     taskID,
			Status: status,
		})
	case "AskUserQuestion":
		if qs, ok := input["questions"].([]interface{}); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]interface{}); ok {
				if text, _ := q["question"].(string); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

// collectToolResult records a tool_result's matching id and the is_error flag.
func collectToolResult(block map[string]interface{}, ev *tailer.ParsedEvent) {
	if toolUseID, ok := block["tool_use_id"].(string); ok && toolUseID != "" {
		ev.ToolResultIDs = append(ev.ToolResultIDs, toolUseID)
	}
	if isErr, ok := block["is_error"].(bool); ok && isErr {
		ev.IsError = true
	}
}

// applyAssistantText fills AssistantText for assistant/streaming events,
// falling back to the tail of an AskUserQuestion prompt when the message
// carries no text block. For user events it just signals the tool-name reset.
func applyAssistantText(raw map[string]interface{}, ev *tailer.ParsedEvent, eventType, askUserQuestion string) {
	switch eventType {
	case "assistant", eventTypeAssistantStreaming, "assistant_message", "assistant_output":
		ev.AssistantText = tailer.ExtractAssistantText(raw)
		if ev.AssistantText == "" && askUserQuestion != "" {
			runes := []rune(askUserQuestion)
			if len(runes) > 200 {
				ev.AssistantText = "…" + string(runes[len(runes)-200:])
			} else {
				ev.AssistantText = askUserQuestion
			}
		}
	case "user", "user_message", "user_input":
		ev.ClearToolNames = true
	}
}

// extractClaudeCodeModel extracts model name and context window from a Claude Code event.
func extractClaudeCodeModel(raw map[string]interface{}) (string, int64) {
	var modelName string
	var contextWindow int64

	// Check direct fields.
	if model, ok := raw["model"].(string); ok {
		modelName = model
	} else if request, ok := raw["request"].(map[string]interface{}); ok {
		if model, ok := request["model"].(string); ok {
			modelName = model
		}
	} else if metadata, ok := raw["metadata"].(map[string]interface{}); ok {
		if model, ok := metadata["model"].(string); ok {
			modelName = model
		}
	}

	// message.model (assistant messages).
	if modelName == "" {
		if message, ok := raw["message"].(map[string]interface{}); ok {
			if model, ok := message["model"].(string); ok {
				modelName = model
			}
		}
	}

	// Extended context detection.
	if strings.Contains(modelName, "[1m]") {
		contextWindow = 1000000
	}

	// context_management.context_window (most accurate source).
	if cm, ok := raw["context_management"].(map[string]interface{}); ok {
		if cw, ok := cm["context_window"].(float64); ok && cw > 0 {
			contextWindow = int64(cw)
		}
	}

	if modelName != "" {
		modelName = tailer.NormalizeModelName(modelName)
	}
	return modelName, contextWindow
}

// findUsageMap returns the first usage block found in a Claude Code event.
// Claude Code places it at raw["usage"], raw["message"]["usage"], or
// raw["response"]["usage"] depending on event type.
func findUsageMap(raw map[string]interface{}) map[string]interface{} {
	if u, ok := raw["usage"].(map[string]interface{}); ok {
		return u
	}
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if u, ok := msg["usage"].(map[string]interface{}); ok {
			return u
		}
	}
	if resp, ok := raw["response"].(map[string]interface{}); ok {
		if u, ok := resp["usage"].(map[string]interface{}); ok {
			return u
		}
	}
	return nil
}

// extractClaudeCodeTokens extracts token info from a Claude Code event.
// Used for context-utilization display (Tokens field on ParsedEvent).
func extractClaudeCodeTokens(raw map[string]interface{}) *tailer.TokenSnapshot {
	if usage := findUsageMap(raw); usage != nil {
		return tailer.ExtractUsage(usage)
	}
	return nil
}

// PendingContribution returns the in-progress turn's contribution so the tailer
// can include it in the live cost display before the next turn begins.
func (p *Parser) PendingContribution() *tailer.PerTurnContribution {
	return p.pendingContrib
}

// GetParserLedger implements tailer.ParserStateProvider. Saves lastRequestID so
// the dedup cursor resumes at the correct turn boundary after a daemon restart.
func (p *Parser) GetParserLedger() tailer.ParserLedger {
	return tailer.ParserLedger{LastRequestID: p.lastRequestID}
}

// SetParserLedger implements tailer.ParserStateProvider. Restores the dedup
// cursor; pendingContrib is intentionally not restored because the partial turn
// will be re-emitted as a new Contribution when the next requestId arrives.
func (p *Parser) SetParserLedger(l tailer.ParserLedger) {
	p.lastRequestID = l.LastRequestID
}

// extractAnthropicUsageBreakdown builds a UsageBreakdown from a Claude Code
// event, including Anthropic's nested 5m/1h cache-write sub-rates when present.
func extractAnthropicUsageBreakdown(raw map[string]interface{}) tailer.UsageBreakdown {
	usage := findUsageMap(raw)
	if usage == nil {
		return tailer.UsageBreakdown{}
	}

	bd := tailer.UsageBreakdown{}
	if v, ok := usage["input_tokens"].(float64); ok {
		bd.Input = int64(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		bd.Output = int64(v)
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		bd.CacheRead = int64(v)
	}

	// Anthropic ephemeral cache writes come in two formats:
	//   New: usage.cache_creation.ephemeral_5m_input_tokens / ephemeral_1h_input_tokens
	//   Old: usage.cache_creation_input_tokens (flat, treated as 5m)
	// The nested path wins when present; the flat fallback only fires when both
	// nested buckets are absent, so there is no double-counting.
	if cc, ok := usage["cache_creation"].(map[string]interface{}); ok {
		if v, ok := cc["ephemeral_5m_input_tokens"].(float64); ok {
			bd.CacheCreation5m = int64(v)
		}
		if v, ok := cc["ephemeral_1h_input_tokens"].(float64); ok {
			bd.CacheCreation1h = int64(v)
		}
	}
	if bd.CacheCreation5m == 0 && bd.CacheCreation1h == 0 {
		if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
			bd.CacheCreation5m = int64(v)
		}
	}
	return bd
}

// CountOpenSubagents returns the number of in-process Claude Code sub-agents
// that are NOT already tracked as file-based child sessions. Current Claude
// Code writes an isSidechain transcript under `<parent>/subagents/agent-*.jsonl`
// for every Agent tool call (including Explore/Plan), so the fswatcher picks
// them up as child SessionStates and the file-based path alone is the single
// source of truth. Counting open Agent tool_use entries in LastOpenToolNames
// as well would double-count each running subagent.
//
// We keep this function as the seam the adapter exposes to the metrics layer
// so that if a future Claude Code revision reintroduces truly in-process
// subagents that don't create transcripts, we only need to change this file.
func CountOpenSubagents(m *tailer.SessionMetrics) int {
	return 0
}

// extractXMLField pulls the inner text of <tag>...</tag> from a flat XML blob.
// Used to read task-id, tool-use-id, and status from task-notification events.
// Returns "" if the tag is missing or malformed.
func extractXMLField(xml, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(xml, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(xml[start:], close)
	if end < 0 {
		return ""
	}
	return xml[start : start+end]
}

// isClaudeCodeMessageEvent returns true for event types that count as messages.
func isClaudeCodeMessageEvent(eventType string) bool {
	switch eventType {
	case "user_message", "assistant_message", "tool_call", "tool_result",
		"user_input", "assistant_output", "user", "assistant", "tool_use", "message",
		eventTypeAssistantStreaming:
		return true
	}
	return false
}
