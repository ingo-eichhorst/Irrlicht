//go:build darwin || linux

package services_test

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Issue #2107: opencode 2.x sessions created through the service's HTTP API,
// the desktop app or the web app have no client process. They live inside the
// one `opencode serve --service` process per user, so every such root binds to
// that one PID. These tests wire the PID manager from the real opencode
// declaration through the projections startup.go uses (agents.SharedPIDOwners,
// agents.SessionHosts, agents.LauncherPIDs) and stand a real process in for
// the service: the probe reads the PID's argv itself, through
// processlifecycle.ReadArgv, so a faked argv table cannot reach it.

// opencodeServiceArgv is the service's argv as the sandbox's `ps -o args= -p`
// shows it: `/Users/factory/.oc2sb/shim/opencode serve --service`, path
// shortened.
var (
	opencodeServiceArgv = []string{"/x/shim/opencode", "serve", "--service"}
	opencodeTUIArgv     = []string{"/opt/homebrew/bin/opencode"}
)

// opencodeStandInDeadline bounds the wait for a stand-in's argv to read back:
// on Linux /proc/<pid>/cmdline can read empty right after Start, before the
// child's exec has landed.
const opencodeStandInDeadline = 10 * time.Second

// startOpencodeStandIn starts bash under argv — argv[0] as given, which is
// what ReadArgv reports — and returns its PID once ReadArgv reads argv back,
// polled to opencodeStandInDeadline. With arguments after argv[0], bash runs
// the first as a script file, written here into the process's cwd, and takes
// the rest as its positional parameters; with none, it reads commands from its
// stdin. Either way it blocks reading stdin until cleanup closes it, and the
// script runs only builtins (read, :), so it never forks a second process.
// Checked on darwin with `ps -o args= -p <pid>`: the stand-in for
// opencodeServiceArgv lists as `/x/shim/opencode serve --service`.
func startOpencodeStandIn(t *testing.T, argv []string) int {
	t.Helper()
	dir := t.TempDir()
	if len(argv) > 1 {
		if err := os.WriteFile(filepath.Join(dir, argv[1]), []byte("while read -r _; do :; done\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := &exec.Cmd{Path: "/bin/bash", Args: argv, Dir: dir}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start opencode stand-in: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	start := time.Now()
	var got []string
	if !pollUntil(opencodeStandInDeadline, func() bool {
		got = processlifecycle.ReadArgv(cmd.Process.Pid)
		return slices.Equal(got, argv)
	}) {
		t.Fatalf("after %v the stand-in's argv reads %q, want %q",
			time.Since(start).Round(time.Millisecond), got, argv)
	}
	return cmd.Process.Pid
}

// opencodeV2Store is an opencode 2.x store holding session_v2 rows, built from
// opencode 2.1.4's real schema (the adapter's testdata/v2-schema.sql).
type opencodeV2Store struct {
	t      *testing.T
	dbPath string
	db     *sql.DB
}

func newOpencodeV2Store(t *testing.T) *opencodeV2Store {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("..", "..", "adapters", "inbound", "agents", "opencode", "testdata", "v2-schema.sql"))
	if err != nil {
		t.Fatalf("read the 2.x schema: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("create the 2.x schema: %v", err)
	}
	return &opencodeV2Store{t: t, dbPath: dbPath, db: db}
}

// session inserts a live session_v2 row for id in dir and returns the
// transcript path the watcher keys it on.
func (s *opencodeV2Store) session(id, dir string) string {
	s.t.Helper()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO session_v2 (id, project_id, slug, directory, version, time_created, time_updated)
		VALUES (?, 'prj', 'slug', ?, '2.1.4', ?, ?)`, id, dir, now, now); err != nil {
		s.t.Fatalf("insert session %s: %v", id, err)
	}
	return s.dbPath + "-wal?session=" + id
}

func (s *opencodeV2Store) archive(id string) {
	s.t.Helper()
	if _, err := s.db.Exec(`UPDATE session_v2 SET time_archived = ? WHERE id = ?`, time.Now().UnixMilli(), id); err != nil {
		s.t.Fatalf("archive %s: %v", id, err)
	}
}

// opencodeRoot builds a client-less opencode root bound to pid, first seen age
// ago and last updated idle ago.
func opencodeRoot(id, cwd, transcriptPath, state string, pid int, age, idle time.Duration) *session.SessionState {
	now := time.Now()
	return &session.SessionState{
		SessionID: id, Adapter: opencode.AdapterName, State: state,
		PID: pid, CWD: cwd, TranscriptPath: transcriptPath,
		FirstSeen: now.Add(-age).Unix(), UpdatedAt: now.Add(-idle).Unix(),
	}
}

// opencodeSharedPIDFixture is two client-less opencode roots in one store,
// both hosted by one live service stand-in — the shape of qa-run.sh's S1 and
// S2 sessions. first is the older root.
type opencodeSharedPIDFixture struct {
	store         *opencodeV2Store
	servicePID    int
	cwd           string
	first, second string // transcript paths
}

func newOpencodeSharedPIDFixture(t *testing.T, hostArgv []string) *opencodeSharedPIDFixture {
	t.Helper()
	f := &opencodeSharedPIDFixture{store: newOpencodeV2Store(t), cwd: t.TempDir()}
	f.servicePID = startOpencodeStandIn(t, hostArgv)
	f.first = f.store.session("ses_first", f.cwd)
	f.second = f.store.session("ses_second", f.cwd)
	return f
}

func (f *opencodeSharedPIDFixture) seed(pidOfSecond int) (repo *mockRepo, first, second *session.SessionState) {
	first = opencodeRoot("ses_first", f.cwd, f.first, session.StateReady, f.servicePID, 2*time.Second, 0)
	second = opencodeRoot("ses_second", f.cwd, f.second, session.StateReady, pidOfSecond, time.Second, 0)
	repo = newMockRepo()
	repo.states[first.SessionID] = first
	repo.states[second.SessionID] = second
	return repo, first, second
}

// opencodeSharedPIDOwners wires ownership probes exactly as startup.go does.
func opencodeSharedPIDOwners() map[string]agent.SharedPIDOwnerFunc {
	return agents.SharedPIDOwners([]agent.Agent{opencode.Agent()})
}

func opencodeRootsPresent(repo *mockRepo) (first, second bool) {
	_, errFirst := repo.Load("ses_first")
	_, errSecond := repo.Load("ses_second")
	return errFirst == nil, errSecond == nil
}

// Red-first (#2107): two client-less opencode roots hosted by one service
// share its PID on every same-PID path. On the P2 build opencode declared no
// SharedPIDOwner, so each path kept only the newer root: qa-run.sh's S2
// binding would have evicted S1.
func TestSamePIDPaths_ClientlessOpencodeRootsShareTheServicePID(t *testing.T) {
	for _, tc := range []struct {
		path        string
		secondBound bool
		act         func(pm *services.PIDManager, f *opencodeSharedPIDFixture, first, second *session.SessionState)
	}{
		{"assignment", false, func(pm *services.PIDManager, f *opencodeSharedPIDFixture, _, second *session.SessionState) {
			pm.HandlePIDAssigned(f.servicePID, second.SessionID)
		}},
		{"periodic", true, func(pm *services.PIDManager, _ *opencodeSharedPIDFixture, _, _ *session.SessionState) {
			pm.CheckPIDLiveness()
		}},
		{"seed", true, func(pm *services.PIDManager, _ *opencodeSharedPIDFixture, first, second *session.SessionState) {
			pm.SeedPIDs([]*session.SessionState{first, second})
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			f := newOpencodeSharedPIDFixture(t, opencodeServiceArgv)
			pidOfSecond := 0
			if tc.secondBound {
				pidOfSecond = f.servicePID
			}
			repo, first, second := f.seed(pidOfSecond)

			tc.act(newPIDManagerWithSharedPIDOwners(repo, opencodeSharedPIDOwners()), f, first, second)

			if a, b := opencodeRootsPresent(repo); !a || !b {
				t.Fatalf("the %s path removed a client-less opencode root sharing the service's pid "+
					"(ses_first present=%v, ses_second present=%v)", tc.path, a, b)
			}
		})
	}
}

// Shared ownership is not "kept forever": once the older root's session is
// archived, its probe answers no and the periodic sweep retires it, keeping
// the newer one. Seen red with the probe's session_v2 read removed
// (tools/lib/opencode-v2-service-mutations_test.sh).
func TestCheckPIDLiveness_ArchivedOpencodeRootOnTheServiceIsRetired(t *testing.T) {
	f := newOpencodeSharedPIDFixture(t, opencodeServiceArgv)
	repo, _, _ := f.seed(f.servicePID)
	f.store.archive("ses_first")

	newPIDManagerWithSharedPIDOwners(repo, opencodeSharedPIDOwners()).CheckPIDLiveness()

	first, second := opencodeRootsPresent(repo)
	if first {
		t.Error("an opencode root whose session was archived survived the same-PID sweep")
	}
	if !second {
		t.Error("the same-PID sweep removed the opencode root whose session is live")
	}
}

// Lock (#2107): inside one TUI the probe answers no for the client's PID, so
// the newer root still supersedes the older one there, as on 1.x — the
// service's shared ownership does not leak to a client.
func TestHandlePIDAssigned_OpencodeRootsOnOneClientStayExclusive(t *testing.T) {
	f := newOpencodeSharedPIDFixture(t, opencodeTUIArgv)
	repo, _, second := f.seed(0)

	newPIDManagerWithSharedPIDOwners(repo, opencodeSharedPIDOwners()).HandlePIDAssigned(f.servicePID, second.SessionID)

	first, stillSecond := opencodeRootsPresent(repo)
	if first {
		t.Error("the older opencode root survived a newer root binding its TUI's pid")
	}
	if !stillSecond {
		t.Error("the newer opencode root was removed")
	}
}

// opencodeHostedPIDManager wires opencode's declarations into a PID manager
// as startup.go does: the session-host predicate with the real argv reader,
// its (absent) launcher hook and release probe. consent, when non-nil, is the
// observe-consent gate.
func opencodeHostedPIDManager(repo *mockRepo, consent func(adapter string) bool) *services.PIDManager {
	all := []agent.Agent{opencode.Agent()}
	pm := services.NewPIDManager(services.PIDManagerDeps{
		Repo: repo, Log: &mockLogger{}, ReadyTTL: 10 * time.Minute,
		SharedPIDOwners:  agents.SharedPIDOwners(all),
		ReleasedPIDs:     agents.ReleasedPIDs(all),
		OnSessionDeleted: func(string) {},
	})
	pm.SetInfraReaper(agents.ArgvExcluders(all), processlifecycle.ReadArgv)
	pm.SetSessionHosts(agents.SessionHosts(all), processlifecycle.ReadArgv)
	pm.SetLauncherPIDs(agents.LauncherPIDs(all))
	if consent != nil {
		pm.SetConsentGate(consent)
	}
	return pm
}

// Red-first (#2107): a client-less session idles on the service, whose PID
// outlives it, and nothing in the store says a client has gone (#2104's
// detach measurement). The ready-session TTL ends it. On the P2 build a live
// PID kept any root from the TTL, so such a root stayed until the service
// exited. The other rows are locks: the root inside the TTL shows that the
// TTL, not the service binding, ends it; working is not ready, so the TTL
// leaves it alone; without opencode's observe consent the bound PID's argv is
// not read, so the live PID keeps protecting the root; and a root bound to a
// TUI client keeps that protection too — the rule is about the service, not
// about opencode, and the client's exit ends such a root.
func TestCheckPIDLiveness_IdleClientlessOpencodeRootEndsAtTheReadyTTL(t *testing.T) {
	noOpencode := func(adapter string) bool { return adapter != opencode.AdapterName }
	for _, tc := range []struct {
		name     string
		hostArgv []string
		state    string
		idle     time.Duration
		consent  func(string) bool
		wantGone bool
	}{
		{"ready, idle past the TTL", opencodeServiceArgv, session.StateReady, time.Hour, nil, true},
		{"ready, inside the TTL", opencodeServiceArgv, session.StateReady, time.Minute, nil, false},
		{"working, idle past the TTL", opencodeServiceArgv, session.StateWorking, time.Hour, nil, false},
		{"observe consent withheld", opencodeServiceArgv, session.StateReady, time.Hour, noOpencode, false},
		{"bound to a TUI client", opencodeTUIArgv, session.StateReady, time.Hour, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pid := startOpencodeStandIn(t, tc.hostArgv)
			store := newOpencodeV2Store(t)
			cwd := t.TempDir()
			repo := newMockRepo()
			root := opencodeRoot("ses_api", cwd, store.session("ses_api", cwd), tc.state, pid, 2*time.Hour, tc.idle)
			repo.states[root.SessionID] = root

			opencodeHostedPIDManager(repo, tc.consent).CheckPIDLiveness()

			if _, err := repo.Load("ses_api"); (err != nil) != tc.wantGone {
				t.Errorf("%s: idle opencode root on a live pid removed = %t, want %t",
					tc.name, err != nil, tc.wantGone)
			}
		})
	}
}

// Red-first (#2107): a client-less root bound to the service does not pair
// with a TUI placeholder in its directory — #2082's pairing, which retires it
// with no grace. That pairing assumes a hosted root's client left the
// placeholder behind, which is codex's shape: in 2.x a client's session binds
// to the client itself, and a service-bound root has no client. It went red
// once #2105 declared opencode's SessionHostArgv. Codex keeps the pairing:
// TestCheckPIDLiveness_CodexPlaceholderRetiredOnceItsRootBindsToAppServer is
// its lock.
//
// Only the pairing. Once the placeholder is older than preSessionSweepGrace
// (90s), the #113 same-cwd fallback (findSupersedingSession's matchCWD) still
// retires it, as it does a TUI's placeholder beside any other adapter's root
// in its cwd: the placeholder here is 30s old for that reason. Seen in review
// with a 120s-old placeholder; left as residue.
func TestCheckPIDLiveness_ServiceBoundOpencodeRootLeavesTheTUIPlaceholder(t *testing.T) {
	svc := startOpencodeStandIn(t, opencodeServiceArgv)
	tui := startOpencodeStandIn(t, opencodeTUIArgv)
	store := newOpencodeV2Store(t)
	cwd := t.TempDir()
	repo := newMockRepo()
	placeholder := fmt.Sprintf("proc-%d", tui)
	now := time.Now()
	repo.states[placeholder] = &session.SessionState{
		SessionID: placeholder, Adapter: opencode.AdapterName, State: session.StateReady,
		PID: tui, CWD: cwd, FirstSeen: now.Add(-30 * time.Second).Unix(), UpdatedAt: now.Unix(),
	}
	root := opencodeRoot("ses_api", cwd, store.session("ses_api", cwd), session.StateReady, svc, 10*time.Second, 0)
	repo.states[root.SessionID] = root

	opencodeHostedPIDManager(repo, nil).CheckPIDLiveness()

	if _, err := repo.Load(placeholder); err != nil {
		t.Fatalf("placeholder %s was retired by a service-bound opencode root in its cwd", placeholder)
	}
}

// Red-first (#2107 review): a root bound to the service captures no launcher.
// The service was spawned by some client and inherits that client's terminal
// env — on the dev host the sandbox service's `ps eww` showed TERM_PROGRAM=zed
// and a HERDR_PANE_ID — so reading the launcher from its PID would point
// every API, desktop or web session at an unrelated terminal. The triage's
// rule is "no launcher". The reader here answers for any PID, so the test is
// about which roots are asked, not about whose env is readable. A root bound
// to a TUI client is the lock: it keeps its launcher.
func TestLauncherCapture_ServiceBoundOpencodeRootTakesNoLauncher(t *testing.T) {
	for _, tc := range []struct {
		name         string
		hostArgv     []string
		seeded       bool // the root is already bound, as at daemon start
		wantLauncher bool
		act          func(pm *services.PIDManager, root *session.SessionState, pid int)
	}{
		{"service, assignment", opencodeServiceArgv, false, false, func(pm *services.PIDManager, root *session.SessionState, pid int) {
			pm.HandlePIDAssigned(pid, root.SessionID)
		}},
		{"service, seed", opencodeServiceArgv, true, false, func(pm *services.PIDManager, root *session.SessionState, _ int) {
			pm.SeedPIDs([]*session.SessionState{root})
		}},
		{"TUI client, assignment", opencodeTUIArgv, false, true, func(pm *services.PIDManager, root *session.SessionState, pid int) {
			pm.HandlePIDAssigned(pid, root.SessionID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pid := startOpencodeStandIn(t, tc.hostArgv)
			store := newOpencodeV2Store(t)
			cwd := t.TempDir()
			repo := newMockRepo()
			bound := 0
			if tc.seeded {
				bound = pid
			}
			root := opencodeRoot("ses_api", cwd, store.session("ses_api", cwd), session.StateReady, bound, time.Minute, 0)
			repo.states[root.SessionID] = root
			pm := opencodeHostedPIDManager(repo, nil)
			pm.SetLauncherEnvReader(func(int) (*session.Launcher, bool) {
				return &session.Launcher{TermProgram: "SpawningClientsTerminal"}, true
			})

			tc.act(pm, root, pid)

			got, err := repo.Load("ses_api")
			if err != nil {
				t.Fatalf("the root is gone: %v", err)
			}
			if (got.Launcher != nil) != tc.wantLauncher {
				t.Errorf("%s: launcher = %+v, want one captured = %t", tc.name, got.Launcher, tc.wantLauncher)
			}
		})
	}
}
