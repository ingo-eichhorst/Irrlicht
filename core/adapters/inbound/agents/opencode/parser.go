package opencode

import (
	"encoding/json"
	"time"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for OpenCode sessions.
//
// OpenCode stores session data in a SQLite database rather than JSONL files.
// Each row in the `part` table has a `data` JSON column that this parser
// interprets. The watcher reads rows from the DB and calls ParseLine with the
// unmarshalled JSON map for each part.
//
// Key part types from OpenCode's schema:
//
//	step-start    — begins an LLM generation step (skip; used for context only)
//	step-finish   — ends a step; reason="stop" signals turn completion
//	text          — assistant text output
//	tool          — tool call; state.status tracks pending→running→completed
//
// Token/cost data lives on step-finish parts and on the parent `message` row
// (role="assistant"). The parser extracts cost and token counts from
// step-finish to populate PerTurnContribution.
//
// The watcher passes message role context via the synthetic "_role" key in
// the raw map.
//
// Parser keeps minimal state to translate OpenCode's snapshot-style
// `todowrite` tool (which rewrites the entire todo list on every call) into
// the canonical TaskCreate/TaskUpdate delta sequence the tailer expects.
// Each Parser instance corresponds to one transcript/session scan; state
// resets across scans because ComputeMetrics constructs a fresh Parser.
type Parser struct {
	// todos reconciles OpenCode's todowrite snapshots into task-progress
	// deltas. OpenCode todos have no stable identifier, so they're keyed by
	// `content` — the closest thing to identity.
	todos tailer.TodoReconciler
}

// ParseLine parses a raw map representing one OpenCode part row into a
// normalized ParsedEvent. The map is expected to contain the decoded JSON
// from the `part.data` column. The watcher may inject:
//
//	"_role"    — the role from the parent message row ("user" / "assistant")
//	"_session" — the session ID (for CWD lookup, if needed)
//	"_cwd"     — the session's working directory
//	"_ts"      — epoch-ms timestamp from part.time_updated
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{}

	// Extract synthetic context injected by the watcher.
	if ts, ok := raw["_ts"].(float64); ok && ts > 0 {
		ev.Timestamp = time.UnixMilli(int64(ts))
	}
	if cwd, ok := raw["_cwd"].(string); ok {
		ev.CWD = cwd
	}

	partType, _ := raw["type"].(string)

	// An aborted/errored turn (quota, context overflow, provider error) records
	// the failure on the parent MESSAGE (message.data.error), which the opencode
	// onboarding driver exports as the synthetic "_error" key on every part of
	// that message. opencode emits NO step-finish reason="error" part for such a
	// turn — only a bare step-start — so on the REPLAY path "_error" is the sole
	// turn-ending signal (the live daemon settles via watcher.go isErrorMessage
	// reading the message row directly; #493). Mirror that here so a replayed
	// errored turn closes instead of sticking in "working". step-finish is
	// excluded — it carries its own terminal reason, handled below — and a
	// normal (non-errored) part exports "_error": null, so this never fires
	// outside an actual error.
	//
	// #1800 CHANGES TWO THINGS HERE. It carries the failure detail through
	// instead of discarding it, and it routes the shape test through
	// sessionErrorFrom — the same predicate watcher.go's isErrorMessage uses.
	// Before that they disagreed: this site fired on ANY non-nil value, so an
	// `"_error": {}` or `"_error": false` ended the turn under replay while
	// the live daemon ignored it. A golden and the daemon disagreeing about
	// the same session is the one thing the replay corpus exists to rule out.
	if partType != "step-finish" {
		if se := sessionErrorFrom(raw["_error"]); se != nil {
			ev.EventType = "turn_done"
			ev.SessionError = se
			return ev
		}
	}

	switch partType {
	case "step-start":
		// go:S1871 — same body as default below, kept as its own case
		// deliberately: "step-start" is step-finish's documented counterpart
		// (both are real, named opencode part types, unlike default's true
		// catch-all for part types this parser doesn't model), so it stays
		// named here for symmetry with step-finish even though there is
		// nothing to extract from it today.
		ev.Skip = true
		return ev

	case "step-finish":
		return parseStepFinish(raw, ev)

	case "text":
		return parseTextPart(raw, ev)

	case "tool":
		return p.parseToolPart(raw, ev)

	default:
		// snapshot, file, image, and other part types — skip
		ev.Skip = true
		return ev
	}
}

