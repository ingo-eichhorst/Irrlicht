package claudecode

import (
	"regexp"
	"strings"

	"irrlicht/core/pkg/tailer"
	"irrlicht/core/pkg/transcript"
)

// eventTypeAssistantStreaming is emitted for intermediate Claude Code assistant
// messages (thinking blocks, partial text) that should not trigger IsAgentDone().
const eventTypeAssistantStreaming = "assistant_streaming"

// xmlFieldTaskID is the <task-id> XML field name background-task markers
// carry in prompt/tool-result text.
const xmlFieldTaskID = "task-id"

// backgroundSpawnRe matches the text Claude Code writes in a `Bash`
// tool_result when the command was launched with `run_in_background: true`:
//
//	Command running in background with ID: bc1h56v8v. Output is being written to: /private/tmp/.../tasks/bc1h56v8v.output. You will be notified when it completes. To check interim output, use Read on that file path.
//
// Group 1 is the background id; group 2 is the output-file path. The
// background id and output path are read from the *result* because the Bash
// tool_use input carries only the run_in_background flag — the id is assigned
// at launch and reported back here. See issue #445.
//
// The path is a single whitespace-delimited token, but Claude writes it
// mid-sentence ("…output. You will be notified…"), so `(\S+)` captures the
// sentence-ending period as a trailing ".". collectToolResult strips that one
// trailing period; leaving it in yields a non-existent "…output." path on which
// the daemon's lsof liveness probe finds no writer, wrongly settling a
// still-running background session to `ready`. Matching the whole token (rather
// than anchoring on a `.output` suffix) keeps detection working even if Claude
// ever names the file differently.
var backgroundSpawnRe = regexp.MustCompile(`running in background with ID: (\S+?)\.\s+Output is being written to:\s+(\S+)`)

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
	// The Claude Code CLI stamps its own version on every user/assistant line
	// (e.g. "2.1.186"). Read it before the early-return paths so it is captured
	// even from system/user events. The tailer keeps the first non-empty value.
	if v, ok := raw["version"].(string); ok {
		ev.AgentVersion = v
	}

	eventType := resolveEventType(raw)

	if handleEarlyReturn(eventType, raw, ev) {
		return ev
	}

	ev.ModelName, ev.ContextWindow = extractClaudeCodeModel(raw)
	ev.Tokens = extractClaudeCodeTokens(raw)
	p.applyRequestIDContribution(raw, ev)

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
	case "attachment":
		handleAttachmentEvent(raw, ev)
		return true
	}
	return false
}

// handleAttachmentEvent extracts task_reminder snapshots into ev.TaskSnapshot
// and skips the event from the message-content pipeline. Other attachment
// kinds are ignored. The reminder is Claude Code's authoritative view of which
// task IDs it's currently tracking; the tailer uses it to reconcile drift
// from stale TaskUpdate deltas that never get a `completed` follow-up
// (issue #282).
func handleAttachmentEvent(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	ev.Skip = true
	att, ok := raw["attachment"].(map[string]interface{})
	if !ok {
		return
	}
	kind, _ := att["type"].(string)
	cmdMode, _ := att["commandMode"].(string)
	// A queued_command "task-notification" attachment carries a
	// <task-notification> XML blob — the headless-shape sibling of the
	// origin.kind task-notification in handleTaskNotification. A terminal one
	// ends a tracked Bash background process by its <task-id>. See issue #445.
	if kind == "queued_command" || cmdMode == "task-notification" {
		if prompt, _ := att["prompt"].(string); prompt != "" {
			if id := extractXMLField(prompt, xmlFieldTaskID); id != "" && backgroundStatusTerminated(prompt) {
				ev.TerminatedBackgroundTaskIDs = append(ev.TerminatedBackgroundTaskIDs, id)
			}
		}
		return
	}
	if kind != "task_reminder" {
		return
	}
	contentArr, ok := att["content"].([]interface{})
	if !ok {
		// Defensive: an absent content field is not a snapshot. An explicit
		// `content: []` decodes to a non-nil empty slice and DOES count
		// (Claude is telling us nothing is active).
		return
	}
	snap := make([]tailer.TaskSnapshotEntry, 0, len(contentArr))
	for _, item := range contentArr {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := entry["id"].(string)
		if id == "" {
			continue
		}
		subject, _ := entry["subject"].(string)
		activeForm, _ := entry["activeForm"].(string)
		status, _ := entry["status"].(string)
		snap = append(snap, tailer.TaskSnapshotEntry{
			ID:         id,
			Subject:    subject,
			ActiveForm: activeForm,
			Status:     status,
		})
	}
	ev.TaskSnapshot = &snap
}

