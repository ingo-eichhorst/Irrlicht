package opencode

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
	"irrlicht/core/pkg/sqlitero"
	"irrlicht/core/pkg/tailer"
)

// ComputeMetrics queries the OpenCode SQLite database for the session
// identified by sessionID and returns normalized SessionMetrics.
//
// transcriptPath encodes the database path and session ID in the format used
// by the metrics adapter: it is either the raw DB path (when the caller is the
// watcher's initial scan) or "<dbPath>?session=<sessionID>" as set by
// agent.Event.TranscriptPath in later activity events.
//
// Returns nil, nil when the session has no parts yet.
func ComputeMetrics(transcriptPath, sessionID string) (*session.SessionMetrics, error) {
	dbPath, sid := parseTranscriptPath(transcriptPath, sessionID)
	if dbPath == "" || sid == "" {
		return nil, nil
	}

	db, err := sqlitero.Open(dbPath)
	if err != nil {
		return nil, nil
	}
	defer db.Close()

	return querySessionMetrics(db, sid, dbPath)
}

// parseTranscriptPath extracts the database path and session ID from a
// transcriptPath string. The format is either:
//   - "<dbPath>"                         — sessionID is passed as a separate arg
//   - "<dbPath>-wal"                     — WAL path used by watcher; strip suffix
//   - "<dbPath>-wal?session=<id>"        — session ID embedded; strip WAL suffix
//   - "<dbPath>?session=<id>"            — session ID embedded, no WAL suffix
func parseTranscriptPath(transcriptPath, sessionID string) (dbPath, sid string) {
	if strings.Contains(transcriptPath, "?session=") {
		parts := strings.SplitN(transcriptPath, "?session=", 2)
		// Strip any -wal suffix from the DB path component.
		dbPath = strings.TrimSuffix(parts[0], "-wal")
		return dbPath, parts[1]
	}
	// Strip -wal suffix if present (watcher uses WAL path for staleness check).
	dbPath = strings.TrimSuffix(transcriptPath, "-wal")
	return dbPath, sessionID
}