// parseStepFinish handles the step-finish part type.
// reason="stop"       → agent has finished the turn → emit "turn_done"
// reason="interrupted"→ user cancelled (Ctrl+C)     → emit "turn_done"
// reason="length"     → context window exceeded      → emit "turn_done"
// reason="error"      → API/other error              → emit "turn_done"
// reason="tool-calls" → agent is about to call tools → emit "assistant_message"
//
// Token and cost data from step-finish is used to build a PerTurnContribution
// for all reasons except "tool-calls" (which represents a mid-turn pause).
func parseStepFinish(raw map[string]interface{}, ev *tailer.ParsedEvent) *tailer.ParsedEvent {
	reason, _ := raw["reason"].(string)

	applyStepFinishTokens(raw, reason, ev)

	switch reason {
	case "stop":
		// Primary done signal — IsAgentDone() fires via this path.
		ev.EventType = "turn_done"
	case "tool-calls":
		// Agent is about to invoke tools; stay in working state.
		ev.EventType = "assistant_message"
	case "interrupted":
		// User cancelled (Ctrl+C). The agent has genuinely stopped.
		ev.EventType = "turn_done"
	case "length":
		// Context window exceeded — the agent stopped generating.
		ev.EventType = "turn_done"
	case "error":
		// API or other error — the agent stopped generating.
		ev.EventType = "turn_done"
		// #1800. step-finish carries no message of its own — the reason IS
		// the whole report — so Message says only what opencode said. NO
		// FIXTURE COVERS THIS ARM: across every recorded opencode transcript
		// the step-finish reasons are 49 × "stop", 44 × "tool-calls" and
		// 1 × "length", and opencode's own 2-14 cell is stamped
		// driver_capability: gap:error-export. Stated so the arm is not read
		// as fixture-backed.
		ev.SessionError = &tailer.SessionError{
			Phase:   tailer.ErrorPhaseTerminal,
			Class:   "provider",
			Message: "turn ended with reason \"error\"",
		}
	case "content-filter":
		// Model output was filtered — generation is definitively done.
		ev.EventType = "turn_done"
	default:
		// Unknown reason — conservatively treat as assistant_message.
		ev.EventType = "assistant_message"
	}
	return ev
}

// applyStepFinishTokens extracts tokens and cost from a step-finish event,
// regardless of reason, and — for every reason except "tool-calls" (a
// mid-turn pause) — builds a PerTurnContribution from it. OpenCode reports
// per-step tokens (not cumulative), so each non-tool-calls step-finish
// directly represents a billable turn (or a billable partial-step on
// interrupt / error / length).
func applyStepFinishTokens(raw map[string]interface{}, reason string, ev *tailer.ParsedEvent) {
	tokens, ok := raw["tokens"].(map[string]interface{})
	if !ok {
		return
	}
	snap := &tailer.TokenSnapshot{}
	if v, ok := tokens["input"].(float64); ok {
		snap.Input = int64(v)
	}
	if v, ok := tokens["output"].(float64); ok {
		snap.Output = int64(v)
	}
	if cache, ok := tokens["cache"].(map[string]interface{}); ok {
		if v, ok := cache["read"].(float64); ok {
			snap.CacheRead = int64(v)
		}
		if v, ok := cache["write"].(float64); ok {
			snap.CacheCreation = int64(v)
		}
	}
	if v, ok := tokens["total"].(float64); ok {
		snap.Total = int64(v)
	}
	ev.Tokens = snap

	if reason == "tool-calls" {
		return
	}
	usage := tailer.UsageBreakdown{
		Input:     snap.Input,
		Output:    snap.Output,
		CacheRead: snap.CacheRead,
		// OpenCode's cache.write maps to ephemeral cache creation.
		CacheCreation5m: snap.CacheCreation,
	}
	modelName, _ := raw["_model"].(string)
	if modelName != "" {
		ev.ModelName = modelName
	}
	cost := extractCost(raw)
	contrib := &tailer.PerTurnContribution{
		Model: modelName,
		Usage: usage,
	}
	if cost > 0 {
		contrib.ProviderCostUSD = &cost
	}
	ev.Contribution = contrib
}

