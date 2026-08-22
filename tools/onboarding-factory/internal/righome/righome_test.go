package righome

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/agent"
	"irrlicht/tools/onboarding-factory/internal/hookcov"
	"irrlicht/tools/onboarding-factory/internal/shard"
)

// permissionKeyHooks mirrors hookcov's private constant of the same value. The
// join below is pinned against hookcov.Declared(), so a rename on either side
// surfaces as an adapter reporting no hooks permission rather than as silence.
const permissionKeyHooks = "hooks"

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("resolving the repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestRigHomeTableMatchesTheAdapterRegistry is the static arm: every adapter
// that installs hooks into the user's config either has a row in the rig's
// table or a named reason it cannot have one, and neither declaration names an
// adapter that does not exist.
//
// This is the WEAKER of the two arms in this file and it is worth saying so.
// It cannot tell a good isolation story from a bad one — only that somebody
// decided. Its value is that a SEVENTH hook adapter is graded by existing
// rather than by whoever adds it remembering the recording rig exists, and that
// the two structural exemptions (claudecode, gemini-cli) are written down where
// the next person looks rather than rediscovered.
func TestRigHomeTableMatchesTheAdapterRegistry(t *testing.T) {
	rows, err := Table(repoRoot(t))
	if err != nil {
		t.Fatalf("reading the rig's home table: %v", err)
	}

	declares := hookcov.Declared()
	hooksAdapters := 0
	for _, d := range declares {
		if d {
			hooksAdapters++
		}
	}
	// Vacuity guard. Reconcile refuses an empty projection, but a projection
	// that is merely all-false passes every check below having graded nothing.
	if hooksAdapters == 0 {
		t.Fatalf("hookcov.Declared() reports no hooks-declaring adapter at all (%d adapters) — "+
			"the join is broken, not clean", len(declares))
	}

	for _, p := range Reconcile(rows, Unisolatable, declares) {
		t.Error(p)
	}
}

// TestEveryRigHomeRowRelocatesBothHalves is the arm that carries the weight.
//
// A row is a promise the rig acts on: it exports that variable before spawning
// the daemon, and the driver passes the same value to the agent CLI. Two things
// have to move for that to mean anything, and an adapter can move one without
// the other — claudecode is exactly that shape, which is why it is exempt
// rather than rowed:
//
//	the SESSION ROOT the daemon watches (agent.Source), and
//	every FILE the hooks install writes (Writes.Path plus every Also).
//
// If the session root does not move, the daemon watches the operator's real
// store and records their own sessions instead of the driver's. If a written
// file does not move, the install lands in the operator's real config while
// everything else claims to be isolated — the worst of the three outcomes,
// because it is the one that looks isolated.
//
// It grades the DECLARED variable against the REAL resolvers, so a row naming a
// variable no adapter reads fails here even though it is perfectly well formed
// and passes the static arm above.
func TestEveryRigHomeRowRelocatesBothHalves(t *testing.T) {
	rows, err := Table(repoRoot(t))
	if err != nil {
		t.Fatalf("reading the rig's home table: %v", err)
	}

	for _, row := range rows {
		t.Run(row.Adapter, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "scratch-home")
			t.Setenv(row.EnvVar, home)

			a := registryAdapterAfterEnv(t, row.Adapter)
			assertSessionRootRelocates(t, row, home, a)
			assertHookWritesRelocate(t, row, home, a)
		})
	}
}

// registryAdapterAfterEnv builds the registry and finds one adapter, and is a
// separate step because it must run AFTER the caller has set the env var.
//
// That ordering is the property rather than test hygiene. Three of the four
// rows (copilot, codex, kiro-cli) declare a static Source.Dir computed by their
// package's sessionsDir() at Agent() CONSTRUCTION time, so the watched root is
// frozen when the registry is built; only vibe's DirFunc re-resolves lazily.
// That is exactly why run-cell.sh has to export before spawn_record_daemon
// rather than at any convenient point — a daemon already running cannot be
// relocated. Building the registry first here reported all three as "does not
// relocate", which is true of the object and false of the daemon.
func registryAdapterAfterEnv(t *testing.T, slug string) agent.Agent {
	t.Helper()
	for _, cand := range agents.All() {
		if shard.SlugForAdapter(cand.Identity.Name) == slug {
			return cand
		}
	}
	t.Fatalf("no registry adapter with slug %q", slug)
	return agent.Agent{}
}

