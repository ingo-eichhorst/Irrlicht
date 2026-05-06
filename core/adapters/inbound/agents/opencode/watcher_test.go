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
			if ev.Adapter != AdapterName {
				t.Errorf("Adapter = %q, want %q", ev.Adapter, AdapterName)
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

	// First scan to discover and set lastArchivedCheck.
	w.scanSessions()
	drainEvents(ch)

	// Wait so time_archived is distinctly after lastArchivedCheck.
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
	now := time.Now().UnixMilli()
	insertSessionWithArchive := func() {
		insertSession(t, db, "ses_already_archived", "/tmp", now, "")
		_, err := db.Exec(`UPDATE session SET time_archived = ? WHERE id = ?`, now, "ses_already_archived")
		if err != nil {
			t.Fatalf("archive session: %v", err)
		}
	}
	insertSessionWithArchive()

	// firstArchivedCheck is zero → emitRemovedForArchivedSessions returns early.
	// Even if it did run, the session was never in cursors → no event.

	w.scanSessions()
	events := collectEvents(ch, 500*time.Millisecond)

	// Should not emit EventRemoved for an unknown archived session.
	for _, ev := range events {
		if ev.Type == agent.EventRemoved {
			t.Errorf("unexpected EventRemoved for session not in cursors: %v", ev)
		}
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
