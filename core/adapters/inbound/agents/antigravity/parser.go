package antigravity

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/pkg/tailer"
)

// Parser implements tailer.TranscriptParser for Antigravity session transcripts
// (the filtered transcript.jsonl). Each line is one step sharing the envelope
// {step_index, source, type, status, created_at, content} plus an optional
// tool_calls array and thinking text. The shapes:
//
//   - source "USER_EXPLICIT", type "USER_INPUT" — the user's prompt, wrapped in
//     <USER_REQUEST>…</USER_REQUEST> plus metadata blocks. Opens a turn. A
//     <USER_SETTINGS_CHANGE> block records a model switch — the only place a
//     model name appears — which the parser harvests for display.
//   - source "MODEL", type "PLANNER_RESPONSE" — an assistant step. With
//     tool_calls it states intent ("I will …") and invokes a tool (the turn
//     continues); with NO tool_calls it is the turn's terminal line (often
//     empty content), settling the session to ready.
//   - source "MODEL", any other type (RUN_COMMAND, LIST_DIRECTORY, …) — the
//     RESULT of the preceding tool call. Antigravity links neither tool call
//     nor result by ID and runs tools sequentially, so the parser models a
//     single open tool at a time: a result line closes it and reads success/
//     failure from the result text.
//   - source "SYSTEM" (CONVERSATION_HISTORY, CHECKPOINT) — skipped.
//
// Cost is not surfaced (the JSONL carries no per-turn pricing), but token usage
// and the canonical model id ARE read on turn_done from the sibling per-
// conversation SQLite store (see dbmetrics.go) so the context bar can render
// (#719); state, cwd, the display model, and tool/timeline activity all derive
// from the transcript alone.
//
// Statefulness:
//   - cwd: harvested from a run_command tool call's Cwd arg and attached to
//     every emitted event so PID discovery can match the owning agy process.
//   - model: harvested from the first <USER_SETTINGS_CHANGE> and attached to
//     subsequent assistant events; replaced by the store's canonical id once
//     read so the model stays resolvable and the context bar doesn't flicker.
//   - openToolID: the synthetic ID of the currently-open tool call, closed by
//     the next result line (and swept by the tailer on turn_done).
//   - path: the transcript path, injected via tailer.TranscriptPathAware, used
//     to locate the sibling conversation store on turn_done.
//   - db: memoizes the last store read by (mtime, size).
type Parser struct {
	cwd        string
	model      string
	openToolID string
	path       string
	db         dbCache
}

// SetTranscriptPath implements tailer.TranscriptPathAware: the tailer injects
// the transcript path so turn_done can locate the sibling conversation store.
func (p *Parser) SetTranscriptPath(path string) { p.path = path }

// userSettingsModelRe pulls the model name out of a <USER_SETTINGS_CHANGE>
// block, e.g. "changed setting `Model Selection` from None to Gemini 3.5 Flash
// (Medium)." — capturing "Gemini 3.5 Flash". The non-greedy capture stops at the
// trailing " (mode)", a sentence-ending ". ", or a newline; ".{0,3}" absorbs the
// backtick/space between "Selection" and "from".
var userSettingsModelRe = regexp.MustCompile(`Model Selection.{0,3}from .+? to (.+?)(?: \(|\. |\n|$)`)

// ParseLine normalizes one Antigravity transcript line.
func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	ev := &tailer.ParsedEvent{Timestamp: parseCreatedAt(raw)}

	source, _ := raw["source"].(string)
	typ, _ := raw["type"].(string)

	switch {
	case typ == "USER_INPUT":
		p.parseUserInput(raw, ev)
	case source == "MODEL" && typ == "PLANNER_RESPONSE":
		p.parsePlannerResponse(raw, ev)
	case source == "MODEL":
		// Any other MODEL step is a tool RESULT (RUN_COMMAND, LIST_DIRECTORY, …).
		p.parseToolResult(raw, ev)
	default:
		ev.Skip = true // SYSTEM steps (CONVERSATION_HISTORY, CHECKPOINT) and unknowns
	}

	// Carry the known cwd on every emitted event; the first non-skip event lands
	// it in state.CWD for PID binding (mirrors the Gemini CLI adapter).
	if p.cwd != "" {
		ev.CWD = p.cwd
	}
	return ev
}

// parseUserInput opens a turn and harvests the model name from a
// <USER_SETTINGS_CHANGE> block — the only place the model appears. The block is
// emitted on the first turn ("from None to <model>") and again on every later
// /model switch ("from <old> to <new>"), so the parser updates p.model on EACH
// occurrence rather than only the first — otherwise a mid-session switch would
// be silently ignored and every turn would keep reporting the boot model
// (issue #707, scenario 5.3 model-switch-midsession). A normal prompt carries no
// settings-change, so the regex misses and the current model is preserved.
func (p *Parser) parseUserInput(raw map[string]any, ev *tailer.ParsedEvent) {
	if content, _ := raw["content"].(string); content != "" {
		if m := userSettingsModelRe.FindStringSubmatch(content); m != nil {
			p.model = tailer.NormalizeModelName(strings.TrimSpace(m[1]))
		}
	}
	ev.EventType = "user_message"
	ev.ClearToolNames = true
}

