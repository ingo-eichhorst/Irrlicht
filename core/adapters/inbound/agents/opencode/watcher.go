package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite" // pure-Go SQLite driver; no CGo required

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/sqlitero"
)

const defaultMinScanGap = 500 * time.Millisecond

// sessionQueryParam is the query-string key appended to the WAL path so
// MetricsProvider (opencode/metrics.go ComputeMetrics) can recover the
// session ID via parseTranscriptPath.
const sessionQueryParam = "?session="

// Watcher monitors the OpenCode SQLite database for new and updated sessions.
// It implements inbound.Watcher.
//
// Detection strategy:
//  1. Watch the database WAL file (opencode.db-wal) via fsnotify — every
//     write by OpenCode flushes to the WAL, triggering a Write event.
//  2. On each WAL event, read the store's schema (storeSchema): an opencode
//     2.x store carrying `session_v2` is read by scanV2 (watcher_v2.go), and
//     everything below describes the 1.x reader.
//  3. Query the `session` table for rows updated since the last poll to
//     discover new sessions.
//  4. For each known session, query the `part` table for rows updated since
//     the last cursor to derive activity events and state transitions.
//  5. Query for recently archived sessions (time_archived IS NOT NULL) to
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
	dbPath   string         // absolute path to opencode.db
	adapter  string         // always "opencode"
	identity agent.Identity // populated via WithIdentity
	maxAge   time.Duration  // ignore sessions older than this (0 = no limit)

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

	// liveCWDs returns the set of directories currently owned by a live
	// opencode client. Defaults to LiveClientCWDs; overridable from tests.
	liveCWDs func() map[string]struct{}

	// sawV2Store is the schema the previous scan saw: true once the store
	// carries `session_v2` (#2105). Read and written only by storeSchema, on
	// scanSessions' goroutine, so the reader switch is logged on a transition
	// rather than on every scan.
	sawV2Store bool
}

// sessionCursor tracks the last-seen part timestamp and deduplicates parts
// at that timestamp to prevent a same-millisecond cursor race.
type sessionCursor struct {
	lastTS  int64
	seenIDs map[string]struct{} // part IDs already processed at lastTS

	// emitted is true once EventNewSession has been broadcast for this
	// session. Sessions tracked but not emitted are awaiting a live
	// opencode process to own their CWD.
	emitted bool

	// lastObserved is the wall-clock time of the most recent scan that saw
	// this session. Drives gcExpiredCursors independently of lastTS, which
	// only advances on new parts (a session bumped without new parts would
	// otherwise look stale).
	lastObserved time.Time
}

// transcriptPathFor renders this watcher's own spelling of a session key.
//
// A method on the watcher rather than a call to the package-level
// transcriptPathFor, because a watcher built with NewWithDBPath points at a
// scratch database and must key sessions on THAT one; the package-level
// function is what a caller with no watcher in hand (the hook receiver) uses,
// and New() below makes the two agree by resolving through the same StorePath.
func (w *Watcher) transcriptPathFor(sessionID string) string {
	return w.dbPath + "-wal" + sessionQueryParam + sessionID
}

// New creates a Watcher for the OpenCode database relative to $HOME.
func New(maxAge time.Duration) *Watcher {
	return &Watcher{
		dbPath:            StorePath(),
		adapter:           AdapterName,
		maxAge:            maxAge,
		cursors:           make(map[string]*sessionCursor),
		minScanGap:        defaultMinScanGap,
		lastArchivedCheck: time.Now(),
		liveCWDs:          defaultLiveCWDs,
	}
}

// NewWithDBPath creates a Watcher targeting a specific DB path.
// Intended for tests.
func NewWithDBPath(dbPath string, maxAge time.Duration) *Watcher {
	return &Watcher{
		dbPath:            dbPath,
		adapter:           AdapterName,
		maxAge:            maxAge,
		cursors:           make(map[string]*sessionCursor),
		minScanGap:        defaultMinScanGap,
		lastArchivedCheck: time.Now(),
		liveCWDs:          defaultLiveCWDs,
	}
}

