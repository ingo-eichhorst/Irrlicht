package copilot

import (
	"sort"
	"strings"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// Envelope event types Copilot writes to events.jsonl. Verified live against
// CLI 1.0.77; every line shares {id, parentId, timestamp, type, data} and
// differs only in the shape of data.
const (
	evSessionStart     = "session.start"
	evModelChange      = "session.model_change"
	evAutoModeResolved = "session.auto_mode_resolved"
	evUserMessage      = "user.message"
	evAssistantMessage = "assistant.message"
	evTurnStart        = "assistant.turn_start"
	evTurnEnd          = "assistant.turn_end"
	evToolStart        = "tool.execution_start"
	evToolComplete     = "tool.execution_complete"
	evPermRequested    = "permission.requested"
	evPermCompleted    = "permission.completed"
	evUsageCheckpoint  = "session.usage_checkpoint"
	evSessionError     = "session.error"
	evShutdown         = "session.shutdown"
	evSubagentStarted  = "subagent.started"
	evSubagentDone     = "subagent.completed"
	evUserRequestedRun = "tool.user_requested"
	evCompactionStart  = "session.compaction_start"
	// autoModelPlaceholder is what session.model_change reports while the
	// hydra router has not yet chosen; the real model arrives on
	// session.auto_mode_resolved.
	autoModelPlaceholder = "auto"
	evCompactionDone     = "session.compaction_complete"
)

// Parser implements tailer.TranscriptParser for GitHub Copilot CLI
// transcripts. Every line is one append-only event in a uniform envelope:
//
//	{"id":"…","parentId":"…|null","timestamp":"…","type":"…","data":{…}}
//
// The envelope is what makes a plain JSONLineParser sufficient — unlike VS
// Code's own chatSessions store, which is snapshot-plus-delta and would need a
// bespoke reconstructing parser.
//
// Two properties of Copilot's vocabulary shape the mapping:
//
//   - Turn boundaries are EXPLICIT. assistant.turn_start / assistant.turn_end
//     carry a turnId, so "the agent finished" is read rather than inferred
//     from a quiet tail (contrast gemini-cli, which needs a heuristic).
//   - Lifecycle and metrics events are NOT activity. session.start,
//     model changes, usage checkpoints and shutdown are all marked Skip so
//     they can't trip the classifier's default "transcript activity →
//     working" rule — a shutdown that flipped a finished session back to
//     working would be exactly wrong. They still carry cwd, model, version
//     and token metadata, because the tailer's applySkippedEvent applies
//     that metadata for skipped events too.
type Parser struct {
	// cwd is the session's working directory, learned from session.start and
	// re-stamped on every event so PID binding has it as early as possible.
	cwd string
	// model is the active model, learned from model_change /
	// auto_mode_resolved / assistant.message and kept lit between turns.
	model string
	// lastUsage is the per-model cumulative usage already emitted as a
	// contribution, so each metrics event contributes only its delta exactly
	// once — the same high-water-mark technique vibe uses, and the reason a
	// re-read of a finished transcript doesn't double the session's cost.
	lastUsage map[string]tailer.UsageBreakdown
	// pendingContributions holds per-model deltas not yet attached to an
	// event. ParsedEvent carries a single Contribution but Copilot reports
	// usage for EVERY model the session touched, so a session that switched
	// models has more deltas than the event can hold; the surplus queues here
	// and drains one per subsequent event.
	pendingContributions []tailer.PerTurnContribution
	// openSubagents is the set of in-process subagents started and not yet
	// completed, keyed by toolCallId. Copilot's subagents write no session
	// directory of their own, so this parser-side count is the only source
	// for SessionMetrics.OpenSubagents.
	openSubagents map[string]struct{}
}

// eventHandler maps one Copilot event type onto a ParsedEvent. Handlers take
// the parser so the stateful ones (model, cwd, usage, subagents) can reach it;
// the stateless ones ignore it.
type eventHandler func(p *Parser, data map[string]any, ev *tailer.ParsedEvent)

// setEventType returns a handler that just labels the event. Used for the
// arms whose whole behaviour is "this event type means X to the tailer".
func setEventType(t string) eventHandler {
	return func(_ *Parser, _ map[string]any, ev *tailer.ParsedEvent) { ev.EventType = t }
}

// skip returns a handler that drops the event. Skipped events still flow
// through the postlude in ParseLine, so their cwd/model metadata is applied.
func skip() eventHandler {
	return func(_ *Parser, _ map[string]any, ev *tailer.ParsedEvent) { ev.Skip = true }
}

// eventHandlers is the complete type→behaviour map for Copilot's transcript.
// It is a table rather than a switch on purpose: every record shares one
// envelope and differs only by `type`, so the mapping IS the parser, and a
// table keeps it readable at a glance as the vocabulary grows. An unlisted
// type falls to the default in ParseLine.
var eventHandlers = map[string]eventHandler{
	evSessionStart: (*Parser).parseSessionStart,

	// Model identity arrives on two different events with two different field
	// names; neither is activity, so both are skipped after being recorded.
	evModelChange: func(p *Parser, d map[string]any, ev *tailer.ParsedEvent) {
		p.setModel(str(d, "newModel"), ev)
		ev.Skip = true
	},
	evAutoModeResolved: func(p *Parser, d map[string]any, ev *tailer.ParsedEvent) {
		p.setModel(str(d, "chosenModel"), ev)
		ev.Skip = true
	},

	evUserMessage:      func(_ *Parser, d map[string]any, ev *tailer.ParsedEvent) { parseUserMessage(d, ev) },
	evAssistantMessage: (*Parser).parseAssistantMessage,

	// turn_start must NOT be skipped: Copilot closes a turn after every tool
	// call and opens the next one ~50-80ms later with NO user.message in
	// between, so on a continuation turn this is the ONLY signal that the
	// agent is working again. Skipping it left the session reading idle for
	// the whole continuation — 122ms in one recording and 5.24 SECONDS in
	// another (#1256). Non-settling, so the classifier's default
	// transcript-activity rule holds the session `working`.
	evTurnStart: setEventType("turn_start"),
	evTurnEnd:   setEventType("turn_done"),

	evToolStart: func(_ *Parser, d map[string]any, ev *tailer.ParsedEvent) { parseToolStart(d, ev) },
	// A `!` shell escape: the user ran a command locally, with no model turn.
	// See parseToolComplete for why it must not register as activity.
	evUserRequestedRun: skip(),
	evToolComplete:     func(_ *Parser, d map[string]any, ev *tailer.ParsedEvent) { parseToolComplete(d, ev) },

	evPermRequested: func(_ *Parser, d map[string]any, ev *tailer.ParsedEvent) { parsePermissionRequested(d, ev) },
	evPermCompleted: func(_ *Parser, d map[string]any, ev *tailer.ParsedEvent) { parsePermissionCompleted(d, ev) },

	evUsageCheckpoint: (*Parser).parseUsage,
	evShutdown:        (*Parser).parseUsage,

	// A session-level failure (#1799). Skipped for the same reason every other
	// lifecycle event in this table is — see the package doc's "Lifecycle and
	// metrics events are NOT activity" — and the failure still lands, because
	// the tailer folds ParsedEvent.SessionError in applyMetadata, which runs on
	// the skipped path too (that is precisely why #1798 put it there).
	//
	// Skipping is what keeps the recorded trace honest. copilot writes
	// assistant.turn_end ~6ms BEFORE this event, so the turn really did end;
	// letting session.error become the session's LastEventType would flip
	// is_agent_done to false in every state-transition snapshot and claim the
	// agent was still mid-turn. The session still reads `error`, because the
	// classifier's session_error rule sits ABOVE agent_done and fires on the
	// error itself rather than on the tail's shape.
	evSessionError: func(_ *Parser, d map[string]any, ev *tailer.ParsedEvent) { parseSessionError(d, ev) },

	evSubagentStarted: func(p *Parser, d map[string]any, ev *tailer.ParsedEvent) { p.trackSubagent(d, true, ev) },
	evSubagentDone:    func(p *Parser, d map[string]any, ev *tailer.ParsedEvent) { p.trackSubagent(d, false, ev) },

	// Compaction is real work the user is waiting on, and a manual /compact
	// emits NO user.message and no assistant.turn_start — so without this arm
	// the whole summarization call fell to the default Skip and the session
	// sat in `ready` while Copilot's own TUI showed a compacting spinner.
	// Non-settling, so the transcript-activity rule holds it `working`.
	//
	// Unlike Claude Code this needs neither IsManualCompactBoundary nor the
	// hook-driven SignalCompactInProgress overlay: Copilot writes both
	// boundaries to disk as ordinary events, so the hold is durable and
	// replays. compaction_complete is the turn-shaped counterpart —
	// turn_done is the only event type that returns the session to ready.
	evCompactionStart: setEventType("compaction_start"),
	evCompactionDone:  setEventType("turn_done"),
}

// ParseLine normalizes one Copilot transcript line into a ParsedEvent.
func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	typ, _ := raw["type"].(string)
	if typ == "" {
		return &tailer.ParsedEvent{Skip: true}
	}
	data, _ := raw["data"].(map[string]any)
	ev := &tailer.ParsedEvent{Timestamp: tailer.ParseTimestamp(raw)}

	if h, ok := eventHandlers[typ]; ok {
		h(p, data, ev)
	} else {
		// Includes system.message (the system-prompt injection) and every
		// event type a future CLI adds.
		ev.Skip = true
	}

	// Stamp cwd and model on every event, skipped ones included: the tailer
	// applies both from skipped events, and cwd in particular is wanted as
	// early as possible so process binding doesn't wait for the first turn.
	if p.cwd != "" {
		ev.CWD = p.cwd
	}
	if p.model != "" && ev.ModelName == "" {
		ev.ModelName = tailer.NormalizeModelName(p.model)
	}
	p.drainPendingContribution(ev)
	return ev
}

