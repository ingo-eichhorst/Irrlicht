//go:build darwin

package processlifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"irrlicht/core/domain/session"
)

// This file covers #1501: a tmux pane's host resolved from the client tmux
// itself names, rather than from a process-ancestry walk that terminates at
// the reparented server.
//
// The live half is deliberately NOT committed here. Driving a real tmux server
// needs a tmux on the machine, a short-enough socket path (sun_path is capped
// at 104 bytes, which t.TempDir() under /var/folders exceeds) and a pty for the
// client — none of which a CI runner has, and a test that SKIPS on all three is
// a gate whose absence reads as a pass. What the live run measured is recorded
// where the code depends on it instead: tmuxListedClients' exit-status table
// and tmuxClientLauncher's reparenting note.

// fakeTmux writes an executable stand-in for the tmux CLI that prints stdout
// and exits with code, and points tmuxPath at it for the duration of the test.
// It is how the whole chain — scan, candidate loop, adoption — is driven
// without a tmux server.
func fakeTmux(t *testing.T, stdout string, code int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "tmux")
	// A script rather than a Go fake, because tmuxClientPIDs closes its
	// arguments over an exec.CommandContext on tmuxPath — the seam being
	// exercised is that var, so the fake has to be an executable.
	body := "#!/bin/sh\nprintf '%s' \"" + stdout + "\"\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	prev := tmuxPath
	tmuxPath = script
	t.Cleanup(func() { tmuxPath = prev })
}

// forgetTmuxClientMemo drops socketPath's memo entry. Every socket a test
// touches leaves one behind — non-answers included — so this is the single
// spelling of the locking protocol, mirroring forgetHerdrClientMemo.
func forgetTmuxClientMemo(socketPath string) {
	tmuxClientHosts.mu.Lock()
	defer tmuxClientHosts.mu.Unlock()
	delete(tmuxClientHosts.entries, socketPath)
}

func TestTmuxClientPIDsFrom(t *testing.T) {
	cases := map[string]struct {
		out  string
		want []int
	}{
		"one client":                                  {"38291\n", []int{38291}},
		"two clients":                                 {"24980\n25162\n", []int{24980, 25162}},
		"no trailing newline":                         {"7", []int{7}},
		"detached: exit 0, no rows":                   {"", nil},
		"blank lines are skipped":                     {"\n\n41\n\n", []int{41}},
		"a row we cannot parse is skipped, not fatal": {"nonsense\n41\n", []int{41}},
		"a nonsense pid is skipped":                   {"0\n-3\n41\n", []int{41}},
	}
	for name, tc := range cases {
		if got := tmuxClientPIDsFrom(tc.out); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: tmuxClientPIDsFrom(%q) = %v, want %v", name, tc.out, got, tc.want)
		}
	}
}

// TestTmuxListedClients is the classification #1501 could most easily have got
// wrong by copying lsofProbeRan. tmux has no "nothing to report" status: the
// detach case is exit ZERO with empty output, so every non-zero exit is a
// failure to reach the server and carries no verdict about who is attached.
//
// Getting it wrong is not a cosmetic difference — it is #1485 in a new tool. A
// socket we cannot reach would be reported as an authoritative detach, and the
// sweep would clear a resolved host on it.
func TestTmuxListedClients(t *testing.T) {
	if !tmuxListedClients(nil) {
		t.Error("exit 0 is the only answer tmux gives, and it must be one")
	}
	// The discriminator against lsof's reading, spelled as a comparison so the
	// two classifiers cannot silently converge.
	exit1 := runFakeExit(t, 1)
	if tmuxListedClients(exit1) {
		t.Error("tmux exit 1 is 'no server running' / 'error connecting' — not a detach")
	}
	if !lsofProbeRan(exit1) {
		t.Error("lsof's exit 1 IS an answer; if this ever changes the contrast above stops being the point")
	}
	if tmuxListedClients(runFakeExit(t, 2)) {
		t.Error("no non-zero exit is allowlisted for tmux")
	}
	if _, err := runProbe(context.Background(), probeTmuxClients, missingBinaryCmd()); tmuxListedClients(err) {
		t.Error("a child that never started answered nothing (#1538)")
	}
}

