package processlifecycle_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// opencodeV2Processes is the process table #2104's live verification measured
// for opencode 2.x, plus the `opencode2` and npm `opencode.exe` spellings of a
// client: the background service (`serve --service`, cwd $HOME) and TUI
// clients, each with its project directory as its cwd — and a process that
// only NAMES an opencode binary in its arguments, as #2106's live run's
// tui-detach.py driver did.
func opencodeV2Processes(t *testing.T) (home, tuiProject, tui2Project, exeProject, driverDir string) {
	t.Helper()
	home = canonicalTempDir(t)
	tuiProject = canonicalTempDir(t)
	tui2Project = canonicalTempDir(t)
	exeProject = canonicalTempDir(t)
	driverDir = canonicalTempDir(t)
	processlifecycle.SetFakeProcessesForTest(t, map[int]processlifecycle.FakeProcess{
		42001: {Name: "opencode", CWD: home, Argv: []string{"/x/shim/opencode", "serve", "--service"}},
		42002: {Name: "opencode", CWD: tuiProject, Argv: []string{"/opt/homebrew/bin/opencode"}},
		42003: {Name: "opencode2", CWD: tui2Project, Argv: []string{"/Users/x/.local/opencode-v2/bin/opencode2", "--prompt", "Reply with PONG."}},
		42004: {Name: "opencode.exe", CWD: exeProject, Argv: []string{"/x/node_modules/@opencode/cli/bin/opencode.exe"}},
		42005: {Name: "python3", CWD: driverDir, Argv: []string{"python3", "/x/tui-detach.py", "/x/shim/opencode"}},
	})
	return home, tuiProject, tui2Project, exeProject, driverDir
}

// TestOpenCodeDiscoverPID_BindsClientsNotTheService is #2106's red-first test
// for PID binding. A session binds to the client in its directory, whichever
// spelling the client was started under — and never to the 2.x service,
// whose cwd is $HOME: a session in $HOME has no client here. On main
// DiscoverPID matched by comm `opencode` alone, so it bound a $HOME session to
// the service and found no `opencode2` or `opencode.exe` client at all.
func TestOpenCodeDiscoverPID_BindsClientsNotTheService(t *testing.T) {
	home, tuiProject, tui2Project, exeProject, driverDir := opencodeV2Processes(t)
	for _, c := range []struct {
		name string
		cwd  string
		want int
	}{
		{"service in $HOME is not a client", home, 0},
		{"TUI as opencode", tuiProject, 42002},
		{"TUI as opencode2", tui2Project, 42003},
		{"npm opencode.exe", exeProject, 42004},
		{"a driver naming opencode in its arguments is not a client", driverDir, 0},
	} {
		got, err := opencode.DiscoverPID(c.cwd, "", nil)
		if err != nil || got != c.want {
			t.Errorf("%s: DiscoverPID(%s) = (%d, %v), want (%d, nil)", c.name, c.cwd, got, err, c.want)
		}
	}
}

// TestOpenCodeLiveClientCWDs_ExcludesTheService: the watcher admits a 2.x
// session only while a client owns its directory, so the live set it reads
// must hold every client's cwd and not the service's $HOME — or every
// session the service hosts there would read as client-run (#2106).
func TestOpenCodeLiveClientCWDs_ExcludesTheService(t *testing.T) {
	home, tuiProject, tui2Project, exeProject, driverDir := opencodeV2Processes(t)
	live, err := opencode.LiveClientCWDs()
	if err != nil {
		t.Fatalf("LiveClientCWDs: %v", err)
	}
	for _, cwd := range []string{tuiProject, tui2Project, exeProject} {
		if _, ok := live[cwd]; !ok {
			t.Errorf("client cwd %s missing from the live set %v", cwd, live)
		}
	}
	if _, ok := live[home]; ok {
		t.Errorf("the service's cwd %s (service in $HOME is not a client) is in the live set %v", home, live)
	}
	if _, ok := live[driverDir]; ok {
		t.Errorf("a driver naming opencode only in its arguments put %s in the live set %v", driverDir, live)
	}
}

