package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite" // pure-Go SQLite driver; no CGo required

	"irrlicht/core/domain/agent"
)

// Watcher monitors the OpenCode SQLite database for new and updated sessions.
// It implements inbound.AgentWatcher.
//
// Detection strategy:
//  1. Watch the database WAL file (opencode.db-wal) via fsnotify — every
//     write by OpenCode flushes to the WAL, triggering a Write event.
//  2. On each WAL event, query the `session` table for rows updated since the
//     last poll to discover new sessions.
//  3. For each known session, query the `part` table for rows updated since
//     the last cursor to derive activity events and state transitions.
//  4. Query for recently archived sessions (time_archived IS NOT NULL) to
//     emit EventRemoved for sessions that were closed by the user.
//
// The watcher emits the same agent.Event types as the fswatcher:
//   - EventNewSession  — a new session row appeared in the DB
//   - EventActivity    — new/updated part rows in a known session
//   - EventRemoved     — a session row was archived or deleted
//
// TranscriptPath in all emitted events is set to the WAL file path
// (opencode.db-wal) rather than the main DB file. This ensures
// isStaleTranscript() checks the WAL — which is updated on every OpenCode
// write — rather than the main DB file which is only updated on checkpoint.
// The session ID is the OpenCode session UUID (ses_...).
type Watcher struct {
	dbPath  string        // absolute path to opencode.db
	adapter string        // always "opencode"
	maxAge  time.Duration // ignore sessions older than this (0 = no limit)

	subMu sync.Mutex
	subs  []chan agent.Event

	// per-session cursor: last part.time_updated and set of already-seen part
	// IDs at that timestamp. Using seenIDs alongside >= avoids missing parts
	// that share the same millisecond timestamp (cursor race condition).
	mu      sync.Mutex
	cursors map[string]*sessionCursor // session_id → cursor state

	// lastScan tracks when we last ran scanSessions so we can debounce
	// the fsnotify feedback loop: opening the DB read-only touches the
	// shared-memory file (.db-shm) which can trigger spurious Write events.
	lastScan   time.Time
	scanMu     sync.Mutex
	minScanGap time.Duration // minimum time between scans

	// lastArchivedCheck is the cut-off for querying newly archived sessions.
	lastArchivedCheck time.Time
}

// sessionCursor tracks the last-seen part timestamp and deduplicates parts
// at that timestamp to prevent a same-millisecond cursor race.
type sessionCursor struct {
	lastTS  int64
	seenIDs map[string]struct{} // part IDs already processed at lastTS
}

// New creates a Watcher for the OpenCode database relative to $HOME.
func New(maxAge time.Duration) *Watcher {
	home, _ := os.UserHomeDir()
	return &Watcher{
		dbPath:     filepath.Join(home, dbRelPath),
		adapter:    AdapterName,
		maxAge:     maxAge,
		cursors:    make(map[string]*sessionCursor),
		minScanGap: 500 * time.Millisecond,
	}
}

// NewWithDBPath creates a Watcher targeting a specific DB path.
// Intended for tests.
func NewWithDBPath(dbPath string, maxAge time.Duration) *Watcher {
	return &Watcher{
		dbPath:     dbPath,
		adapter:    AdapterName,
		maxAge:     maxAge,
		cursors:    make(map[string]*sessionCursor),
		minScanGap: 500 * time.Millisecond,
	}
}

// Root returns the watched database path (satisfies the interface used in
// main.go for logging the watcher roots).
func (w *Watcher) Root() string { return w.dbPath }

// Adapter returns the adapter name.
func (w *Watcher) Adapter() string { return w.adapter }

