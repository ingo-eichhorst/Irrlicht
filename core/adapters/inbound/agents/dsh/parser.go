package dsh

import (
	"fmt"
	"strings"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

const (
	recordSession          = "session"
	recordTurnStart        = "turn/start"
	recordTurnEnd          = "turn/end"
	recordApprovalAsked    = "approval/asked"
	recordApprovalDecided  = "approval/decided"
	recordAssistantMessage = "assistant/message"
	recordUserMessage      = "user/message"
	recordToolCall         = "tool/call"
	recordToolResult       = "tool/result"
	recordTodoWrite        = "todo/write"
	recordCompactionStart  = "compaction/start"
	recordCompactionEnd    = "compaction/end"
	recordRequestHeader    = "request/header"
	recordRequestContext   = "request/context"
)

// Parser normalizes DeepSeek Harness v3 durable records. Once a session
// header reports another version, every later record stays refused with the
// same session error. A parser restored at a nonzero tail offset can start
// after the header; the ledger already carries any earlier refusal.
type Parser struct {
	unsupportedVersion string
	todos              tailer.TodoReconciler
}

type recordHandler func(*Parser, map[string]any, *tailer.ParsedEvent)

func statelessHandler(handler func(map[string]any, *tailer.ParsedEvent)) recordHandler {
	return func(_ *Parser, raw map[string]any, event *tailer.ParsedEvent) {
		handler(raw, event)
	}
}

var recordHandlers = map[string]recordHandler{
	recordSession:          (*Parser).parseSession,
	recordTurnStart:        statelessHandler(parseTurnStart),
	recordTurnEnd:          statelessHandler(parseTurnEnd),
	recordApprovalAsked:    statelessHandler(parseApprovalAsked),
	recordApprovalDecided:  statelessHandler(parseApprovalDecided),
	recordAssistantMessage: statelessHandler(parseAssistantMessage),
	recordUserMessage:      statelessHandler(parseUserMessage),
	recordToolCall:         statelessHandler(parseToolCall),
	recordToolResult:       statelessHandler(parseToolResult),
	recordTodoWrite:        (*Parser).parseTodoWrite,
	recordCompactionStart:  statelessHandler(parseCompactionStart),
	recordCompactionEnd:    statelessHandler(parseCompactionEnd),
	recordRequestHeader:    statelessHandler(parseRequestHeader),
	recordRequestContext:   statelessHandler(parseRequestContext),
}

func (p *Parser) GetParserLedger() tailer.ParserLedger {
	return tailer.ParserLedger{UnsupportedFormatVersion: p.unsupportedVersion}
}

func (p *Parser) SetParserLedger(ledger tailer.ParserLedger) {
	p.unsupportedVersion = ledger.UnsupportedFormatVersion
}

func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{Timestamp: recordTimestamp(raw)}
	if p.unsupportedVersion != "" {
		p.applyUnsupportedVersion(ev)
		return ev
	}

	recordType, _ := raw["type"].(string)
	handler, ok := recordHandlers[recordType]
	if !ok {
		ev.Skip = true
		return ev
	}
	handler(p, raw, ev)
	return ev
}

func parseTurnStart(_ map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_start"
}

func (p *Parser) parseSession(raw map[string]any, ev *tailer.ParsedEvent) {
	version, ok := number(raw, "version")
	if !ok || version != supportedFormatVersion {
		if ok {
			p.unsupportedVersion = fmt.Sprintf("%v", version)
		} else {
			p.unsupportedVersion = "missing or non-numeric"
		}
		p.applyUnsupportedVersion(ev)
		return
	}
	ev.Skip = true
	ev.CWD = text(raw, "cwd")
}

func (p *Parser) applyUnsupportedVersion(ev *tailer.ParsedEvent) {
	ev.Skip = true
	ev.SessionError = &tailer.SessionError{
		Phase:   tailer.ErrorPhaseTerminal,
		Class:   "unsupported_transcript_version",
		Message: fmt.Sprintf("DeepSeek Harness transcript version %s is unsupported; supported version is %d", p.unsupportedVersion, supportedFormatVersion),
	}
}

func parseTurnEnd(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_done"
	data := object(raw, "data")
	reason := object(data, "reason")
	if text(reason, "kind") != "error" {
		return
	}
	errorData := object(reason, "error")
	class := strings.ToLower(text(errorData, "code"))
	if class == "" {
		class = "turn_failed"
	}
	message := strings.TrimSpace(text(errorData, "message"))
	if message == "" {
		message = "DeepSeek Harness turn failed"
	}
	ev.SessionError = &tailer.SessionError{
		Phase:   tailer.ErrorPhaseTerminal,
		Class:   class,
		Message: message,
	}
}

func parseApprovalAsked(raw map[string]any, ev *tailer.ParsedEvent) {
	data := object(raw, "data")
	id := text(data, "id")
	if id == "" {
		ev.Skip = true
		return
	}
	ev.EventType = "permission_requested"
	ev.PermissionRequestIDs = []string{id}
	detail := strings.TrimSpace(text(data, "reason"))
	if detail == "" {
		detail = text(data, "toolName")
	}
	ev.AssistantText = tailer.TruncateAssistantText(detail)
}

