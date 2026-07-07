package kirocli

import (
	"strconv"
	"time"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for Kiro CLI transcripts.
// Kiro wraps every event in a versioned envelope with the payload under
// "data" and the message parts under "data.content[]":
//
//	{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"hi"}],"meta":{"timestamp":1780612717}}}
//	{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":""},{"kind":"toolUse","data":{...}}]}}
//	{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"...","status":"success"}}]}}
//
// There is no explicit end-of-turn marker: mid-turn assistant messages
// carry toolUse blocks, and the FINAL assistant message of a turn is
// text-only — so a text-only AssistantMessage maps to turn_done (verified
// against live 2.5.1 sessions, .build/refresh/kiro-cli-smoke/FINDINGS.md).
// The JSONL carries no model/token/cost fields; model + context utilization
// live in the <uuid>.json metadata sidecar, which the parser reads once per
// turn_done via the path injected through tailer.TranscriptPathAware (#599).
type Parser struct {
	// path is the transcript being parsed, injected by the tailer at
	// construction. Empty when the runtime predates the injection (or in
	// path-less tests) — sidecar enrichment is then skipped.
	path string
	// sidecar memoizes the last model-state read by (mtime, size) so a
	// backfill of a long transcript scans the static sidecar once, not once
	// per historical turn_done.
	sidecar sidecarCache
	// nextTaskID mirrors the tailer's monotonic taskSeq so the Update deltas
	// emitted on `complete` carry the same IDs the tailer assigns at Create
	// time. todoIDByKiroID maps kiro's own todo id (the string id the model
	// passes back in completed_task_ids) to that synthetic ID. Lazily
	// initialized; one Parser instance per transcript scan.
	nextTaskID     int
	todoIDByKiroID map[string]string
}

// SetTranscriptPath implements tailer.TranscriptPathAware.
func (p *Parser) SetTranscriptPath(path string) { p.path = path }

// ParseLine parses a Kiro CLI JSONL line into a normalized ParsedEvent.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{
		Timestamp: parseKiroTimestamp(raw),
	}

	kind, _ := raw["kind"].(string)
	if kind == "" {
		ev.Skip = true
		return ev
	}

	data, _ := raw["data"].(map[string]interface{})
	content := dataContent(data)

	switch kind {
	case "Prompt":
		ev.EventType = "user_message"
		ev.ClearToolNames = true

	case "AssistantMessage":
		var toolUses []tailer.ToolUse
		var lastText string
		for _, item := range content {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch block["kind"] {
			case "toolUse":
				if d, ok := block["data"].(map[string]interface{}); ok {
					id, _ := d["toolUseId"].(string)
					name, _ := d["name"].(string)
					if name != "" {
						toolUses = append(toolUses, tailer.ToolUse{ID: id, Name: name})
					}
					if name == "todo_list" {
						input, _ := d["input"].(map[string]interface{})
						p.appendTodoListDeltas(input, ev)
					}
				}
			case "text":
				if text, ok := block["data"].(string); ok && text != "" {
					lastText = text
					if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
						ev.TaskEstimate = est
					}
					if s := tailer.ScanTaskSummary(text, ev.Timestamp); s != nil {
						ev.TaskSummary = s
					}
				}
			}
		}
		if len(toolUses) > 0 {
			// Mid-turn: the model is invoking tools and will produce more
			// events; keep the session working.
			ev.EventType = "assistant"
			ev.ToolUses = toolUses
		} else {
			// Text-only assistant message = end of the user turn.
			ev.EventType = "turn_done"
			p.applySidecarMetrics(ev)
		}
		ev.AssistantText = tailer.TruncateAssistantText(lastText)

	case "ToolResults":
		ev.EventType = "tool_result"
		for _, item := range content {
			block, ok := item.(map[string]interface{})
			if !ok || block["kind"] != "toolResult" {
				continue
			}
			d, ok := block["data"].(map[string]interface{})
			if !ok {
				continue
			}
			if id, _ := d["toolUseId"].(string); id != "" {
				ev.ToolResultIDs = append(ev.ToolResultIDs, id)
			}
			// status is the tool HARNESS verdict, not the command's: a shell
			// command exiting non-zero is still status:"success" (the exit
			// code is payload data), while tool-input validation failures AND
			// user-cancelled tools (Esc) are both status:"error" — kiro has
			// no separate cancelled status (live-probed on 2.6.0, #592
			// finding 3). A cancellation is distinguishable only via
			// data.results[id].result == "Cancelled", should that ever
			// matter.
			if status, _ := d["status"].(string); status != "" && status != "success" {
				ev.IsError = true
			}
		}

	case "Clear":
		// /clear continues in the SAME session file (no new UUID); the
		// marker itself carries no state transition.
		//
		// godre:S1871 — same body as default below, kept as its own case
		// deliberately: this documents "Clear" as a recognized, understood
		// marker kind (as opposed to default's true catch-all for kinds this
		// parser doesn't know about), so default can later change — e.g. to
		// log or count unrecognized kinds — without silently changing
		// behavior for this one.
		ev.Skip = true
		return ev

	default:
		ev.Skip = true
		return ev
	}

	return ev
}