// defaultLiveCWDs returns the set of CWDs currently held by live opencode
// clients. Failures are logged and treated as "no live processes" — the
// scan continues but won't surface ghost sessions.
func defaultLiveCWDs() map[string]struct{} {
	set, err := LiveClientCWDs()
	if err != nil {
		log.Printf("opencode: LiveCWDs: %v", err)
		return nil
	}
	return set
}

// Root returns the watched database path (satisfies the interface used in
// main.go for logging the watcher roots).
func (w *Watcher) Root() string { return w.dbPath }

// Adapter returns the adapter name.
func (w *Watcher) Adapter() string { return w.adapter }

// WithIdentity sets the full agent.Identity for this watcher so it
// satisfies inbound.Watcher. Returns the watcher for chaining.
func (w *Watcher) WithIdentity(id agent.Identity) *Watcher {
	w.identity = id
	return w
}

// Identity returns the agent.Identity supplied via WithIdentity, or the
// zero value if WithIdentity was never called.
func (w *Watcher) Identity() agent.Identity {
	return w.identity
}

// Watch begins monitoring the OpenCode database. It blocks until ctx is
// cancelled.
func (w *Watcher) Watch(ctx context.Context) error {
	// Wait for the DB to exist before starting fsnotify.
	if err := w.waitForDB(ctx); err != nil {
		return err
	}

	// Initial scan: emit EventNewSession for existing sessions so the daemon
	// picks up sessions that were created before it started.
	// By the time Watch() is called, Subscribe() has already been called by
	// the detector, so the inline scan safely reaches registered subscribers.
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
				dbFile := filepath.Base(w.dbPath)
				if name == dbFile || name == dbFile+"-wal" {
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

// sessionRow is a single row read from the `session` table by querySessions.
type sessionRow struct {
	id          string
	directory   string
	timeUpdated int64
	parentID    string // empty string if NULL
}

// scanSessions queries the DB for session and part updates, emitting events
// to all subscribers. Debounced: calls closer together than minScanGap are
// dropped to prevent a feedback loop where our own read-only DB open triggers
// fsnotify Write events on the shared-memory file (.db-shm).
func (w *Watcher) scanSessions() {
	if !w.shouldScan() {
		return
	}
	w.lastScan = time.Now()

	db, err := sqlitero.Open(w.dbPath)
	if err != nil {
		log.Printf("opencode: open store read-only: %v", err)
		return
	}
	defer db.Close()

	isV2, ok := w.storeSchema(db)
	if !ok {
		return
	}
	if isV2 {
		w.scanV2(db)
		return
	}

	sessions, err := w.querySessions(db, v1SessionsQuery)
	if err != nil {
		log.Printf("opencode: db.Query(session): %v", err)
		return
	}

	// Gate EventNewSession on a live opencode process owning the session's
	// CWD — otherwise every non-archived row in the DB would be re-emitted
	// as live on each daemon start.
	live := w.liveCWDs()

	w.mu.Lock()
	defer w.mu.Unlock()

	scanWallClock := time.Now()
	for _, s := range sessions {
		cur := w.sessionCursorFor(s, scanWallClock)
		w.maybeEmitNewSession(s, cur, live)
		w.scanParts(db, s.id, s.directory, cur)
	}

	w.emitRemovedForArchivedSessions(db)
	w.gcExpiredCursors()
}

// storeSchema reports which reader this scan runs — the 2.x reader when the
// store carries `session_v2`, the 1.x reader otherwise — logging only when
// that answer changes. ok is false when the schema could not be read.
//
// The 1.x reader never runs against a 2.x store (#2105): a FRESH 2.x store has
// no `session`, `message` or `part` table, and a MIGRATED one keeps them
// frozen, so 1.x queries there either fail on every scan or admit 1.x rows as
// live whenever an `opencode` process sits in their directory. The schema is
// read on every scan because a running daemon sees a 1.x store become a 2.x
// one when opencode 2.x first opens it.
//
// A schema lookup that fails skips this scan rather than guessing: a store the
// watcher cannot classify is the last one to run either reader against. The
// next WAL write scans again.
func (w *Watcher) storeSchema(db *sql.DB) (isV2, ok bool) {
	isV2, err := storeIsV2(db)
	if err != nil {
		log.Printf("opencode: read store schema: %v — skipping this scan", err)
		return false, false
	}
	if isV2 != w.sawV2Store {
		w.sawV2Store = isV2
		if isV2 {
			log.Printf("opencode: %s has session_v2 (an opencode 2.x store) — reading it with the 2.x reader", w.dbPath)
		} else {
			log.Printf("opencode: %s no longer has session_v2 — the 1.x reader resumes", w.dbPath)
		}
		// A cursor is a position in one reader's tables; carried across a
		// switch it would read the other reader's rows against it.
		w.mu.Lock()
		w.cursors = make(map[string]*sessionCursor)
		w.mu.Unlock()
	}
	return isV2, true
}

// storeIsV2 reports whether the opencode store behind db was written by
// opencode 2.x, which is the one fact the 1.x reader needs: whether its tables
// still mean what it thinks. `session_v2` is the table 2.x introduced; it is
// present in a fresh 2.x store and in a 1.x store 2.x migrated in place, and
// absent from every 1.x store.
func storeIsV2(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_v2'`,
	).Scan(&n)
	return n > 0, err
}

// shouldScan reports whether enough time has passed since the last scan to
// run another one, debouncing the fsnotify feedback loop described in
// scanSessions' doc comment.
func (w *Watcher) shouldScan() bool {
	w.scanMu.Lock()
	defer w.scanMu.Unlock()
	return time.Since(w.lastScan) >= w.minScanGap
}

// v1SessionsQuery lists the 1.x sessions updated at or after its one
// argument, a cutoff in epoch ms (0 for no limit).
const v1SessionsQuery = `
	SELECT id, directory, time_updated, parent_id
	FROM session
	WHERE time_archived IS NULL
	  AND time_updated >= ?
	ORDER BY time_updated DESC
	LIMIT 200
`

// querySessions runs a sessions query — v1SessionsQuery or v2SessionsQuery —
// with the maxAge cutoff (none when maxAge is 0) and scans the matching rows.
// time_updated finds both new sessions and recently active ones.
func (w *Watcher) querySessions(db *sql.DB, query string) ([]sessionRow, error) {
	cutoff := int64(0)
	if w.maxAge > 0 {
		cutoff = time.Now().Add(-w.maxAge).UnixMilli()
	}
	rows, err := db.Query(query, cutoff)
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

// scanSessionRow reads one session row. Applies the maxAge cutoff a second
// time per-row (time_updated can have advanced since the query ran, and a
// zero maxAge means "no limit" so the query itself has no cutoff to apply).
// Returns ok=false on a scan error or a row that has aged out.
func (w *Watcher) scanSessionRow(rows *sql.Rows) (sessionRow, bool) {
	var s sessionRow
	var parentID sql.NullString
	if err := rows.Scan(&s.id, &s.directory, &s.timeUpdated, &parentID); err != nil {
		log.Printf("opencode: rows.Scan(session): %v", err)
		return sessionRow{}, false
	}
	if parentID.Valid {
		s.parentID = parentID.String
	}
	if w.maxAge > 0 && time.Since(time.UnixMilli(s.timeUpdated)) > w.maxAge {
		return sessionRow{}, false
	}
	return s, true
}

// sessionCursorFor returns the cursor tracking session s, creating and
// seeding one on first sight so a later live-transition's scanParts doesn't
// back-fill historical parts as fresh activity.
func (w *Watcher) sessionCursorFor(s sessionRow, observedAt time.Time) *sessionCursor {
	cur, known := w.cursors[s.id]
	if !known {
		cur = &sessionCursor{
			lastTS:  s.timeUpdated,
			seenIDs: make(map[string]struct{}),
		}
		w.cursors[s.id] = cur
	}
	cur.lastObserved = observedAt
	return cur
}

// maybeEmitNewSession broadcasts EventNewSession the first time session s is
// seen owned by a live opencode process.
func (w *Watcher) maybeEmitNewSession(s sessionRow, cur *sessionCursor, live map[string]struct{}) {
	if cur.emitted {
		return
	}
	if _, alive := live[s.directory]; !alive {
		cur.lastTS = s.timeUpdated
		return
	}
	w.emitNewSession(s)
	cur.emitted = true
}

// emitNewSession, emitActivity and emitRemoved build the three events both
// readers emit, so the payload has one spelling. TranscriptPath encodes the
// session ID so MetricsProvider (opencode/metrics.go ComputeMetrics) can
// extract it via parseTranscriptPath; its WAL suffix routes
// isStaleTranscript() at the WAL (updated on every write), not the
// checkpoint-only main DB.
func (w *Watcher) emitNewSession(s sessionRow) {
	w.broadcast(agent.Event{
		Type:            agent.EventNewSession,
		SessionID:       s.id,
		ProjectDir:      filepath.Base(s.directory),
		TranscriptPath:  w.transcriptPathFor(s.id),
		CWD:             s.directory,
		ParentSessionID: s.parentID,
	})
}

func (w *Watcher) emitActivity(sessionID, directory string, terminal bool) {
	w.broadcast(agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sessionID,
		ProjectDir:     filepath.Base(directory),
		TranscriptPath: w.transcriptPathFor(sessionID),
		CWD:            directory,
		Terminal:       terminal,
	})
}

func (w *Watcher) emitRemoved(sessionID string) {
	w.broadcast(agent.Event{
		Type:           agent.EventRemoved,
		SessionID:      sessionID,
		TranscriptPath: w.transcriptPathFor(sessionID),
	})
}

// gcExpiredCursors drops cursor entries whose most recent observation
// predates the maxAge window — those sessions cannot reappear in the
// SQL query, so retaining the cursor is pure waste. Bounds map growth
// for users who accumulate many OpenCode sessions over time.
func (w *Watcher) gcExpiredCursors() {
	if w.maxAge <= 0 {
		return
	}
	cutoff := time.Now().Add(-w.maxAge)
	for id, cur := range w.cursors {
		if !cur.lastObserved.IsZero() && cur.lastObserved.Before(cutoff) {
			delete(w.cursors, id)
		}
	}
}

// emitRemovedForArchivedSessions queries for sessions whose time_archived
// column was set since the last check and emits EventRemoved for those
// that are tracked in our cursors map.
//
// On the first call after daemon start, the cutoff is seeded to "now" and we
// return early — pre-startup archives are intentionally ignored here because
// the cursors map is empty at that point (every emit gates on `if !known`).
// Carryover archives from a previous daemon run are handled separately by
// the DB-backed-orphan branch in PIDManager.CleanupZombies, which operates
// on persisted state files rather than this in-memory cursor set.
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
		_, known := w.cursors[id]
		if !known {
			continue
		}
		w.emitRemoved(id)
		delete(w.cursors, id)
	}
}

