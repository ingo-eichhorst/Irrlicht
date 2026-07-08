package geminicli

import (
	"regexp"
	"strings"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for Gemini CLI session
// transcripts. Each transcript line is one of:
//
//   - a session header — {"sessionId","projectHash","startTime","kind"}; no
//     "type" and no "$set". Skipped.
//   - a "$set" mutation envelope — {"$set":{"messages":[…]}} seeds the initial
//     messages array (the bootstrap <session_context>, which carries the
//     workspace cwd), while {"$set":{"lastUpdated":…}} is a bare heartbeat.
//     Both are skipped; the cwd is harvested into parser state.
//   - a bare message — {"id","type","content",…} appended (or rewritten in
//     place under the same id) as the conversation advances. type "user"
//     carries either a text prompt or functionResponse tool results; type
//     "gemini" is an assistant message that may carry toolCalls and a per-
//     message token block.
//
// Statefulness:
//   - cwd: harvested from the bootstrap <session_context> and attached to every
//     emitted event so PID discovery (pid.go) can match the owning process by
//     working directory — the transcript path itself encodes only the project
//     name, not the cwd.
//   - committed: per assistant-message-id billable usage already contributed.
//     Gemini rewrites a streaming assistant message in place under one id, so
//     contributions are deduped by id to avoid double-billing a re-emission.
//
// Restart note: the tailer resumes each file from its persisted offset, so a
// fresh Parser only ever sees post-offset lines and never re-bills the history.
// The lone residual edge — a daemon restart landing mid-stream of a single
// assistant message — could re-bill that one message; negligible, and not
// worth a ParserStateProvider (ParserLedger has no per-id field).
type Parser struct {
	cwd       string
	committed map[string]tailer.UsageBreakdown

	// todos reconciles the `write_todos` snapshot Gemini-2 models emit (a
	// full-list-replace tool, like codex `update_plan` / opencode
	// `todowrite`) into task-progress deltas. Todos carry no stable ID, so
	// they're keyed by their `description` text.
	todos tailer.TodoReconciler
}

// workspaceRe pulls the first workspace directory out of the bootstrap
// <session_context> block, whose relevant lines read:
//
//   - **Workspace Directories:**
//   - /private/tmp/foo
var workspaceRe = regexp.MustCompile(`(?s)Workspace Directories:[^\n]*\n\s*-\s*([^\n]+)`)

// backgroundPIDRe pulls the PID out of the run_shell_command result text for a
// backgrounded launch, e.g. "Command moved to background (PID: 33701). Output
// hidden. Press Ctrl+B to view." The PID is Gemini's background-process handle.
var backgroundPIDRe = regexp.MustCompile(`\(PID:\s*(\d+)\)`)

// backgroundSpawnFromToolCall recognizes a `run_shell_command` toolCall launched
// with `is_background: true` and returns the spawn keyed on its reported PID, so
// the shared tailer increments BackgroundProcessCount (issue #661, the Gemini
// parallel of #445). Detection is gated on the structured is_background arg —
// not on the result text alone — so prose echoing the launch phrase can't
// fabricate a phantom process.
//
// Unlike Claude Code, Gemini hides the backgrounded command's output (viewable
// only via Ctrl+B) and writes no `tasks/<id>.output` file, so OutputPath is
// empty. Because the spawn is keyed on the PID, the tailer surfaces it in
// BackgroundProcessPIDs and the daemon's PID-liveness probe (kill(pid, 0))
// holds the session `working` until the process exits — the second half of
// #661 (the lsof-on-output-file probe has nothing to inspect for Gemini).
func backgroundSpawnFromToolCall(tcm map[string]interface{}) *tailer.BackgroundSpawn {
	if name, _ := tcm["name"].(string); name != "run_shell_command" {
		return nil
	}
	args, _ := tcm["args"].(map[string]interface{})
	if bg, _ := args["is_background"].(bool); !bg {
		return nil
	}
	m := backgroundPIDRe.FindStringSubmatch(shellResultOutput(tcm))
	if m == nil {
		return nil
	}
	return &tailer.BackgroundSpawn{BashID: m[1]}
}

// shellResultOutput pulls the result's response.output text out of a finished
// Gemini toolCall (toolCalls[].result[].functionResponse.response.output).
func shellResultOutput(tcm map[string]interface{}) string {
	res, _ := tcm["result"].([]interface{})
	for _, r := range res {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		fr, ok := rm["functionResponse"].(map[string]interface{})
		if !ok {
			continue
		}
		resp, ok := fr["response"].(map[string]interface{})
		if !ok {
			continue
		}
		if out, _ := resp["output"].(string); out != "" {
			return out
		}
	}
	return ""
}

// ParseLine normalizes one Gemini CLI transcript line.
func (p *Parser) ParseLine(raw map[string]interface{}) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{Timestamp: tailer.ParseTimestamp(raw)}

	switch {
	case raw["$set"] != nil:
		p.harvestSet(raw)
		ev.Skip = true
	case raw["type"] != nil:
		if !p.parseMessage(raw, ev) {
			ev.Skip = true
		}
	default:
		ev.Skip = true // session header or anything unrecognised
	}

	// Carry the known cwd on every emitted event; the first non-skip event
	// (the opening user prompt) is what lands it in state.CWD for PID binding.
	if p.cwd != "" {
		ev.CWD = p.cwd
	}
	return ev
}

