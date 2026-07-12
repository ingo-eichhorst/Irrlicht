package vibe

import (
	"encoding/json"
	"strings"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for Mistral Vibe transcripts.
// Each messages.jsonl line is one message sharing the envelope
// {role, content, message_id, injected} plus role-specific fields:
//
//   - role "user" — a prompt. Opens a turn. `injected:true` marks Vibe's own
//     context injections (e.g. a `!`-shell escape result fed back as context);
//     both count as user activity, so both map to user_message.
//   - role "assistant" with a non-empty tool_calls[] — the model is invoking
//     tools; the turn continues (working). Tool calls use the OpenAI shape:
//     each carries an `id` and a nested `function.name` (NOT a flat `name`).
//     Such a message usually has no content.
//   - role "assistant" with NO tool_calls — the turn's terminal message: the
//     model's answer text (with optional reasoning_content). Settles to
//     turn_done.
//   - role "tool" — a tool result, linked to its call by `tool_call_id`.
//
// The JSONL carries no timestamp, cwd, model, or usage. Timestamps fall back
// to tailer.ParseTimestamp (→ time.Now for live tailing; scan time on
// backfill). cwd, model, and context tokens come from the sibling meta.json
// sidecar, read via the path injected through tailer.TranscriptPathAware.
type Parser struct {
	// path is the transcript being parsed, injected by the tailer at
	// construction. Empty in path-less tests — sidecar enrichment is skipped.
	path string
	// sidecar memoizes the last meta.json read by (mtime, size).
	sidecar sidecarCache
	// todos folds vibe's whole-list `todo` tool snapshots into task-progress
	// deltas; one reconciler per transcript scan.
	todos tailer.TodoReconciler
	// lastPromptTokens / lastCompletionTokens are the session-cumulative token
	// counts already emitted as contributions, so each turn contributes only its
	// delta exactly once (dedup across re-reads of the same static sidecar).
	lastPromptTokens     int64
	lastCompletionTokens int64
}

// SetTranscriptPath implements tailer.TranscriptPathAware: the tailer injects
// the transcript path so the parser can locate the sibling meta.json sidecar.
func (p *Parser) SetTranscriptPath(path string) { p.path = path }

// ParseLine normalizes one Vibe transcript line into a ParsedEvent. Each role
// carries its own well-defined shape, so the per-role logic is delegated to a
// dedicated parseXMessage function instead of growing one large switch body.
func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	role, _ := raw["role"].(string)
	if role == "" {
		return &tailer.ParsedEvent{Skip: true}
	}
	ev := &tailer.ParsedEvent{Timestamp: tailer.ParseTimestamp(raw)}

	switch role {
	case "user":
		parseUserMessage(raw, ev)
	case "assistant":
		p.parseAssistantMessage(raw, ev)
	case "tool":
		parseToolResultMessage(raw, ev)
	default:
		ev.Skip = true
		return ev
	}
	if ev.Skip {
		return ev
	}

	p.applySidecar(ev)
	return ev
}

// parseUserMessage handles role:"user" lines.
//
// Vibe writes the result of a `!`-shell escape as an injected:true user
// message ("Manual `!` command result from the user. Use this as context
// only. …"). It is context for the NEXT real turn, not a user turn of its
// own: treating it as activity flips the session to working with no
// turn_done to ever close it, so the session sticks in working after the
// shell command returns. Skip it — the real prompt that follows
// (injected:false) drives the turn. Injected user messages are ALWAYS the
// shell-escape wrapper (verified across live 2.19.0 transcripts).
func parseUserMessage(raw map[string]any, ev *tailer.ParsedEvent) {
	if injected, _ := raw["injected"].(bool); injected {
		ev.Skip = true
		return
	}
	ev.EventType = "user_message"
	ev.ClearToolNames = true
	if c, _ := raw["content"].(string); c != "" {
		ev.UserText = strings.TrimSpace(c)
	}
}

// parseAssistantMessage handles role:"assistant" lines: tool_calls[] means
// the turn continues (working); no tool_calls means this is the turn's
// terminal message (turn_done).
func (p *Parser) parseAssistantMessage(raw map[string]any, ev *tailer.ParsedEvent) {
	if content, _ := raw["content"].(string); strings.TrimSpace(content) != "" {
		ev.AssistantText = tailer.TruncateAssistantText(content)
		if est := tailer.ScanTaskEstimate(content, ev.Timestamp); est != nil {
			ev.TaskEstimate = est
		}
		if s := tailer.ScanTaskSummary(content, ev.Timestamp); s != nil {
			ev.TaskSummary = s
		}
	}
	if toolUses := parseToolCalls(raw); len(toolUses) > 0 {
		// Mid-turn: the model is invoking tools and will produce more
		// events; keep the session working.
		ev.EventType = "assistant_message"
		ev.ToolUses = toolUses
		p.appendTodoDeltas(raw, ev)
	} else {
		// No tool calls — this is the terminal message of the turn.
		ev.EventType = "turn_done"
	}
}

// parseToolResultMessage handles role:"tool" lines: a tool result linked to
// its call by tool_call_id.
func parseToolResultMessage(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "tool_result"
	if id, _ := raw["tool_call_id"].(string); id != "" {
		ev.ToolResultIDs = []string{id}
	}
}

