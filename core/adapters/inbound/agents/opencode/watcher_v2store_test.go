package opencode

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"irrlicht/core/domain/agent"
)

// This file is #2105's "V1 reader stands down" half: an opencode store that
// carries `session_v2` was written by opencode 2.x, and the V1 reader must
// neither run V1 SQL against it nor admit the frozen V1 rows a migration leaves
// behind.
//
// Shapes measured against the sandbox store /Users/factory/.oc1sb/…/opencode.db
// (a 1.x store opencode 2.1.4 migrated in place), read with
// `sqlite3 -readonly <db> .tables` and `.schema session_v2`:
//   - a MIGRATED store keeps the 1.x `session`/`message`/`part` tables, rows
//     and all, and gains `session_v2` and `session_message` beside them;
//   - a FRESH 2.x store has `session_v2` and no `session`, `message` or `part`
//     table at all (#2105's issue body).
//
// The 2.x tables are opencode 2.1.4's real ones (testdata/v2-schema.sql): since
// #2106 a 2.x store is READ, by its own reader, so a stand-in table with a
// handful of its columns would fail that reader's queries instead of standing
// for a 2.x store.

func execAll(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

// captureStdLog routes the stdlib logger — which the watcher and
// ComputeMetrics write their per-scan query errors to — into a buffer for the
// rest of the test.
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func countNewSessions(events []agent.Event) int {
	n := 0
	for _, ev := range events {
		if ev.Type == agent.EventNewSession {
			n++
		}
	}
	return n
}

// seedRecentV1Session writes one live-looking 1.x session — a recent row with a
// message and a part — the shape a migration leaves frozen in `session`.
func seedRecentV1Session(t *testing.T, db *sql.DB, id, dir string) {
	t.Helper()
	now := time.Now().UnixMilli()
	insertSession(t, db, id, dir, now, "")
	insertMessage(t, db, "msg_"+id, id, "user", "test-model", now)
	insertPart(t, db, partRow{id: "part_" + id, sessionID: id, messageID: "msg_" + id,
		partType: "text", data: `{"type":"text","text":"hello"}`, ts: now})
}

// TestScanSessions_MigratedV2StoreAdmitsNoFrozenV1Row is the red-first defect
// test for the admission half. A 1.x row frozen in a migrated store, in a
// directory a live `opencode` process holds (an opencode 2.x TUI is comm
// `opencode` too), was admitted as a live session on main.
//
// The 1.x-only row is the control that makes the test fail loudly rather than
// pass vacuously: the same seed, without `session_v2`, must still emit — so a
// scan that never ran cannot read as a stand-down.
func TestScanSessions_MigratedV2StoreAdmitsNoFrozenV1Row(t *testing.T) {
	for _, c := range []struct {
		name     string
		migrated bool
		want     int
	}{
		{"v1_only_store_still_emits", false, 1},
		{"migrated_store_admits_nothing", true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, db := setupTestDB(t)
			seedRecentV1Session(t, db, "ses_frozen", "/Users/x/project")
			if c.migrated {
				createV2Schema(t, db)
			}
			ch := w.Subscribe()
			defer w.Unsubscribe(ch)

			w.scanSessions()

			if got := countNewSessions(collectEvents(ch, 200*time.Millisecond)); got != c.want {
				t.Errorf("EventNewSession count = %d, want %d", got, c.want)
			}
		})
	}
}

// TestScanSessions_FreshV2StoreRunsNoV1SQL is the red-first defect test for the
// query half. A fresh 2.x store has no `session` table, and on main every scan
// logged `no such table: session` to stderr — 26 such lines in the sandbox's
// qa/main-p1base daemon.out (#2105 triage correction 3).
func TestScanSessions_FreshV2StoreRunsNoV1SQL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	createV2Schema(t, db)

	logs := captureStdLog(t)
	w := NewWithDBPath(dbPath, time.Hour)
	w.minScanGap = 0
	w.liveCWDs = func() map[string]struct{} { return map[string]struct{}{"/Users/x/project": {}} }
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	w.scanSessions()
	w.scanSessions() // the archived-session query runs from the second scan on

	if got := countNewSessions(collectEvents(ch, 200*time.Millisecond)); got != 0 {
		t.Errorf("EventNewSession count = %d on a store with no 1.x tables, want 0", got)
	}
	if strings.Contains(logs.String(), "no such table") {
		t.Errorf("the 1.x reader ran 1.x SQL against a 2.x store:\n%s", logs.String())
	}
}

