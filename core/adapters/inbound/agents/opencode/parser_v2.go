package opencode

import (
	"sort"
	"strings"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// The opencode 2.x branch of the parser (#2106). Its input is one
// `session_message` row: the row's `data` JSON with the row's `type` column
// added as "type". Shapes below were read from the sandbox store opencode
// 2.1.4 wrote and from the schema its service serves (openapi
// Session.Message.*):
//
//	user       {text, files[]} — a prompt
//	assistant  {model:{id,providerID}, content[], finish, tokens, cost, error}
//	           — ONE step, rewritten in place while it streams. content[]
//	           holds reasoning, text and tool items; a tool item's progress is
//	           state.status: streaming, running (sampled while its permission
//	           was held), completed or error. A step still streaming has no
//	           finish and no tokens yet.
//	idle       {outcome, error} — the turn ended: succeeded, interrupted or
//	           failed.
//
// No 1.x part type is user, assistant or idle, so the row's type alone picks
// the branch. Read off the recorded corpus: every `type` across the 100
// replaydata/agents/opencode/**/*.jsonl files is one of compaction, patch,
// reasoning, step-finish, step-start, text or tool (`find
// replaydata/agents/opencode -name '*.jsonl'` piped through a JSON census of
// the field).
const (
	v2TypeUser      = "user"
	v2TypeAssistant = "assistant"
	v2TypeIdle      = "idle"
)

// v2State is what the 2.x branch carries from one row to the next within one
// pass over a session.
type v2State struct {
	// openTools are the tool calls the latest assistant rows show running,
	// keyed by call id. An idle row closes whatever is left.
	openTools map[string]struct{}
	// stepError is the error on the latest assistant row since the last idle
	// row, for a failed outcome whose idle row carries none.
	stepError map[string]interface{}
}

// parseV2Message parses raw into ev when it is a 2.x session_message row,
// and reports whether it was one.
func (p *Parser) parseV2Message(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	switch raw["type"] {
	case v2TypeUser:
		ev.EventType = "user_message"
		ev.ClearToolNames = true
		ev.UserText, _ = raw["text"].(string)
	case v2TypeAssistant:
		p.parseV2Assistant(raw, ev)
	case v2TypeIdle:
		p.parseV2Idle(raw, ev)
	default:
		return false
	}
	return true
}

// parseV2Assistant reads one step. It is "assistant_message" whatever the
// step's finish — the idle row, not the step, ends the turn — with the
// step's tool calls as ToolUses (still running) and ToolResultIDs (done).
func (p *Parser) parseV2Assistant(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	ev.EventType = "assistant_message"
	if model, ok := raw["model"].(map[string]interface{}); ok {
		ev.ModelName, _ = model["id"].(string)
	}
	if tokens, ok := raw["tokens"].(map[string]interface{}); ok {
		ev.Tokens = v2Tokens(tokens)
	}
	if e, ok := raw["error"].(map[string]interface{}); ok && len(e) > 0 {
		p.v2.stepError = e
	}

	content, _ := raw["content"].([]interface{})
	var texts []string
	for _, item := range content {
		part, _ := item.(map[string]interface{})
		switch part["type"] {
		case "text":
			if text, _ := part["text"].(string); text != "" {
				texts = append(texts, text)
			}
		case "tool":
			p.foldV2Tool(part, ev)
		}
	}
	if len(texts) > 0 {
		applyAssistantText(strings.Join(texts, ""), ev)
	}
}

// foldV2Tool records one tool item of a step.
func (p *Parser) foldV2Tool(part map[string]interface{}, ev *tailer.ParsedEvent) {
	id, _ := part["id"].(string)
	name, _ := part["name"].(string)
	state, _ := part["state"].(map[string]interface{})
	status, _ := state["status"].(string)
	if p.v2.openTools == nil {
		p.v2.openTools = make(map[string]struct{})
	}
	switch status {
	case "streaming", "running":
		ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
		p.v2.openTools[id] = struct{}{}
	case "completed", "error":
		ev.ToolResultIDs = append(ev.ToolResultIDs, id)
		ev.IsError = ev.IsError || status == "error"
		delete(p.v2.openTools, id)
	}
}

// parseV2Idle reads the end of a turn. Every outcome is turn_done; a failed
// one also carries the failure. An idle row is the end of the session's run,
// so a tool its last stored step still shows running is closed here rather
// than left holding the session in working.
func (p *Parser) parseV2Idle(raw map[string]interface{}, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_done"
	for id := range p.v2.openTools {
		ev.ToolResultIDs = append(ev.ToolResultIDs, id)
	}
	sort.Strings(ev.ToolResultIDs)
	p.v2.openTools = nil

	if outcome, _ := raw["outcome"].(string); outcome == "failed" {
		ev.SessionError = v2SessionError(raw["error"], p.v2.stepError)
	}
	p.v2.stepError = nil
}

// v2SessionError builds the failure of a failed outcome from the idle row's
// own error (Session.StructuredError: type, message, status), else the last
// step's. The one failed outcome measured — a session whose model its
// provider does not serve — wrote no step and put the error on the idle row:
// {type: "provider.no-route", message: "Model unavailable: …"}. With neither,
// the failure is still reported, saying only what is known.
func v2SessionError(idleErr interface{}, stepErr map[string]interface{}) *tailer.SessionError {
	e, _ := idleErr.(map[string]interface{})
	if len(e) == 0 {
		e = stepErr
	}
	se := &tailer.SessionError{
		Phase:   tailer.ErrorPhaseTerminal,
		Class:   "provider",
		Message: `turn ended with outcome "failed"`,
	}
	if class, _ := e["type"].(string); class != "" {
		se.Class = class
	}
	if msg := opencodeErrorMessage(e); msg != "" {
		se.Message = msg
	}
	se.HTTPStatus = tailer.OptInt(e, "status")
	return se
}

// v2Tokens reads a step's token counts. 2.x reports no total, so Total is the
// sum of all five — the context the step left behind. Input does not include
// the cached prompt: a sandbox step reported input 45 beside cache.read 7456.
func v2Tokens(tokens map[string]interface{}) *tailer.TokenSnapshot {
	snap := tokenSnapshot(tokens)
	reasoning, _ := tokens["reasoning"].(float64)
	snap.Total = snap.Input + snap.Output + int64(reasoning) + snap.CacheRead + snap.CacheCreation
	return snap
}

// applyAssistantText sets the assistant-text fields both branches derive from
// a step's full text: the task estimate and summary markers and the hedging
// sample (#558, #737) from the FULL text, the display text truncated.
func applyAssistantText(text string, ev *tailer.ParsedEvent) {
	if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
		ev.TaskEstimate = est
	}
	if s := tailer.ScanTaskSummary(text, ev.Timestamp); s != nil {
		ev.TaskSummary = s
	}
	ev.AssistantText = tailer.TruncateAssistantText(text)
	ev.Hedge = session.MeasureHedgingCached(text) // re-parsed every poll by the store fold
}