// assertSessionRootRelocates is half one: if the watched root does not move,
// the daemon records the operator's own sessions instead of the driver's.
func assertSessionRootRelocates(t *testing.T, row Row, home string, a agent.Agent) {
	t.Helper()
	src, isFiles := a.Source.(agent.FilesUnderRoot)
	if !isFiles {
		t.Fatalf("adapter %q declares Source %T, which this arm cannot resolve — a row for a "+
			"non-FilesUnderRoot adapter needs its own half-one check rather than being waved "+
			"through", row.Adapter, a.Source)
	}
	roots := src.AllRootsFor("darwin")
	if len(roots) == 0 {
		t.Fatalf("adapter %q resolves no session roots at all", row.Adapter)
	}
	for _, root := range roots {
		if !strings.HasPrefix(root, home) {
			t.Errorf("with %s=%s the session root is still %q — the daemon would watch the "+
				"operator's real store while the driver wrote to the scratch home, and the "+
				"recording would contain their sessions and not the driver's",
				row.EnvVar, home, root)
		}
	}
}

// assertHookWritesRelocate is half two: if a written file does not move, the
// install lands in the operator's real config while everything else in the run
// claims to be isolated — the worst of the outcomes, because it looks isolated.
func assertHookWritesRelocate(t *testing.T, row Row, home string, a agent.Agent) {
	t.Helper()
	var hooks *agent.Permission
	for i := range a.Permissions {
		if a.Permissions[i].Key == permissionKeyHooks {
			hooks = &a.Permissions[i]
		}
	}
	if hooks == nil {
		t.Fatalf("adapter %q has a rig home row but declares no hooks permission — this arm's "+
			"second half has nothing to grade, so the row's promise is only half checked; say "+
			"so in the table rather than leaving it implied", row.Adapter)
	}
	if hooks.Writes == nil || hooks.Writes.Path == nil {
		t.Fatalf("adapter %q's hooks permission declares no Writes.Path", row.Adapter)
	}

	resolvers := append([]func() (string, error){hooks.Writes.Path}, hooks.Writes.Also...)
	for i, resolve := range resolvers {
		label := "Writes.Path"
		if i > 0 {
			label = "Writes.Also[" + itoa(i-1) + "]"
		}
		got, err := resolve()
		if err != nil {
			t.Fatalf("resolving %s for %q: %v", label, row.Adapter, err)
		}
		if !strings.HasPrefix(got, home) {
			t.Errorf("with %s=%s the hooks install still writes %s to %q — the install would "+
				"land in the operator's real config while everything else in the run claimed "+
				"to be isolated", row.EnvVar, home, label, got)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestEveryRigHomeRowsDriverPassesTheHomeThroughTmux is the other half of the
// promise a row makes, and the half the rig cannot keep on its own.
//
// run-cell.sh EXPORTS the variable, which reaches the daemon because the daemon
// is a direct child. The agent CLI is not: the driver launches it with `tmux
// new-session`, and the pane's command is spawned by the tmux SERVER, whose
// environment was fixed when that server started. On a dev machine a server is
// essentially always already running — started by the operator's own terminal,
// long before any recording — so the export does NOT reach the pane. Measured
// on a private `-L` socket so no real session was touched: against a server
// started without the variable, a pane created by a shell that exported it
// reads `<UNSET>`, and the same server with an explicit `env VAR=…` prefix on
// the new-session command line reads the value.
//
// The consequence is the worst kind of failure the rig has: the daemon watches
// the scratch home, the CLI writes to the real one, the recording contains no
// session at all, and nothing anywhere says why. So every rowed adapter's
// interactive driver must pass the home explicitly. codex's driver already did;
// this arm is what stops that from being one driver's private knowledge.
func TestEveryRigHomeRowsDriverPassesTheHomeThroughTmux(t *testing.T) {
	root := repoRoot(t)
	rows, err := Table(root)
	if err != nil {
		t.Fatalf("reading the rig's home table: %v", err)
	}

	for _, row := range rows {
		t.Run(row.Adapter, func(t *testing.T) {
			path := filepath.Join(root, "replaydata", "agents", row.Adapter, "driver-interactive.sh")
			src, err := os.ReadFile(path) // #nosec G304 -- path built from the repo root and a table row
			if err != nil {
				t.Fatalf("reading %s's driver: %v — a table row for an adapter with no driver "+
					"cannot be checked, and a check that cannot run must say so", row.Adapter, err)
			}

			for _, l := range tmuxLaunchesMissingEnv(string(src), row.EnvVar) {
				t.Errorf("%s:%d launches the agent under tmux without an explicit `env %s=…` "+
					"prefix.\n%s\nThe pane inherits the tmux SERVER's environment, not the "+
					"exported one, so on any machine with a server already running the daemon "+
					"would watch the scratch home while the CLI wrote to the real one.",
					path, l.line, row.EnvVar, l.window)
			}
			// Vacuity guard: a driver with no tmux launch at all satisfies the
			// check above by having nothing to walk.
			if countTmuxLaunches(string(src)) == 0 {
				t.Errorf("%s contains no `tmux new-session` at all — this arm graded nothing", path)
			}
		})
	}
}

// launchSite is one `tmux new-session` that does not carry the home variable.
type launchSite struct {
	line   int
	window string
}

// tmuxLaunchWindow returns the launch statement starting at index i — the line
// plus up to three continuations, since these launches routinely wrap.
func tmuxLaunchWindow(lines []string, i int) string {
	end := i + 4
	if end > len(lines) {
		end = len(lines)
	}
	return strings.TrimRight(strings.Join(lines[i:end], "\n"), "\n")
}

// countTmuxLaunches is the vacuity guard's own measurement, kept separate from
// the verdict so "no launches found" cannot be confused with "all launches OK".
func countTmuxLaunches(src string) int {
	n := 0
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "tmux new-session") {
			n++
		}
	}
	return n
}

// launchWindowCarries reports whether one `tmux new-session` statement passes
// envVar into the pane, in any spelling the drivers actually use.
//
// Three axes, and each was learned from a driver that was correct and would
// have been failed by a narrower rule.
//
// QUOTING: codex writes `env "CODEX_HOME=$X"`; a future driver may not quote.
//
// MECHANISM: an `env VAR=… <cmd>` prefix on the command, which the *_HOME rows
// use, or tmux's own `-e VAR=…` flag, which gemini-cli's driver already uses
// for GEMINI_API_KEY. The two are equivalent here — `-e` sets the new session's
// environment and the pane command inherits it.
//
// POSITION: one `env` prefix carries as MANY assignments as it likes, and only
// the first of them follows the word `env`. This is the axis the first draft
// got wrong, and it is not hypothetical: `env "KIRO_HOME=…" "IRRLICHT_BIND_ADDR=…"`
// is what kiro-cli's driver writes, and a rule keyed on the literal
// `env IRRLICHT_BIND_ADDR=` reported it as missing. A finding against a driver
// that is correct is the one kind that gets a rule ignored rather than fixed.
//
// So: an `-e` flag naming the variable, or an `env` prefix somewhere in the
// statement plus an assignment to that variable after it. The assignment must
// start on a quote or whitespace boundary, so FOO_IRRLICHT_BIND_ADDR= is not
// mistaken for it — the false direction that matters is claiming the variable
// is carried when it is not.
func launchWindowCarries(window, envVar string) bool {
	for _, flag := range []string{"-e \"" + envVar + "=", "-e " + envVar + "="} {
		if strings.Contains(window, flag) {
			return true
		}
	}
	env := strings.Index(window, "env ")
	if env < 0 {
		return false
	}
	rest := window[env:]
	for _, assign := range []string{"\"" + envVar + "=", " " + envVar + "="} {
		if strings.Contains(rest, assign) {
			return true
		}
	}
	return false
}

// tmuxLaunchesMissingEnv names every launch site that does not pass envVar
// explicitly, in any spelling launchWindowCarries accepts.
func tmuxLaunchesMissingEnv(src, envVar string) []launchSite {
	var out []launchSite
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "tmux new-session") {
			continue
		}
		window := tmuxLaunchWindow(lines, i)
		if launchWindowCarries(window, envVar) {
			continue
		}
		out = append(out, launchSite{line: i + 1, window: window})
	}
	return out
}
