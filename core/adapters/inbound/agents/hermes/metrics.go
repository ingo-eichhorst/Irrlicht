package hermes

import (
	"database/sql"
	"log"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; no CGo required

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
	"irrlicht/core/pkg/tailer"
)

// sessionQueryParam is the query-string key appended to the store path so
// ComputeMetrics can recover the session ID from agent.Event.TranscriptPath.
const sessionQueryParam = "?session="

// Every query filters `source IN ('cli','tui','subagent')` — the surfaces that are
// actually coding sessions. Hermes' messaging gateway (WhatsApp, Slack,
// Discord, …) writes into the SAME sessions table, and without the filter a
// WhatsApp conversation would surface in irrlicht as a coding session.
//
// 'cli' covers one-shot (-z), piped runs AND the interactive `hermes chat`
// REPL; 'tui' is the separate TUI-gateway surface. The pair is spelled out
// as a literal inside each query rather than concatenated in from a
// constant, so no statement is assembled from a variable.

// liveCWDForSession resolves the working directory of the single live Hermes
// session process, for rows that carry none. Package-level so tests can stub
// out the process probe.
var liveCWDForSession = defaultLiveCWD

// ComputeMetrics reads the Hermes store for one session and returns
// normalized SessionMetrics.
//
// storePath is either the raw store path or "<path>?session=<id>" as set by
// the watcher on agent.Event.TranscriptPath. Returns nil, nil when the
// session has no rows yet — a session that exists but has not produced a
// message is not an error.
func ComputeMetrics(storePath, sessionID string) (*session.SessionMetrics, error) {
	dbPath, sid := parseStorePath(storePath, sessionID)
	if dbPath == "" || sid == "" {
		return nil, nil
	}

	db, err := openReadOnly(dbPath)
	if err != nil {
		return nil, nil
	}
	defer db.Close()

	return querySessionMetrics(db, sid)
}

// openReadOnly opens the store read-only with a short busy timeout.
//
// Hermes runs the store in journal_mode=delete (verified: `pragma
// journal_mode` returns "delete", and no -wal file exists next to a live
// store), so a reader can collide with a writer's exclusive lock. The
// timeout bounds that wait instead of blocking the watcher's scan loop.
//
// The "file:" prefix is load-bearing, not decoration: modernc.org/sqlite
// only parses DSN query parameters for a file: URI. Given a bare path it
// strips the query and opens with SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE,
// so "?mode=ro" is silently ignored — verified against v1.55.0, where a
// bare-path DSN CREATED a missing database and accepted a CREATE TABLE.
// This adapter's permission is observe-kind and its consent copy promises
// "No row is ever written", so the mode has to actually be enforced.
func openReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_timeout=500")
}

// parseStorePath splits a "<path>?session=<id>" transcript path. When no
// session is embedded the caller-supplied sessionID stands.
func parseStorePath(storePath, sessionID string) (dbPath, sid string) {
	if strings.Contains(storePath, sessionQueryParam) {
		parts := strings.SplitN(storePath, sessionQueryParam, 2)
		return parts[0], parts[1]
	}
	return storePath, sessionID
}

// querySessionMetrics folds one session's rows into SessionMetrics.
//
// Token totals and cost come from the `sessions` row rather than being
// summed from messages: Hermes maintains the aggregate itself
// (input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) and
// leaves messages.token_count NULL in practice. The messages are still
// replayed through Parser because they carry the state signal — turn
// boundaries, open tool calls, last assistant text — that the aggregate
// cannot express.
func querySessionMetrics(db *sql.DB, sessionID string) (*session.SessionMetrics, error) {
	row, ok := scanSessionRow(db, sessionID)
	if !ok {
		return nil, nil
	}

	metrics := &session.SessionMetrics{
		ModelName:     firstNonEmpty(row.model, "unknown"),
		PressureLevel: "unknown",
		LastCWD:       resolveLastCWD(row.cwd),
	}

	fold, ok := foldMessages(db, sessionID, row.model, row.cwd)
	if !ok {
		return nil, nil
	}
	applyFold(metrics, fold)
	applyAggregates(metrics, row)
	applyPricingAndContext(metrics)

	return metrics, nil
}

// storeMetricsRow is the `sessions` row querySessionMetrics reads. Hermes
// maintains the token aggregates itself, so they are taken from here rather
// than summed over messages.
type storeMetricsRow struct {
	model, cwd          string
	startedAt, endedAt  sql.NullFloat64
	inTok, outTok       int64
	cacheRead, cacheWri int64
}