// drainPendingContribution attaches one queued per-model contribution to this
// event. Draining on EVERY event (not just metrics ones) is what keeps the
// queue short: a checkpoint is always followed by more transcript activity, so
// surplus deltas land within a line or two rather than waiting for the next
// checkpoint. Metrics events are skipped by the parser but the tailer still
// applies their token metadata, so a drained contribution counts either way.
func (p *Parser) drainPendingContribution(ev *tailer.ParsedEvent) {
	if ev.Contribution != nil || len(p.pendingContributions) == 0 {
		return
	}
	ev.Contribution = &p.pendingContributions[0]
	p.pendingContributions = p.pendingContributions[1:]
}

// parseSessionStart reads the session header: the working directory (nested
// under data.context) and the CLI version. It is Skip=true because a session
// coming into existence is not agent activity — the session's initial state is
// ready, and only a user prompt should move it to working.
func (p *Parser) parseSessionStart(data map[string]any, ev *tailer.ParsedEvent) {
	if ctx, ok := data["context"].(map[string]any); ok {
		if cwd := str(ctx, "cwd"); cwd != "" {
			p.cwd = cwd
		}
	}
	ev.AgentVersion = str(data, "copilotVersion")
	ev.Skip = true
}

// setModel records a model change. Copilot reports "auto" before the router
// resolves it, then the concrete model on session.auto_mode_resolved; both
// arrive as ordinary events, so a mid-session /model switch is picked up the
// same way the initial selection is.
func (p *Parser) setModel(model string, ev *tailer.ParsedEvent) {
	// "auto" is the router's placeholder, not a model: session.model_change
	// reports it before session.auto_mode_resolved names the concrete choice.
	// Accepting it surfaces "auto" as the session's model in the UI for the
	// gap between the two events, and drives a pricing lookup that can only
	// miss ("no pricing for model \"auto\"" in a real recording).
	if model == "" || model == autoModelPlaceholder {
		return
	}
	p.model = model
	ev.ModelName = tailer.NormalizeModelName(model)
}