// Watch begins monitoring the OpenCode database. It blocks until ctx is
// cancelled.
func (w *Watcher) Watch(ctx context.Context) error {
	// Wait for the DB to exist before starting fsnotify.
	if err := w.waitForDB(ctx); err != nil {
		return err
	}

	// Initial scan: emit EventNewSession for existing sessions so the daemon
	// picks up sessions that were created before it started.
	// Small delay to ensure detector.Run() has called Subscribe() before we
	// broadcast the first batch of events — Watch() and detector.Run() start
	// concurrently as goroutines and Subscribe() must win the race.
	go func() {
		time.Sleep(200 * time.Millisecond)
		w.scanSessions()
	}()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("opencode watcher: create fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	// Watch the parent directory rather than the DB file directly. SQLite WAL
	// writes update opencode.db-wal; fsnotify on the directory catches both
	// the WAL and the main DB file for checkpoint events.
	dbDir := filepath.Dir(w.dbPath)
	if err := watcher.Add(dbDir); err != nil {
		return fmt.Errorf("opencode watcher: watch %s: %w", dbDir, err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			// React to writes to the DB or WAL file.
			if ev.Op&fsnotify.Write != 0 {
				name := filepath.Base(ev.Name)
				if name == "opencode.db" || name == "opencode.db-wal" {
					w.scanSessions()
				}
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
		}
	}
}

// Subscribe returns a channel that receives agent events.
func (w *Watcher) Subscribe() <-chan agent.Event {
	ch := make(chan agent.Event, 64)
	w.subMu.Lock()
	w.subs = append(w.subs, ch)
	w.subMu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel and closes it.
func (w *Watcher) Unsubscribe(ch <-chan agent.Event) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	for i, s := range w.subs {
		if s == ch {
			w.subs = append(w.subs[:i], w.subs[i+1:]...)
			close(s)
			return
		}
	}
}

// scanSessions queries the DB for session and part updates, emitting events
// to all subscribers. Debounced: calls closer together than minScanGap are
// dropped to prevent a feedback loop where our own read-only DB open triggers
// fsnotify Write events on the shared-memory file (.db-shm).
func (w *Watcher) scanSessions() {
	w.scanMu.Lock()
	if time.Since(w.lastScan) < w.minScanGap {
		w.scanMu.Unlock()
		return
	}
	w.lastScan = time.Now()
	w.scanMu.Unlock()
	db, err := sql.Open("sqlite", w.dbPath+"?mode=ro&_journal=WAL&_timeout=500")
	if err != nil {
		return
	}
	defer db.Close()

	// Query sessions updated within maxAge (or all if maxAge=0).
	// We use time_updated to find both new sessions and recently active ones.
	var rows *sql.Rows
	var queryErr error
	if w.maxAge > 0 {
		cutoff := time.Now().Add(-w.maxAge).UnixMilli()
		rows, queryErr = db.Query(`
			SELECT id, directory, time_updated, parent_id
			FROM session
			WHERE time_archived IS NULL
			  AND time_updated >= ?
			ORDER BY time_updated DESC
			LIMIT 200
		`, cutoff)
	} else {
		rows, queryErr = db.Query(`
			SELECT id, directory, time_updated, parent_id
			FROM session
			WHERE time_archived IS NULL
			ORDER BY time_updated DESC
			LIMIT 200
		`)
	}
	if queryErr != nil {
		return
	}
	defer rows.Close()

	type sessionRow struct {
		id          string
		directory   string
		timeUpdated int64
		parentID    string // empty string if NULL
	}
	var sessions []sessionRow
	for rows.Next() {
		var s sessionRow
		var parentID sql.NullString
		if err := rows.Scan(&s.id, &s.directory, &s.timeUpdated, &parentID); err != nil {
			continue
		}
		if parentID.Valid {
			s.parentID = parentID.String
		}
		if w.maxAge > 0 {
			age := time.Since(time.UnixMilli(s.timeUpdated))
			if age > w.maxAge {
				continue
			}
		}
		sessions = append(sessions, s)
	}
	rows.Close()

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, s := range sessions {
		cur, known := w.cursors[s.id]
		if !known {
			// New session — emit EventNewSession.
			// Encode the session ID into TranscriptPath so that the
			// MetricsProvider (opencode/metrics.go ComputeMetrics) can
			// extract it via parseTranscriptPath. The WAL suffix is
			// preserved so isStaleTranscript() checks the WAL (updated on
			// every OpenCode write) rather than the main DB (checkpoint-only).
			walPath := w.dbPath + "-wal" + "?session=" + s.id
			cur = &sessionCursor{seenIDs: make(map[string]struct{})}
			w.cursors[s.id] = cur
			w.broadcast(agent.Event{
				Type:            agent.EventNewSession,
				Adapter:         w.adapter,
				SessionID:       s.id,
				ProjectDir:      filepath.Base(s.directory),
				TranscriptPath:  walPath,
				CWD:             s.directory,
				ParentSessionID: s.parentID,
			})
		}

		// Scan for new parts since cursor.
		w.scanParts(db, s.id, s.directory, cur)
	}

	// Emit EventRemoved for sessions that were archived since the last scan.
	w.emitRemovedForArchivedSessions(db)
}

// emitRemovedForArchivedSessions queries for sessions whose time_archived
// column was set since the last check and emits EventRemoved for those
// that are tracked in our cursors map.
func (w *Watcher) emitRemovedForArchivedSessions(db *sql.DB) {
	if w.lastArchivedCheck.IsZero() {
		w.lastArchivedCheck = time.Now()
		return
	}
	cutoff := w.lastArchivedCheck.UnixMilli()
	w.lastArchivedCheck = time.Now()

	rows, err := db.Query(`
		SELECT id FROM session
		WHERE time_archived IS NOT NULL
		  AND time_archived > ?
		ORDER BY time_archived DESC
		LIMIT 200
	`, cutoff)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		cur, known := w.cursors[id]
		if !known {
			continue
		}
		walPath := w.dbPath + "-wal" + "?session=" + id
		w.broadcast(agent.Event{
			Type:           agent.EventRemoved,
			Adapter:        w.adapter,
			SessionID:      id,
			TranscriptPath: walPath,
		})
		delete(w.cursors, id)
		_ = cur
	}
}

// scanParts queries new/updated part rows for a session since lastCursor and
// emits EventActivity events. Updates the cursor on success.
//
// Uses >= (not >) on time_updated to avoid missing parts that share the same
// millisecond timestamp. A per-cursor set of already-seen part IDs prevents
// re-emitting the same parts on subsequent scans.
func (w *Watcher) scanParts(db *sql.DB, sessionID, directory string, cur *sessionCursor) {
	lastTS := int64(0)
	if cur != nil {
		lastTS = cur.lastTS
	}

	// Join with message to get the role context for each part.
	// Use >= rather than > to catch all parts at the last cursor timestamp
	// (same-ms race). We deduplicate via cur.seenIDs.
	rows, err := db.Query(`
		SELECT p.id, p.data, p.time_updated, m.data as message_data
		FROM part p
		JOIN message m ON p.message_id = m.id
		WHERE p.session_id = ?
		  AND p.time_updated >= ?
		ORDER BY p.time_updated ASC, p.id ASC
	`, sessionID, lastTS)
	if err != nil {
		return
	}
	defer rows.Close()

	var maxSeen int64
	hasActivity := false
	newSeenIDs := make(map[string]struct{})

	for rows.Next() {
		var partID, partData, msgData string
		var timeUpdated int64
		if err := rows.Scan(&partID, &partData, &timeUpdated, &msgData); err != nil {
			continue
		}
		// Skip parts already seen at the current cursor timestamp.
		if cur != nil && timeUpdated == lastTS {
			if _, seen := cur.seenIDs[partID]; seen {
				continue
			}
		}
		// Track for dedup when the timestamp moves forward.
		if timeUpdated > maxSeen {
			maxSeen = timeUpdated
			newSeenIDs = make(map[string]struct{})
		}
		if timeUpdated == maxSeen {
			newSeenIDs[partID] = struct{}{}
		}
		hasActivity = true
		_ = partData // activity signalled via EventActivity; parsing in metrics collector
		_ = msgData
	}

	if hasActivity && maxSeen > lastTS {
		cur.lastTS = maxSeen
		cur.seenIDs = newSeenIDs
	} else if hasActivity && maxSeen == lastTS && maxSeen != 0 {
		// Same timestamp — merge new IDs into the existing set.
		for id := range newSeenIDs {
			cur.seenIDs[id] = struct{}{}
		}
	}

	if hasActivity {
		w.broadcast(agent.Event{
			Type:           agent.EventActivity,
			Adapter:        w.adapter,
			SessionID:      sessionID,
			ProjectDir:     filepath.Base(directory),
			TranscriptPath: w.dbPath + "-wal" + "?session=" + sessionID,
			CWD:            directory,
		})
	}
}

// broadcast sends an event to all subscribers (non-blocking, drops on full).
func (w *Watcher) broadcast(ev agent.Event) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// waitForDB blocks until the OpenCode database file exists or ctx is cancelled.
func (w *Watcher) waitForDB(ctx context.Context) error {
	if _, err := os.Stat(w.dbPath); err == nil {
		return nil
	}

	// Watch the parent directory for the DB file to appear.
	dbDir := filepath.Dir(w.dbPath)
	if _, err := os.Stat(dbDir); err != nil {
		// XDG data dir doesn't exist yet — poll with a ticker.
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				if _, err := os.Stat(w.dbPath); err == nil {
					return nil
				}
			}
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(dbDir); err != nil {
		return err
	}
	if _, err := os.Stat(w.dbPath); err == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&fsnotify.Create != 0 {
				if filepath.Base(ev.Name) == "opencode.db" {
					return nil
				}
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
		}
	}
}