// scanSessionRow reads one coding-surface session row. ok is false when the
// session does not exist or is not a coding surface — neither is an error.
func scanSessionRow(db *sql.DB, sessionID string) (storeMetricsRow, bool) {
	var (
		model, cwd, endReason              sql.NullString
		startedAt, endedAt                 sql.NullFloat64
		inTok, outTok, cacheRead, cacheWri sql.NullInt64
	)
	err := db.QueryRow(`
		SELECT model, cwd, end_reason, started_at, ended_at,
		       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		FROM sessions
		WHERE id = ? AND source IN ('cli','tui','subagent')`, sessionID).
		Scan(&model, &cwd, &endReason, &startedAt, &endedAt,
			&inTok, &outTok, &cacheRead, &cacheWri)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("hermes: query sessions row %s: %v", sessionID, err)
		}
		return storeMetricsRow{}, false
	}
	return storeMetricsRow{
		model:     model.String,
		cwd:       cwd.String,
		startedAt: startedAt,
		endedAt:   endedAt,
		inTok:     inTok.Int64,
		outTok:    outTok.Int64,
		cacheRead: cacheRead.Int64,
		cacheWri:  cacheWri.Int64,
	}, true
}

// resolveLastCWD falls back to the live process for a row that carries no cwd.
//
// A source='cli' session has no stored cwd (Hermes records one only from its
// TUI gateway), and the daemon cannot recover it later from a
// "<store>?session=<id>" path the way it would from a real transcript file.
// Resolving it here means the binding BACKFILLS on the next activity event —
// RefreshOnActivity applies a non-empty LastCWD — instead of being frozen at
// whatever discovery happened to see.
func resolveLastCWD(storedCWD string) string {
	if storedCWD != "" || liveCWDForSession == nil {
		return storedCWD
	}
	return liveCWDForSession()
}

// applyFold copies the state signal derived from replaying the messages.
func applyFold(metrics *session.SessionMetrics, fold messageFold) {
	metrics.LastEventType = fold.lastEventType
	metrics.LastAssistantText = fold.lastAssistantText
	metrics.PendingWaitingCue = fold.pendingWaitingCue
	metrics.HasOpenToolCall = len(fold.openTools) > 0
	metrics.OpenToolCallCount = len(fold.openTools)
	metrics.LastOpenToolNames = openToolNames(fold.openTools)
	metrics.Tasks = fold.tasks
}

// applyAggregates copies the token counters and elapsed time off the row.
func applyAggregates(metrics *session.SessionMetrics, row storeMetricsRow) {
	metrics.CumInputTokens = row.inTok
	metrics.CumOutputTokens = row.outTok
	metrics.CumCacheReadTokens = row.cacheRead
	metrics.CumCacheCreationTokens = row.cacheWri
	metrics.TotalTokens = row.inTok + row.outTok
	metrics.ElapsedSeconds = elapsedSeconds(row.startedAt, row.endedAt)
}

// elapsedSeconds is the session's wall-clock duration, or 0 while it is still
// live (ended_at NULL) or if the two stamps do not order sensibly.
func elapsedSeconds(startedAt, endedAt sql.NullFloat64) int64 {
	if !startedAt.Valid || !endedAt.Valid {
		return 0
	}
	if endedAt.Float64 <= startedAt.Float64 {
		return 0
	}
	return int64(endedAt.Float64 - startedAt.Float64)
}

// applyPricingAndContext derives cost, CO2 and context pressure from the
// token counters already on metrics.
//
// Cost is priced by irrlicht's own model-alias table rather than read from
// sessions.estimated_cost_usd. Hermes reports 0.0 with
// billing_mode='subscription_included' on a subscription provider, which is a
// statement about the user's plan, not about what the turn was worth — the
// repo convention is that cost is general pricing and only quota counters are
// taken from the agent verbatim.
func applyPricingAndContext(metrics *session.SessionMetrics) {
	cm := capacity.DefaultCapacityManager()
	if cm != nil && metrics.ModelName != "" {
		metrics.EstimatedCostUSD = cm.EstimateCostUSD(
			metrics.ModelName, metrics.CumInputTokens, metrics.CumOutputTokens,
			metrics.CumCacheReadTokens, metrics.CumCacheCreationTokens)
	}
	grams, tier := capacity.EstimateCO2Grams(metrics.ModelName,
		metrics.CumInputTokens+metrics.CumOutputTokens+
			metrics.CumCacheReadTokens+metrics.CumCacheCreationTokens)
	metrics.EstimatedCO2Grams = grams
	metrics.CO2Tier = string(tier)

	metrics.ContextWindow, metrics.ContextUtilization, metrics.PressureLevel, metrics.ContextWindowUnknown =
		tailer.ComputeContextUtilization(metrics.ModelName, metrics.TotalTokens, cm, 0)
}