// runFakeExit produces the error a child that ran to a normal exit with code
// returns — the shape probeAnswered classifies, obtained from a real child
// rather than a hand-built *exec.ExitError.
func runFakeExit(t *testing.T, code int) error {
	t.Helper()
	_, err := runProbe(context.Background(), probeTmuxClients, answeringCmd(strconv.Itoa(code)))
	if code != 0 && err == nil {
		t.Fatalf("exit %d must produce an error", code)
	}
	return err
}

func TestTmuxClientPIDsVia(t *testing.T) {
	ctx := context.Background()

	t.Run("a live server reports its clients newest-attach-first", func(t *testing.T) {
		pids, probed := tmuxClientPIDsVia(ctx, answeringStdout("24980\n25162\n"))
		if !probed {
			t.Fatal("exit 0 answered")
		}
		// Descending PID: pids are allocated ascending, so the newest attach is
		// the window the user most recently chose. Same rule as herdr's.
		if !reflect.DeepEqual(pids, []int{25162, 24980}) {
			t.Errorf("got %v, want newest-first [25162 24980]", pids)
		}
	})

	t.Run("a live server with nothing attached is an ANSWER", func(t *testing.T) {
		pids, probed := tmuxClientPIDsVia(ctx, answeringStdout(""))
		if !probed {
			t.Fatal("exit 0 with no rows is 'nothing is displaying this pane', not a failed probe")
		}
		if len(pids) != 0 {
			t.Errorf("got %v, want none", pids)
		}
	})

	t.Run("an unreachable server is NOT a detach", func(t *testing.T) {
		if _, probed := tmuxClientPIDsVia(ctx, answeringCmd("1")); probed {
			t.Error("exit 1 must poison the answer, or a stale socket clears a live host (#1485)")
		}
	})

	t.Run("a child that never started is not a detach either", func(t *testing.T) {
		if _, probed := tmuxClientPIDsVia(ctx, missingBinaryCmd()); probed {
			t.Error("a probe that did not run says nothing about who is attached")
		}
	})
}

// answeringStdout builds a child that prints out on stdout and exits 0 — the
// shape `tmux list-clients -F` answers in.
func answeringStdout(out string) shelloutCmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s' \"$0\"", out)
	}
}

// TestTmuxClientPIDs_NoBinaryIsNotADetach pins the reading tmuxClientPIDs
// deliberately does NOT share with kittyWindowIDForPIDVia's absent kitten. A
// session in a tmux pane is standing proof that tmux exists on this machine, so
// failing to find the binary is our lookup failing, not a fact about the
// installation — and answering "nothing attached" would clear a host on it.
func TestTmuxClientPIDs_NoBinaryIsNotADetach(t *testing.T) {
	prev := tmuxPath
	tmuxPath = ""
	t.Cleanup(func() { tmuxPath = prev })
	if _, probed := tmuxClientPIDs(context.Background(), "/tmp/whatever"); probed {
		t.Error("no tmux binary is 'I could not look', not 'nobody is attached'")
	}
	tmuxPath = prev
	if _, probed := tmuxClientPIDs(context.Background(), ""); probed {
		t.Error("no socket path is no address to ask about")
	}
}

// TestTmuxClientPIDsIsCountedUnderItsOwnKind is #1534's runtime half for the
// ninth probe: the site reaches the counter, under tmux.list_clients and
// nothing else. TestEveryProbeSiteDeclaresItsOwnKind grades the declaration
// statically; this grades the wiring.
func TestTmuxClientPIDsIsCountedUnderItsOwnKind(t *testing.T) {
	before := readProbeLedger()
	if _, probed := tmuxClientPIDsVia(context.Background(), answeringStdout("41\n")); !probed {
		t.Fatal("exit 0 answered")
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"tmux.list_clients": {Probe: "tmux.list_clients", Answered: 1},
	}, "tmuxClientPIDsVia's shellout is the tmux.list_clients probe")

	before = readProbeLedger()
	if _, probed := tmuxClientPIDsVia(context.Background(), missingBinaryCmd()); probed {
		t.Fatal("a tmux that never started must report probed=false")
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"tmux.list_clients": {Probe: "tmux.list_clients", Unanswered: 1},
	}, "the non-answer is the one that must not read as a detach")
}

