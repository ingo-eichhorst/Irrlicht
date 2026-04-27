package opencode

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"irrlicht/core/domain/session"
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
//   - "<dbPath>"                  — sessionID is passed as a separate arg
//   - "<dbPath>?session=<id>"     — session ID is embedded in the path
func parseTranscriptPath(transcriptPath, sessionID string) (dbPath, sid string) {
	if strings.Contains(transcriptPath, "?session=") {
		parts := strings.SplitN(transcriptPath, "?session=", 2)
		return parts[0], parts[1]
	}
	return transcriptPath, sessionID
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
		return nil, nil
	}
	defer rows.Close()

	metrics := &session.SessionMetrics{
		ModelName:    "unknown",
		PressureLevel: "unknown",
		LastCWD:      directory,
	}

	parser := &Parser{}
	var lastEventType string
	openTools := make(map[string]string) // callID → toolName
	var lastAssistantText string
	var cumCost float64
	var cumInput, cumOutput, cumCacheRead int64
	hasData := false
	var lastTS time.Time

	for rows.Next() {
		var partData, msgData string
		var timeUpdated int64
		if err := rows.Scan(&partData, &timeUpdated, &msgData); err != nil {
			continue
		}
		hasData = true
		ts := time.UnixMilli(timeUpdated)
		if ts.After(lastTS) {
			lastTS = ts
		}

		// Parse message data for role.
		var msgMap map[string]interface{}
		_ = json.Unmarshal([]byte(msgData), &msgMap)
		role, _ := msgMap["role"].(string)
		modelID, _ := msgMap["modelID"].(string)
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

	return metrics, nil
}