// opencodeV2StoreWith builds an opencode 2.x store from opencode 2.1.4's real
// schema (the adapter's testdata/v2-schema.sql) holding one session_v2 row per
// id — archived when archived[id] — and returns its path.
func opencodeV2StoreWith(t *testing.T, dir string, archived map[string]bool, ids ...string) string {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("..", "opencode", "testdata", "v2-schema.sql"))
	if err != nil {
		t.Fatalf("read the 2.x schema: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("create the 2.x schema: %v", err)
	}
	for _, id := range ids {
		var archivedAt any
		if archived[id] {
			archivedAt = int64(1792960000000)
		}
		if _, err := db.Exec(`INSERT INTO session_v2 (id, project_id, slug, directory, version, time_created, time_updated, time_archived)
			VALUES (?, 'prj', 'slug', ?, '2.1.4', 1792950000000, 1792950000000, ?)`, id, dir, archivedAt); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	return dbPath
}

// opencodeV1Store builds a store with only 1.x's `session` table — no
// session_v2 — holding id, and returns its path.
func opencodeV1Store(t *testing.T, dir, id string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL, time_archived INTEGER)`,
		`INSERT INTO session (id, directory) VALUES ('` + id + `', '` + dir + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return dbPath
}

// sessionKey is the watcher's key for a session: the store's WAL with the
// session id in a query parameter.
func sessionKey(dbPath, id string) string { return dbPath + "-wal?session=" + id }

// TestOpenCodeDiscoverPID_BindsAClientlessSessionToTheService is #2107's
// red-first test for binding. A session no client runs — created through the
// service's HTTP API, the desktop app or the web app — binds to the one 2.x
// service, which hosts it. On the P2 build it bound to nothing (PID 0), and
// with no PID neither the service's exit nor the same-PID policy reached it.
//
// The other rows are locks on what the fallback must not reach: a client in
// the directory still wins, and the service is bound only for a live 2.x row —
// not an archived one, and not a 1.x store's session, which no 2.x service
// hosts (another HOME's service can run beside the daemon's own 1.x store).
func TestOpenCodeDiscoverPID_BindsAClientlessSessionToTheService(t *testing.T) {
	_, tuiProject, _, _, _ := opencodeV2Processes(t)
	elsewhere := canonicalTempDir(t)
	v2 := opencodeV2StoreWith(t, elsewhere, map[string]bool{"ses_archived": true}, "ses_api", "ses_tui", "ses_archived")
	v1 := opencodeV1Store(t, elsewhere, "ses_v1")
	for _, c := range []struct {
		name, cwd, key string
		want           int
	}{
		{"client-less 2.x session binds to the service", elsewhere, sessionKey(v2, "ses_api"), 42001},
		{"a client in the session's directory still wins", tuiProject, sessionKey(v2, "ses_tui"), 42002},
		{"an archived session is not bound to the service", elsewhere, sessionKey(v2, "ses_archived"), 0},
		{"a session the store does not hold is not bound to the service", elsewhere, sessionKey(v2, "ses_unknown"), 0},
		{"a 1.x store's session is not bound to a 2.x service", elsewhere, sessionKey(v1, "ses_v1"), 0},
	} {
		got, err := opencode.DiscoverPID(c.cwd, c.key, nil)
		if err != nil || got != c.want {
			t.Errorf("%s: DiscoverPID(%s) = (%d, %v), want (%d, nil)", c.name, c.cwd, got, err, c.want)
		}
	}
}

// TestOpenCodeDiscoverPID_BindsNoServiceWhenItCannotTellWhich: the service
// binds a fixed port, so one runs per user — but another HOME's or another
// channel's can run beside it, and which one hosts a session is not in its
// row. Two services bind none rather than guess; none binds none.
func TestOpenCodeDiscoverPID_BindsNoServiceWhenItCannotTellWhich(t *testing.T) {
	home, other, project := canonicalTempDir(t), canonicalTempDir(t), canonicalTempDir(t)
	key := sessionKey(opencodeV2StoreWith(t, project, nil, "ses_api"), "ses_api")
	for _, c := range []struct {
		name  string
		procs map[int]processlifecycle.FakeProcess
	}{
		{"two services", map[int]processlifecycle.FakeProcess{
			43001: {Name: "opencode", CWD: home, Argv: []string{"/opt/homebrew/bin/opencode", "serve", "--service"}},
			43002: {Name: "opencode.exe", CWD: other, Argv: []string{"/x/node_modules/@opencode/cli/bin/opencode.exe", "serve", "--service"}},
		}},
		{"no service", map[int]processlifecycle.FakeProcess{
			43003: {Name: "opencode", CWD: home, Argv: []string{"/opt/homebrew/bin/opencode", "serve"}},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			processlifecycle.SetFakeProcessesForTest(t, c.procs)
			if got, err := opencode.DiscoverPID(project, key, nil); err != nil || got != 0 {
				t.Errorf("DiscoverPID = (%d, %v), want (0, nil)", got, err)
			}
		})
	}
}