// querySessionMetrics fetches and aggregates part rows for a session.
func querySessionMetrics(db *sql.DB, sessionID, dbPath string) (*session.SessionMetrics, error) {
	// Fetch session CWD.
	var directory string
	_ = db.QueryRow(`SELECT directory FROM session WHERE id = ?`, sessionID).Scan(&directory)

	// Fetch all parts ordered by creation time.
	rows, err := db.Query(`
		SELECT p.data, p.time_updated, m.data as msg_data
		FROM part p
		JOIN message m ON p.message_id = m.id
		WHERE p.session_id = ?
		ORDER BY p.time_created ASC, p.id ASC
	`, sessionID)
	if err != nil {
		log.Printf("opencode: db.Query(part): %v", err)
		return nil, nil
	}
	defer rows.Close()

	metrics := &session.SessionMetrics{
		ModelName:     "unknown",
		PressureLevel: "unknown",
		LastCWD:       directory,
	}

	parser := &Parser{}
	var lastEventType string
	openTools := make(map[string]string) // callID → toolName
	var lastAssistantText string
	var lastTaskEstimate, firstTaskEstimate *tailer.TaskEstimate
	var cumCost float64
	var cumInput, cumOutput, cumCacheRead int64
	hasData := false
	var firstTS, lastTS time.Time

	// Task accumulator mirrors the tailer's TaskDelta fold (tailer.go:708-728)
	// because OpenCode's metrics path bypasses the tailer. See issue #277.
	var tasks []session.Task
	taskByID := make(map[string]int)

	// The session-level failure fold, mirroring the tailer's applySessionError
	// (core/pkg/tailer/tailer_metrics.go) because this path bypasses the
	// tailer entirely — the same reason the task accumulator above is a
	// hand-rolled mirror. Unlike the tailer's, this one needs no persistence
	// across passes: querySessionMetrics rebuilds from EVERY part row on every
	// call, so the fold below re-derives the current verdict from the whole
	// history each time and the sticky field is simply this local.
	var sessionErr *tailer.SessionError

	for rows.Next() {
		var partData, msgData string
		var timeUpdated int64
		if err := rows.Scan(&partData, &timeUpdated, &msgData); err != nil {
			log.Printf("opencode: rows.Scan(part): %v", err)
			continue
		}
		hasData = true
		trackTimestampRange(time.UnixMilli(timeUpdated), &firstTS, &lastTS)

		role, modelID := applyRoleAndModel(msgData, metrics)

		raw, ok := buildPartRaw(partData, role, directory, timeUpdated, modelID, msgData)
		if !ok {
			continue
		}

		ev := parser.ParseLine(raw)
		if ev == nil || ev.Skip {
			continue
		}

		lastEventType = ev.EventType
		applySessionError(ev, &sessionErr)

		applyToolTracking(ev, &openTools)
		tailer.ApplyTaskDeltas(ev.TaskDeltas, &tasks, taskByID)
		// Snapshot reconcile — mirrors tailer.go:reconcileTaskSnapshot.
		// `todowrite` is a full-list replace by OpenCode semantics, so a
		// snapshot is authoritative for both pruning (todos removed from
		// the call vanish from metrics.Tasks) and status reversions the
		// delta path skips by design. reconcileTaskSnapshot no-ops when
		// there is no snapshot or no tasks yet.
		tailer.ReconcileTaskSnapshot(ev.TaskSnapshot, &tasks, &taskByID)
		applyContribution(ev.Contribution, &cumInput, &cumOutput, &cumCacheRead, &cumCost)
		trackTokensAndText(ev, metrics, &lastAssistantText)

		// Track the latest task-estimate marker (issue #558) — mirrors the
		// tailer's lastTaskEstimate persistence, which this path bypasses.
		// A real user part resets it (new task/redirect — same rule as the
		// tailer, including the tool-result guard): only markers after the
		// last user message count.
		applyTaskEstimateTracking(ev, &lastTaskEstimate, &firstTaskEstimate)
	}

	if !hasData {
		return nil, nil
	}

	metrics.LastEventType = lastEventType
	metrics.HasOpenToolCall = len(openTools) > 0
	metrics.OpenToolCallCount = len(openTools)
	metrics.LastOpenToolNames = openToolNamesFrom(openTools)
	metrics.LastAssistantText = lastAssistantText
	metrics.EstimatedCostUSD = cumCost
	metrics.CumInputTokens = cumInput
	metrics.CumOutputTokens = cumOutput
	metrics.CumCacheReadTokens = cumCacheRead
	metrics.ElapsedSeconds = int64(lastTS.Sub(firstTS).Seconds())
	metrics.Tasks = tasks
	metrics.SessionError = convertSessionError(sessionErr)

	// Surface the agent-authored task estimate + projected completion ETA
	// (issue #558) — mirrors the conversion the shared metrics adapter does
	// for tailer-path agents (metrics/adapter.go), which this path bypasses.
	attachTaskEstimateAndETA(metrics, lastTaskEstimate, firstTaskEstimate)

	cm := capacity.DefaultCapacityManager()
	metrics.ContextWindow, metrics.ContextUtilization, metrics.PressureLevel, metrics.ContextWindowUnknown =
		tailer.ComputeContextUtilization(metrics.ModelName, metrics.TotalTokens, cm, 0)

	return metrics, nil
}

// applySessionError folds one parsed event into the running session-level
// failure. It is the tailer's applySessionError rule restated for the path
// that bypasses the tailer, and the ORDER is the same and is deliberate:
// clear first, then record, so an event carrying BOTH a failure and a turn
// boundary — which is exactly opencode's shape, since the errored part is
// itself the turn_done — reports a turn that ended in failure rather than a
// turn that recovered.
//
// The two clearing events are the two halves of #1796's settled rule: a turn
// boundary (the retry case settling green) and a genuine new user turn. A
// TOOL RESULT IS NOT A RECOVERY, which is what StartsNewUserTurn encodes —
// see it for the #558 incident behind the distinction.
func applySessionError(ev *tailer.ParsedEvent, cur **tailer.SessionError) {
	if *cur != nil && recoversFromSessionError(ev) {
		*cur = nil
	}
	if ev.SessionError != nil {
		*cur = ev.SessionError
	}
}