// applySidecarMetrics fills model and context-utilization metadata from the
// <uuid>.json sidecar — the JSONL itself carries none (#599). Reading once
// per turn_done bounds the cost at one sidecar scan per completed turn. If
// kiro flushes the sidecar after the final AssistantMessage, the values lag
// one turn and self-correct on the next.
func (p *Parser) applySidecarMetrics(ev *tailer.ParsedEvent) {
	if p.path == "" {
		return
	}
	state := readSidecarModelState(p.path, &p.sidecar)
	if state == nil || state.ModelInfo.ModelID == "" {
		return
	}
	// "auto" (no pinned model) passes through NormalizeModelName unchanged —
	// matching what kiro's own TUI footer shows.
	ev.ModelName = tailer.NormalizeModelName(state.ModelInfo.ModelID)
	if w := state.ModelInfo.ContextWindowTokens; w > 0 {
		ev.ContextWindow = w
		if pct := state.ContextUsagePercentage; pct > 0 {
			// The sidecar reports context fill as a PERCENTAGE (raw
			// input/output_token_count are zero in kiro 2.5/2.6). Derive the
			// token total so ComputeContextUtilization (total/window)
			// reproduces kiro's own number exactly.
			ev.Tokens = &tailer.TokenSnapshot{Total: int64(pct / 100 * float64(w))}
		}
	}
}

// appendTodoListDeltas synthesizes TaskCreate/TaskUpdate deltas from a kiro
// `todo_list` toolUse input, so kiro's built-in checklist surfaces in the
// session `tasks` field the same way opencode's `todowrite` does (#589).
//
// kiro's model is create→complete (NOT a per-item status array): a `create`
// command carries the full `tasks[]` (each `{task_description}`); subsequent
// `complete` commands carry `completed_task_ids[]` (the kiro todo ids the
// model passes back as strings). kiro never surfaces an intermediate
// in_progress state, so items go straight pending → completed.
//
// kiro assigns each created task an id; the ids in the live transcript are the
// 1-based create order ("1","2","3"). We map each kiro id to the synthetic ID
// the tailer will assign at Create time (nextTaskID, mirroring its taskSeq) so
// the complete-time Update deltas target the right task regardless of kiro's
// id scheme.
func (p *Parser) appendTodoListDeltas(input map[string]interface{}, ev *tailer.ParsedEvent) {
	if input == nil {
		return
	}
	switch cmd, _ := input["command"].(string); cmd {
	case "create":
		tasks, _ := input["tasks"].([]interface{})
		for i, raw := range tasks {
			task, _ := raw.(map[string]interface{})
			desc, _ := task["task_description"].(string)
			if desc == "" {
				continue
			}
			p.nextTaskID++
			if p.todoIDByKiroID == nil {
				p.todoIDByKiroID = make(map[string]string)
			}
			// kiro ids the created tasks 1-based in create order; record both
			// the create-order key and the synthetic id so complete lookups
			// resolve whichever form kiro echoes back.
			p.todoIDByKiroID[strconv.Itoa(i+1)] = strconv.Itoa(p.nextTaskID)
			ev.TaskDeltas = append(ev.TaskDeltas, tailer.TaskDelta{
				Op:      tailer.TaskOpCreate,
				Subject: desc,
			})
		}
	case "complete":
		ids, _ := input["completed_task_ids"].([]interface{})
		for _, raw := range ids {
			kiroID, _ := raw.(string)
			id, ok := p.todoIDByKiroID[kiroID]
			if !ok {
				continue
			}
			ev.TaskDeltas = append(ev.TaskDeltas, tailer.TaskDelta{
				Op:     tailer.TaskOpUpdate,
				ID:     id,
				Status: tailer.TaskStatusCompleted,
			})
		}
	}
}

// parseKiroTimestamp reads data.meta.timestamp (epoch seconds — present on
// Prompt events only), falling back to tailer.ParseTimestamp for any
// top-level timestamp shape, which itself falls back to time.Now().
//
// The fallback means non-Prompt events are stamped with parse time: accurate
// while tailing live, but on BACKFILL of a pre-existing transcript every
// AssistantMessage/ToolResults event lands at scan time — a rescued session's
// LastMessageAt reads as "just now" rather than its real last activity.
// SessionStartAt stays correct (the first Prompt carries a real timestamp).
// Nothing better is available in the JSONL (#592, finding 2).
func parseKiroTimestamp(raw map[string]interface{}) time.Time {
	if data, ok := raw["data"].(map[string]interface{}); ok {
		if meta, ok := data["meta"].(map[string]interface{}); ok {
			if ts, ok := meta["timestamp"].(float64); ok && ts > 0 {
				return time.Unix(int64(ts), 0)
			}
		}
	}
	return tailer.ParseTimestamp(raw)
}

// dataContent returns data.content as a slice, tolerating absent fields.
func dataContent(data map[string]interface{}) []interface{} {
	if data == nil {
		return nil
	}
	content, _ := data["content"].([]interface{})
	return content
}