// handleSystemEvent maps turn_duration / stop_hook_summary and the manual
// compact_boundary subtypes to turn_done; everything else is skipped.
//
// The skip branch covers purely informational subtypes that Claude Code
// writes after a turn has already ended — most notably `away_summary`,
// the idle recap emitted ~3 minutes after end_turn. These must NOT be
// promoted to turn_done: they describe what happened, they aren't a turn
// completion themselves. The tailer's per-pass NoSubstantiveActivity flag
// then lets the detector ignore the resulting mtime touch (issue #329).
//
// A manual /compact is the exception: its compact_boundary replaces the
// context and definitively ends the prior turn — even one stranded mid
// tool-use with no turn_done of its own. Promoting it to turn_done sweeps any
// lingering open tool call so the session releases working → ready (#656), and
// flags IsManualCompactBoundary so the detector can clear its force-working
// hold (#657). Auto-compaction fires mid-turn and continues, so it stays
// skipped — promoting it would emit a spurious ready-blip.
func handleSystemEvent(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	subtype, _ := raw["subtype"].(string)
	if subtype == "turn_duration" || subtype == "stop_hook_summary" {
		ev.EventType = "turn_done"
		return
	}
	if subtype == "compact_boundary" {
		if meta, ok := raw["compactMetadata"].(map[string]interface{}); ok {
			if trigger, _ := meta["trigger"].(string); trigger == "manual" {
				ev.EventType = "turn_done"
				ev.IsManualCompactBoundary = true
				return
			}
		}
	}
	ev.Skip = true
}

