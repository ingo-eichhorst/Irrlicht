package opencode

import (
	"database/sql"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
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

	db, err := sql.Open("sqlite", dbPath+"?mode=ro&_journal=WAL&_timeout=500")
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

		raw, ok := buildPartRaw(partData, role, directory, timeUpdated, modelID)
		if !ok {
			continue
		}

		ev := parser.ParseLine(raw)
		if ev == nil || ev.Skip {
			continue
		}

		lastEventType = ev.EventType

		applyToolTracking(ev, &openTools)
		applyTaskDeltas(ev.TaskDeltas, &tasks, taskByID)
		// Snapshot reconcile — mirrors tailer.go:reconcileTaskSnapshot.
		// `todowrite` is a full-list replace by OpenCode semantics, so a
		// snapshot is authoritative for both pruning (todos removed from
		// the call vanish from metrics.Tasks) and status reversions the
		// delta path skips by design. reconcileTaskSnapshot no-ops when
		// there is no snapshot or no tasks yet.
		reconcileTaskSnapshot(ev.TaskSnapshot, &tasks, &taskByID)
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

	// Surface the agent-authored task estimate + projected completion ETA
	// (issue #558) — mirrors the conversion the shared metrics adapter does
	// for tailer-path agents (metrics/adapter.go), which this path bypasses.
	attachTaskEstimateAndETA(metrics, lastTaskEstimate, firstTaskEstimate)

	cm := capacity.DefaultCapacityManager()
	metrics.ContextWindow, metrics.ContextUtilization, metrics.PressureLevel, metrics.ContextWindowUnknown =
		tailer.ComputeContextUtilization(metrics.ModelName, metrics.TotalTokens, cm, 0)

	return metrics, nil
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
func buildPartRaw(partData, role, cwd string, timeUpdated int64, modelID string) (raw map[string]interface{}, ok bool) {
	if err := json.Unmarshal([]byte(partData), &raw); err != nil {
		return nil, false
	}
	raw["_role"] = role
	raw["_cwd"] = cwd
	raw["_ts"] = float64(timeUpdated)
	if modelID != "" {
		raw["_model"] = modelID
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

// applyTaskDeltas folds TaskCreate/TaskUpdate deltas into tasks and
// taskByID, mirroring the tailer's TaskDelta fold (tailer.go:708-728)
// because OpenCode's metrics path bypasses the tailer. See issue #277.
func applyTaskDeltas(deltas []tailer.TaskDelta, tasks *[]session.Task, taskByID map[string]int) {
	for _, d := range deltas {
		switch d.Op {
		case tailer.TaskOpCreate:
			id := strconv.Itoa(len(*tasks) + 1)
			*tasks = append(*tasks, session.Task{
				ID:          id,
				Subject:     d.Subject,
				Description: d.Description,
				ActiveForm:  d.ActiveForm,
				Status:      tailer.TaskStatusPending,
			})
			taskByID[id] = len(*tasks) - 1
		case tailer.TaskOpUpdate:
			if idx, ok := taskByID[d.ID]; ok && d.Status != "" {
				(*tasks)[idx].Status = d.Status
			}
		}
	}
}

// reconcileTaskSnapshot applies a todowrite snapshot to tasks and taskByID,
// mirroring tailer.go:reconcileTaskSnapshot. `todowrite` is a full-list
// replace by OpenCode semantics, so a snapshot is authoritative for both
// pruning (todos removed from the call vanish from metrics.Tasks) and status
// reversions the delta path skips by design. A nil snapshot or an empty task
// list is a no-op.
func reconcileTaskSnapshot(snapshot *[]tailer.TaskSnapshotEntry, tasks *[]session.Task, taskByID *map[string]int) {
	if snapshot == nil || len(*tasks) == 0 {
		return
	}
	snapByID := make(map[string]tailer.TaskSnapshotEntry, len(*snapshot))
	for _, entry := range *snapshot {
		snapByID[entry.ID] = entry
	}
	kept := make([]session.Task, 0, len(*tasks))
	for i := range *tasks {
		entry, present := snapByID[(*tasks)[i].ID]
		if !present {
			continue
		}
		if entry.Status != "" && entry.Status != (*tasks)[i].Status {
			(*tasks)[i].Status = entry.Status
		}
		kept = append(kept, (*tasks)[i])
	}
	*tasks = kept
	newByID := make(map[string]int, len(*tasks))
	for i := range *tasks {
		newByID[(*tasks)[i].ID] = i
	}
	*taskByID = newByID
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
