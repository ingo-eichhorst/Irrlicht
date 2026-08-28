package junie

import (
	"strings"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// Top-level event kinds Junie writes to events.jsonl. Verified against live
// CLI sessions (August 2026); every line carries a top-level "kind" plus a
// "timestampMs" unix-milliseconds stamp.
const (
	kindUserPrompt        = "UserPromptEvent"
	kindTaskStarted       = "TaskStartedEvent"
	kindTaskState         = "TaskState"
	kindAgentTaskFailed   = "AgentTaskFailedEvent"
	kindCancelAgent       = "CancelAgentEvent"
	kindUserAsyncResponse = "UserAsyncResponseEvent"
	kindUserResponse      = "UserResponseEvent"
	kindSessionA2ux       = "SessionA2uxEvent"
)

// Nested agentEvent kinds carried inside SessionA2uxEvent.event.agentEvent.
// Each block event is a stepId-keyed UPDATE stream: the same stepId is
// re-emitted as its status moves IN_PROGRESS → COMPLETED/FAILED, and the
// whole set is replayed once more (status COMPLETED, state COMPLETED) when
// the task finalizes — which is why tool pairing must be idempotent.
const (
	aeAgentThought  = "AgentThoughtBlockUpdatedEvent"
	aeTerminal      = "TerminalBlockUpdatedEvent"
	aeTool          = "ToolBlockUpdatedEvent"
	aeViewFiles     = "ViewFilesBlockUpdatedEvent"
	aeFileChanges   = "FileChangesBlockUpdatedEvent"
	aeMcp           = "McpBlockUpdatedEvent"
	aeResult        = "ResultBlockUpdatedEvent"
	aeAskAsync      = "AskAsyncRequestUpdatedEvent"
	aeChoice        = "ChoiceRequestUpdatedEvent"
	aeAgentFailure  = "AgentFailureEvent"
	aeLlmMetadata   = "LlmResponseMetadataEvent"
	aeContextWindow = "ContextWindowReportEvent"
	aeCurrentDir    = "CurrentDirectoryUpdatedEvent"
)

// agentEvent status values (block lifecycle, distinct from event.state which
// is the SESSION's state stamped onto every A2ux line).
const (
	statusInProgress = "IN_PROGRESS"
	statusCompleted  = "COMPLETED"
	statusFailed     = "FAILED"
)

// stateInputRequired is the session state Junie stamps while it is blocked on
// the user — a question (AskAsync), a choice, or a command approval. It is
// the transcript-tier waiting signal (the copilot pattern): no hook exists to
// deliver it, so the parser opens/closes PermissionRequestIDs from it.
const stateInputRequired = "INPUT_REQUIRED"

// Parser implements tailer.TranscriptParser for Junie's events.jsonl. Each
// line is one JSON event with a top-level "kind"; agent activity arrives
// wrapped in SessionA2uxEvent envelopes whose event.agentEvent carries the
// actual block update.
//
// The mapping, from live captures (all shapes in testdata/ and the
// parser_test fixtures are verbatim captured lines):
//
//   - UserPromptEvent → user_message; opens the turn, clears tool state.
//   - TaskStartedEvent → turn_start (copilot precedent, #1256): a resumed
//     task can start with NO UserPromptEvent, so this is the only signal the
//     agent is working again.
//   - TaskState → turn_done. Junie writes it once when the task finalizes —
//     after failures too (state COMPLETED follows AgentTaskFailedEvent), so
//     it is the turn boundary, not a success verdict.
//   - AgentFailureEvent (nested) / AgentTaskFailedEvent (top-level) →
//     SessionError. The nested event carries message+errorCode, the
//     top-level one carries nothing, so the parser remembers having seen the
//     detailed one and suppresses the generic duplicate that follows it.
//   - CancelAgentEvent → IsUserInterrupt (a real user cancellation).
//   - INPUT_REQUIRED: Ask/Choice/approval block updates under
//     event.state=INPUT_REQUIRED open PermissionRequestIDs keyed on stepId;
//     resolution is the same stepId leaving INPUT_REQUIRED (or reporting
//     status COMPLETED), with UserAsyncResponseEvent / UserResponseEvent /
//     CancelAgentEvent / TaskState draining every open id as a safety net.
//   - Terminal/Tool/ViewFiles/FileChanges/Mcp block updates → ToolUses /
//     ToolResultIDs keyed on stepId.
//   - ResultBlockUpdatedEvent → assistant_message carrying the final answer.
//   - LlmResponseMetadataEvent / ContextWindowReportEvent → model, token,
//     cost and context-window metrics — Skip=true bookkeeping the tailer
//     folds through the skipped-event metadata path. See metrics.go.
//   - CurrentDirectoryUpdatedEvent → CWD. The only place Junie names the
//     session's working directory (no top-level cwd field exists for the
//     generic extractor to read); without it every Junie session lands in
//     the dashboard's "unknown" project group. Same Skip=true routing.
//
// Unknown kinds (and Junie has many high-volume ones: AvailablePullRequests,
// AgentCurrentStatusUpdated, TipSuggestionCreated, ...) are skipped — the
// schema is unversioned, so a line the parser can't read must never crash
// the tail.
type Parser struct {
	// openInputRequests is the stepId-keyed set of INPUT_REQUIRED prompts
	// opened and not yet resolved. Needed parser-side (not just in the
	// tailer's openPermissions) because the resolution events that drain it
	// — UserAsyncResponseEvent, CancelAgentEvent, TaskState — carry no
	// stepId of their own.
	openInputRequests map[string]struct{}
	// sawFailureDetail is true once an AgentFailureEvent (which carries
	// message+errorCode) has been seen for the current task, so the bare
	// AgentTaskFailedEvent that follows doesn't overwrite the detailed
	// SessionError with a generic one. Reset on the next user prompt.
	sawFailureDetail bool
	// modelElectionTokens is the largest prompt-side token footprint any
	// single LLM call has carried this turn — the high-water mark of the
	// main-model election (see electModel in metrics.go). Reset on the next
	// user prompt.
	modelElectionTokens int64
}

// ParseLine normalizes one Junie events.jsonl line into a ParsedEvent.
func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	kind, _ := raw["kind"].(string)
	if kind == "" {
		return &tailer.ParsedEvent{Skip: true}
	}
	ev := &tailer.ParsedEvent{Timestamp: parseTimestampMs(raw)}

	switch kind {
	case kindUserPrompt:
		p.parseUserPrompt(raw, ev)
	case kindTaskStarted:
		ev.EventType = "turn_start"
	case kindTaskState:
		p.parseTaskState(raw, ev)
	case kindAgentTaskFailed:
		p.parseAgentTaskFailed(ev)
	case kindCancelAgent:
		p.parseCancel(ev)
	case kindUserAsyncResponse, kindUserResponse:
		p.parseUserResponse(ev)
	case kindSessionA2ux:
		p.parseA2ux(raw, ev)
	default:
		// UserMessagesCommittedToHistory, SystemMessageEvent,
		// SkillsStatusEvent, PlanReviewResolvedEvent, and every kind a
		// future Junie adds.
		ev.Skip = true
	}
	return ev
}