// TestResolveTmuxClientLauncherVia covers the tri-state the whole indirection
// hands its caller. The loop, the cap and the budget behind it are
// resolveClientHostIdentityVia's and are covered by the #1492/#1529 tests; what
// is new here is that tmux's scan feeds it with the same (pids, probed) shape
// herdr's does.
func TestResolveTmuxClientLauncherVia(t *testing.T) {
	iterm := func(context.Context, int) (*session.Launcher, bool) {
		return &session.Launcher{TermProgram: "iTerm.app", TTY: "/dev/ttys012"}, true
	}

	t.Run("a scan that did not run answers nothing", func(t *testing.T) {
		scan := func(context.Context, string) ([]int, bool) { return nil, false }
		if host, probed := resolveTmuxClientLauncherVia("/s", scan, iterm); host != nil || probed {
			t.Errorf("got (%+v, %v), want (nil, false)", host, probed)
		}
	})

	t.Run("no client attached is an answer, and it is 'no host'", func(t *testing.T) {
		scan := func(context.Context, string) ([]int, bool) { return nil, true }
		host, probed := resolveTmuxClientLauncherVia("/s", scan, iterm)
		if host != nil {
			t.Errorf("nothing is displaying this pane: %+v", host)
		}
		if !probed {
			t.Error("every candidate (none) was read, so this is evidence")
		}
	})

	t.Run("an attached client's identity is the pane's host", func(t *testing.T) {
		scan := func(context.Context, string) ([]int, bool) { return []int{4242}, true }
		host, probed := resolveTmuxClientLauncherVia("/s", scan, iterm)
		if !probed || host == nil || host.TermProgram != "iTerm.app" {
			t.Fatalf("got (%+v, %v), want the client's identity", host, probed)
		}
	})

	t.Run("a candidate that could not be read poisons the negative answer", func(t *testing.T) {
		scan := func(context.Context, string) ([]int, bool) { return []int{4242}, true }
		unreadable := func(context.Context, int) (*session.Launcher, bool) {
			return &session.Launcher{}, false
		}
		if host, probed := resolveTmuxClientLauncherVia("/s", scan, unreadable); host != nil || probed {
			t.Errorf("got (%+v, %v), want (nil, false) — #1492 inherited for free", host, probed)
		}
	})
}

// TestTmuxClientHostMemoCollapsesRepeats is #1501's cost profile in the form
// that actually matters: every pane of one tmux server shares a socket, and a
// user with eight panes open is ordinary. Without the memo the startup seed and
// every liveness sweep would pay one scan per pane.
func TestTmuxClientHostMemoCollapsesRepeats(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "tmux-sock")
	t.Cleanup(func() { forgetTmuxClientMemo(socket) })

	scans := 0
	probe := func(string) (*session.Launcher, bool) {
		scans++
		return &session.Launcher{TermProgram: "ghostty"}, true
	}
	for i := 0; i < 8; i++ {
		if host, probed := tmuxClientHosts.resolve(socket, probe); !probed || host.TermProgram != "ghostty" {
			t.Fatalf("resolve %d: got (%+v, %v)", i, host, probed)
		}
	}
	if scans != 1 {
		t.Errorf("eight panes of one server cost %d scans, want 1", scans)
	}
}

// TestTmuxAndHerdrMemosDoNotShareEntries pins the reason clientHostMemo is a
// TYPE with two instances rather than one map keyed by socket path: the two
// namespaces are unrelated, and a collision would serve one multiplexer's
// answer to the other's pane.
func TestTmuxAndHerdrMemosDoNotShareEntries(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "same-path")
	t.Cleanup(func() { forgetTmuxClientMemo(socket); forgetHerdrClientMemo(socket) })

	tmuxClientHosts.put(socket, &session.Launcher{TermProgram: "ghostty"}, true)
	if _, hit := herdrClientHosts.get(socket); hit {
		t.Error("a tmux answer must not be served to a herdr pane on the same path")
	}
}