// TestScanSessions_LogsTheStandDownOncePerTransition pins the log contract the
// stand-down adds: one line when the store turns 2.x, not one per scan — the
// watcher scans on every WAL write.
func TestScanSessions_LogsTheStandDownOncePerTransition(t *testing.T) {
	w, db := setupTestDB(t)
	createV2Schema(t, db)
	logs := captureStdLog(t)

	for i := 0; i < 3; i++ {
		w.scanSessions()
	}

	if n := strings.Count(logs.String(), "session_v2"); n != 1 {
		t.Errorf("stand-down logged %d time(s) over three scans, want exactly once:\n%s", n, logs.String())
	}
}

// TestComputeMetrics_MigratedV2StoreReadsNothing is the red-first defect test
// for the metrics half: on main, ComputeMetrics folded a frozen 1.x session's
// parts out of a migrated store. The 1.x-only case is the control.
func TestComputeMetrics_MigratedV2StoreReadsNothing(t *testing.T) {
	for _, c := range []struct {
		name     string
		migrated bool
		wantNil  bool
	}{
		{"v1_only_store_still_reads", false, false},
		{"migrated_store_reads_nothing", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, db := setupTestDB(t)
			seedRecentV1Session(t, db, "ses_frozen", "/Users/x/project")
			if c.migrated {
				createV2Schema(t, db)
			}

			metrics, err := ComputeMetrics(w.transcriptPathFor("ses_frozen"), "")
			if err != nil {
				t.Fatalf("ComputeMetrics: %v", err)
			}
			if got := metrics == nil; got != c.wantNil {
				t.Errorf("ComputeMetrics returned nil = %t, want %t", got, c.wantNil)
			}
		})
	}
}

// TestScanSessions_MigrationUnderARunningWatcherReplaysNothing pins the cursor
// reset storeSchema does when the store's schema switches (#2106 review). The
// in-place migration keeps a 1.x session's id and timestamps — measured on the
// sandbox's migrated store, read with mode=ro: the imported assistant row's
// session_message.time_updated is 1792931562493, later than the session's
// last 1.x part (1792931562448). A 1.x cursor carried into the 2.x reader
// would therefore read the imported row as fresh activity, and with no idle
// row ever arriving for it the session would sit in working.
func TestScanSessions_MigrationUnderARunningWatcherReplaysNothing(t *testing.T) {
	w, db := setupTestDB(t)
	now := time.Now().UnixMilli()
	insertSession(t, db, "ses_m", "/Users/x/project", now-10000, "")
	insertMessage(t, db, "msg_a", "ses_m", "assistant", "m", now-10000)
	insertPart(t, db, partRow{id: "prt_1", sessionID: "ses_m", messageID: "msg_a", partType: "text",
		data: `{"type":"text","text":"hi"}`, ts: now - 9000})
	insertPart(t, db, partRow{id: "prt_2", sessionID: "ses_m", messageID: "msg_a", partType: "step-finish",
		data: `{"type":"step-finish","reason":"stop"}`, ts: now - 5000})
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	w.scanSessions()
	if n := len(eventsOfType(drainNow(ch), agent.EventNewSession)["ses_m"]); n != 1 {
		t.Fatalf("the 1.x session got %d EventNewSession before the migration, want 1", n)
	}

	createV2Schema(t, db)
	execAll(t, db,
		fmt.Sprintf(`INSERT INTO session_v2 (id, project_id, slug, directory, version, time_created, time_updated)
			VALUES ('ses_m', 'prj', 'slug', '/Users/x/project', '1.19.4', %d, %d)`, now-10000, now-3000),
		fmt.Sprintf(`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
			VALUES ('msg_a', 'ses_m', 'assistant', 1, %d, %d, '{"model":{"id":"m"},"content":[]}')`, now-10000, now-4000),
	)
	w.scanSessions()
	if evs := drainNow(ch); len(evs) != 0 {
		t.Errorf("the migrated, untouched 1.x session produced %d event(s), want 0: %+v", len(evs), evs)
	}
}