// isTerminalPart reports whether a part JSON blob is a step-finish with a
// turn-ending reason (stop / interrupted / length / error). Returns false for
// tool-calls pauses or any parse failure — fail-open.
func isTerminalPart(data string) bool {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return false
	}
	if t, _ := raw["type"].(string); t != "step-finish" {
		return false
	}
	switch reason, _ := raw["reason"].(string); reason {
	case "stop", "interrupted", "length", "error", "content-filter":
		return true
	}
	return false
}

// isErrorMessage reports whether a message JSON blob carries a non-empty
// `error`. OpenCode records a turn that aborted (provider/runtime error,
// quota, context overflow) on message.data.error rather than emitting a
// step-finish part with reason="error", so for such turns the message-level
// error is the only signal that the turn has ended — without it the session
// sticks in `working` until the process exits. (#493)
//
// Delegates the shape rules to sessionErrorFrom so this predicate and the
// parser's `_error` branch cannot drift: before #1800 they were two
// independent readings of the same field and disagreed on `{}` and on
// non-error shapes, which meant a replay golden and the live daemon could
// classify one session two ways.
func isErrorMessage(data string) bool {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return false
	}
	return sessionErrorFrom(raw["error"]) != nil
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

	acc := partScanAccumulator{seenIDs: make(map[string]struct{})}
	for rows.Next() {
		acc.scanRow(rows, cur, lastTS)
	}
	acc.updateCursor(cur, lastTS)

	if acc.hasActivity {
		w.emitActivity(sessionID, directory, acc.hasTerminal)
	}
}