// spawnClientSleeperWithEnv starts a stand-in for an attached client that
// outlives the whole test.
//
// spawnSleeperWithEnv's helper exits after 3s, and under a loaded
// `go test ./core/... -race` run these cases reached ReadLauncherEnv after it
// had gone — so hostIdentity read a dead pid, answered complete=false, and the
// case failed on the fixture rather than on the code. Measured: one such flake
// in a full-suite run. GO_WANT_LAUNCHER_HELPER_HOLD is TestHelperProcess's
// existing 30s path, added for the herdr tests for the same reason.
func spawnClientSleeperWithEnv(t *testing.T, env []string) int {
	t.Helper()
	return spawnSleeperWithEnv(t, append(env,
		"GO_WANT_LAUNCHER_HELPER_HOLD="+filepath.Join(t.TempDir(), "held")))
}

// spawnPaneSleeperWithEnv starts a helper whose PARENT IS INIT, which is what
// makes it a faithful stand-in for a process inside a tmux pane.
//
// spawnSleeperWithEnv's helper is a child of the test binary, so its ancestry
// walk climbs `go test` → the shell → the terminal and resolves a host — and
// that host is precisely what tmuxPaneAwaitsItsClient reads as "this process
// reported a host of its own, leave it alone". A real pane cannot do that: the
// tmux server daemonizes and is reparented to PID 1 (measured, tmux 3.6a), so
// the walk from a pane terminates at init having found nothing, which is what
// leaves the two host fields empty for a genuine pane and for nothing else.
//
// Backgrounding from a shell that then exits reproduces exactly that
// reparenting, and it is the difference between this file testing the gate and
// testing the harness: without it these cases fail on the fixture's ancestry
// rather than on anything the code did.
func spawnPaneSleeperWithEnv(t *testing.T, env []string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c",
		os.Args[0]+" -test.run='^TestHelperProcess$' -test.count=1 >/dev/null 2>&1 & echo $!")
	cmd.Env = append([]string{
		"GO_WANT_LAUNCHER_HELPER=1",
		// Outlives the test for the reason spawnClientSleeperWithEnv gives.
		"GO_WANT_LAUNCHER_HELPER_HOLD=" + filepath.Join(t.TempDir(), "held"),
	}, env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("spawn orphaned helper: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("helper pid %q: %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// Poll for the two conditions this fixture's claim rests on, rather than
	// sleeping a fixed 120ms and hard-failing if they have not happened yet
	// (#1586: 120ms was enough on an idle machine and a guarantee on none, on
	// the hot path of the family this file is part of, where a timing flake is
	// indistinguishable from a regression in whatever is under review).
	//
	// Both, and in this order, because the sleep was covering a different one
	// from the hard-fail — see waitForLauncherEnv for the measurement. The env
	// is the condition that is genuinely pending here; the reparenting is what
	// makes the pid a faithful stand-in for a pane, and its `ps` is what a
	// loaded machine starves.
	waitForLauncherEnv(t, pid, cmd.Env)
	waitForReparentToInit(t, pid)
	return pid
}

// --- end to end, through ReadLauncherEnv ------------------------------------
//
// These drive the whole chain against real processes whose env sysctl can
// actually read (spawnSleeperWithEnv self-execs the unsigned test binary),
// with only the tmux CLI faked. That is the seam worth faking: everything
// below it — the env whitelist, launcherFromEnv's suppression, the ancestry
// fallbacks, hostIdentity of the client, AdoptHostIdentity's put-back — is the
// production code.

// TestReadLauncherEnv_Tmux_AttachedClientIsTheHost is the green half of
// #1501's defect. Measured live before the fix, against a real tmux 3.6a
// server with a client attached: the pane resolved to
// {TmuxPane, TmuxSocket, TTY} and nothing else, so
// SessionLauncher.resolveActivator returned nil and clicking the session did
// nothing.
func TestReadLauncherEnv_Tmux_AttachedClientIsTheHost(t *testing.T) {
	clientPID := spawnClientSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=iTerm.app",
		"ITERM_SESSION_ID=w0t0p0-CLIENT",
	})
	fakeTmux(t, strconv.Itoa(clientPID)+"\n", 0)

	socket := "/tmp/tmux-501/e2e-attached"
	t.Cleanup(func() { forgetTmuxClientMemo(socket) })
	agentPID := spawnPaneSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=tmux",
		"TMUX=" + socket + ",1234,0",
		"TMUX_PANE=%17",
	})

	l, hostKnown := ReadLauncherEnv(agentPID)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if !hostKnown {
		t.Error("the scan ran and answered, so the host was determined")
	}
	if l.TermProgram != "iTerm.app" || l.ITermSessionID != "w0t0p0-CLIENT" {
		t.Errorf("the attached client's window was not adopted: %+v", l)
	}
	if l.TmuxPane != "%17" || l.TmuxSocket != socket {
		t.Errorf("the pane lost the address control and TmuxActivator route on: %+v", l)
	}
}