// handleUserEvent handles the user event variants that should short-circuit
// the parser (isMeta, compact summary, task-notification, local-command
// wrappers). Returns true when caller should return ev immediately.
// Interrupts are recorded on ev but do NOT short-circuit.
func handleUserEvent(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	if isMeta, ok := raw["isMeta"].(bool); ok && isMeta {
		ev.Skip = true
		return true
	}
	// The synthetic continuation summary written when the conversation is
	// compacted ("This session is being continued from a previous
	// conversation…") is not a real user turn. A manual /compact writes it
	// with no assistant event or turn_done following, so letting it through
	// flipped ready → working with nothing to transition back (issue #641).
	if isCompact, ok := raw["isCompactSummary"].(bool); ok && isCompact {
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
				AgentID:   extractXMLField(content, xmlFieldTaskID),
				ToolUseID: extractXMLField(content, "tool-use-id"),
				Status:    extractXMLField(content, "status"),
			})
			// A terminal task-notification also ends a Bash background process
			// when its <task-id> matches a tracked backgroundTaskId — the
			// completion path orchestrated/SDK claude uses instead of a
			// BashOutput poll. Dropping a non-matching id (a subagent's) is a
			// harmless no-op in the tailer. See issue #445.
			if id := extractXMLField(content, xmlFieldTaskID); id != "" && backgroundStatusTerminated(content) {
				ev.TerminatedBackgroundTaskIDs = append(ev.TerminatedBackgroundTaskIDs, id)
			}
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
	// Claude Code's authoritative marker that this tool_result is a Bash
	// run_in_background launch: a sibling `toolUseResult.backgroundTaskId`.
	// Gating spawn detection on this (rather than regexing arbitrary result
	// prose) prevents a foreground tool that merely echoes "running in
	// background with ID: …" from creating a phantom background process.
	// See issue #445.
	bgTaskID := backgroundTaskIDOf(raw)
	createdTaskID := createdTaskIDOf(raw)
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
			// The task-summary marker rides in the Bash tool's `description`
			// (a tool_use input field), not in assistant prose — issue #617's
			// mandatory-carrier instruction, since pre-tool-call text blocks
			// can vanish. Scan the input so the summary is parsed from the
			// transcript and survives replay, mirroring the hook path's
			// scanToolInput. Assistant events only (a user echoing a marker
			// must not feed it). ETA stays prose/hook-only, so apply only the
			// summary.
			if isAssistantEventType(ev.EventType) {
				if _, s, q := scanValueForMarkers(block["input"], ev.Timestamp); s != nil || q != nil {
					if s != nil {
						ev.TaskSummary = s
					}
					if q != nil {
						ev.TaskQuestion = q
					}
				}
			}
		case "tool_result":
			collectToolResult(block, ev, bgTaskID, createdTaskID)
		case "text":
			// Task-estimate markers live in the agent's own prose, so only
			// assistant events qualify (a user pasting a marker must not feed
			// the ETA). Scan the full block text — ev.AssistantText is
			// tail-truncated to 200 runes and would lose early markers.
			if !isAssistantEventType(ev.EventType) {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
					ev.TaskEstimate = est
				}
				if s := tailer.ScanTaskSummary(text, ev.Timestamp); s != nil {
					ev.TaskSummary = s
				}
				// The question marker rides end-of-turn prose (the agent's final
				// line when it asks the user something), which survives the
				// text-drop, so the text-block scan is its primary path (#759).
				if q := tailer.ScanTaskQuestion(text, ev.Timestamp); q != nil {
					ev.TaskQuestion = q
				}
			}
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
			// The authoritative task ID only exists in the tool_result
			// (`toolUseResult.task.id`); carry the tool_use id so the tailer
			// can pair the later assign_id delta with this create. See #615.
			ToolUseID: id,
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
	case "BashOutput":
		// The agent is polling a background process by id; remember the
		// pairing so a terminated status on the matching tool_result can be
		// attributed to the right background process. See issue #445.
		if bashID := backgroundID(input); bashID != "" && id != "" {
			ev.BashOutputPolls = append(ev.BashOutputPolls, tailer.BashOutputPoll{
				ToolUseID: id,
				BashID:    bashID,
			})
		}
	case "KillShell":
		// Explicit, single-event termination of a background process.
		if bashID := backgroundID(input); bashID != "" {
			ev.KilledShellIDs = append(ev.KilledShellIDs, bashID)
		}
	}
	return ""
}

// backgroundID reads the background-process id from a BashOutput / KillShell
// tool input. Claude Code names the field `bash_id` on BashOutput and
// `shell_id` on KillShell; accept either so one helper covers both.
func backgroundID(input map[string]interface{}) string {
	if v, _ := input["bash_id"].(string); v != "" {
		return v
	}
	if v, _ := input["shell_id"].(string); v != "" {
		return v
	}
	return ""
}

// collectToolResult records a tool_result's matching id and the is_error flag,
// and mines the result text for background-process signals: a Bash
// run_in_background launch (spawn) and a BashOutput poll reporting a
// terminated status. bgTaskID is the event's structured
// `toolUseResult.backgroundTaskId` ("" when absent) — a spawn is recorded only
// when it is present, so arbitrary tool output echoing the launch phrase can't
// fabricate a background process. See issue #445.
// createdTaskID is the event's structured `toolUseResult.task.id` ("" when
// absent) — the authoritative ID of a task created by the matching TaskCreate
// tool_use; forwarded to the tailer as an assign_id delta. See issue #615.
func collectToolResult(block map[string]interface{}, ev *tailer.ParsedEvent, bgTaskID, createdTaskID string) {
	toolUseID, _ := block["tool_use_id"].(string)
	if toolUseID != "" {
		ev.ToolResultIDs = append(ev.ToolResultIDs, toolUseID)
	}
	if isErr, ok := block["is_error"].(bool); ok && isErr {
		ev.IsError = true
	}
	if createdTaskID != "" && toolUseID != "" {
		ev.TaskDeltas = append(ev.TaskDeltas, tailer.TaskDelta{
			Op:        tailer.TaskOpAssignID,
			ID:        createdTaskID,
			ToolUseID: toolUseID,
		})
	}

	text := toolResultText(block)
	if text == "" {
		return
	}
	// A real Bash run_in_background launch (gated on the structured
	// backgroundTaskId). The output path comes from the result text; we record
	// the spawn only when both the id and a path are known, so the
	// transcript-derived count never includes a process the daemon can't probe.
	if bgTaskID != "" {
		if m := backgroundSpawnRe.FindStringSubmatch(text); m != nil {
			ev.BackgroundSpawns = append(ev.BackgroundSpawns, tailer.BackgroundSpawn{
				BashID: bgTaskID,
				// Strip the sentence-ending period the launch text places after
				// the path ("…output. You will be notified…") so the recorded
				// path is the real file the lsof liveness probe must check.
				OutputPath: strings.TrimSuffix(m[2], "."),
			})
		}
	}
	// A BashOutput poll reports the process status. Any status other than
	// "running" (e.g. completed / killed / failed) means the background
	// process has terminated; the tailer attributes it to the polled id (and
	// acts only when that id is a tracked poll, so a stray <status> elsewhere
	// is a no-op).
	if toolUseID != "" && backgroundStatusTerminated(text) {
		ev.TerminatedBashOutputIDs = append(ev.TerminatedBashOutputIDs, toolUseID)
	}
}