// harvestSet pulls the workspace cwd out of a {"$set":{"messages":[…]}}
// envelope. A {"$set":{"lastUpdated":…}} heartbeat carries no messages and is
// a no-op.
func (p *Parser) harvestSet(raw map[string]interface{}) {
	set, ok := raw["$set"].(map[string]interface{})
	if !ok {
		return
	}
	msgs, ok := set["messages"].([]interface{})
	if !ok {
		return
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if cwd := workspaceFromContent(msg["content"]); cwd != "" {
			p.cwd = cwd
		}
	}
}

// parseMessage dispatches a bare message by its "type". Returns false for
// messages that carry no observable signal (unknown types, empty user turns).
func (p *Parser) parseMessage(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	switch raw["type"].(string) {
	case "user":
		return p.parseUser(raw, ev)
	case "gemini":
		return p.parseAssistant(raw, ev)
	case "error":
		return p.parseError(raw, ev)
	case "info":
		return p.parseInfo(raw, ev)
	default:
		return false // system / compression / unknown
	}
}

// terminalInfoMarkers are the observed type:"info" notices that ABORT the turn
// with no following gemini message — the turn's last word. Kept to a
// conservative allowlist of LEADING markers seen in recorded fixtures: a
// cancelled request (user Esc / quota abort: "Request cancelled.") and a failed
// request ("This request failed. Press F12 …"). Matched with HasPrefix on the
// trimmed content. Benign info notices (e.g. "Model set to gemini-2.5-flash", an
// empty placeholder) continue the turn and must NOT match.
var terminalInfoMarkers = []string{
	"Request cancelled",
	"This request failed",
}

// parseError handles a top-level type:"error" message: gemini-cli records a
// turn that aborted on an API error this way (upstream PR #13300). Gemini emits
// no end-of-turn marker and there is no inactivity sweep on `working`, so this
// is the turn's last word — settle to ready, surfacing the error text for the
// waiting display (#665).
func (p *Parser) parseError(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	content, _ := raw["content"].(string)
	ev.EventType = "turn_done"
	ev.AssistantText = tailer.TruncateAssistantText(content)
	ev.IsError = true
	return true
}

// parseInfo handles a bare "info" notice — a mixed-semantics line. A TERMINAL
// notice aborts the turn with no following "gemini" message, the same stuck-in-
// working gap as #665: the cancel notice Gemini writes when the user aborts with
// ESC / on a quota abort ("Request cancelled.", #659), and a failed request
// ("This request failed …", #676). Both settle the open turn to ready. Unlike
// parseError, a terminal info is NOT an agent error and carries no agent text,
// so it settles with turn_done alone — no IsError, no AssistantText overwrite
// (matching #659; a user ESC must not be surfaced as the agent's errored last
// word). A BENIGN notice (compression, system chatter, "Model set to …", empty
// placeholder) carries no signal and is skipped. The classifier is a
// conservative PREFIX allowlist (terminalInfoMarkers) anchored on the trimmed
// content, so a marker merely embedded mid-notice cannot false-settle a
// still-working session — a false-settle mid-turn is worse than the false-stick
// this guards against (codex keys off a structural "turn_aborted" marker).
func (p *Parser) parseInfo(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	content, _ := raw["content"].(string)
	trimmed := strings.TrimSpace(content)
	for _, marker := range terminalInfoMarkers {
		if strings.HasPrefix(trimmed, marker) {
			ev.EventType = "turn_done"
			return true
		}
	}
	return false
}