// parseUserPrompt handles a user prompt — the event that opens a turn.
func (p *Parser) parseUserPrompt(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "user_message"
	ev.ClearToolNames = true
	if c := str(raw, "prompt"); c != "" {
		ev.UserText = strings.TrimSpace(c)
	}
	// A fresh prompt supersedes anything still marked waiting (the tailer's
	// ClearToolNames rule wipes its openPermissions too) and starts a task
	// whose failures haven't been reported yet.
	ev.PermissionResolvedIDs = p.drainOpenInputRequests()
	p.sawFailureDetail = false
	// A new turn re-elects its main model; the prompt's own launch-model
	// attachment (rare, but authoritative when present) seeds the display
	// until the first main-model call wins the token election.
	p.modelElectionTokens = 0
	ev.ModelName = launchModelFromPrompt(raw)
}

// parseTaskState handles the task-finalization event. Junie writes it exactly
// once per task, after failures too (a FAILED task still ends with state
// COMPLETED in every live capture), so any TaskState is the turn boundary.
// A FAILED state has never been captured; if one ever arrives it is treated
// as a task-level failure on top of the boundary — stated so this arm is not
// read as fixture-backed.
func (p *Parser) parseTaskState(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_done"
	ev.PermissionResolvedIDs = p.drainOpenInputRequests()
	if str(raw, "state") == statusFailed {
		ev.SessionError = &tailer.SessionError{
			Phase:   tailer.ErrorPhaseUnknown,
			Class:   "task_failed",
			Message: "Junie task failed",
		}
	}
}

