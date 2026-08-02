package hermes

import (
	"encoding/json"
	"time"

	"irrlicht/core/domain/session"
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
type Parser struct {
	// todos reconciles the `todo` tool's whole-list snapshots into task
	// deltas. Hermes rewrites the entire list on every call (todo_tool.py's
	// replace mode), which is exactly the shape TodoReconciler exists for.
	// Stateful, so one Parser per session — ComputeMetrics builds a fresh one
	// and replays every row, so the reconciler is rebuilt deterministically.
	todos tailer.TodoReconciler
}

// todoToolName is hermes' single task-management tool. It ships in the default
// CLI toolset, so a task list is ordinary traffic rather than an opt-in.
const todoToolName = "todo"

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
			// Order matters: the waiting cue is computed from the FULL text
			// before truncation, because TruncateAssistantText keeps only a
			// bounded tail and a cue sitting further back would be dropped.
			// Both rules are the shared ones every adapter uses, not local
			// reimplementations.
			ev.PendingWaitingCue = session.ProseIndicatesWaiting(tailer.WaitingScanWindow(text))
			ev.AssistantText = tailer.TruncateAssistantText(text)
		}
		calls := decodeToolCalls(raw[keyToolCalls])
		ev.ToolUses = toolUses(calls)
		// The todo call is reported as a tool use AND mined for the plan it
		// carries: dropping the use would leave a tool open that never closes.
		p.todos.Reconcile(todosFromCalls(calls), ev)
		if finish, _ := raw[keyFinish].(string); finishReasonEndsTurn(finish) {
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
// finishReasonContinues is the ONE finish_reason that means the agent is still
// working. Hermes v0.19.0's agent/conversation_loop.py assigns 'stop',
// 'length' (hit max output tokens), 'content_filter' (model declined),
// 'incomplete' (provider-mapped truncation) and 'tool_calls'.
const finishReasonContinues = "tool_calls"

// finishReasonEndsTurn reports whether a finish_reason closes the turn.
//
// Deliberately a DENYLIST, not an allowlist of terminal values, because the
// two failure modes are wildly asymmetric. This adapter declares no IdleFlush
// and there is no working sweep (removed in PR #106), so the store's
// finish_reason is the only thing that can ever settle a Hermes session: an
// allowlist missing a terminal value strands the session in `working`
// permanently, with no recovery path. Mistaking a future continuing value for
// a terminal one merely settles a beat early and is corrected by the next row.
//
// An empty reason is the in-flight row hermes writes before the turn closes,
// so it continues.
func finishReasonEndsTurn(finish string) bool {
	return finish != "" && finish != finishReasonContinues
}

// toolCall is one decoded entry of the `tool_calls` column. Arguments is a
// JSON *string* (the column double-encodes it), so it is decoded lazily and
// only for the calls that carry a payload we read.
type toolCall struct {
	ID       string `json:"id"`
	CallID   string `json:"call_id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// decodeToolCalls unmarshals the column into calls. A malformed value yields
// nil rather than an error: a row we cannot read must not stall the session.
func decodeToolCalls(v interface{}) []toolCall {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	var calls []toolCall
	if err := json.Unmarshal([]byte(s), &calls); err != nil {
		return nil
	}
	return calls
}

func parseToolCalls(v interface{}) []tailer.ToolUse {
	return toolUses(decodeToolCalls(v))
}

func toolUses(calls []toolCall) []tailer.ToolUse {
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

// todosFromCalls extracts the todo-list snapshot carried by a `todo` call.
//
// Hermes' todo tool takes `todos: [{id, content, status}]` (tools/todo_tool.py,
// statuses pending|in_progress|completed|cancelled). The per-todo `id` is
// stable, unlike opencode's, but the todos are keyed here by `content` anyway:
// Key doubles as TaskDelta.Subject, and an agent-chosen id like "t2" is not
// something to show a user. Two todos sharing content collapse — the same
// documented trade-off every other TodoReconciler caller makes.
//
// A read-mode call (no `todos` param) and unparseable arguments both yield
// nil, which Reconcile treats as a no-op.
func todosFromCalls(calls []toolCall) []tailer.Todo {
	var todos []tailer.Todo
	for _, c := range calls {
		if c.Function.Name != todoToolName || c.Function.Arguments == "" {
			continue
		}
		var args struct {
			Todos []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
			continue
		}
		for _, t := range args.Todos {
			todos = append(todos, tailer.Todo{Key: t.Content, Status: t.Status})
		}
	}
	return todos
}