// collectGeminiUserParts walks a user-role message's parts, collecting any
// functionResponse ids (tool results) and the first non-empty text part.
// Split out of parseUser (go:S3776).
func collectGeminiUserParts(parts []interface{}) (toolResultIDs []string, firstText string) {
	for _, part := range parts {
		pm, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		if fr, ok := pm["functionResponse"].(map[string]interface{}); ok {
			if id, _ := fr["id"].(string); id != "" {
				toolResultIDs = append(toolResultIDs, id)
			}
			continue
		}
		if text, _ := pm["text"].(string); text != "" && firstText == "" {
			firstText = text
		}
	}
	return toolResultIDs, firstText
}

// parseUser handles a user-role message: a real text prompt, or the model's
// tool outputs recorded as functionResponse parts.
func (p *Parser) parseUser(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	parts, _ := raw["content"].([]interface{})
	toolResultIDs, firstText := collectGeminiUserParts(parts)

	// Tool results: Gemini records the model's tool outputs as a user-role
	// message of functionResponse parts. This closes the matching open tools
	// but is NOT a new user turn — it must not ClearToolNames or settle state.
	if len(toolResultIDs) > 0 {
		ev.EventType = "function_call_output"
		ev.ToolResultIDs = toolResultIDs
		return true
	}

	if firstText == "" {
		return false
	}

	// The bootstrap <session_context> is normally delivered via "$set", but
	// guard the bare-message path too: harvest its cwd, don't open a turn.
	if strings.HasPrefix(strings.TrimSpace(firstText), "<session_context>") {
		if m := workspaceRe.FindStringSubmatch(firstText); m != nil {
			p.cwd = strings.TrimSpace(m[1])
		}
		return false
	}

	// A `!cmd` shell-escape runs in a local shell with no LLM round-trip, but
	// Gemini still persists it as an ordinary user text message that opens with
	// this preamble (gemini-cli's useExecutionLifecycle.ts). It is not a real
	// user turn, and no terminal `gemini` message follows to settle it — so
	// treating it as a prompt would stick the session in working. Skip it, the
	// way claudecode skips its <bash-input>/<bash-stdout> wrappers.
	if strings.HasPrefix(strings.TrimSpace(firstText), "I ran the following shell command:") {
		return false
	}

	ev.EventType = "user_message"
	ev.ClearToolNames = true
	ev.UserText = firstText // post-filtered prompt — heuristic summary (#738)
	return true
}

// parseAssistant handles a "gemini" assistant message: its text, model, per-
// message token usage, tool calls, and the inferred end-of-turn.
func (p *Parser) parseAssistant(raw map[string]interface{}, ev *tailer.ParsedEvent) bool {
	ev.EventType = "assistant_message"

	content, _ := raw["content"].(string)
	ev.AssistantText = tailer.TruncateAssistantText(content)
	if est := tailer.ScanTaskEstimate(content, ev.Timestamp); est != nil {
		ev.TaskEstimate = est
	}
	if s := tailer.ScanTaskSummary(content, ev.Timestamp); s != nil {
		ev.TaskSummary = s
	}
	if model, _ := raw["model"].(string); model != "" {
		ev.ModelName = tailer.NormalizeModelName(model)
	}

	id, _ := raw["id"].(string)
	p.applyTokens(id, raw, ev)

	toolCalls, _ := raw["toolCalls"].([]interface{})
	for _, tc := range toolCalls {
		tcm, ok := tc.(map[string]interface{})
		if !ok {
			continue
		}
		p.applyGeminiToolCall(tcm, ev)
	}

	// Gemini emits no explicit end-of-turn marker. An assistant message that
	// carries final text and opens no further tool calls is the turn's last
	// word — settle to ready. A streaming placeholder (empty content) or a
	// tool-calling message keeps the session working.
	if strings.TrimSpace(content) != "" && len(toolCalls) == 0 {
		ev.EventType = "turn_done"
	}
	return true
}

// applyGeminiToolCall folds one toolCalls[] entry into ev: the tool-use
// record, write_todos delta reconciliation, background-spawn detection, and
// terminal-status result closing. Split out of parseAssistant (go:S3776).
func (p *Parser) applyGeminiToolCall(tcm map[string]interface{}, ev *tailer.ParsedEvent) {
	callID, _ := tcm["id"].(string)
	name, _ := tcm["name"].(string)
	if callID != "" || name != "" {
		ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: callID, Name: name})
	}
	// write_todos carries a full-list snapshot of the session's todos;
	// reconcile it into TaskDeltas so the dashboard's task dots populate.
	if name == "write_todos" {
		p.appendWriteTodosDeltas(tcm, ev)
	}
	if sp := backgroundSpawnFromToolCall(tcm); sp != nil {
		ev.BackgroundSpawns = append(ev.BackgroundSpawns, *sp)
	}
	// Gemini persists a finished toolCall with a terminal status and an
	// embedded result, so close it here too — a session the daemon only
	// observes after the fact still balances. A duplicate close from the
	// later functionResponse line is harmless.
	switch status, _ := tcm["status"].(string); status {
	case "success":
		if callID != "" {
			ev.ToolResultIDs = append(ev.ToolResultIDs, callID)
		}
	case "error", "cancelled":
		if callID != "" {
			ev.ToolResultIDs = append(ev.ToolResultIDs, callID)
		}
		ev.IsError = true
	}
}