// parseAgentTaskFailed handles the top-level failure marker. It carries no
// message — the detail lives on the nested AgentFailureEvent that precedes it
// — so it only reports a SessionError when no detailed one was seen, keeping
// the informative message from being overwritten by a generic one.
//
// Skip=true (the copilot session.error precedent): Junie writes TaskState
// right after, so the turn really does end, and the failure still lands
// because the tailer folds SessionError from skipped events too (#1798).
func (p *Parser) parseAgentTaskFailed(ev *tailer.ParsedEvent) {
	ev.Skip = true
	if p.sawFailureDetail {
		return
	}
	ev.SessionError = &tailer.SessionError{
		Phase:   tailer.ErrorPhaseUnknown,
		Class:   "task_failed",
		Message: "Junie task failed",
	}
}

// parseCancel handles the user cancelling the running task — a genuine
// interrupt, kept distinct from failures so the classifier can settle the
// session to ready rather than error. Junie still finalizes the task
// afterwards (block replay + TaskState), so the open prompts are drained
// here to release a cancel-while-waiting session immediately.
func (p *Parser) parseCancel(ev *tailer.ParsedEvent) {
	ev.EventType = "user_message"
	ev.ClearToolNames = true
	ev.IsUserInterrupt = true
	ev.PermissionResolvedIDs = p.drainOpenInputRequests()
}

// parseUserResponse handles the user answering an agent question
// (UserAsyncResponseEvent) or choice prompt (UserResponseEvent). The events
// carry no stepId, so every open prompt is resolved — an answer means the
// user is no longer being waited on, whichever prompt it addressed.
func (p *Parser) parseUserResponse(ev *tailer.ParsedEvent) {
	ev.EventType = "permission_completed"
	ev.PermissionResolvedIDs = p.drainOpenInputRequests()
}

// parseA2ux unwraps a SessionA2uxEvent envelope and routes its agentEvent.
func (p *Parser) parseA2ux(raw map[string]any, ev *tailer.ParsedEvent) {
	envelope, _ := raw["event"].(map[string]any)
	agentEvent, _ := envelope["agentEvent"].(map[string]any)
	kind := str(agentEvent, "kind")
	state := str(envelope, "state")

	switch kind {
	case aeAskAsync, aeChoice:
		p.parseInputRequest(agentEvent, state, ev)
	case aeTerminal, aeTool, aeViewFiles, aeFileChanges, aeMcp:
		// A block asking for approval is an input request first: the command
		// has not run yet, so opening a ToolUse for it would be wrong.
		if _, isApproval := agentEvent["approvalRequest"]; isApproval || p.isOpenInputRequest(agentEvent) {
			p.parseInputRequest(agentEvent, state, ev)
			if ev.EventType == "permission_requested" {
				return
			}
		}
		parseToolBlock(agentEvent, kind, ev)
	case aeAgentThought:
		parseAgentThought(agentEvent, ev)
	case aeResult:
		parseResultBlock(agentEvent, ev)
	case aeAgentFailure:
		p.parseAgentFailure(agentEvent, ev)
	case aeLlmMetadata:
		p.parseLlmMetadata(agentEvent, ev)
	case aeContextWindow:
		parseContextWindowReport(agentEvent, ev)
	case aeCurrentDir:
		parseCurrentDirectory(agentEvent, ev)
	default:
		ev.Skip = true
	}
}