// TestReadLauncherEnv_Tmux_DetachedResolvesNoHost is the honest-failure half,
// and the exact counterpart of the herdr one: a tmux server with no client
// attached answers exit 0 with no rows, which is evidence that nothing is
// displaying this pane rather than a probe that failed.
func TestReadLauncherEnv_Tmux_DetachedResolvesNoHost(t *testing.T) {
	fakeTmux(t, "", 0)

	socket := "/tmp/tmux-501/e2e-detached"
	t.Cleanup(func() { forgetTmuxClientMemo(socket) })
	agentPID := spawnPaneSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=tmux",
		"TMUX=" + socket + ",1234,0",
		"TMUX_PANE=%17",
		// Inherited from whatever terminal started the server, and wrong:
		// #1499's suppression is what keeps these out, and this asserts the
		// client indirection does not put them back by another route.
		"ITERM_SESSION_ID=w0t0p0-STALE",
		"KITTY_WINDOW_ID=42",
	})

	l, hostKnown := ReadLauncherEnv(agentPID)
	if l == nil {
		t.Fatal("expected non-nil launcher (the pane address is still identity)")
	}
	if !hostKnown {
		t.Error("an empty client list is an ANSWER: the server was reached")
	}
	if l.TermProgram != "" || l.HostBundleID != "" {
		t.Errorf("no client is attached, so no host may be reported: %+v", l)
	}
	if l.ITermSessionID != "" || l.KittyWindowID != "" {
		t.Errorf("inherited identity must stay suppressed: %+v", l)
	}
	if l.TmuxPane != "%17" {
		t.Errorf("TmuxPane: got %q", l.TmuxPane)
	}
}

// TestReadLauncherEnv_Tmux_UnreachableServerKeepsNothingAndKnowsNothing is the
// third state. An exit-1 tmux says nothing about who is attached, so hostKnown
// must be false — that bit is the only thing standing between a stale socket
// and the sweep clearing a resolved host on it (#1485/#1492).
func TestReadLauncherEnv_Tmux_UnreachableServerKeepsNothingAndKnowsNothing(t *testing.T) {
	fakeTmux(t, "no server running on /tmp/x\n", 1)

	socket := "/tmp/tmux-501/e2e-unreachable"
	t.Cleanup(func() { forgetTmuxClientMemo(socket) })
	agentPID := spawnPaneSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=tmux",
		"TMUX=" + socket + ",1234,0",
		"TMUX_PANE=%17",
	})

	l, hostKnown := ReadLauncherEnv(agentPID)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if hostKnown {
		t.Error("a tmux we could not reach determined nothing; reporting otherwise clears a live host on the next sweep")
	}
	if l.TmuxPane != "%17" {
		t.Errorf("TmuxPane: got %q", l.TmuxPane)
	}
}