// applyTokens reads Gemini's per-message token block and emits the latest-turn
// snapshot plus a deduped billable contribution.
func (p *Parser) applyTokens(id string, raw map[string]interface{}, ev *tailer.ParsedEvent) {
	tok, ok := raw["tokens"].(map[string]interface{})
	if !ok {
		return
	}
	input := intFromAny(tok["input"])
	output := intFromAny(tok["output"])
	cached := intFromAny(tok["cached"])
	total := intFromAny(tok["total"])

	// Latest-turn snapshot for context-utilization display.
	ev.Tokens = &tailer.TokenSnapshot{
		Input:     input,
		Output:    output,
		CacheRead: cached,
		Total:     total,
	}

	// Per-message billable usage. Gemini's `input` is inclusive of `cached`,
	// so bill the non-cached remainder as Input and the cached portion as
	// CacheRead to avoid double counting (same convention as the Codex mapping).
	billed := tailer.UsageBreakdown{
		Input:     max(int64(0), input-cached),
		Output:    output,
		CacheRead: cached,
	}

	// No id to dedup on — contribute the whole turn (each is unique).
	if id == "" {
		if billed.Input > 0 || billed.Output > 0 || billed.CacheRead > 0 {
			ev.Contribution = &tailer.PerTurnContribution{Model: ev.ModelName, Usage: billed}
		}
		return
	}

	// A streaming message is rewritten under one id; contribute only the
	// forward delta so re-emissions aren't billed twice.
	if p.committed == nil {
		p.committed = make(map[string]tailer.UsageBreakdown)
	}
	prev := p.committed[id]
	delta := tailer.UsageBreakdown{
		Input:     max(int64(0), billed.Input-prev.Input),
		Output:    max(int64(0), billed.Output-prev.Output),
		CacheRead: max(int64(0), billed.CacheRead-prev.CacheRead),
	}
	if delta.Input > 0 || delta.Output > 0 || delta.CacheRead > 0 {
		ev.Contribution = &tailer.PerTurnContribution{Model: ev.ModelName, Usage: delta}
		p.committed[id] = billed
	}
}

// appendWriteTodosDeltas reads the write_todos snapshot from toolCall.args.todos
// and (a) appends the minimal TaskCreate/TaskUpdate sequence that brings the
// accumulator in line with the snapshot, and (b) emits a TaskSnapshot listing
// every todo currently tracked. The snapshot is what the tailer's
// reconcileTaskSnapshot consumes to prune entries that vanished from a later
// call and to honour status reversions the Update path skips by design.
//
// Mirrors opencode's todowrite handling — Gemini's todos likewise carry no
// stable ID, so they're keyed by their `description` text. Two todos sharing
// the same description collapse into one tracked task (a silent, acceptable
// trade-off, as in opencode).
func (p *Parser) appendWriteTodosDeltas(tcm map[string]interface{}, ev *tailer.ParsedEvent) {
	args, _ := tcm["args"].(map[string]interface{})
	if args == nil {
		return
	}
	rawTodos, _ := args["todos"].([]interface{})
	todos := make([]tailer.Todo, 0, len(rawTodos))
	for _, raw := range rawTodos {
		todo, _ := raw.(map[string]interface{})
		if todo == nil {
			continue
		}
		desc, _ := todo["description"].(string)
		status, _ := todo["status"].(string)
		todos = append(todos, tailer.Todo{Key: desc, Status: status})
	}
	p.todos.Reconcile(todos, ev)
}

// workspaceFromContent finds the first workspace directory inside a message's
// content parts (the bootstrap <session_context> text block).
func workspaceFromContent(content interface{}) string {
	parts, ok := content.([]interface{})
	if !ok {
		return ""
	}
	for _, part := range parts {
		pm, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		text, _ := pm["text"].(string)
		if text == "" {
			continue
		}
		if m := workspaceRe.FindStringSubmatch(text); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// intFromAny coerces a JSON number (decoded as float64) to int64.
func intFromAny(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
