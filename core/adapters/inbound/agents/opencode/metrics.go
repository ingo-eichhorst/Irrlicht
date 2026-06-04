package opencode

import (
	"database/sql"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

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
		ts := time.UnixMilli(timeUpdated)
		if firstTS.IsZero() {
			firstTS = ts
		}
		if ts.After(lastTS) {
			lastTS = ts
		}

		// Parse message data for role and model. OpenCode nests the model
		// fields under message.data.model = {providerID, modelID}; older
		// (or hypothetical future) builds may surface modelID at the top
		// level, so fall back to that path if the nested one is empty.
		var msgMap map[string]interface{}
		_ = json.Unmarshal([]byte(msgData), &msgMap)
		role, _ := msgMap["role"].(string)
		var modelID string
		if model, ok := msgMap["model"].(map[string]interface{}); ok {
			modelID, _ = model["modelID"].(string)
		}
		if modelID == "" {
			modelID, _ = msgMap["modelID"].(string)
		}
		if modelID != "" {
			metrics.ModelName = tailer.NormalizeModelName(modelID)
		}

		// Build the raw map the parser expects.
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(partData), &raw); err != nil {
			continue
		}
		raw["_role"] = role
		raw["_cwd"] = directory
		raw["_ts"] = float64(timeUpdated)
		if modelID != "" {
			raw["_model"] = modelID
		}

		ev := parser.ParseLine(raw)
		if ev == nil || ev.Skip {
			continue
		}

		lastEventType = ev.EventType

		// Accumulate tool tracking.
		for _, tu := range ev.ToolUses {
			openTools[tu.ID] = tu.Name
		}
		for _, rid := range ev.ToolResultIDs {
			delete(openTools, rid)
		}
		if ev.ClearToolNames {
			openTools = make(map[string]string)
		}

		for _, d := range ev.TaskDeltas {
			switch d.Op {
			case tailer.TaskOpCreate:
				id := strconv.Itoa(len(tasks) + 1)
				tasks = append(tasks, session.Task{
					ID:          id,
					Subject:     d.Subject,
					Description: d.Description,
					ActiveForm:  d.ActiveForm,
					Status:      tailer.TaskStatusPending,
				})
				taskByID[id] = len(tasks) - 1
			case tailer.TaskOpUpdate:
				if idx, ok := taskByID[d.ID]; ok && d.Status != "" {
					tasks[idx].Status = d.Status
				}
			}
		}

		// Snapshot reconcile — mirrors tailer.go:reconcileTaskSnapshot.
		// `todowrite` is a full-list replace by OpenCode semantics, so a
		// snapshot is authoritative for both pruning (todos removed from
		// the call vanish from metrics.Tasks) and status reversions the
		// delta path skips by design.
		if ev.TaskSnapshot != nil && len(tasks) > 0 {
			snapByID := make(map[string]tailer.TaskSnapshotEntry, len(*ev.TaskSnapshot))
			for _, entry := range *ev.TaskSnapshot {
				snapByID[entry.ID] = entry
			}
			kept := make([]session.Task, 0, len(tasks))
			for i := range tasks {
				entry, present := snapByID[tasks[i].ID]
				if !present {
					continue
				}
				if entry.Status != "" && entry.Status != tasks[i].Status {
					tasks[i].Status = entry.Status
				}
				kept = append(kept, tasks[i])
			}
			tasks = kept
			taskByID = make(map[string]int, len(tasks))
			for i := range tasks {
				taskByID[tasks[i].ID] = i
			}
		}

		// Accumulate cost/tokens from PerTurnContribution.
		if ev.Contribution != nil {
			cumInput += ev.Contribution.Usage.Input
			cumOutput += ev.Contribution.Usage.Output
			cumCacheRead += ev.Contribution.Usage.CacheRead
			if ev.Contribution.ProviderCostUSD != nil {
				cumCost += *ev.Contribution.ProviderCostUSD
			}
		}

		// Track latest token snapshot for context utilization.
		if ev.Tokens != nil {
			metrics.TotalTokens = ev.Tokens.Total
		}

		// Track assistant text.
		if ev.AssistantText != "" {
			lastAssistantText = ev.AssistantText
		}

		// Track the latest task-estimate marker (issue #558) — mirrors the
		// tailer's lastTaskEstimate persistence, which this path bypasses.
		// A real user part resets it (new task/redirect — same rule as the
		// tailer, including the tool-result guard): only markers after the
		// last user message count.
		if ev.TaskEstimate != nil {
			if firstTaskEstimate == nil ||
				(lastTaskEstimate != nil && ev.TaskEstimate.CompletedRounds < lastTaskEstimate.CompletedRounds) {
				firstTaskEstimate = ev.TaskEstimate
			}
			lastTaskEstimate = ev.TaskEstimate
		}
		if ev.ClearToolNames && len(ev.ToolResultIDs) == 0 {
			lastTaskEstimate = nil
			firstTaskEstimate = nil
		}
	}

	if !hasData {
		return nil, nil
	}

	// Build open tool names list.
	var openToolNames []string
	for _, name := range openTools {
		openToolNames = append(openToolNames, name)
	}

	metrics.LastEventType = lastEventType
	metrics.HasOpenToolCall = len(openTools) > 0
	metrics.OpenToolCallCount = len(openTools)
	metrics.LastOpenToolNames = openToolNames
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
	if lastTaskEstimate != nil {
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

	cm := capacity.DefaultCapacityManager()
	metrics.ContextWindow, metrics.ContextUtilization, metrics.PressureLevel, metrics.ContextWindowUnknown =
		tailer.ComputeContextUtilization(metrics.ModelName, metrics.TotalTokens, cm, 0)

	return metrics, nil
}