// backgroundTaskIDOf returns the structured `toolUseResult.backgroundTaskId`
// from a Claude Code event, or "" when absent. This top-level field (sibling
// to `message`) is Claude Code's authoritative marker that a Bash tool_result
// was a run_in_background launch. See issue #445.
func backgroundTaskIDOf(raw map[string]interface{}) string {
	tur, ok := raw["toolUseResult"].(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := tur["backgroundTaskId"].(string)
	return id
}

// createdTaskIDOf returns the structured `toolUseResult.task.id` from a Claude
// Code event, or "" when absent. This top-level field is Claude Code's
// authoritative record of the ID assigned by a TaskCreate — the tool_use input
// carries no ID, and reconstructing it by counting creates desyncs whenever a
// tailer starts mid-session (issue #615).
func createdTaskIDOf(raw map[string]interface{}) string {
	tur, ok := raw["toolUseResult"].(map[string]interface{})
	if !ok {
		return ""
	}
	task, ok := tur["task"].(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := task["id"].(string)
	return id
}

// toolResultText flattens a tool_result's content into plain text. Claude Code
// writes the content either as a bare string or as an array of
// {type:"text", text:…} blocks.
func toolResultText(block map[string]interface{}) string {
	switch c := block["content"].(type) {
	case string:
		return c
	case []interface{}:
		var parts []string
		for _, item := range c {
			if b, ok := item.(map[string]interface{}); ok {
				if t, _ := b["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// backgroundStatusTerminated reports whether a BashOutput tool_result's text
// indicates the background process is no longer running. Claude Code surfaces
// the process state in a `<status>…</status>` field; only "running" means
// alive, so any other present value is treated as terminated. Matching on
// "not running" rather than a specific terminal word keeps this robust to the
// exact wording (completed / killed / failed). See issue #445.
func backgroundStatusTerminated(text string) bool {
	status := strings.ToLower(strings.TrimSpace(extractXMLField(text, "status")))
	return status != "" && status != "running"
}

// isAssistantEventType reports whether eventType is one of the assistant
// message flavours (matching applyAssistantText's assistant case).
func isAssistantEventType(eventType string) bool {
	switch eventType {
	case "assistant", eventTypeAssistantStreaming, "assistant_message", "assistant_output":
		return true
	}
	return false
}

// applyAssistantText fills AssistantText for assistant/streaming events,
// falling back to the tail of an AskUserQuestion prompt when the message
// carries no text block. For user events it just signals the tool-name reset.
func applyAssistantText(raw map[string]interface{}, ev *tailer.ParsedEvent, eventType, askUserQuestion string) {
	switch eventType {
	case "assistant", eventTypeAssistantStreaming, "assistant_message", "assistant_output":
		ev.AssistantText = tailer.ExtractAssistantText(raw)
		if ev.AssistantText == "" && askUserQuestion != "" {
			ev.AssistantText = tailer.TruncateAssistantText(askUserQuestion)
		}
	case "user", "user_message", "user_input":
		ev.ClearToolNames = true
		ev.UserText = tailer.ExtractUserText(raw)
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
