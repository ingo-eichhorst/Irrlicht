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
//
// The watcher emits the same agent.Event types as the fswatcher:
//   - EventNewSession  — a new session row appeared in the DB
//   - EventActivity    — new/updated part rows in a known session
//   - EventRemoved     — a session row was deleted (or archived)
//
// TranscriptPath in all emitted events is set to the DB file path so the
// daemon's tailer infrastructure can address the session. The session ID is
// the OpenCode session UUID (ses_...).
type Watcher struct {
	dbPath  string        // absolute path to opencode.db
	adapter string        // always "opencode"
	maxAge  time.Duration // ignore sessions older than this (0 = no limit)

	subMu sync.Mutex
	subs  []chan agent.Event

	// per-session cursor: last part.time_updated seen, keyed by session ID.
	mu      sync.Mutex
	cursors map[string]int64 // session_id → last time_updated ms
}

// New creates a Watcher for the OpenCode database relative to $HOME.
func New(maxAge time.Duration) *Watcher {
	home, _ := os.UserHomeDir()
	return &Watcher{
		dbPath:  filepath.Join(home, dbRelPath),
		adapter: AdapterName,
		maxAge:  maxAge,
		cursors: make(map[string]int64),
	}
}

// NewWithDBPath creates a Watcher targeting a specific DB path.
// Intended for tests.
func NewWithDBPath(dbPath string, maxAge time.Duration) *Watcher {
	return &Watcher{
		dbPath:  dbPath,
		adapter: AdapterName,
		maxAge:  maxAge,
		cursors: make(map[string]int64),
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
	w.scanSessions()

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
// to all subscribers. Errors opening the DB are silently ignored (the DB may
// be locked by a WAL checkpoint).
func (w *Watcher) scanSessions() {
	db, err := sql.Open("sqlite", w.dbPath+"?mode=ro&_journal=WAL&_timeout=500")
	if err != nil {
		return
	}
	defer db.Close()

	// Query sessions updated since our last known cursor per session.
	// We use time_updated to find both new sessions and recently active ones.
	rows, err := db.Query(`
		SELECT id, directory, time_updated
		FROM session
		WHERE time_archived IS NULL
		ORDER BY time_updated DESC
		LIMIT 200
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	type sessionRow struct {
		id          string
		directory   string
		timeUpdated int64
	}
	var sessions []sessionRow
	for rows.Next() {
		var s sessionRow
		if err := rows.Scan(&s.id, &s.directory, &s.timeUpdated); err != nil {
			continue
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
		cursor, known := w.cursors[s.id]
		if !known {
			// New session — emit EventNewSession.
			w.cursors[s.id] = 0
			w.broadcast(agent.Event{
				Type:           agent.EventNewSession,
				Adapter:        w.adapter,
				SessionID:      s.id,
				ProjectDir:     filepath.Base(s.directory),
				TranscriptPath: w.dbPath,
			})
		}

		// Scan for new parts since cursor.
		w.scanParts(db, s.id, s.directory, cursor)
	}
}

// scanParts queries new/updated part rows for a session since lastCursor and
// emits EventActivity events. Updates the cursor on success.
func (w *Watcher) scanParts(db *sql.DB, sessionID, directory string, lastCursor int64) {
	// Join with message to get the role context for each part.
	rows, err := db.Query(`
		SELECT p.id, p.data, p.time_updated, m.data as message_data
		FROM part p
		JOIN message m ON p.message_id = m.id
		WHERE p.session_id = ?
		  AND p.time_updated > ?
		ORDER BY p.time_updated ASC
	`, sessionID, lastCursor)
	if err != nil {
		return
	}
	defer rows.Close()

	var maxSeen int64
	hasActivity := false

	for rows.Next() {
		var partID, partData, msgData string
		var timeUpdated int64
		if err := rows.Scan(&partID, &partData, &timeUpdated, &msgData); err != nil {
			continue
		}
		if timeUpdated > maxSeen {
			maxSeen = timeUpdated
		}
		hasActivity = true
		_ = partData // activity signalled via EventActivity; parsing in metrics collector
		_ = msgData
	}

	if hasActivity && maxSeen > lastCursor {
		w.cursors[sessionID] = maxSeen
		w.broadcast(agent.Event{
			Type:           agent.EventActivity,
			Adapter:        w.adapter,
			SessionID:      sessionID,
			ProjectDir:     filepath.Base(directory),
			TranscriptPath: w.dbPath,
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