// parseUserMessage handles a user prompt — the event that opens a turn.
func parseUserMessage(data map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "user_message"
	ev.ClearToolNames = true
	if c := str(data, "content"); c != "" {
		ev.UserText = strings.TrimSpace(c)
	}
}

// parseAssistantMessage handles the model's reply. It does NOT open tool calls
// from data.toolRequests: Copilot emits a dedicated tool.execution_start for
// every invocation, so reading both would double-count the same call.
//
// This event never settles the turn — assistant.turn_end does that — so a
// reply that precedes further tool work correctly leaves the session working.
func (p *Parser) parseAssistantMessage(data map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "assistant_message"
	model := str(data, "model")
	p.setModel(model, ev)
	// outputTokens is the ONLY token count available while a session is
	// running, so it is what keeps cost live rather than flat-zero until the
	// agent exits. See parseUsage for why the cumulative block can't do it.
	p.queueOutputIncrement(model, num(data, "outputTokens"))
	content := str(data, "content")
	if strings.TrimSpace(content) == "" {
		return
	}
	ev.AssistantText = tailer.TruncateAssistantText(content)
	// Carry the waiting verdict over the FULL text, not just the truncated
	// display tail — a question sitting before the tail would otherwise settle
	// the turn to ready while the agent waits on the user (issue #1150). Same
	// computation claudecode and codex perform.
	ev.PendingWaitingCue = session.ProseIndicatesWaiting(tailer.WaitingScanWindow(content))
	if est := tailer.ScanTaskEstimate(content, ev.Timestamp); est != nil {
		ev.TaskEstimate = est
	}
	if s := tailer.ScanTaskSummary(content, ev.Timestamp); s != nil {
		ev.TaskSummary = s
	}
}

