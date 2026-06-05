package kirocli

import (
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
// The JSONL carries no model/token/cost fields; those live in the <uuid>.json
// metadata sidecar, which this parser does not read.
type Parser struct{}

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
				}
			case "text":
				if text, ok := block["data"].(string); ok && text != "" {
					lastText = text
					if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
						ev.TaskEstimate = est
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
		}
		if runes := []rune(lastText); len(runes) > 200 {
			lastText = string(runes[:200])
		}
		ev.AssistantText = lastText

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
		ev.Skip = true
		return ev

	default:
		ev.Skip = true
		return ev
	}

	return ev
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
