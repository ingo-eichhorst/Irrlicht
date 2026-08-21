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

			// agents.All() is called AFTER the variable is set, and the order is
			// the property rather than test hygiene. Three of the four rows
			// (copilot, codex, kiro-cli) declare a static Source.Dir computed by
			// their package's sessionsDir() at Agent() CONSTRUCTION time, so the
			// watched root is frozen when the registry is built; only vibe's
			// DirFunc re-resolves lazily. That is exactly why run-cell.sh has to
			// export before spawn_record_daemon rather than at any convenient
			// point — a daemon already running cannot be relocated. Building the
			// registry first here reported all three as "does not relocate",
			// which is true of the object and false of the daemon.
			var a agent.Agent
			found := false
			for _, cand := range agents.All() {
				if shard.SlugForAdapter(cand.Identity.Name) == row.Adapter {
					a, found = cand, true
				}
			}
			if !found {
				t.Fatalf("no registry adapter with slug %q", row.Adapter)
			}

			// Half one: the watched session root.
			src, isFiles := a.Source.(agent.FilesUnderRoot)
			if !isFiles {
				t.Fatalf("adapter %q declares Source %T, which this arm cannot resolve — a row "+
					"for a non-FilesUnderRoot adapter needs its own half-one check rather than "+
					"being waved through", row.Adapter, a.Source)
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

			// Half two: every file the hooks install writes.
			var hooks *agent.Permission
			for i := range a.Permissions {
				if a.Permissions[i].Key == permissionKeyHooks {
					hooks = &a.Permissions[i]
				}
			}
			if hooks == nil {
				t.Fatalf("adapter %q has a rig home row but declares no hooks permission — this "+
					"arm's second half has nothing to grade, so the row's promise is only half "+
					"checked; say so in the table rather than leaving it implied", row.Adapter)
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
					t.Errorf("with %s=%s the hooks install still writes %s to %q — the install "+
						"would land in the operator's real config while everything else in the run "+
						"claimed to be isolated", row.EnvVar, home, label, got)
				}
			}
		})
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

			lines := strings.Split(string(src), "\n")
			launches := 0
			for i, line := range lines {
				if !strings.Contains(line, "tmux new-session") {
					continue
				}
				launches++
				// The launch commonly spans continuation lines; take a small
				// window rather than the single line.
				end := i + 4
				if end > len(lines) {
					end = len(lines)
				}
				window := strings.Join(lines[i:end], "\n")
				if !strings.Contains(window, "env \""+row.EnvVar+"=") &&
					!strings.Contains(window, "env "+row.EnvVar+"=") {
					t.Errorf("%s:%d launches the agent under tmux without an explicit "+
						"`env %s=…` prefix.\n%s\nThe pane inherits the tmux SERVER's environment, "+
						"not the exported one, so on any machine with a server already running the "+
						"daemon would watch the scratch home while the CLI wrote to the real one.",
						path, i+1, row.EnvVar, strings.TrimRight(window, "\n"))
				}
			}
			// Vacuity guard: a driver with no tmux launch at all satisfies the
			// loop above by never entering it.
			if launches == 0 {
				t.Errorf("%s contains no `tmux new-session` at all — this arm graded nothing", path)
			}
		})
	}
}