// parseToolStart opens a tool call. Copilot supplies a real toolCallId, so
// unlike antigravity no synthetic ID has to be minted to pair the completion.
func parseToolStart(data map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "function_call"
	id := str(data, "toolCallId")
	name := str(data, "toolName")
	if name == "" {
		return
	}
	ev.ToolUses = []tailer.ToolUse{{ID: id, Name: name}}
}

// parseToolComplete closes the tool call its toolCallId names, and flags a
// failed execution so the timeline can show it as an error.
func parseToolComplete(data map[string]any, ev *tailer.ParsedEvent) {
	// A user-requested run is Copilot's `!` shell escape — the user ran a
	// command locally and the model was never called (the CLI's own credit
	// counter stays at zero). It is NOT agent activity, and crucially it is
	// followed by no assistant.turn_end: mapping it to function_call_output
	// bounces a settled session ready -> working with nothing left to close
	// the turn, stranding it in working for the rest of the session. Skipping
	// it is the same fix mistral-vibe applies to its own `!` escape.
	if requested, _ := data["isUserRequested"].(bool); requested {
		ev.Skip = true
		return
	}
	ev.EventType = "function_call_output"
	if id := str(data, "toolCallId"); id != "" {
		ev.ToolResultIDs = []string{id}
	}
	if success, ok := data["success"].(bool); ok && !success {
		ev.IsError = true
	}
}

// parseSessionError maps copilot's `session.error` onto a session-level failure
// (#1799). Two shapes are recorded, and they differ in whether statusCode
// exists at all: errorType "rate_limit" carries 429, errorType "query" carries
// no code — which is why HTTPStatus is a pointer and why num() (which returns 0
// for an absent key) must not be used for it.
//
// PHASE STAYS UNKNOWN, deliberately. copilot's payload says nothing about
// whether another attempt is coming: there is no retry counter, no phase field,
// and copilot emits no per-attempt error event at all — its retry ladder is
// invisible on disk. session.ErrorPhaseUnknown is documented as exactly this
// case ("a copilot session.error with errorType 'query' is exactly this"), and
// claiming "terminal" would mean deriving a verdict from the message prose.
//
// What IS observed, in both recordings, is that assistant.turn_end lands ~6ms
// BEFORE this event and session.shutdown after it. That ordering is what makes
// the error survive: the turn boundary finds nothing to clear, and shutdown
// carries no boundary of its own. It is a property of these two recordings, not
// a guarantee the payload states, so it informs the tests rather than the phase.
//
// Class is copilot's own errorType verbatim. session.SessionError.Class is
// free-form precisely so an adapter can pass through the vocabulary its agent
// already uses instead of inventing a mapping.
//
// IsError is not touched: that channel is a failed tool result, which is the
// agent working normally.
//
// Skip=true with no EventType — see the table arm for why. A skipped event's
// EventType is never applied to the session, so naming one here would be a
// value no reader could act on.
func parseSessionError(data map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	ev.SessionError = &tailer.SessionError{
		Phase:      tailer.ErrorPhaseUnknown,
		Class:      str(data, "errorType"),
		Message:    strings.TrimSpace(str(data, "message")),
		HTTPStatus: optInt(data, "statusCode"),
	}
}

