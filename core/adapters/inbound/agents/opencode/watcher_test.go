package opencode

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"irrlicht/core/domain/agent"
)

// setupTestDB creates an in-memory SQLite database with the OpenCode schema
// and returns a Watcher targeting it plus the raw *sql.DB for test data insertion.
func setupTestDB(t *testing.T) (*Watcher, *sql.DB) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	// Create minimal OpenCode schema.
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS session (
			id TEXT PRIMARY KEY,
			directory TEXT NOT NULL DEFAULT '',
			time_created INTEGER NOT NULL DEFAULT 0,
			time_updated INTEGER NOT NULL DEFAULT 0,
			time_archived INTEGER,
			parent_id TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS message (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			modelID TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL DEFAULT '{}',
			time_created INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS part (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL DEFAULT '{}',
			time_created INTEGER NOT NULL DEFAULT 0,
			time_updated INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	// Enable WAL mode.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}

	w := NewWithDBPath(dbPath, 1*time.Hour)
	w.minScanGap = 0 // disable debounce for direct scanSessions() calls in tests
	// Default test stub: every CWD currently in the DB is "live". Tests that
	// want to exercise the no-live-process gate override this.
	w.liveCWDs = func() map[string]struct{} {
		rows, err := db.Query(`SELECT DISTINCT directory FROM session`)
		if err != nil {
			return nil
		}
		defer rows.Close()
		set := make(map[string]struct{})
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err == nil {
				set[d] = struct{}{}
			}
		}
		return set
	}
	return w, db
}

// insertSession inserts a session row and returns its ID.
func insertSession(t *testing.T, db *sql.DB, id, directory string, ts int64, parentID string) {
	t.Helper()
	var parentIDVal interface{}
	if parentID != "" {
		parentIDVal = parentID
	}
	_, err := db.Exec(
		`INSERT INTO session (id, directory, time_created, time_updated, parent_id)
		 VALUES (?, ?, ?, ?, ?)`,
		id, directory, ts, ts, parentIDVal,
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

// insertMessage inserts a message row.
func insertMessage(t *testing.T, db *sql.DB, id, sessionID, role, modelID string, ts int64) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO message (id, session_id, role, modelID, data, time_created)
		 VALUES (?, ?, ?, ?, '{}', ?)`,
		id, sessionID, role, modelID, ts,
	)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

// insertPart inserts a part row.
func insertPart(t *testing.T, db *sql.DB, id, sessionID, messageID, partType, data string, ts int64) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO part (id, session_id, message_id, type, data, time_created, time_updated)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, sessionID, messageID, partType, data, ts, ts,
	)
	if err != nil {
		t.Fatalf("insert part: %v", err)
	}
}

// collectEvents reads events from ch until timeout, returning all received.
func collectEvents(ch <-chan agent.Event, timeout time.Duration) []agent.Event {
	var events []agent.Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			return events
		}
	}
}

func TestScanSessions_NewSession(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_abc", "/home/user/project", now, "")
	insertMessage(t, db, "msg_1", "ses_abc", "user", "claude-sonnet", now)
	insertPart(t, db, "part_1", "ses_abc", "msg_1", "text", `{"text":"hello"}`, now)

	w.scanSessions()

	events := collectEvents(ch, 500*time.Millisecond)
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	var newSessions int
	for _, ev := range events {
		if ev.Type == agent.EventNewSession {
			newSessions++
			if ev.SessionID != "ses_abc" {
				t.Errorf("SessionID = %q, want ses_abc", ev.SessionID)
			}
		}
	}
	if newSessions != 1 {
		t.Errorf("expected 1 EventNewSession, got %d", newSessions)
	}
}

func TestScanSessions_Activity(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_act", "/tmp", now, "")
	insertMessage(t, db, "msg_a", "ses_act", "assistant", "gpt-4", now)
	insertPart(t, db, "part_a", "ses_act", "msg_a", "text", `{"text":"first"}`, now)

	// First scan: discover session.
	w.scanSessions()
	drainEvents(ch)

	// Add a new part with later timestamp.
	later := now + 1000
	insertPart(t, db, "part_b", "ses_act", "msg_a", "text", `{"text":"second"}`, later)

	w.scanSessions()

	events := collectEvents(ch, 500*time.Millisecond)
	var activities int
	for _, ev := range events {
		if ev.Type == agent.EventActivity {
			activities++
			if ev.SessionID != "ses_act" {
				t.Errorf("Activity SessionID = %q, want ses_act", ev.SessionID)
			}
		}
	}
	if activities != 1 {
		t.Errorf("expected 1 EventActivity, got %d (%v)", activities, events)
	}
}

func TestScanSessions_Removed(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_rm", "/tmp", now, "")

	// First scan: surface the session and seed lastArchivedCheck.
	w.scanSessions()
	drainEvents(ch)

	// Wait so time_archived lands strictly after lastArchivedCheck.
	time.Sleep(10 * time.Millisecond)

	// Archive the session.
	archiveTime := time.Now().UnixMilli()
	_, err := db.Exec(`UPDATE session SET time_archived = ? WHERE id = ?`, archiveTime, "ses_rm")
	if err != nil {
		t.Fatalf("archive session: %v", err)
	}

	w.scanSessions()

	events := collectEvents(ch, 500*time.Millisecond)
	var removals int
	for _, ev := range events {
		if ev.Type == agent.EventRemoved {
			removals++
			if ev.SessionID != "ses_rm" {
				t.Errorf("Removed SessionID = %q, want ses_rm", ev.SessionID)
			}
		}
	}
	if removals != 1 {
		t.Errorf("expected 1 EventRemoved, got %d (%v)", removals, events)
	}

	// Cursor should be cleaned up.
	w.mu.Lock()
	_, exists := w.cursors["ses_rm"]
	w.mu.Unlock()
	if exists {
		t.Error("cursor for ses_rm should be deleted after EventRemoved")
	}
}

func TestScanSessions_CursorRace(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_race", "/tmp", now, "")
	insertMessage(t, db, "msg_r1", "ses_race", "assistant", "gpt-4", now)
	insertMessage(t, db, "msg_r2", "ses_race", "assistant", "gpt-4", now)

	// Two parts with the SAME millisecond timestamp.
	insertPart(t, db, "part_r1", "ses_race", "msg_r1", "text", `{"text":"a"}`, now)
	insertPart(t, db, "part_r2", "ses_race", "msg_r2", "text", `{"text":"b"}`, now)

	// First scan: discover both parts at once.
	w.scanSessions()
	drainEvents(ch)

	// Second scan: no new parts — should NOT re-emit activity.
	w.scanSessions()
	events := collectEvents(ch, 500*time.Millisecond)
	var activities int
	for _, ev := range events {
		if ev.Type == agent.EventActivity {
			activities++
		}
	}
	if activities != 0 {
		t.Errorf("expected 0 EventActivity on re-scan (no new parts), got %d", activities)
	}

	// Add a third part with the SAME timestamp (same-ms race scenario).
	insertPart(t, db, "part_r3", "ses_race", "msg_r1", "text", `{"text":"c"}`, now)

	w.scanSessions()
	events = collectEvents(ch, 500*time.Millisecond)
	for _, ev := range events {
		if ev.Type == agent.EventActivity {
			activities++
		}
	}
	if activities != 1 {
		t.Errorf("expected 1 EventActivity for new part at same timestamp, got %d (%v)", activities, events)
	}
}

func TestScanSessions_NoDupOnSecondScan(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_nodup", "/tmp", now, "")
	insertMessage(t, db, "msg_nd", "ses_nodup", "user", "gpt-4", now)
	insertPart(t, db, "part_nd", "ses_nodup", "msg_nd", "text", `{"text":"hi"}`, now)

	// Two scans without any new parts.
	w.scanSessions()
	drainEvents(ch)

	w.scanSessions()
	events := collectEvents(ch, 500*time.Millisecond)
	var activities int
	for _, ev := range events {
		if ev.Type == agent.EventActivity {
			activities++
		}
	}
	if activities != 0 {
		t.Errorf("expected 0 EventActivity on unchanged scan, got %d", activities)
	}
}

func TestScanSessions_ParentSessionID(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_parent", "/tmp", now, "")
	insertSession(t, db, "ses_child", "/tmp", now, "ses_parent")

	w.scanSessions()

	events := collectEvents(ch, 500*time.Millisecond)
	childFound := false
	for _, ev := range events {
		if ev.Type == agent.EventNewSession && ev.SessionID == "ses_child" {
			childFound = true
			if ev.ParentSessionID != "ses_parent" {
				t.Errorf("ParentSessionID = %q, want ses_parent", ev.ParentSessionID)
			}
		}
	}
	if !childFound {
		t.Error("expected EventNewSession for child session")
	}
}

func TestScanSessions_NoArchivedBeforeCheck(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	// Create an already-archived session (before watcher knows about it).
	// The session was never in cursors → emitRemovedForArchivedSessions's
	// `if !known` gate skips it, so no EventRemoved should fire. Carryover
	// archives from a previous daemon run are handled by
	// PIDManager.CleanupZombies's DB-backed-orphan branch instead.
	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_already_archived", "/tmp", now, "")
	if _, err := db.Exec(`UPDATE session SET time_archived = ? WHERE id = ?`, now, "ses_already_archived"); err != nil {
		t.Fatalf("archive session: %v", err)
	}

	w.scanSessions()
	events := collectEvents(ch, 500*time.Millisecond)

	for _, ev := range events {
		if ev.Type == agent.EventRemoved {
			t.Errorf("unexpected EventRemoved for session not in cursors: %v", ev)
		}
	}
}

// TestScanSessions_NoLiveProcessSuppressesGhosts is the regression for the
// v0.3.12 ghost-sessions bug: when no opencode CLI is running, historical DB
// rows must NOT be surfaced as live sessions.
func TestScanSessions_NoLiveProcessSuppressesGhosts(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	// No opencode processes are alive.
	w.liveCWDs = func() map[string]struct{} { return nil }

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_ghost1", "/tmp", now, "")
	insertSession(t, db, "ses_ghost2", "/home/user/projects/foo", now, "")
	insertSession(t, db, "ses_ghost3", "/home/user/projects/bar", now, "")

	w.scanSessions()

	events := collectEvents(ch, 500*time.Millisecond)
	for _, ev := range events {
		if ev.Type == agent.EventNewSession {
			t.Errorf("expected zero EventNewSession with no live opencode process, got %+v", ev)
		}
	}
}

// TestScanSessions_GCsExpiredCursors verifies that cursors for sessions
// whose lastTS has aged out of the maxAge window are dropped from memory.
// Without this GC, the cursors map grows without bound for users who
// accumulate many OpenCode sessions but rarely run the CLI (every session
// the watcher ever saw would stick around as a not-emitted cursor).
func TestScanSessions_GCsExpiredCursors(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	// Generous maxAge so no realistic scheduler stall can push the fresh
	// session across the boundary. The old version used maxAge=100ms and a
	// 150ms sleep — under -race on a loaded CI runner, >100ms could elapse
	// between stamping ses_recent fresh and scanSessions' row filter, aging
	// the FRESH session out too (flaked on the Linux runner).
	w.maxAge = time.Hour

	// Two sessions with distinct CWDs, both fresh enough to appear in scan.
	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_recent", "/tmp/recent", now, "")
	insertSession(t, db, "ses_will_age", "/tmp/age-out", now, "")

	w.scanSessions()
	drainEvents(ch)

	w.mu.Lock()
	if len(w.cursors) != 2 {
		t.Fatalf("after first scan: cursors=%d, want 2", len(w.cursors))
	}
	w.mu.Unlock()

	// Age ses_will_age out deterministically — backdate instead of sleep:
	// its DB row leaves the scan window (row filter + SQL cutoff), and its
	// cursor's lastObserved (what gcExpiredCursors actually checks) predates
	// the cutoff. ses_recent's row stays at `now`, comfortably within the
	// hour-wide window.
	past := time.Now().Add(-2 * time.Hour)
	if _, err := db.Exec(`UPDATE session SET time_updated = ? WHERE id = ?`, past.UnixMilli(), "ses_will_age"); err != nil {
		t.Fatalf("backdate time_updated: %v", err)
	}
	w.mu.Lock()
	w.cursors["ses_will_age"].lastObserved = past
	w.mu.Unlock()

	w.scanSessions()

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.cursors["ses_recent"]; !ok {
		t.Error("ses_recent cursor was GC'd but it's within maxAge")
	}
	if _, ok := w.cursors["ses_will_age"]; ok {
		t.Errorf("ses_will_age cursor should have been GC'd (lastTS older than maxAge), cursors=%v", keysOf(w.cursors))
	}
}

// keysOf returns the keys of m as a slice, for test diagnostic output.
func keysOf(m map[string]*sessionCursor) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestScanSessions_EmitsWhenProcessBecomesLive verifies that a session
// initially gated out by the no-live-process check still emits EventNewSession
// once the user starts opencode in its CWD.
func TestScanSessions_EmitsWhenProcessBecomesLive(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	var liveSet map[string]struct{}
	w.liveCWDs = func() map[string]struct{} { return liveSet }

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_dormant", "/tmp/work", now, "")
	insertMessage(t, db, "msg_d", "ses_dormant", "user", "gpt-4", now)
	insertPart(t, db, "part_d1", "ses_dormant", "msg_d", "text", `{"text":"hello"}`, now)

	// First scan: process not live → no emit.
	w.scanSessions()
	drainEvents(ch)

	// User starts opencode in /tmp/work.
	liveSet = map[string]struct{}{"/tmp/work": {}}

	// New activity arrives.
	later := now + 1000
	insertPart(t, db, "part_d2", "ses_dormant", "msg_d", "text", `{"text":"more"}`, later)
	if _, err := db.Exec(`UPDATE session SET time_updated = ? WHERE id = ?`, later, "ses_dormant"); err != nil {
		t.Fatalf("bump time_updated: %v", err)
	}

	w.scanSessions()
	events := collectEvents(ch, 500*time.Millisecond)

	var newSessions, activities int
	for _, ev := range events {
		switch ev.Type {
		case agent.EventNewSession:
			if ev.SessionID == "ses_dormant" {
				newSessions++
			}
		case agent.EventActivity:
			if ev.SessionID == "ses_dormant" {
				activities++
			}
		}
	}
	if newSessions != 1 {
		t.Errorf("expected 1 EventNewSession after process becomes live, got %d (events=%v)", newSessions, events)
	}
	// Activity for the new part should also fire — but not for the historical
	// part_d1 that landed before the session was surfaced.
	if activities != 1 {
		t.Errorf("expected 1 EventActivity (for the post-emit part), got %d (events=%v)", activities, events)
	}
}

func TestScanSessions_MultipleArchived(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_a", "/tmp", now, "")
	insertSession(t, db, "ses_b", "/tmp", now, "")
	insertSession(t, db, "ses_c", "/tmp", now, "")

	// First scan to discover all three and set lastArchivedCheck.
	w.scanSessions()
	time.Sleep(10 * time.Millisecond)

	// Drain events.
	drainEvents(ch)

	// Archive two sessions.
	archiveTime := time.Now().UnixMilli()
	db.Exec(`UPDATE session SET time_archived = ? WHERE id = ?`, archiveTime, "ses_a")
	db.Exec(`UPDATE session SET time_archived = ? WHERE id = ?`, archiveTime, "ses_c")

	w.scanSessions()
	events := collectEvents(ch, 500*time.Millisecond)

	removedIDs := make(map[string]bool)
	for _, ev := range events {
		if ev.Type == agent.EventRemoved {
			removedIDs[ev.SessionID] = true
		}
	}
	if !removedIDs["ses_a"] {
		t.Error("expected EventRemoved for ses_a")
	}
	if !removedIDs["ses_c"] {
		t.Error("expected EventRemoved for ses_c")
	}
	if removedIDs["ses_b"] {
		t.Error("unexpected EventRemoved for non-archived ses_b")
	}
	if len(removedIDs) != 2 {
		t.Errorf("expected 2 EventRemoved, got %d (%v)", len(removedIDs), removedIDs)
	}
}

func drainEvents(ch <-chan agent.Event) {
	for {
		select {
		case <-ch:
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

func TestIsTerminalPart(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"stop", `{"type":"step-finish","reason":"stop"}`, true},
		{"interrupted", `{"type":"step-finish","reason":"interrupted"}`, true},
		{"length", `{"type":"step-finish","reason":"length"}`, true},
		{"error", `{"type":"step-finish","reason":"error"}`, true},
		{"content-filter", `{"type":"step-finish","reason":"content-filter"}`, true},
		{"tool-calls", `{"type":"step-finish","reason":"tool-calls"}`, false},
		{"unknown-reason", `{"type":"step-finish","reason":"unknown"}`, false},
		{"not-step-finish", `{"type":"text","text":"hello"}`, false},
		{"malformed-json", `{not valid json`, false},
		{"empty", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTerminalPart(tc.data); got != tc.want {
				t.Errorf("isTerminalPart(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

func TestIsErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"error-object", `{"role":"assistant","error":{"name":"ProviderError","message":"boom"}}`, true},
		{"error-string", `{"error":"boom"}`, true},
		{"empty-error-string", `{"error":""}`, false},
		{"empty-error-object", `{"error":{}}`, false},
		{"null-error", `{"error":null}`, false},
		{"false-error", `{"error":false}`, false},
		{"zero-error", `{"error":0}`, false},
		{"array-error", `{"error":[]}`, false},
		{"no-error", `{"role":"assistant"}`, false},
		{"malformed-json", `{not valid json`, false},
		{"empty", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isErrorMessage(tc.data); got != tc.want {
				t.Errorf("isErrorMessage(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// TestScanSessions_ErrorMessageTerminal verifies that an aborted/errored turn —
// which opencode records on message.data.error WITHOUT a step-finish
// reason=error part — still produces a terminal EventActivity so the session
// settles to ready instead of sticking in working. (#493)
func TestScanSessions_ErrorMessageTerminal(t *testing.T) {
	w, db := setupTestDB(t)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_err", "/tmp", now, "")
	// A message whose data carries message.data.error (insertMessage hardcodes
	// data='{}', so insert directly to populate the error field).
	if _, err := db.Exec(
		`INSERT INTO message (id, session_id, role, modelID, data, time_created)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"msg_err", "ses_err", "assistant", "gpt-4",
		`{"role":"assistant","error":{"name":"ProviderError","message":"boom"}}`, now,
	); err != nil {
		t.Fatalf("insert error message: %v", err)
	}
	// A NON-terminal part (plain text), so the ONLY terminal signal is the
	// message-level error — proving isErrorMessage is what flips Terminal.
	insertPart(t, db, "part_err", "ses_err", "msg_err", "text", `{"text":"partial"}`, now)

	w.scanSessions()

	events := collectEvents(ch, 500*time.Millisecond)
	var sawTerminal, sawActivity bool
	for _, ev := range events {
		if ev.Type == agent.EventActivity && ev.SessionID == "ses_err" {
			sawActivity = true
			if ev.Terminal {
				sawTerminal = true
			}
		}
	}
	if !sawActivity {
		t.Fatalf("expected an EventActivity for ses_err, got events=%v", events)
	}
	if !sawTerminal {
		t.Errorf("expected EventActivity.Terminal=true for an errored turn (message.data.error), got events=%v", events)
	}
}