// recoversFromSessionError reports whether ev is one of the two events that
// count as the next successful turn: a turn boundary, or a genuine new user
// turn. Named so applySessionError above reads as clear-then-record rather than
// as a compound condition.
func recoversFromSessionError(ev *tailer.ParsedEvent) bool {
	return ev.EventType == "turn_done" || ev.StartsNewUserTurn()
}

// convertSessionError copies the tailer's mirror type into the domain one.
//
// A NEAR-TWIN OF replayengine.convertSessionError, and deliberately not shared.
// The two sit on opposite sides of the hexagon — this one in an inbound
// adapter, that one in application/ — and there is no package today that both
// may import: the duplication is a direct consequence of #1798's tailer-mirror
// types, which exist precisely so a parser need not import the domain.
// core/adapters/outbound/metrics is where the tailer's own doc comment says
// this glue belongs, but it carries no SessionError conversion yet and opencode
// imports nothing from outbound/ — so giving these a shared home is a
// cross-cutting refactor that touches #1798's code, not a tidy-up for this PR.
// Recorded rather than silently duplicated; worth a follow-up.
// The pointers are deep-copied rather than aliased for the same reason
// replayengine.convertSessionError does it: two passes over the same row
// would otherwise share numeric fields, and a later mutation of one would be
// visible through the other.
func convertSessionError(se *tailer.SessionError) *session.SessionError {
	if se == nil {
		return nil
	}
	return &session.SessionError{
		Phase:       session.ErrorPhase(se.Phase),
		Class:       se.Class,
		Message:     se.Message,
		HTTPStatus:  copyPtr(se.HTTPStatus),
		Attempt:     copyPtr(se.Attempt),
		MaxAttempts: copyPtr(se.MaxAttempts),
		RetryIn:     copyPtr(se.RetryIn),
	}
}

// copyPtr returns a pointer to a copy of *p, or nil for a nil p.
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// trackTimestampRange extends the [firstTS, lastTS] window to include ts.
func trackTimestampRange(ts time.Time, firstTS, lastTS *time.Time) {
	if firstTS.IsZero() {
		*firstTS = ts
	}
	if ts.After(*lastTS) {
		*lastTS = ts
	}
}

// applyRoleAndModel parses a message-row JSON blob for its role and model
// ID, updating metrics.ModelName when a model ID is present. OpenCode nests
// the model fields under message.data.model = {providerID, modelID}; older
// (or hypothetical future) builds may surface modelID at the top level, so
// fall back to that path if the nested one is empty.
func applyRoleAndModel(msgData string, metrics *session.SessionMetrics) (role, modelID string) {
	var msgMap map[string]interface{}
	_ = json.Unmarshal([]byte(msgData), &msgMap)
	role, _ = msgMap["role"].(string)
	if model, ok := msgMap["model"].(map[string]interface{}); ok {
		modelID, _ = model["modelID"].(string)
	}
	if modelID == "" {
		modelID, _ = msgMap["modelID"].(string)
	}
	if modelID != "" {
		metrics.ModelName = tailer.NormalizeModelName(modelID)
	}
	return role, modelID
}

// buildPartRaw unmarshals a part's JSON data column and injects the
// synthetic context keys Parser.ParseLine expects. Returns ok=false when the
// JSON fails to parse.
//
// `_error` is the key #1800 added, and its absence was a real gap rather than
// an omission of detail. The onboarding driver exports message.data.error onto
// every part as `_error`, so the parser's error branch fired under REPLAY —
// but this function builds the parser's input on the LIVE path and never set
// the key, so live sessions took a different branch through the same parser.
// The live turn still ended (watcher.go's isErrorMessage sets Terminal on the
// activity event), but ComputeMetrics saw an ordinary part and produced
// neither the turn_done nor, once #1798 existed, any SessionError at all.
func buildPartRaw(partData, role, cwd string, timeUpdated int64, modelID, msgData string) (raw map[string]interface{}, ok bool) {
	if err := json.Unmarshal([]byte(partData), &raw); err != nil {
		return nil, false
	}
	raw["_role"] = role
	raw["_cwd"] = cwd
	raw["_ts"] = float64(timeUpdated)
	if modelID != "" {
		raw["_model"] = modelID
	}
	if errVal := messageErrorFrom(msgData); errVal != nil {
		raw["_error"] = errVal
	}
	return raw, true
}