// optInt reads a JSON number as *int, returning nil when the key is absent or
// is not a number. The optional counterpart to num(), which zero-defaults —
// see parseSessionError for why absence must not read as HTTP 0.
func optInt(m map[string]any, key string) *int {
	if m == nil {
		return nil
	}
	f, ok := m[key].(float64)
	if !ok {
		return nil
	}
	v := int(f)
	return &v
}

// parsePermissionRequested opens a permission prompt: the agent has stopped
// and is waiting for the user to approve or deny a tool call. This is the
// signal that makes `waiting` reachable from the transcript alone — verified
// live by leaving CLI 1.0.77 sitting on its confirmation prompt and reading
// what had already been written to events.jsonl.
//
// Pairing is on requestId, not the toolCallId that also appears in the
// payload: the tool call and the prompt are different things, and a single
// tool call can be re-prompted.
func parsePermissionRequested(data map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "permission_requested"
	if id := str(data, "requestId"); id != "" {
		ev.PermissionRequestIDs = []string{id}
	}
}

// parsePermissionCompleted closes the prompt its requestId names. The
// resolution is recorded regardless of result.kind: an approval and a denial
// both mean the agent is no longer blocked on the user. Denials are not
// mapped to IsToolDenial — that flag is Claude Code's turn-ending
// cancellation marker, whereas Copilot carries on after a denial, and
// borrowing it would push the session to ready mid-turn.
func parsePermissionCompleted(data map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "permission_completed"
	if id := str(data, "requestId"); id != "" {
		ev.PermissionResolvedIDs = []string{id}
	}
}

// parseUsage reads Copilot's own cumulative usage block, carried on
// session.usage_checkpoint (mid-session) and session.shutdown (final):
//
//	"modelMetrics": {"gpt-5-mini": {"usage": {"inputTokens":14078,
//	  "outputTokens":156, "cacheReadTokens":0, "cacheWriteTokens":0}, …}},
//	"currentTokens": 15144
//
// Cost is derived from these tokens through the shared capacity price map, per
// the maintainer decision on #1256: general token pricing, the same as every
// other adapter. Copilot's own billing units (totalNanoAiu — AI credits — and
// the legacy totalPremiumRequests) are deliberately NOT turned into dollars
// here; reconstructing vendor billing from a multiplier is what that decision
// ruled out.
//
// The event is Skip=true: a usage checkpoint is bookkeeping, not agent
// activity, and a shutdown that flipped a finished session back to `working`
// would be plainly wrong. The tailer applies token metadata from skipped
// events, so the numbers still land.
//
// IMPORTANT — the two events carry DIFFERENT payloads. Verified live on CLI
// 1.0.77 by driving an interactive session and reading what actually landed:
//
//	session.usage_checkpoint → data keys are only
//	                           modelCacheState, totalNanoAiu, totalPremiumRequests
//	session.shutdown         → adds modelMetrics, tokenDetails, currentTokens
//
// So a checkpoint carries AI credits but NO token counts at all, and the
// cumulative block exists only at shutdown. Reading modelMetrics alone
// therefore leaves a live session showing no tokens and no cost for its entire
// lifetime — which is exactly what happened before this was checked against a
// running session rather than against a finished transcript's final line.
//
// Live token accounting instead comes from per-message
// assistant.message.outputTokens (see queueOutputIncrement). Input tokens have
// no live signal, so they land in one delta at shutdown; the shared high-water
// mark in lastUsage is what stops that final delta from re-counting the output
// already contributed per message.
func (p *Parser) parseUsage(data map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	if total := num(data, "currentTokens"); total > 0 {
		ev.Tokens = &tailer.TokenSnapshot{Total: total}
	}
	if model := str(data, "currentModel"); model != "" {
		p.model = model
		ev.ModelName = tailer.NormalizeModelName(model)
	}
	metrics, _ := data["modelMetrics"].(map[string]any)
	for _, model := range sortedKeys(metrics) {
		entry, ok := metrics[model].(map[string]any)
		if !ok {
			continue
		}
		usage, ok := entry["usage"].(map[string]any)
		if !ok {
			continue
		}
		p.queueContribution(model, tailer.UsageBreakdown{
			Input:     num(usage, "inputTokens"),
			Output:    num(usage, "outputTokens"),
			CacheRead: num(usage, "cacheReadTokens"),
			// Copilot reports one cache-write figure with no TTL split;
			// CacheCreation5m is the field the price map falls back through
			// for a provider that doesn't distinguish 5m from 1h.
			CacheCreation5m: num(usage, "cacheWriteTokens"),
		})
	}
}