// parseInputRequest opens or resolves a user-input prompt, pairing on stepId.
// Open: the session is INPUT_REQUIRED and this block still awaits its answer.
// Resolve: an already-open stepId reports COMPLETED or reappears outside
// INPUT_REQUIRED (a terminal approval is granted by being re-emitted with
// state IN_PROGRESS, not by a status change). Anything else — most commonly
// the end-of-task replay of long-resolved prompts — is skipped.
func (p *Parser) parseInputRequest(agentEvent map[string]any, state string, ev *tailer.ParsedEvent) {
	stepID := str(agentEvent, "stepId")
	if stepID == "" {
		ev.Skip = true
		return
	}
	status := str(agentEvent, "status")
	if state == stateInputRequired && status != statusCompleted {
		if p.openInputRequests == nil {
			p.openInputRequests = make(map[string]struct{})
		}
		p.openInputRequests[stepID] = struct{}{}
		ev.EventType = "permission_requested"
		ev.PermissionRequestIDs = []string{stepID}
		if title := str(agentEvent, "title"); title != "" {
			ev.AssistantText = tailer.TruncateAssistantText(title)
		}
		return
	}
	if _, open := p.openInputRequests[stepID]; open {
		delete(p.openInputRequests, stepID)
		ev.EventType = "permission_completed"
		ev.PermissionResolvedIDs = []string{stepID}
		return
	}
	ev.Skip = true
}

// isOpenInputRequest reports whether this block update's stepId names a
// prompt the parser is holding open — the resolution path for a terminal
// approval, whose grant arrives as an ordinary block update.
func (p *Parser) isOpenInputRequest(agentEvent map[string]any) bool {
	_, open := p.openInputRequests[str(agentEvent, "stepId")]
	return open
}

// parseToolBlock maps a tool-shaped block update onto the ToolUses /
// ToolResultIDs pairing, keyed on stepId. The tailer's open-tool bookkeeping
// is idempotent per id, which absorbs both repeated IN_PROGRESS updates and
// the end-of-task replay (orphan resolutions are harmless no-ops).
func parseToolBlock(agentEvent map[string]any, kind string, ev *tailer.ParsedEvent) {
	stepID := str(agentEvent, "stepId")
	if stepID == "" {
		ev.Skip = true
		return
	}
	// An approval replay routed through parseInputRequest first may have been
	// marked Skip there; a real block status below overrides that verdict.
	switch str(agentEvent, "status") {
	case statusInProgress:
		ev.Skip = false
		if ev.EventType == "" {
			ev.EventType = "function_call"
		}
		ev.ToolUses = []tailer.ToolUse{{ID: stepID, Name: toolBlockName(agentEvent, kind)}}
	case statusCompleted, statusFailed:
		ev.Skip = false
		if ev.EventType == "" {
			ev.EventType = "function_call_output"
		}
		ev.ToolResultIDs = append(ev.ToolResultIDs, stepID)
		// A FAILED block is a failed tool run (a build that broke, a port in
		// use) — the agent working normally, so IsError, never SessionError.
		if str(agentEvent, "status") == statusFailed {
			ev.IsError = true
		}
	default:
		if ev.EventType == "" && len(ev.PermissionResolvedIDs) == 0 {
			ev.Skip = true
		}
	}
}