// partScanAccumulator folds one pass over a session's new rows — scanParts'
// 1.x parts or scanV2Messages' 2.x messages — into the final
// hasActivity/hasTerminal/maxSeen/seenIDs state, keeping each scan a flat
// pipeline of query → accumulate → update cursor → emit.
type partScanAccumulator struct {
	maxSeen     int64
	hasActivity bool
	hasTerminal bool
	seenIDs     map[string]struct{}
}

// scanRow reads one part/message-joined row, observes it, and marks the turn
// terminal when the part or its message ends it.
func (a *partScanAccumulator) scanRow(rows *sql.Rows, cur *sessionCursor, lastTS int64) {
	var partID, partData, msgData string
	var timeUpdated int64
	if err := rows.Scan(&partID, &partData, &timeUpdated, &msgData); err != nil {
		return
	}
	if !a.observe(partID, timeUpdated, cur, lastTS) {
		return
	}
	// A turn ends via a step-finish part with a terminal reason, OR — for an
	// aborted/errored turn — opencode records the failure on
	// message.data.error and emits NO step-finish reason=error part, so the
	// message-level error is the terminal signal for a failed turn. Note this
	// is part-driven: a turn that errors before emitting ANY part won't be
	// surfaced here (the part↔message join has no row to carry msgData) — that
	// rarer zero-part case is the remaining half of #493. (#493)
	if a.hasTerminal || (!isTerminalPart(partData) && !isErrorMessage(msgData)) {
		return
	}
	a.hasTerminal = true
}

