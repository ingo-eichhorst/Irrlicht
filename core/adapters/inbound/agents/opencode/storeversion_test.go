package opencode

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// storeAt points StorePath at a fresh database file under a temp dir and
// returns it open for writing fixture rows.
func storeAt(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	t.Setenv("OPENCODE_DB", dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestNewestObservedCLIVersion_AMigratedStoreAnswersTwoX reads the committed
// fixture of a 1.x store 2.1.4 migrated in place (#2106): session_v2 holds the
// imported 1.x session at version 1.19.4, created first, and the 2.1.4 session
// created after it. The newest CREATED is the CLI the user runs now.
func TestNewestObservedCLIVersion_AMigratedStoreAnswersTwoX(t *testing.T) {
	db := storeAt(t)
	createV2Schema(t, db)
	loadFixtureRows(t, db, v2MigratedRows, insertSessions)

	if got := newestObservedCLIVersion(); got != "2.1.4" {
		t.Errorf("newestObservedCLIVersion() = %q, want 2.1.4 — the migrated store's newest session", got)
	}
}

// TestNewestObservedCLIVersion_CreationNotUpdateDecides pins the ordering: the
// imported 1.x session is touched LAST, and it still does not decide, because
// it was created first. A reader ordering by time_updated would answer 1.19.4
// and install the artifact a 2.x opencode rejects.
func TestNewestObservedCLIVersion_CreationNotUpdateDecides(t *testing.T) {
	db := storeAt(t)
	createV2Schema(t, db)
	loadFixtureRows(t, db, v2MigratedRows, insertSessions)
	if _, err := db.Exec(`UPDATE session_v2 SET time_updated = 9999999999999 WHERE id = ?`, sesImported); err != nil {
		t.Fatalf("touch the imported session: %v", err)
	}

	if got := newestObservedCLIVersion(); got != "2.1.4" {
		t.Errorf("newestObservedCLIVersion() = %q after the imported 1.x session was updated last, want 2.1.4", got)
	}
}

// TestNewestObservedCLIVersion_AOneXStoreAnswersFromSession: a store 2.x never
// opened has no session_v2, and the 1.x `session` table answers.
func TestNewestObservedCLIVersion_AOneXStoreAnswersFromSession(t *testing.T) {
	db := storeAt(t)
	execAll(t, db,
		`CREATE TABLE session (id TEXT PRIMARY KEY, version TEXT NOT NULL, time_created INTEGER NOT NULL)`,
		`INSERT INTO session VALUES ('ses_old', '1.18.15', 1), ('ses_new', '1.19.4', 2)`,
	)

	if got := newestObservedCLIVersion(); got != "1.19.4" {
		t.Errorf("newestObservedCLIVersion() = %q, want 1.19.4 — the 1.x store's newest session", got)
	}
}

// TestNewestObservedCLIVersion_NothingToReadAnswersNothing covers the three
// stores that hold no answer — none at all, a fresh 2.x one with no session,
// and one whose tables neither major created — and pins that the read-only open
// creates no file where there was none.
func TestNewestObservedCLIVersion_NothingToReadAnswersNothing(t *testing.T) {
	t.Run("no_store", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "opencode.db")
		t.Setenv("OPENCODE_DB", missing)
		if got := newestObservedCLIVersion(); got != "" {
			t.Errorf("newestObservedCLIVersion() = %q with no store, want \"\"", got)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Errorf("reading a missing store left a file behind (stat err %v) — the read must be read-only", err)
		}
	})
	t.Run("fresh_two_x_store", func(t *testing.T) {
		createV2Schema(t, storeAt(t))
		if got := newestObservedCLIVersion(); got != "" {
			t.Errorf("newestObservedCLIVersion() = %q for a store with no session, want \"\"", got)
		}
	})
	t.Run("foreign_tables", func(t *testing.T) {
		execAll(t, storeAt(t), `CREATE TABLE unrelated (x TEXT)`)
		if got := newestObservedCLIVersion(); got != "" {
			t.Errorf("newestObservedCLIVersion() = %q for a store with no session table, want \"\"", got)
		}
	})
}
