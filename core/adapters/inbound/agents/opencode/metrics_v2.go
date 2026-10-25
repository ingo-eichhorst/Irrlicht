package opencode

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// queryV2SessionMetrics is ComputeMetrics for an opencode 2.x store (#2106).
//
// The session's token totals and cost are session_v2's own columns, not a sum
// of its steps: they also count calls no step row carries — in the sandbox
// store, a session's three steps summed to 2219 input tokens where session_v2
// said 2254, the difference being its title call. Everything else — model,
// context, open tools, turn end, failure, text — is the fold of its
// session_message rows in seq order.
func queryV2SessionMetrics(db *sql.DB, sessionID string) (*session.SessionMetrics, error) {
	var (
		directory string
		modelRef  sql.NullString
		totals    v2Totals
	)
	err := db.QueryRow(`
		SELECT directory, model, cost, tokens_input, tokens_output, tokens_cache_read, tokens_cache_write
		FROM session_v2 WHERE id = ?
	`, sessionID).Scan(&directory, &modelRef, &totals.cost, &totals.input, &totals.output,
		&totals.cacheRead, &totals.cacheWrite)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("opencode: db.QueryRow(session_v2): %v", err)
		}
		return nil, nil
	}

	rows, err := db.Query(`
		SELECT type, data, time_updated
		FROM session_message
		WHERE session_id = ?
		ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		log.Printf("opencode: db.Query(session_message): %v", err)
		return nil, nil
	}
	defer rows.Close()

	fold := newSessionFold(directory)
	for rows.Next() {
		var typ, data string
		var timeUpdated int64
		if err := rows.Scan(&typ, &data, &timeUpdated); err != nil {
			log.Printf("opencode: rows.Scan(session_message): %v", err)
			continue
		}
		fold.row(timeUpdated)

		if raw, ok := buildV2MessageRaw(typ, data, directory, timeUpdated); ok {
			fold.observe(raw)
		}
	}
	// A session with no step yet — a failed one can end without any — names
	// its model only on its own row (`{"id": …, "providerID": …}`, measured).
	if fold.metrics.ModelName == "unknown" {
		if id := modelRefID(modelRef.String); id != "" {
			fold.metrics.ModelName = tailer.NormalizeModelName(id)
		}
	}

	metrics := fold.result()
	if metrics == nil {
		return nil, nil
	}
	metrics.EstimatedCostUSD = totals.cost
	metrics.CumInputTokens = totals.input
	metrics.CumOutputTokens = totals.output
	metrics.CumCacheReadTokens = totals.cacheRead
	metrics.CumCacheCreationTokens = totals.cacheWrite
	return metrics, nil
}

// v2Totals are a 2.x session's own token and cost columns.
type v2Totals struct {
	cost                                 float64
	input, output, cacheRead, cacheWrite int64
}

// buildV2MessageRaw decodes one session_message row into the map
// Parser.ParseLine takes: the row's data JSON with its `type` column added and
// the same synthetic context keys buildPartRaw gives a 1.x part. ok is false
// when the data does not parse.
func buildV2MessageRaw(typ, data, cwd string, timeUpdated int64) (raw map[string]interface{}, ok bool) {
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, false
	}
	raw["type"] = typ
	raw["_cwd"] = cwd
	raw["_ts"] = float64(timeUpdated)
	return raw, true
}

// modelRefID reads the id out of session_v2.model, a JSON model reference, or
// "" when the column is empty or unreadable.
func modelRefID(ref string) string {
	var m struct {
		ID string `json:"id"`
	}
	if ref == "" || json.Unmarshal([]byte(ref), &m) != nil {
		return ""
	}
	return m.ID
}
