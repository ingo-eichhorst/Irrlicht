package opencode

import (
	"database/sql"
	"errors"
	"log"
	"time"
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
// first saw the session — while a live client owns its directory, or, for a
// session no client runs (one created through the service's HTTP API, the
// desktop app or the web app), while a 2.x service is up to host it (#2107);
// what the store already held at first sight is never activity. That is what
// keeps the 1.x rows a 2.x migration imports (version 1.19.4, time_idle NULL,
// no idle row) from being admitted on their imported state, without reading
// `version`: a row is either written to again or it is history. It is also
// what keeps a daemon start from admitting every session the service still
// lists — up to maxAge (5 days) of them. The cost, stated rather than
// discovered: after a daemon restart, an idle 2.x session is not shown until
// its next write.
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
	sessions, err := w.querySessions(db, v2SessionsQuery)
	if err != nil {
		log.Printf("opencode: db.Query(session_v2): %v", err)
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	admits := w.admissionOnce()
	listed := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		listed[s.id] = struct{}{}
		w.scanV2Session(db, s, admits, now)
	}
	w.emitRemovedForGoneV2Sessions(db, listed)
	w.gcExpiredCursors()
}

// v2SessionsQuery lists the 2.x sessions a client may be running: not
// archived, updated at or after its one argument (querySessions' maxAge
// cutoff). Suspended rows stay listed — see the time_suspended note above.
const v2SessionsQuery = `
	SELECT id, directory, time_updated, parent_id
	FROM session_v2
	WHERE time_archived IS NULL
	  AND time_updated >= ?
	ORDER BY time_updated DESC
	LIMIT 200
`

// admissionOnce returns the lookup scanV2Session admits a session with: a
// live client owns its directory, or else a 2.x service is up to host it.
// Each half enumerates processes at most once per scan, on first use: the 2.x
// reader needs them only to admit a session with new activity, which most
// scans have none of, and the client half is a process listing plus an argv
// and a cwd read per match, the service half a listing plus an argv read per
// match. A failed enumeration is not retried within the scan.
func (w *Watcher) admissionOnce() func(dir string) bool {
	var live map[string]struct{}
	liveRead, serviceRead, serviceUp := false, false, false
	return func(dir string) bool {
		if !liveRead {
			live, liveRead = w.liveCWDs(), true
		}
		if _, ok := live[dir]; ok {
			return true
		}
		if !serviceRead {
			serviceUp, serviceRead = w.serviceUp(), true
		}
		return serviceUp
	}
}

// scanV2Session reads session s's new and rewritten messages and emits what
// they amount to. The first time s is seen, its messages only seed the
// cursor. A session not yet admitted is admitted on activity only while
// admits answers yes for its directory.
func (w *Watcher) scanV2Session(db *sql.DB, s sessionRow, admits func(dir string) bool, now time.Time) {
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
		if !admits(s.directory) {
			return
		}
		w.emitNewSession(s)
		cur.emitted = true
	}
	w.emitActivity(s.id, s.directory, acc.hasTerminal)
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
			w.emitRemoved(id)
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
