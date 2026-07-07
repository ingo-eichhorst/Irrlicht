package vibe

import (
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
}

// SetTranscriptPath implements tailer.TranscriptPathAware: the tailer injects
// the transcript path so the parser can locate the sibling meta.json sidecar.
func (p *Parser) SetTranscriptPath(path string) { p.path = path }

// ParseLine normalizes one Vibe transcript line into a ParsedEvent.
func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	role, _ := raw["role"].(string)
	if role == "" {
		return &tailer.ParsedEvent{Skip: true}
	}
	ev := &tailer.ParsedEvent{Timestamp: tailer.ParseTimestamp(raw)}

	switch role {
	case "user":
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		if c, _ := raw["content"].(string); c != "" {
			ev.UserText = strings.TrimSpace(c)
		}

	case "assistant":
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
		} else {
			// No tool calls — this is the terminal message of the turn.
			ev.EventType = "turn_done"
		}

	case "tool":
		ev.EventType = "tool_result"
		if id, _ := raw["tool_call_id"].(string); id != "" {
			ev.ToolResultIDs = []string{id}
		}

	default:
		ev.Skip = true
		return ev
	}

	p.applySidecar(ev)
	return ev
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
	if ev.EventType == "turn_done" && st.contextTokens > 0 {
		ev.Tokens = &tailer.TokenSnapshot{Total: st.contextTokens}
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