// TestReadLauncherEnv_Tmux_InheritedPaneKeepsItsOwnHost is the guard that stops
// #1501 from re-opening #1499. A GUI terminal or IDE launched from inside a
// pane inherits $TMUX_PANE while reporting a perfectly good host of its own.
// Resolving THAT pane's client and adopting it would replace a correct host
// with the window displaying a pane the session is not in.
//
// Two claims, and the second is the one a naive gate fails: the identity
// survives, AND tmux is never consulted at all — asserted by pointing tmuxPath
// at a fake that would resolve a different host if it ran.
//
// Since #1582 it carries a third, which is the one that was seen RED: the
// inherited address is not recorded either. Until then the launcher kept it
// beside the correct host, control.resolveBackend picked the tmux backend from
// that field alone, and the backchannel typed into a pane in a window the user
// was not looking at.
//
// That third claim is why the hostKnown assertion below reads the other way
// round from #1501's. The tri-state answers "was the host of the pane this
// launcher ADDRESSES determined", and this launcher now addresses no pane at
// all — so true is not a claim about the foreign pane's client, it is the
// ordinary answer for an ordinary launcher, and it is what keeps this session
// out of the liveness sweep's per-tick re-read (services.hostedInAMultiplexerPane).
// The state it used to protect against is gone with the address.
func TestReadLauncherEnv_Tmux_InheritedPaneKeepsItsOwnHost(t *testing.T) {
	otherClientPID := spawnClientSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=ghostty",
	})
	fakeTmux(t, strconv.Itoa(otherClientPID)+"\n", 0)

	socket := "/tmp/tmux-501/e2e-inherited"
	t.Cleanup(func() { forgetTmuxClientMemo(socket) })
	agentPID := spawnClientSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		// Its own, reported by itself — so launcherFromEnv never suppressed it.
		"TERM_PROGRAM=iTerm.app",
		"ITERM_SESSION_ID=w0t0p0-OWN",
		// Inherited from the pane the terminal was launched from.
		"TMUX=" + socket + ",1234,0",
		"TMUX_PANE=%17",
	})

	l, hostKnown := ReadLauncherEnv(agentPID)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if l.TermProgram != "iTerm.app" || l.ITermSessionID != "w0t0p0-OWN" {
		t.Errorf("a session that reported its own host must keep it: %+v", l)
	}
	if l.TmuxPane != "" || l.TmuxSocket != "" {
		t.Errorf("an inherited pane address must not be recorded — control.resolveBackend routes on it "+
			"and would send-keys into a pane in someone else's window (#1582): pane=%q socket=%q",
			l.TmuxPane, l.TmuxSocket)
	}
	if !hostKnown {
		t.Error("this launcher addresses no pane, so there is no pane host left unlooked-up to report: " +
			"answering false would put a plain session back into the sweep's per-tick re-read")
	}
}

// TestReadLauncherEnv_Tmux_KittyLaunchedFromAPaneKeepsTheAncestryHost is the
// harder version of the same guard, and the one the three-term gate exists for.
// kitty sets no $TERM_PROGRAM of its own (upstream kitty#4793), so a kitty
// launched from a tmux pane inherits TERM_PROGRAM=tmux along with $TMUX_PANE —
// launcherFromEnv DOES suppress it, and the ancestry fallbacks are what
// recover the real host. Keying the indirection on the pane address alone
// would overwrite that recovery with the pane's client.
//
// The ancestry host is stood in for here by a HostBundleID: this test binary's
// real ancestry is `go test`, not a terminal, so the fallbacks resolve nothing
// on their own. What is being pinned is the gate's reading of the field, which
// is why the assertion is that tmux was not consulted.
func TestReadLauncherEnv_Tmux_KittyLaunchedFromAPaneKeepsTheAncestryHost(t *testing.T) {
	l := &session.Launcher{
		TmuxPane:     "%17",
		TmuxSocket:   "/tmp/tmux-501/x",
		HostBundleID: "net.kovidgoyal.kitty",
	}
	if tmuxPaneAwaitsItsClient(l) {
		t.Error("a host recovered by the ancestry walk is this process's own; the pane's client must not replace it")
	}
	_, probed, addressesAPane := clientHostFor(l)
	if !addressesAPane {
		t.Error("it does carry a pane address — the sweep has to be told that, or it cannot tell this apart from a plain session")
	}
	if probed {
		t.Error("that pane's host was not looked up, and saying otherwise lets a fresh read overwrite this identity")
	}
}