// applySidecar enriches an event with cwd + model on every event (so cwd lands
// early for PID binding and the model stays lit between turns), and the
// context-token count on turn_done (for the context-utilization bar). The
// transcript carries none of these; without the sidecar the session would have
// no project label, model, or context bar. A missing sidecar leaves the event
// unchanged.
func (p *Parser) applySidecar(ev *tailer.ParsedEvent) {
	if p.path == "" {
		return
	}
	st := readSidecar(p.path, &p.sidecar)
	if st == nil {
		return
	}
	if st.cwd != "" {
		ev.CWD = st.cwd
	}
	if st.model != "" {
		ev.ModelName = tailer.NormalizeModelName(st.model)
	}
	if ev.EventType == "turn_done" {
		if st.contextTokens > 0 {
			ev.Tokens = &tailer.TokenSnapshot{Total: st.contextTokens}
		}
		if st.contextWindow > 0 {
			ev.ContextWindow = st.contextWindow
		}
		p.emitContribution(st, ev)
	}
}

// emitContribution attaches the turn's usage as the DELTA of the session-
// cumulative token counts since the last emit. The sidecar retains only
// cumulative totals (not per-turn history), so the delta is the only
// backfill-safe unit: live-tail sees each turn's real delta; a backfill of a
// finished transcript emits the whole session's cumulative once (on the first
// turn_done) and nothing thereafter — the session TOTAL is correct either way,
// only the per-turn split is lost on backfill (the data isn't retained to
// reconstruct it). Cost is left to the capacity price map keyed on Model
// (ProviderCostUSD is not set — it would short-circuit token accumulation).
func (p *Parser) emitContribution(st *sidecarState, ev *tailer.ParsedEvent) {
	dPrompt := st.sessionPromptTokens - p.lastPromptTokens
	dCompletion := st.sessionCompletionTokens - p.lastCompletionTokens
	if dPrompt < 0 {
		dPrompt = 0
	}
	if dCompletion < 0 {
		dCompletion = 0
	}
	if dPrompt == 0 && dCompletion == 0 {
		return
	}
	p.lastPromptTokens = st.sessionPromptTokens
	p.lastCompletionTokens = st.sessionCompletionTokens
	ev.Contribution = &tailer.PerTurnContribution{
		Model: tailer.NormalizeModelName(st.model),
		Usage: tailer.UsageBreakdown{Input: dPrompt, Output: dCompletion},
	}
}

// appendTodoDeltas translates vibe's builtin `todo` tool into task-progress
// deltas so its checklist surfaces in the session `tasks` field the same way
// claudecode's TodoWrite and opencode's todowrite do. Vibe's todo tool is a
// whole-list-replace: an assistant tool_call `todo` with
// arguments={"action":"write","todos":[{"id","content","status","priority"}]}
// carries the FULL list every call. Todos are keyed by their visible content
// (matching the shared TodoReconciler convention); `cancelled` todos are dropped,
// mirroring vibe's own UI which excludes them from the plan. Non-`write` actions
// (e.g. a read/list) carry no state change and are ignored.
func (p *Parser) appendTodoDeltas(raw map[string]any, ev *tailer.ParsedEvent) {
	tcs, _ := raw["tool_calls"].([]any)
	for _, t := range tcs {
		tc, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tc["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, _ := fn["name"].(string); name != "todo" {
			continue
		}

		// arguments is a JSON string in practice; tolerate a decoded object too.
		var argsBytes []byte
		switch a := fn["arguments"].(type) {
		case string:
			argsBytes = []byte(a)
		case map[string]any:
			argsBytes, _ = json.Marshal(a)
		default:
			continue
		}
		var args struct {
			Action string `json:"action"`
			Todos  []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		if json.Unmarshal(argsBytes, &args) != nil || args.Action != "write" {
			continue
		}

		todos := make([]tailer.Todo, 0, len(args.Todos))
		for _, td := range args.Todos {
			if td.Content == "" || td.Status == "cancelled" {
				continue
			}
			todos = append(todos, tailer.Todo{Key: td.Content, Status: td.Status})
		}
		p.todos.Reconcile(todos, ev)
	}
}

// parseToolCalls extracts the tool invocations from an assistant message.
// Vibe uses the OpenAI tool-call shape: tool_calls[] each with an `id` and a
// nested `function.name`. A flat `name` is tolerated as a fallback in case a
// future Vibe release flattens the shape. Calls with no resolvable name are
// dropped.
func parseToolCalls(raw map[string]any) []tailer.ToolUse {
	tcs, _ := raw["tool_calls"].([]any)
	if len(tcs) == 0 {
		return nil
	}
	out := make([]tailer.ToolUse, 0, len(tcs))
	for _, t := range tcs {
		tc, ok := t.(map[string]any)
		if !ok {
			continue
		}
		id, _ := tc["id"].(string)
		name := ""
		if fn, ok := tc["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			name, _ = tc["name"].(string)
		}
		if name != "" {
			out = append(out, tailer.ToolUse{ID: id, Name: name})
		}
	}
	return out
}