func parseApprovalDecided(raw map[string]any, ev *tailer.ParsedEvent) {
	id := text(object(raw, "data"), "id")
	if id == "" {
		ev.Skip = true
		return
	}
	ev.EventType = "permission_completed"
	ev.PermissionResolvedIDs = []string{id}
}

func parseAssistantMessage(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "assistant_message"
	data := object(raw, "data")
	message := object(data, "message")
	parseAssistantText(message, ev)

	model := tailer.NormalizeModelName(text(object(message, "source"), "model"))
	ev.ModelName = model
	parseAssistantUsage(data, model, ev)
}

func parseAssistantText(message map[string]any, ev *tailer.ParsedEvent) {
	fullText := messageText(message)
	if fullText == "" {
		return
	}
	ev.TaskEstimate = tailer.ScanTaskEstimate(fullText, ev.Timestamp)
	ev.TaskSummary = tailer.ScanTaskSummary(fullText, ev.Timestamp)
	ev.AssistantText = tailer.TruncateAssistantText(fullText)
	ev.PendingWaitingCue = session.ProseIndicatesWaiting(tailer.WaitingScanWindow(fullText))
}

func parseAssistantUsage(data map[string]any, model string, ev *tailer.ParsedEvent) {
	usage := object(data, "usage")
	input := integer(usage, "inputTokens")
	output := integer(usage, "outputTokens")
	total := integer(usage, "totalTokens")
	if total == 0 {
		total = input + output
	}
	snapshot := tailer.TokenSnapshot{Input: input, Output: output, Total: total}
	if snapshot == (tailer.TokenSnapshot{}) {
		return
	}
	ev.Tokens = &snapshot
	ev.Contribution = &tailer.PerTurnContribution{
		Model: model,
		Usage: tailer.UsageBreakdown{Input: input, Output: output},
	}
}

func parseUserMessage(raw map[string]any, ev *tailer.ParsedEvent) {
	message := object(raw, "data")
	if text(object(message, "source"), "kind") != "user" {
		ev.Skip = true
		return
	}
	ev.EventType = "user_message"
	ev.ClearToolNames = true
	ev.UserText = messageText(message)
}

func parseToolCall(raw map[string]any, ev *tailer.ParsedEvent) {
	data := object(raw, "data")
	id := text(data, "callId")
	name := text(data, "name")
	if id == "" || name == "" {
		ev.Skip = true
		return
	}
	ev.EventType = "function_call"
	ev.ToolUses = []tailer.ToolUse{{ID: id, Name: name}}
}

func parseToolResult(raw map[string]any, ev *tailer.ParsedEvent) {
	message := object(object(raw, "data"), "message")
	id := text(object(message, "source"), "callId")
	if id == "" {
		ev.Skip = true
		return
	}
	ev.EventType = "function_call_output"
	ev.ToolResultIDs = []string{id}
	for _, item := range array(message, "content") {
		block, _ := item.(map[string]any)
		if value, ok := block["isError"].(bool); ok && value {
			ev.IsError = true
		}
	}
}

// parseTodoWrite reconciles DSH's authoritative whole-list todo snapshot.
// Each durable todo/write record replaces the agent's visible list.
func (p *Parser) parseTodoWrite(raw map[string]any, ev *tailer.ParsedEvent) {
	todos := make([]tailer.Todo, 0)
	for _, rawTodo := range array(object(raw, "data"), "todos") {
		todo, _ := rawTodo.(map[string]any)
		if todo == nil {
			continue
		}
		todos = append(todos, tailer.Todo{
			Key:    text(todo, "content"),
			Status: text(todo, "status"),
		})
	}
	if len(todos) == 0 {
		ev.Skip = true
		return
	}
	ev.EventType = "task_update"
	p.todos.Reconcile(todos, ev)
}

func parseCompactionStart(_ map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_start"
}

func parseCompactionEnd(_ map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_done"
}

func parseRequestHeader(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	header := object(object(raw, "data"), "header")
	config := object(header, "config")
	ev.ModelName = tailer.NormalizeModelName(text(config, "model"))
}

func parseRequestContext(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	data := object(raw, "data")
	ev.ModelName = tailer.NormalizeModelName(text(data, "model"))
	ev.ContextWindow = integer(data, "contextWindow")
}

func recordTimestamp(raw map[string]any) time.Time {
	if millis := integer(raw, "time"); millis != 0 {
		return time.UnixMilli(millis)
	}
	if millis := integer(raw, "createdAt"); millis != 0 {
		return time.UnixMilli(millis)
	}
	return time.Time{}
}

func messageText(message map[string]any) string {
	var parts []string
	for _, item := range array(message, "content") {
		block, ok := item.(map[string]any)
		if !ok || text(block, "type") != "text" {
			continue
		}
		if value := strings.TrimSpace(text(block, "text")); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func object(value map[string]any, key string) map[string]any {
	result, _ := value[key].(map[string]any)
	return result
}

func array(value map[string]any, key string) []any {
	result, _ := value[key].([]any)
	return result
}

func text(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func number(value map[string]any, key string) (float64, bool) {
	result, ok := value[key].(float64)
	return result, ok
}

func integer(value map[string]any, key string) int64 {
	result, _ := number(value, key)
	return int64(result)
}