// parseTextPart handles text parts — assistant text output during a turn.
func parseTextPart(raw map[string]interface{}, ev *tailer.ParsedEvent) *tailer.ParsedEvent {
	role, _ := raw["_role"].(string)
	if role == "user" {
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		if text, ok := raw["text"].(string); ok {
			ev.UserText = text // heuristic summary (#738)
		}
		return ev
	}
	// Assistant text part.
	ev.EventType = "assistant_message"
	if text, ok := raw["text"].(string); ok {
		// Scan the FULL part text for the task-estimate marker (issue #558)
		// before the display truncation below drops all but the last 200 runes.
		if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
			ev.TaskEstimate = est
		}
		if s := tailer.ScanTaskSummary(text, ev.Timestamp); s != nil {
			ev.TaskSummary = s
		}
		ev.AssistantText = tailer.TruncateAssistantText(text)
	}
	return ev
}

// parseToolPart handles tool parts — tool calls and their results.
// OpenCode updates a single part row as a tool progresses through
// pending → running → completed/error states.
//
// The watcher emits a new ParseLine call for each relevant state transition:
//   - status="pending" or "running" → open tool call → ToolUses
//   - status="completed" or "error" → tool result → ToolResultIDs
//
// `todowrite` additionally carries an authoritative snapshot of the session's
// todo list in state.input.todos; the snapshot is translated into TaskDeltas
// so the dashboard's task-progress dots populate the same way they do for
// Claude Code's TaskCreate/TaskUpdate tool calls. See issue #277.
func (p *Parser) parseToolPart(raw map[string]interface{}, ev *tailer.ParsedEvent) *tailer.ParsedEvent {
	state, _ := raw["state"].(map[string]interface{})
	if state == nil {
		ev.Skip = true
		return ev
	}

	status, _ := state["status"].(string)
	callID, _ := raw["callID"].(string)
	toolName, _ := raw["tool"].(string)

	switch status {
	case "pending", "running":
		ev.EventType = "function_call"
		if callID != "" || toolName != "" {
			ev.ToolUses = []tailer.ToolUse{{ID: callID, Name: toolName}}
		}
	case "completed":
		ev.EventType = "function_call_output"
		if callID != "" {
			ev.ToolResultIDs = []string{callID}
		}
	case "error":
		ev.EventType = "function_call_output"
		ev.IsError = true
		if callID != "" {
			ev.ToolResultIDs = []string{callID}
		}
	default:
		ev.Skip = true
		return ev
	}

	if toolName == "todowrite" {
		p.appendTodowriteDeltas(state, ev)
	}
	return ev
}