// parsePlannerResponse handles an assistant step. A response carrying tool_calls
// continues the turn (and opens a tool); a response with none is the turn's
// terminal line and settles the session to ready.
func (p *Parser) parsePlannerResponse(raw map[string]any, ev *tailer.ParsedEvent) {
	content, _ := raw["content"].(string)
	toolCalls, _ := raw["tool_calls"].([]any)

	if strings.TrimSpace(content) != "" {
		ev.AssistantText = tailer.TruncateAssistantText(content)
		if est := tailer.ScanTaskEstimate(content, ev.Timestamp); est != nil {
			ev.TaskEstimate = est
		}
	}
	if p.model != "" {
		ev.ModelName = p.model
	}

	if len(toolCalls) == 0 {
		// No tool call follows — the turn is over (the terminal line is usually
		// an empty PLANNER_RESPONSE). The store has the completed generation's
		// usage by now, so harvest tokens + the canonical model here.
		ev.EventType = "turn_done"
		p.applyDBMetrics(ev)
		return
	}

	ev.EventType = "assistant_message"
	stepIdx := intFromAny(raw["step_index"])
	for i, tc := range toolCalls {
		tcm, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tcm["name"].(string)
		// Antigravity tool calls carry no ID; synthesize a stable, unique one
		// from the step index so the tailer can track and (on turn_done) sweep
		// the open tool.
		id := strconv.FormatInt(stepIdx, 10) + "-" + strconv.Itoa(i)
		ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
		p.openToolID = id // sequential execution: the next result closes this
		if cwd := cwdFromToolCall(tcm); cwd != "" {
			p.cwd = cwd
		}
	}
}

// applyDBMetrics enriches a turn_done event with the latest context-token count
// and canonical model id from the sibling conversation store (#719). The
// transcript carries neither — only the display model name and no usage — so
// without this the context bar never appears for Antigravity. The store is read
// once per turn_done (memoized by mtime/size); a missing or unreadable store
// leaves the event unchanged.
//
// The store's canonical id (e.g. "gemini-3.1-pro-low") is preferred over the
// transcript's display name ("Gemini 3.1 Pro") because only the dash form
// resolves in the capacity context-window map. It is written back to p.model so
// later working-phase events report the same resolvable id and the bar stays
// lit between turns rather than blinking off.
func (p *Parser) applyDBMetrics(ev *tailer.ParsedEvent) {
	if p.path == "" {
		return
	}
	val, ok := readStoreModelTokens(p.path, &p.db)
	if !ok {
		return
	}
	if val.model != "" {
		p.model = tailer.NormalizeModelName(val.model)
		ev.ModelName = p.model
	}
	if val.contextTokens > 0 {
		ev.Tokens = &tailer.TokenSnapshot{Total: val.contextTokens}
	}
}

// parseToolResult closes the open tool call and flags an error if the result
// text reports a command failure. Emitted as function_call_output (a non-
// settling event) so the turn stays working until the terminal PLANNER_RESPONSE.
func (p *Parser) parseToolResult(raw map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "function_call_output"
	if p.openToolID != "" {
		ev.ToolResultIDs = []string{p.openToolID}
		p.openToolID = ""
	}
	if content, _ := raw["content"].(string); commandFailed(content) {
		ev.IsError = true
	}
}

// cwdFromToolCall pulls the working directory out of a run_command tool call's
// Cwd arg. Antigravity stores each arg value as a JSON-quoted string (e.g.
// "Cwd":"\"/Users/ingo\""), so the surrounding quotes are stripped.
//
// When agy sandboxes a command it reports its OWN internal scratch directory
// (…/.gemini/antigravity{,-cli}/scratch) as the Cwd rather than the user's
// workspace — which is never a real project and would mislabel the session, so
// it's rejected (the session stays transcript-first with no cwd). A non-sandbox
// run reports the real workspace, which still flows through for project
// labeling and the optional PID bind.
func cwdFromToolCall(tcm map[string]any) string {
	if name, _ := tcm["name"].(string); name != "run_command" {
		return ""
	}
	args, _ := tcm["args"].(map[string]any)
	if args == nil {
		return ""
	}
	cwd := strings.Trim(strings.TrimSpace(toString(args["Cwd"])), `"`)
	if isScratchDir(cwd) {
		return ""
	}
	return cwd
}

// isScratchDir reports whether a path is agy's internal sandbox scratch
// directory rather than a user workspace. agy's scratch lives at
// …/.gemini/antigravity-cli/scratch (CLI) or …/.gemini/antigravity/scratch (IDE).
func isScratchDir(cwd string) bool {
	return strings.Contains(cwd, ".gemini/antigravity") && strings.Contains(cwd, "/scratch")
}

// toString coerces a JSON value to its string form (Cwd is always a string in
// practice; the guard keeps a non-string from panicking).
func toString(v any) string {
	s, _ := v.(string)
	return s
}

// commandFailed reports whether a tool RESULT's text indicates failure.
// Antigravity's run_command result reads "The command completed successfully."
// or "The command failed with exit code: N".
func commandFailed(content string) bool {
	return strings.Contains(content, "The command failed")
}

// parseCreatedAt reads Antigravity's RFC3339 `created_at` step timestamp (e.g.
// "2026-06-19T05:33:39Z"). Falls back to tailer.ParseTimestamp — which checks
// the generic `timestamp` field and ultimately time.Now() — when `created_at`
// is absent or unparseable, so an event always carries a usable time.
func parseCreatedAt(raw map[string]any) time.Time {
	if s, _ := raw["created_at"].(string); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return tailer.ParseTimestamp(raw)
}

// intFromAny coerces a JSON number (decoded as float64) to int64.
func intFromAny(v any) int64 {
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
