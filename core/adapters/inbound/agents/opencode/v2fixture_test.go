package opencode

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"irrlicht/core/domain/agent"
)

// The opencode 2.x fixtures (#2106) are SQL text rather than a binary store so a
// reviewer can read every row; testdata/v2-*.rows.sql say where each row came
// from and what was rewritten.
const (
	v2SchemaFile   = "testdata/v2-schema.sql"
	v2StoreRows    = "testdata/v2-store.rows.sql"
	v2MigratedRows = "testdata/v2-migrated.rows.sql"
	v2RunningRow   = "testdata/v2-assistant-running-tool.json"

	// fixtureDir is the directory the fixtures rewrite every session's to.
	fixtureDir = "/Users/x/project"

	insertSessions = "INSERT INTO session_v2 "
	insertMessages = "INSERT INTO session_message "
)

// Fixture session ids — see testdata/v2-store.rows.sql for what each one is.
const (
	sesToolTurn    = "ses_e8cf72396ffe3SEqwdj2TSa7eO"
	sesInterrupted = "ses_e8cee7cdaffeDaGj3wQjxk44jX"
	sesParent      = "ses_e8cee2d82ffeYMch3UDUSfnEnq"
	sesChild       = "ses_e8cee0139ffeVEHy1J4Nd2JUSM"
	sesFailed      = "ses_e8bc9b17affeBxXhL5t5nljGWU"
	sesPermission  = "ses_e8bc0440bffezQNIOqef7t0Z3v"

	sesImported = "ses_e8cf4044effevAmWHyNGZ9DxGW" // v2-migrated.rows.sql
	sesMigrated = "ses_e8cf2c6fdffeNSE46y3abNVPFd" // v2-migrated.rows.sql
)

// createV2Schema creates opencode 2.1.4's real session tables in db.
func createV2Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	ddl, err := os.ReadFile(v2SchemaFile)
	if err != nil {
		t.Fatalf("read %s: %v", v2SchemaFile, err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("create the 2.x schema: %v", err)
	}
}

// loadFixtureRows runs every statement in file that starts with prefix — each
// fixture statement is one line — and fails when none does, so a renamed
// prefix cannot load nothing and pass.
func loadFixtureRows(t *testing.T, db *sql.DB, file, prefix string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if _, err := db.Exec(line); err != nil {
			t.Fatalf("%s: %v\n%s", file, err, line)
		}
		n++
	}
	if n == 0 {
		t.Fatalf("%s holds no %q statement", file, prefix)
	}
}

// newV2Store builds an opencode 2.x store in a temp dir and a watcher on it
// whose live-client set owns fixtureDir and which sees no 2.x service — never
// the host's: a dev machine running one would otherwise decide what these
// tests admit (#2107). maxAge is 0: the fixture's timestamps are the
// capture's, not now.
func newV2Store(t *testing.T) (*Watcher, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	createV2Schema(t, db)

	w := NewWithDBPath(dbPath, 0)
	w.minScanGap = 0
	w.liveCWDs = func() map[string]struct{} { return map[string]struct{}{fixtureDir: {}} }
	w.serviceUp = func() bool { return false }
	return w, db
}

// loadV2Fixture loads every session and message row of file.
func loadV2Fixture(t *testing.T, db *sql.DB, file string) {
	t.Helper()
	loadFixtureRows(t, db, file, insertSessions)
	loadFixtureRows(t, db, file, insertMessages)
}

// holdBackAfter moves sessionID's message rows with seq > seq out of
// session_message into the table `held` (which no reader queries), so a test
// can put them back one step at a time.
func holdBackAfter(t *testing.T, db *sql.DB, sessionID string, seq int) {
	t.Helper()
	execAll(t, db,
		`CREATE TABLE IF NOT EXISTS held AS SELECT * FROM session_message WHERE 0`,
		`INSERT INTO held SELECT * FROM session_message WHERE session_id = '`+sessionID+`' AND seq > `+strconv.Itoa(seq),
		`DELETE FROM session_message WHERE session_id = '`+sessionID+`' AND seq > `+strconv.Itoa(seq),
		`UPDATE session_v2 SET time_idle = NULL, idle_outcome = NULL WHERE id = '`+sessionID+`'`,
	)
}

// putBack restores held rows of sessionID matching cond (an SQL predicate
// over `held`'s columns).
func putBack(t *testing.T, db *sql.DB, sessionID, cond string) {
	t.Helper()
	execAll(t, db, `INSERT INTO session_message SELECT * FROM held WHERE session_id = '`+sessionID+`' AND `+cond)
}

// setToRunningTool puts sessionID's seq-5 assistant row in the state it was
// sampled in while its shell call waited on a permission
// (testdata/v2-assistant-running-tool.json, time_updated 1792951699890).
func setToRunningTool(t *testing.T, db *sql.DB, sessionID string) {
	t.Helper()
	raw, err := os.ReadFile(v2RunningRow)
	if err != nil {
		t.Fatalf("read %s: %v", v2RunningRow, err)
	}
	var data bytes.Buffer
	if err := json.Compact(&data, raw); err != nil {
		t.Fatalf("%s: %v", v2RunningRow, err)
	}
	if _, err := db.Exec(`INSERT INTO session_message
		SELECT id, session_id, type, seq, time_created, 1792951699890, ?
		FROM held WHERE session_id = ? AND seq = 5`, data.String(), sessionID); err != nil {
		t.Fatalf("insert the running-tool row: %v", err)
	}
}

// drainNow returns every event already buffered on ch. scanSessions
// broadcasts before it returns (a non-blocking send into the subscriber's
// 64-slot buffer, which these tests never fill), so nothing is waited for.
func drainNow(ch <-chan agent.Event) []agent.Event {
	var evs []agent.Event
	for {
		select {
		case ev := <-ch:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

func eventsOfType(evs []agent.Event, typ agent.EventType) map[string][]agent.Event {
	out := map[string][]agent.Event{}
	for _, ev := range evs {
		if ev.Type == typ {
			out[ev.SessionID] = append(out[ev.SessionID], ev)
		}
	}
	return out
}