// appendTodowriteDeltas reads the todowrite snapshot from state.input.todos
// and (a) appends the minimal TaskCreate/TaskUpdate sequence that brings
// the accumulator in line with the snapshot, and (b) emits a TaskSnapshot
// listing every todo currently tracked by OpenCode for this call. The
// snapshot is what the tailer's reconcileTaskSnapshot consumes to prune
// entries that vanished from a later todowrite call and to honour status
// reversions (e.g. in_progress → pending) the Update path skips by design.
//
// Todos are keyed by their `content` field — OpenCode does not assign
// stable IDs, so two todos sharing the exact same content collapse into a
// single tracked task. Acceptable trade-off: OpenCode's own UI treats
// content as the user-visible label and never displays two todos with
// identical text differently. The duplicate is silent, not noisy.
func (p *Parser) appendTodowriteDeltas(state map[string]interface{}, ev *tailer.ParsedEvent) {
	input, _ := state["input"].(map[string]interface{})
	if input == nil {
		return
	}
	rawTodos, _ := input["todos"].([]interface{})
	todos := make([]tailer.Todo, 0, len(rawTodos))
	for _, raw := range rawTodos {
		todo, _ := raw.(map[string]interface{})
		if todo == nil {
			continue
		}
		content, _ := todo["content"].(string)
		status, _ := todo["status"].(string)
		todos = append(todos, tailer.Todo{Key: content, Status: status})
	}
	p.todos.Reconcile(todos, ev)
}

// extractCost reads the top-level "cost" field from a part data map.
func extractCost(raw map[string]interface{}) float64 {
	if v, ok := raw["cost"].(float64); ok {
		return v
	}
	return 0
}

// messageErrorFrom extracts the raw `error` value from a message row's JSON
// data column, for the live path to hand to the parser. Returns nil — never a
// typed nil — when the blob does not parse or carries no error, so a caller
// can test it against nil directly.
func messageErrorFrom(data string) interface{} {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil
	}
	if raw["error"] == nil {
		return nil
	}
	return raw["error"]
}

// sessionErrorFrom converts opencode's `error` value into a session-level
// failure, or nil when the value is not one.
//
// THE RECORDED SHAPE IS `{name, data:{message}}`, not `{name, message}` as
// this file's comment claimed until #1800. Every non-null `_error` in the
// corpus (4 of them, across two recordings) reads:
//
//	{"name":"UnknownError","data":{"message":"…"}}
//
// A reader written from the old comment would have looked up
// error["message"], found nothing, and shipped an error line with no text.
// Both spellings are accepted here because one of them is documented upstream
// and the other is what was actually observed, and neither costs anything.
//
// `name` is carried as Class rather than mapped: opencode's only recorded
// value is the entirely uninformative "UnknownError", so any mapping would be
// inventing a taxonomy from one sample. Class is free-form by #1798's design
// for exactly this reason — the vocabularies genuinely differ per agent.
//
// Phase is terminal: opencode writes the message-level error INSTEAD OF a
// terminal step-finish part, so it is the turn's last word and no further
// attempt follows. No retry ladder is recorded anywhere in the corpus.
//
// The shape rules are the fail-open ones isErrorMessage has always had: a
// non-empty string or a non-empty object counts; nil, an empty string, an
// empty object, and any other JSON type (bool/number/array) do not.
func sessionErrorFrom(v interface{}) *tailer.SessionError {
	switch e := v.(type) {
	case string:
		if e == "" {
			return nil
		}
		return &tailer.SessionError{
			Phase:   tailer.ErrorPhaseTerminal,
			Class:   "provider",
			Message: e,
		}
	case map[string]interface{}:
		if len(e) == 0 {
			return nil
		}
		name, _ := e["name"].(string)
		if name == "" {
			name = "provider"
		}
		return &tailer.SessionError{
			Phase:   tailer.ErrorPhaseTerminal,
			Class:   name,
			Message: opencodeErrorMessage(e),
		}
	default:
		// nil (absent/null), and any unexpected shape (bool/number/array) —
		// not a real error signal.
		return nil
	}
}

// opencodeErrorMessage digs the human text out of an error object, preferring
// the observed `data.message` over the upstream-documented top-level
// `message`. Returns "" when neither is present, which is honest: an error
// with no text is better than a fabricated one.
func opencodeErrorMessage(e map[string]interface{}) string {
	if data, ok := e["data"].(map[string]interface{}); ok {
		if msg, _ := data["message"].(string); msg != "" {
			return msg
		}
	}
	msg, _ := e["message"].(string)
	return msg
}
