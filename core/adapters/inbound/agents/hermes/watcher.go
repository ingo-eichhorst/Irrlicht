package hermes

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite" // pure-Go SQLite driver; no CGo required

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
)

const (
	// defaultMinScanGap debounces the fsnotify feedback loop: opening the
	// store read-only touches it, which can trigger a further Write event.
	defaultMinScanGap = 500 * time.Millisecond

	// defaultPollInterval is the backstop scan cadence. fsnotify on the
	// store file is the primary trigger, but Hermes runs the store in
	// journal_mode=delete, and SQLite's delete-mode commit path replaces
	// the journal rather than always rewriting the main file in a way every
	// platform reports identically. A slow poll guarantees liveness even if
	// a write lands without a usable notification.
	defaultPollInterval = 2 * time.Second

	// dbWaitInterval is how often Watch re-checks for a not-yet-created
	// store. A user who has never run Hermes has no state.db at all.
	dbWaitInterval = 5 * time.Second
)

// Watcher monitors the Hermes Agent SQLite store for new and updated
// sessions. It implements inbound.Watcher.
//
// Detection strategy:
//  1. Watch the store file (state.db) via fsnotify, plus a slow poll backstop.
//  2. On each trigger, query `sessions` for coding-surface rows started
//     within maxAge to discover new sessions.
//  3. For each known session, compare message_count against the last-seen
//     value to derive activity, and read the newest message's finish_reason
//     to mark a turn terminal.
//  4. A session whose ended_at becomes non-NULL emits one final terminal
//     activity event.
//
// TranscriptPath on every emitted event is "<storePath>?session=<id>" so
// ComputeMetrics can recover the session ID. Unlike opencode there is no WAL
// file to point at: Hermes runs journal_mode=delete (verified), so the main
// store file is what changes on commit and what staleness checks should read.
type Watcher struct {
	dbPath   string
	adapter  string
	identity agent.Identity
	maxAge   time.Duration

	subMu sync.Mutex
	subs  []chan agent.Event

	mu       sync.Mutex
	sessions map[string]*sessionCursor

	scanMu     sync.Mutex
	lastScan   time.Time
	minScanGap time.Duration

	pollInterval time.Duration

	// liveCWD resolves the working directory of a live Hermes session
	// process. It exists because `sessions.cwd` is populated only for TUI
	// sessions — every caller of Hermes' update_session_cwd lives in its
	// tui_gateway — so a `source='cli'` row carries no project binding of
	// its own. Overridable from tests.
	liveCWD func() string
}

// sessionCursor is the per-session state carried between scans.
type sessionCursor struct {
	emitted      bool
	messageCount int64
	ended        bool

	// lastObserved is the wall-clock time of the most recent scan that saw
	// this session, used to prune the map. Without it the daemon retains one
	// entry per Hermes session ever observed, for its whole lifetime.
	lastObserved time.Time
}

// New creates a Watcher for the store at $HERMES_HOME/state.db.
func New(maxAge time.Duration) *Watcher {
	return NewWithStorePath(StorePath(), maxAge)
}

// NewWithStorePath creates a Watcher targeting a specific store path.
// Intended for tests.
func NewWithStorePath(dbPath string, maxAge time.Duration) *Watcher {
	return &Watcher{
		dbPath:       dbPath,
		adapter:      AdapterName,
		maxAge:       maxAge,
		sessions:     make(map[string]*sessionCursor),
		minScanGap:   defaultMinScanGap,
		pollInterval: defaultPollInterval,
		liveCWD:      defaultLiveCWD,
	}
}

// defaultLiveCWD returns the CWD of the single live Hermes session process,
// or "" when there is none or more than one (an ambiguous binding is worse
// than no binding — attributing a session to the wrong project is a silent
// error the user cannot see).
func defaultLiveCWD() string {
	cwds, err := processlifecycle.LiveCWDsByCmdline(processCmdPattern, isServiceArgv)
	if err != nil || len(cwds) != 1 {
		return ""
	}
	for cwd := range cwds {
		return cwd
	}
	return ""
}

// Root returns the watched store path.
func (w *Watcher) Root() string { return w.dbPath }

// Adapter returns the adapter name.
func (w *Watcher) Adapter() string { return w.adapter }