// toolBlockName derives the display name for an open tool block.
// ToolBlockUpdatedEvent names its tool only on completion (toolType, e.g.
// "Search") — the IN_PROGRESS update carries just a caption — so the kind
// supplies a stable fallback.
func toolBlockName(agentEvent map[string]any, kind string) string {
	if t := str(agentEvent, "toolType"); t != "" {
		return t
	}
	switch kind {
	case aeTerminal:
		return "Terminal"
	case aeViewFiles:
		return "ViewFiles"
	case aeFileChanges:
		return "FileChanges"
	case aeMcp:
		return "MCP"
	default:
		return "Tool"
	}
}

// parseCurrentDirectory surfaces the session's working directory. Skip=true —
// pure bookkeeping that must not become LastEventType — and the tailer folds
// CWD from skipped events (#1798). The FIRST such event of a task routinely
// carries an empty currentDirectory; the tailer's empty-guard means it never
// clobbers a real path seen earlier, so no filtering is needed here.
func parseCurrentDirectory(agentEvent map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	ev.CWD = str(agentEvent, "currentDirectory")
}

// parseAgentThought maps the agent narrating its next step — mid-turn
// assistant activity that keeps the session working between tool blocks.
func parseAgentThought(agentEvent map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "assistant_message"
	if text := str(agentEvent, "text"); strings.TrimSpace(text) != "" {
		ev.AssistantText = tailer.TruncateAssistantText(text)
	}
}

// parseResultBlock maps the task's final answer text. Non-settling — the
// TaskState that follows is the turn boundary — and the waiting verdict is
// computed over the FULL text (issue #1150), since a question sitting before
// the truncated display tail would otherwise settle the turn.
func parseResultBlock(agentEvent map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "assistant_message"
	result := str(agentEvent, "result")
	if strings.TrimSpace(result) == "" {
		ev.Skip = true
		return
	}
	ev.AssistantText = tailer.TruncateAssistantText(result)
	ev.PendingWaitingCue = session.ProseIndicatesWaiting(tailer.WaitingScanWindow(result))
}

// parseAgentFailure maps Junie's detailed failure event (message + errorCode)
// onto a session-level error.
//
// PHASE STAYS UNKNOWN, deliberately (the copilot session.error precedent):
// the payload says nothing about whether another attempt is coming, and
// Junie writes TaskState (→ turn_done) immediately after — a phase of
// "retrying" would let that boundary clear the error a few milliseconds
// after it was raised, while Unknown and Terminal both survive it.
//
// Skip=true so the failure bookkeeping doesn't become LastEventType (the
// turn's real epilogue is the TaskState that follows); the error still lands
// because the tailer folds SessionError from skipped events (#1798).
func (p *Parser) parseAgentFailure(agentEvent map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	p.sawFailureDetail = true
	ev.SessionError = &tailer.SessionError{
		Phase:   tailer.ErrorPhaseUnknown,
		Class:   str(agentEvent, "errorCode"),
		Message: strings.TrimSpace(str(agentEvent, "message")),
	}
}

// drainOpenInputRequests empties the open-prompt set, returning the drained
// ids (nil when nothing was open, so callers can assign it unconditionally).
func (p *Parser) drainOpenInputRequests() []string {
	if len(p.openInputRequests) == 0 {
		return nil
	}
	ids := make([]string, 0, len(p.openInputRequests))
	for id := range p.openInputRequests {
		ids = append(ids, id)
	}
	p.openInputRequests = nil
	return ids
}

// parseTimestampMs reads Junie's "timestampMs" unix-milliseconds stamp,
// falling back to the shared ParseTimestamp heuristics (→ scan time) for a
// line without one.
func parseTimestampMs(raw map[string]any) time.Time {
	if ms, ok := raw["timestampMs"].(float64); ok && ms > 0 {
		return time.UnixMilli(int64(ms))
	}
	return tailer.ParseTimestamp(raw)
}

// str reads a string field from a decoded JSON object, returning "" when the
// map is nil, the key is absent, or the value is not a string.
func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