// messageFold is the running state derived from replaying a session's
// messages through Parser — the signal the `sessions` aggregate cannot
// express.
type messageFold struct {
	lastEventType     string
	lastAssistantText string
	pendingWaitingCue bool
	openTools         map[string]string // tool-call id → tool name

	// Task accumulator. The store path bypasses the tailer, so the same fold
	// it performs is applied here via the shared helpers (see #277).
	tasks    []session.Task
	taskByID map[string]int
}

// foldMessages replays one session's message rows through Parser. ok is
// false when the session has no rows at all, which is not an error: the
// sessions row is INSERTed ~2s after launch, before the first reply.
func foldMessages(db *sql.DB, sessionID, model, cwd string) (messageFold, bool) {
	fold := messageFold{openTools: map[string]string{}, taskByID: map[string]int{}}

	rows, err := db.Query(`
		SELECT role, content, tool_calls, tool_call_id, tool_name,
		       finish_reason, display_kind, timestamp
		FROM messages
		WHERE session_id = ? AND active = 1
		ORDER BY timestamp ASC, id ASC`, sessionID)
	if err != nil {
		log.Printf("hermes: query messages for %s: %v", sessionID, err)
		return fold, false
	}
	defer rows.Close()

	parser := &Parser{}
	hasData := false

	for rows.Next() {
		raw, ok := scanMessageRow(rows, model, cwd)
		if !ok {
			continue
		}
		hasData = true

		ev := parser.ParseLine(raw)
		if ev == nil || ev.Skip {
			continue
		}
		fold.apply(ev)
	}
	if err := rows.Err(); err != nil {
		log.Printf("hermes: iterate messages for %s: %v", sessionID, err)
	}
	return fold, hasData
}

// apply folds one parsed event into the running state.
func (f *messageFold) apply(ev *tailer.ParsedEvent) {
	f.lastEventType = ev.EventType

	for _, tu := range ev.ToolUses {
		f.openTools[tu.ID] = tu.Name
	}
	for _, id := range ev.ToolResultIDs {
		delete(f.openTools, id)
	}
	if ev.ClearToolNames {
		f.openTools = map[string]string{}
	}
	// The `todo` tool rewrites the whole list on every call, so the deltas
	// advance it and the snapshot is authoritative for pruning + reversions.
	tailer.ApplyTaskDeltas(ev.TaskDeltas, &f.tasks, f.taskByID)
	tailer.ReconcileTaskSnapshot(ev.TaskSnapshot, &f.tasks, &f.taskByID)
	if ev.AssistantText != "" {
		f.lastAssistantText = ev.AssistantText
		f.pendingWaitingCue = ev.PendingWaitingCue
	}
	// A new user message answers whatever the agent was waiting on, so the
	// cue does not survive it. The ProcessOwnedStore path bypasses the
	// tailer, which is where that reset normally lives.
	if ev.EventType == "user_message" {
		f.pendingWaitingCue = false
	}
}

// scanMessageRow reads one row into the raw map Parser consumes, injecting
// the per-session context that lives on the `sessions` row.
func scanMessageRow(rows *sql.Rows, model, cwd string) (map[string]interface{}, bool) {
	var (
		role                                     string
		content, toolCalls, toolCallID, toolName sql.NullString
		finishReason, displayKind                sql.NullString
		timestamp                                sql.NullFloat64
	)
	if err := rows.Scan(&role, &content, &toolCalls, &toolCallID, &toolName,
		&finishReason, &displayKind, &timestamp); err != nil {
		log.Printf("hermes: scan message row: %v", err)
		return nil, false
	}
	return map[string]interface{}{
		keyRole:        role,
		keyContent:     content.String,
		keyToolCalls:   toolCalls.String,
		keyToolCallID:  toolCallID.String,
		keyToolName:    toolName.String,
		keyFinish:      finishReason.String,
		keyDisplayKind: displayKind.String,
		keyTimestamp:   timestamp.Float64,
		keyModel:       model,
		keyCWD:         cwd,
	}, true
}

func openToolNames(open map[string]string) []string {
	if len(open) == 0 {
		return nil
	}
	names := make([]string, 0, len(open))
	for _, n := range open {
		names = append(names, n)
	}
	return names
}

func firstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