// WithIdentity sets the full agent.Identity so the watcher satisfies
// inbound.Watcher. Returns the watcher for chaining.
func (w *Watcher) WithIdentity(id agent.Identity) *Watcher {
	w.identity = id
	return w
}

// Identity returns the agent.Identity supplied via WithIdentity.
func (w *Watcher) Identity() agent.Identity { return w.identity }

// Subscribe returns a channel receiving agent events.
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

// emit broadcasts an event to all subscribers, dropping it for any
// subscriber whose buffer is full rather than blocking the scan loop.
func (w *Watcher) emit(ev agent.Event) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- ev:
		default:
			log.Printf("hermes: subscriber channel full, dropping %s for %s",
				ev.Type, ev.SessionID)
		}
	}
}

// Watch monitors the store until ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) error {
	if err := w.waitForStore(ctx); err != nil {
		return err
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("hermes: fsnotify unavailable, falling back to polling: %v", err)
	} else {
		defer fsw.Close()
		// Watch the containing directory rather than the file: SQLite's
		// delete-mode commit path replaces the journal file next to the
		// store, and a directory watch survives an inode swap that a
		// file watch would silently stop reporting on.
		if err := fsw.Add(filepath.Dir(w.dbPath)); err != nil {
			log.Printf("hermes: watch %s: %v", filepath.Dir(w.dbPath), err)
		}
	}

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.scan()

	var fsEvents chan fsnotify.Event
	var fsErrors chan error
	if fsw != nil {
		fsEvents, fsErrors = fsw.Events, fsw.Errors
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.scan()
		case ev, ok := <-fsEvents:
			if !ok {
				fsEvents = nil
				continue
			}
			if filepath.Base(ev.Name) == filepath.Base(w.dbPath) {
				w.scan()
			}
		case err, ok := <-fsErrors:
			if !ok {
				fsErrors = nil
				continue
			}
			log.Printf("hermes: fsnotify error: %v", err)
		}
	}
}

// waitForStore blocks until the store file exists or ctx is cancelled.
func (w *Watcher) waitForStore(ctx context.Context) error {
	for {
		if _, err := os.Stat(w.dbPath); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dbWaitInterval):
		}
	}
}