// queueOutputIncrement contributes one assistant message's output tokens.
//
// Unlike modelMetrics this is an INCREMENT, not a cumulative total, so it is
// added straight onto the model's high-water mark. Sharing that mark with
// queueContribution is what makes the two paths compose: by the time
// session.shutdown reports the cumulative total, the output half has already
// been contributed message by message, so its delta is ~zero and only the
// input tokens land — no double counting, and live cost that stops being a
// flat zero.
func (p *Parser) queueOutputIncrement(model string, out int64) {
	if model == "" || out <= 0 {
		return
	}
	if p.lastUsage == nil {
		p.lastUsage = make(map[string]tailer.UsageBreakdown)
	}
	u := p.lastUsage[model]
	u.Output += out
	p.lastUsage[model] = u
	p.pendingContributions = append(p.pendingContributions, tailer.PerTurnContribution{
		Model: tailer.NormalizeModelName(model),
		Usage: tailer.UsageBreakdown{Output: out},
	})
}

// queueContribution records a model's cumulative usage as a delta against what
// has already been contributed, and queues it if anything actually moved.
// Negative deltas (a counter that went backwards, which a session reset or an
// in-place transcript rewrite could produce) clamp to zero rather than
// subtracting from the session total.
func (p *Parser) queueContribution(model string, cum tailer.UsageBreakdown) {
	if p.lastUsage == nil {
		p.lastUsage = make(map[string]tailer.UsageBreakdown)
	}
	prev := p.lastUsage[model]
	delta := tailer.UsageBreakdown{
		Input:           nonNegative(cum.Input - prev.Input),
		Output:          nonNegative(cum.Output - prev.Output),
		CacheRead:       nonNegative(cum.CacheRead - prev.CacheRead),
		CacheCreation5m: nonNegative(cum.CacheCreation5m - prev.CacheCreation5m),
	}
	if delta == (tailer.UsageBreakdown{}) {
		return
	}
	p.lastUsage[model] = cum
	p.pendingContributions = append(p.pendingContributions, tailer.PerTurnContribution{
		Model: tailer.NormalizeModelName(model),
		Usage: delta,
	})
}

// trackSubagent opens or closes an in-process subagent. Copilot's subagents
// (the built-in task/explore/research/code-review agents) run inside the
// parent session and write no session directory of their own, so they are
// counted here rather than linked through ParentSessionID the way codex's and
// pi's file-backed children are. Pairing is on toolCallId.
//
// Both events are Skip=true: a subagent starting or finishing is not itself a
// state change for the parent, which stays working across the whole span.
func (p *Parser) trackSubagent(data map[string]any, started bool, ev *tailer.ParsedEvent) {
	ev.Skip = true
	id := str(data, "toolCallId")
	if id == "" {
		return
	}
	if p.openSubagents == nil {
		p.openSubagents = make(map[string]struct{})
	}
	if started {
		p.openSubagents[id] = struct{}{}
		return
	}
	delete(p.openSubagents, id)
}

// OpenSubagents implements agent.SubagentCounter so the metrics collector
// surfaces Copilot's in-process children. The tailer's own metrics carry no
// signal for them, so the parser's open set is the whole answer.
func (p *Parser) OpenSubagents(_ *tailer.SessionMetrics) int {
	return len(p.openSubagents)
}

// sortedKeys returns a map's keys in a stable order, so the queue of per-model
// contributions is deterministic across runs — replay goldens are compared
// byte-for-byte and Go's map iteration order is not stable.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// num reads a JSON number field as int64, returning 0 when absent or not a
// number (encoding/json decodes every number into float64).
func num(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	f, _ := m[key].(float64)
	return int64(f)
}

// nonNegative clamps a computed delta to zero.
func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
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