// observe folds one row's id and time_updated into the accumulator,
// returning false for a row already seen at the cursor's timestamp (the
// same-ms race, tracked via cur.seenIDs) and tracking dedup IDs as the
// timestamp moves forward. Both readers' cursors are this (time_updated, id)
// pair: a 1.x part and a 2.x session_message row are each rewritten in place
// with a later time_updated.
func (a *partScanAccumulator) observe(id string, timeUpdated int64, cur *sessionCursor, lastTS int64) bool {
	if cur != nil && timeUpdated == lastTS {
		if _, seen := cur.seenIDs[id]; seen {
			return false
		}
	}
	if timeUpdated > a.maxSeen {
		a.maxSeen = timeUpdated
		a.seenIDs = make(map[string]struct{})
	}
	if timeUpdated == a.maxSeen {
		a.seenIDs[id] = struct{}{}
	}
	a.hasActivity = true
	return true
}

// updateCursor advances cur to the accumulator's high-water mark: a full
// replace when the timestamp moved forward, or a same-timestamp merge of
// newly seen IDs when it didn't.
func (a *partScanAccumulator) updateCursor(cur *sessionCursor, lastTS int64) {
	switch {
	case !a.hasActivity:
		return
	case a.maxSeen > lastTS:
		cur.lastTS = a.maxSeen
		cur.seenIDs = a.seenIDs
	case a.maxSeen == lastTS && a.maxSeen != 0:
		// Same timestamp — merge new IDs into the existing set.
		for id := range a.seenIDs {
			cur.seenIDs[id] = struct{}{}
		}
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
	if fileExists(w.dbPath) {
		return nil
	}

	// Watch the parent directory for the DB file to appear.
	dbDir := filepath.Dir(w.dbPath)
	if _, err := os.Stat(dbDir); err != nil {
		// XDG data dir doesn't exist yet — poll with a ticker.
		return w.pollForDB(ctx)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(dbDir); err != nil {
		return err
	}
	if fileExists(w.dbPath) {
		return nil
	}

	return w.watchForDBCreate(ctx, watcher)
}

// fileExists reports whether path currently exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pollForDB ticks every 5s until w.dbPath exists or ctx is cancelled. Used
// when the parent XDG data dir doesn't exist yet, so there's nothing for
// fsnotify to watch.
func (w *Watcher) pollForDB(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if fileExists(w.dbPath) {
				return nil
			}
		}
	}
}

// watchForDBCreate blocks on fsnotify events until w.dbPath is created or ctx
// is cancelled.
func (w *Watcher) watchForDBCreate(ctx context.Context, watcher *fsnotify.Watcher) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if isDBCreateEvent(ev, w.dbPath) {
				return nil
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
		}
	}
}

// isDBCreateEvent reports whether ev is a Create event for dbPath's basename.
func isDBCreateEvent(ev fsnotify.Event, dbPath string) bool {
	return ev.Op&fsnotify.Create != 0 && filepath.Base(ev.Name) == filepath.Base(dbPath)
}