// scan debounces and runs one pass over the store.
func (w *Watcher) scan() {
	w.scanMu.Lock()
	if time.Since(w.lastScan) < w.minScanGap {
		w.scanMu.Unlock()
		return
	}
	w.lastScan = time.Now()
	w.scanMu.Unlock()

	db, err := openReadOnly(w.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	w.scanSessions(db)
}

// storeSessionRow is one row of the sessions table as the watcher reads it.
type storeSessionRow struct {
	id           string
	cwd          string
	parentID     string
	messageCount int64
	ended        bool
}

// scanSessions emits new-session and activity events for coding-surface
// sessions started within maxAge.
func (w *Watcher) scanSessions(db *sql.DB) {
	args := []interface{}{}
	query := `
		SELECT id, COALESCE(cwd,''), COALESCE(parent_session_id,''),
		       COALESCE(message_count,0), ended_at IS NOT NULL
		FROM sessions
		WHERE source IN ('cli','tui')`
	if w.maxAge > 0 {
		query += ` AND started_at > ?`
		args = append(args, float64(time.Now().Add(-w.maxAge).UnixNano())/1e9)
	}
	query += ` ORDER BY started_at DESC LIMIT 200`

	rows, err := db.Query(query, args...)
	if err != nil {
		log.Printf("hermes: query sessions: %v", err)
		return
	}
	defer rows.Close()

	var found []storeSessionRow
	for rows.Next() {
		var r storeSessionRow
		if err := rows.Scan(&r.id, &r.cwd, &r.parentID, &r.messageCount, &r.ended); err != nil {
			log.Printf("hermes: scan session row: %v", err)
			continue
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		log.Printf("hermes: iterate sessions: %v", err)
	}

	// Resolve the live-process CWD at most ONCE per scan, and only if some
	// row actually needs it. Doing it per row shelled out to pgrep + lsof
	// for every CLI session in the window on every 2s tick — at the LIMIT
	// 200 ceiling the scan outran the poll interval.
	fallbackCWD := ""
	if w.liveCWD != nil && anyRowNeedsCWD(found) {
		fallbackCWD = w.liveCWD()
	}

	for _, r := range found {
		w.reconcile(db, r, fallbackCWD)
	}
	w.gcExpiredCursors(found)
}

// anyRowNeedsCWD reports whether some row lacks a stored cwd, which is the
// only case the live-process fallback is consulted for.
func anyRowNeedsCWD(rows []storeSessionRow) bool {
	for _, r := range rows {
		if r.cwd == "" {
			return true
		}
	}
	return false
}

// gcExpiredCursors drops cursors for sessions no longer returned by the
// scan. Safe because the scan filter is `started_at > cutoff` and
// started_at is immutable: a session that ages out can never re-enter the
// window, so a dropped cursor can never cause a duplicate new_session.
func (w *Watcher) gcExpiredCursors(found []storeSessionRow) {
	seen := make(map[string]struct{}, len(found))
	for _, r := range found {
		seen[r.id] = struct{}{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.sessions {
		if _, ok := seen[id]; !ok {
			delete(w.sessions, id)
		}
	}
}

// reconcile turns the delta between a session row and its cursor into events.
func (w *Watcher) reconcile(db *sql.DB, r storeSessionRow, fallbackCWD string) {
	w.mu.Lock()
	cur, known := w.sessions[r.id]
	if !known {
		cur = &sessionCursor{}
		w.sessions[r.id] = cur
	}
	prevCount, prevEnded, emitted := cur.messageCount, cur.ended, cur.emitted
	cur.messageCount, cur.ended, cur.emitted = r.messageCount, r.ended, true
	cur.lastObserved = time.Now()
	w.mu.Unlock()

	cwd := r.cwd
	if cwd == "" {
		cwd = fallbackCWD
	}

	base := agent.Event{
		SessionID:       r.id,
		TranscriptPath:  w.dbPath + sessionQueryParam + r.id,
		CWD:             cwd,
		ProjectDir:      filepath.Base(cwd),
		ParentSessionID: r.parentID,
	}
	if cwd == "" {
		base.ProjectDir = ""
	}

	if !emitted {
		ev := base
		ev.Type = agent.EventNewSession
		w.emit(ev)
	}

	// An activity event is warranted when the conversation grew, or when the
	// session just closed (the closing write may not add a message).
	//
	// On first discovery prevCount is 0, so a session that already has
	// messages would pair new_session with one activity. That pairing is
	// deliberate for a session that is still LIVE: new_session only
	// announces it, while the activity is what tells the daemon to read the
	// store and derive state — without it a session discovered mid-flight (a
	// daemon restart, or consent granted while Hermes is already running)
	// would sit stateless until the user's next message.
	//
	// It is NOT warranted for a session that was already finished when we
	// first saw it. Those are history: new_session alone carries them, and
	// their state is settled. Pairing them doubled the cold-start burst —
	// one scan over a store with N sessions in the window emitted 2N events
	// into a 64-slot channel that drops silently when full, which is the
	// most likely cause of a concurrent session intermittently never
	// surfacing (observed once against a store with 23 sessions in window).
	grew := r.messageCount > prevCount
	justEnded := r.ended && !prevEnded
	coldStartHistory := !emitted && r.ended
	if coldStartHistory || (!grew && !justEnded) {
		return
	}

	ev := base
	ev.Type = agent.EventActivity
	ev.Terminal = justEnded || w.turnIsDone(db, r.id)
	w.emit(ev)
}

// turnIsDone reports whether the newest message closes the turn.
//
// finish_reason='stop' on the newest assistant row is Hermes' explicit
// end-of-turn marker; 'tool_calls' means the agent is still working. This is
// what lets the adapter settle a session without an idle-timeout heuristic.
func (w *Watcher) turnIsDone(db *sql.DB, sessionID string) bool {
	var role, finish sql.NullString
	err := db.QueryRow(`
		SELECT role, finish_reason FROM messages
		WHERE session_id = ? AND active = 1
		ORDER BY timestamp DESC, id DESC LIMIT 1`, sessionID).Scan(&role, &finish)
	if err != nil {
		return false
	}
	return role.String == "assistant" && finish.String == "stop"
}