// applyToolTracking updates the open-tool-call map from one parsed event:
// new tool uses open a call, matching result IDs close it, and a
// ClearToolNames signal (e.g. a fresh user message) drops everything.
func applyToolTracking(ev *tailer.ParsedEvent, openTools *map[string]string) {
	for _, tu := range ev.ToolUses {
		(*openTools)[tu.ID] = tu.Name
	}
	for _, rid := range ev.ToolResultIDs {
		delete(*openTools, rid)
	}
	if ev.ClearToolNames {
		*openTools = make(map[string]string)
	}
}

// openToolNamesFrom returns the names of all currently open tool calls.
func openToolNamesFrom(openTools map[string]string) []string {
	var names []string
	for _, name := range openTools {
		names = append(names, name)
	}
	return names
}

// applyContribution accumulates per-turn usage and cost from a
// PerTurnContribution onto the running totals. A nil contribution is a
// no-op.
func applyContribution(contribution *tailer.PerTurnContribution, cumInput, cumOutput, cumCacheRead *int64, cumCost *float64) {
	if contribution == nil {
		return
	}
	*cumInput += contribution.Usage.Input
	*cumOutput += contribution.Usage.Output
	*cumCacheRead += contribution.Usage.CacheRead
	if contribution.ProviderCostUSD != nil {
		*cumCost += *contribution.ProviderCostUSD
	}
}

// trackTokensAndText updates the latest token snapshot (for context
// utilization) and the latest assistant text seen this scan.
func trackTokensAndText(ev *tailer.ParsedEvent, metrics *session.SessionMetrics, lastAssistantText *string) {
	if ev.Tokens != nil {
		metrics.TotalTokens = ev.Tokens.Total
	}
	if ev.AssistantText != "" {
		*lastAssistantText = ev.AssistantText
	}
}

// applyTaskEstimateTracking tracks the earliest and latest task-estimate
// markers seen this scan (issue #558), mirroring the tailer's
// lastTaskEstimate/firstTaskEstimate persistence, which this path bypasses.
// A real user part resets both (new task/redirect — same rule as the
// tailer, including the tool-result guard): only markers after the last
// user message count.
func applyTaskEstimateTracking(ev *tailer.ParsedEvent, lastTaskEstimate, firstTaskEstimate **tailer.TaskEstimate) {
	if ev.TaskEstimate != nil {
		if *firstTaskEstimate == nil ||
			(*lastTaskEstimate != nil && ev.TaskEstimate.CompletedRounds < (*lastTaskEstimate).CompletedRounds) {
			*firstTaskEstimate = ev.TaskEstimate
		}
		*lastTaskEstimate = ev.TaskEstimate
	}
	if ev.ClearToolNames && len(ev.ToolResultIDs) == 0 {
		*lastTaskEstimate = nil
		*firstTaskEstimate = nil
	}
}

// attachTaskEstimateAndETA surfaces the agent-authored task estimate and
// projected completion ETA (issue #558) onto metrics — mirroring the
// conversion the shared metrics adapter does for tailer-path agents
// (metrics/adapter.go), which this path bypasses. A nil lastTaskEstimate is
// a no-op.
func attachTaskEstimateAndETA(metrics *session.SessionMetrics, lastTaskEstimate, firstTaskEstimate *tailer.TaskEstimate) {
	if lastTaskEstimate == nil {
		return
	}
	toDomain := func(src *tailer.TaskEstimate) *session.TaskEstimate {
		if src == nil {
			return nil
		}
		return &session.TaskEstimate{
			TotalRounds:     src.TotalRounds,
			CompletedRounds: src.CompletedRounds,
			Risk:            src.Risk,
			Confidence:      src.Confidence,
			UpdatedAt:       src.ObservedAt,
		}
	}
	metrics.TaskEstimate = toDomain(lastTaskEstimate)
	if eta := session.ForecastTaskCompletion(metrics.TaskEstimate, toDomain(firstTaskEstimate), metrics.ElapsedSeconds, time.Now()); eta != nil {
		etaUnix := eta.Unix()
		metrics.TaskCompletionEta = &etaUnix
	}
}
