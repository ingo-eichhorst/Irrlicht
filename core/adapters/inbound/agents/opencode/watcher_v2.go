package opencode

import (
	"database/sql"
	"errors"
	"log"
	"path/filepath"
	"time"

	"irrlicht/core/domain/agent"
)

// The opencode 2.x reader (#2106). A 2.x store keeps sessions in `session_v2`
// and their history in `session_message`: one row per user prompt, one per
// assistant step, and one `idle` row when the turn ends. An assistant row is
// written when its step starts and REWRITTEN IN PLACE as the step streams —
// same id, same seq, a later time_updated (measured on the sandbox store with
// `select type, count(*), sum(time_updated>time_created),
// max(time_updated-time_created) from session_message group by type`: 36 of
// 36 assistant rows rewritten, by up to 319519 ms). So the
// activity cursor is (time_updated, id), the 1.x part cursor's shape, and not
// seq, which would see each step once and miss every tool finishing in it.
//
// Admission differs from the 1.x reader in one way. A session is admitted on
// ACTIVITY — a session_message row inserted or rewritten after the watcher
// first saw the session — while a live client owns its directory; what the
// store already held at first sight is never activity. That is what keeps the
// 1.x rows a 2.x migration imports (version 1.19.4, time_idle NULL, no idle
// row) from being admitted on their imported state, without reading `version`:
// a row is either written to again or it is history. The cost, stated rather
// than discovered: after a daemon restart, a 2.x session idling under a live
// client is not shown until its next write.
//
// time_suspended is NOT a close. Sampled at 30 ms against the sandbox store
// while a TUI ran a turn, 2.x set it on the TUI's session 8 ms after creating
// it (created 1792954501603, suspended 1792954501611), held it through the
// turn, and cleared it at the idle (1792954508949). A reader that skipped
// suspended rows lost every TUI session for exactly the span it was working —
// and then first-saw it whole after the idle and never admitted it, which is
// what the dev daemon did (#2106). Only an archived or deleted row is gone.

// scanV2 is scanSessions for a 2.x store.
func (w *Watcher) scanV2(db *sql.DB) {
	sessions, err := w.queryV2Sessions(db)
	if err != nil {
		log.Printf("opencode: db.Query(session_v2): %v", err)
		return
	}

	live := w.liveCWDs()

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	listed := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		listed[s.id] = struct{}{}
		w.scanV2Session(db, s, live, now)
	}
	w.emitRemovedForGoneV2Sessions(db, listed)
	w.gcExpiredCursors()
}

// queryV2Sessions lists the 2.x sessions a client may be running: not
// archived, updated within maxAge (all when maxAge is 0). Suspended rows stay
// listed — see the time_suspended note above.
func (w *Watcher) queryV2Sessions(db *sql.DB) ([]sessionRow, error) {
	cutoff := int64(0)
	if w.maxAge > 0 {
		cutoff = time.Now().Add(-w.maxAge).UnixMilli()
	}
	rows, err := db.Query(`
		SELECT id, directory, time_updated, parent_id
		FROM session_v2
		WHERE time_archived IS NULL
		  AND time_updated >= ?
		ORDER BY time_updated DESC
		LIMIT 200
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []sessionRow
	for rows.Next() {
		if s, ok := w.scanSessionRow(rows); ok {
			sessions = append(sessions, s)
		}
	}
	return sessions, rows.Err()
}

// scanV2Session reads session s's new and rewritten messages and emits what
// they amount to. The first time s is seen, its messages only seed the
// cursor.
func (w *Watcher) scanV2Session(db *sql.DB, s sessionRow, live map[string]struct{}, now time.Time) {
	cur, known := w.cursors[s.id]
	if !known {
		cur = &sessionCursor{seenIDs: make(map[string]struct{})}
		w.cursors[s.id] = cur
	}
	cur.lastObserved = now

	acc, err := scanV2Messages(db, s.id, cur)
	switch {
	case err != nil:
		if !known {
			// An unseeded cursor would read the session's whole history as
			// activity on the next scan.
			delete(w.cursors, s.id)
		}
		return
	case !known, !acc.hasActivity:
		return
	}

	if !cur.emitted {
		if _, alive := live[s.directory]; !alive {
			return
		}
		w.broadcast(agent.Event{
			Type:            agent.EventNewSession,
			SessionID:       s.id,
			ProjectDir:      filepath.Base(s.directory),
			TranscriptPath:  w.transcriptPathFor(s.id),
			CWD:             s.directory,
			ParentSessionID: s.parentID,
		})
		cur.emitted = true
	}
	w.broadcast(agent.Event{
		Type:           agent.EventActivity,
		SessionID:      s.id,
		ProjectDir:     filepath.Base(s.directory),
		TranscriptPath: w.transcriptPathFor(s.id),
		CWD:            s.directory,
		Terminal:       acc.hasTerminal,
	})
}

// scanV2Messages folds sessionID's session_message rows written since cur
// into an accumulator and advances cur past them. An `idle` row ends the turn.
func scanV2Messages(db *sql.DB, sessionID string, cur *sessionCursor) (partScanAccumulator, error) {
	lastTS := cur.lastTS
	acc := partScanAccumulator{seenIDs: make(map[string]struct{})}
	rows, err := db.Query(`
		SELECT id, type, time_updated
		FROM session_message
		WHERE session_id = ?
		  AND time_updated >= ?
		ORDER BY time_updated ASC, id ASC
	`, sessionID, lastTS)
	if err != nil {
		return acc, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, typ string
		var timeUpdated int64
		if err := rows.Scan(&id, &typ, &timeUpdated); err != nil {
			return acc, err
		}
		if acc.observe(id, timeUpdated, cur, lastTS) && typ == v2TypeIdle {
			acc.hasTerminal = true
		}
	}
	if err := rows.Err(); err != nil {
		return acc, err
	}
	acc.updateCursor(cur, lastTS)
	return acc, nil
}

// emitRemovedForGoneV2Sessions removes every tracked session the scan did not
// list whose row is now archived or deleted. A session merely older than
// maxAge is not gone; gcExpiredCursors drops its cursor.
func (w *Watcher) emitRemovedForGoneV2Sessions(db *sql.DB, listed map[string]struct{}) {
	for id, cur := range w.cursors {
		if _, ok := listed[id]; ok {
			continue
		}
		gone, err := v2SessionGone(db, id)
		if err != nil || !gone {
			continue
		}
		if cur.emitted {
			w.broadcast(agent.Event{
				Type:           agent.EventRemoved,
				SessionID:      id,
				TranscriptPath: w.transcriptPathFor(id),
			})
		}
		delete(w.cursors, id)
	}
}

// v2SessionGone reports whether session id's row is archived or no longer in
// session_v2.
func v2SessionGone(db *sql.DB, id string) (bool, error) {
	var closed bool
	err := db.QueryRow(
		`SELECT time_archived IS NOT NULL FROM session_v2 WHERE id = ?`, id,
	).Scan(&closed)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return closed, err
}
