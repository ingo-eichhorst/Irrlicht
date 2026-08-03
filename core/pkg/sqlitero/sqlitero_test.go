package sqlitero

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// seedStore builds a one-table store at an ORDINARY path and copies the
// finished file into dir, returning the copy's path.
//
// The copy is not fussiness: dir's name is the thing under test below, and
// seeding in place would go through modernc with a bare-path DSN — which
// truncates at the first "?" and creates the very stray file these tests
// assert against. Building the fixture somewhere safe keeps the assertions
// about Open.
func seedStore(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "seed.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT); INSERT INTO sessions VALUES ('s1')`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if err := db.Close(); err != nil { // flush before copying the bytes
		t.Fatalf("close seed store: %v", err)
	}
	blob, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read seed store: %v", err)
	}
	dst := filepath.Join(dir, "store.db")
	if err := os.WriteFile(dst, blob, 0o644); err != nil {
		t.Fatalf("write store into %s: %v", dir, err)
	}
	return dst
}

// The whole point of the package: a handle that cannot write, and cannot
// bring a store into existence. Seen red against the bare-path DSN the
// adapters used before ("<path>?mode=ro&…"), which modernc opens
// READWRITE|CREATE — the missing store was created and the CREATE TABLE
// succeeded.
func TestOpen_CannotWriteOrCreate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "store.db")

	db, err := Open(missing)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE probe(x)"); err == nil {
		t.Error("Open permitted a write — mode=ro is not being enforced")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("Open CREATED the store file; an observe-kind permission must never write")
	}
}

// An existing store must still be readable through the same helper.
func TestOpen_ReadsExistingStore(t *testing.T) {
	path := seedStore(t, t.TempDir())
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	var got string
	if err := db.QueryRow(`SELECT id FROM sessions`).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "s1" {
		t.Errorf("read back %q, want s1", got)
	}
}

// The reason opencode's and antigravity's "_journal=WAL" is not carried into
// this package: modernc applies it as `PRAGMA journal_mode=WAL`, which is a
// WRITE against a store that is not already in WAL mode. This asserts the
// direction of that fact — a read-only handle cannot set a journal mode — so
// nobody re-adds the parameter believing it to be inert. The seed store is in
// the default delete mode, which is also hermes' mode.
func TestOpen_RefusesToSetJournalMode(t *testing.T) {
	path := seedStore(t, t.TempDir())
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err == nil {
		t.Error("a read-only handle switched the store's journal mode — that is a write to the agent's store")
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode == "wal" {
		t.Errorf("journal_mode = %q; the store was converted despite the read-only handle", mode)
	}
}

// Store paths are user-controlled ($HERMES_HOME, XDG dirs), and the DSN is a
// URI, so a character the URI reads structurally truncates the path AND
// carries "mode=ro" away with it — at which point the driver opens a SHORTER
// path READWRITE|CREATE and writes a stray database there.
//
// "#" and "%" are fixed by escaping; "?" cannot be (modernc does not decode
// "%3F"), so that one is refused. Either way the invariant is the same:
// nothing is ever created outside the store's own directory.
//
// Seen red against "file:"+path+"?mode=ro&…", one distinct failure per
// character: "#" read "no such table: sessions" out of a truncated path, "?"
// left a stray file beside the store's directory, and "%2F" failed to open.
// The space is a lock — already fine — and is here because it is the special
// character a real macOS home is most likely to contain.
func TestOpen_URISpecialCharsInPath(t *testing.T) {
	cases := []struct {
		dirName    string
		wantReject bool
	}{
		{dirName: "store#dir"},
		{dirName: "store%2Fdir"},
		{dirName: "store dir"},
		{dirName: "store?dir", wantReject: true},
	}
	for _, tc := range cases {
		t.Run(tc.dirName, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, tc.dirName)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatalf("mkdir %q: %v", tc.dirName, err)
			}
			path := seedStore(t, dir)

			db, err := Open(path)
			switch {
			case tc.wantReject:
				if err == nil {
					db.Close()
					t.Errorf("Open accepted a %q path; it must refuse rather than let the driver write a stray database", tc.dirName)
				} else if !errors.Is(err, ErrUnsupportedPath) {
					t.Errorf("Open error = %v, want ErrUnsupportedPath", err)
				}
			case err != nil:
				t.Fatalf("Open: %v", err)
			default:
				defer db.Close()
				var got string
				if err := db.QueryRow(`SELECT id FROM sessions`).Scan(&got); err != nil {
					t.Fatalf("read store under %q: %v", tc.dirName, err)
				}
				if got != "s1" {
					t.Errorf("read back %q, want s1", got)
				}
			}

			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("read %s: %v", root, err)
			}
			for _, e := range entries {
				if e.Name() != tc.dirName {
					t.Errorf("Open created %q outside the store directory — the path was truncated and mode=ro lost", e.Name())
				}
			}
		})
	}
}
