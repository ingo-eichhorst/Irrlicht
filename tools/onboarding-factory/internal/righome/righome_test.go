package righome

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/domain/agent"
	"irrlicht/tools/onboarding-factory/internal/driverteardown"
	"irrlicht/tools/onboarding-factory/internal/hookcov"
	"irrlicht/tools/onboarding-factory/internal/shard"
)

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
// A row is a promise the rig acts on: it exports those variables before
// spawning the daemon, and the driver passes the same values to the agent CLI.
// Two things have to move for that to mean anything, and an adapter can move
// one without the other — claudecode is exactly that shape, which is why it is
// exempt rather than rowed:
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
// It grades the DECLARED variables against the REAL resolvers, through
// GradeRelocation: every daemon-read variable set together must move both
// halves; each one set alone must move something (a row naming a variable no
// resolver reads fails here even though it is perfectly well formed and
// passes the static arm above); and a driver-only variable — opencode-v2's
// XDG_STATE_HOME — must move nothing, rather than being demanded of the
// daemon (#2109).
func TestEveryRigHomeRowRelocatesBothHalves(t *testing.T) {
	rows, err := Table(repoRoot(t))
	if err != nil {
		t.Fatalf("reading the rig's home table: %v", err)
	}

	for _, row := range rows {
		t.Run(row.Adapter, func(t *testing.T) {
			for _, p := range GradeRelocation(row, liveRelocationProbe(t, row)) {
				t.Error(p)
			}
		})
	}
}

// outrankingOverrides names, per adapter, the variables its resolvers read
// BEFORE a row's variables — set, they decide the path whatever the row says.
// opencode.StorePath honours OPENCODE_DB first and returns an absolute value as
// is (core/adapters/inbound/agents/opencode/adapter.go), so a runner that
// exports it would see the opencode-v2 row "fail" to move the store.
var outrankingOverrides = map[string][]string{
	opencode.AdapterName: {"OPENCODE_DB"},
}

// liveRelocationProbe is GradeRelocation's probe over the real registry: it
// points each named variable at its own directory under one scratch home,
// UNSETS the row's other variables and the adapter's outrankingOverrides (so
// a variable set by the test runner's own environment cannot pass for, or
// override, one the row moved), builds the registry, and resolves the
// adapter's session roots and hook writes. The original values come back when
// the test ends.
func liveRelocationProbe(t *testing.T, row Row) RelocationProbe {
	t.Helper()
	home := filepath.Join(t.TempDir(), "scratch-home")
	overrides := outrankingOverrides[shard.DaemonAdapterForSlug(row.Adapter)]
	touched := append(row.AllVars(), overrides...)
	for _, v := range touched {
		if old, ok := os.LookupEnv(v); ok {
			t.Cleanup(func() { os.Setenv(v, old) })
		} else {
			t.Cleanup(func() { os.Unsetenv(v) })
		}
	}
	return func(set []string) (Resolved, error) {
		for _, v := range touched {
			os.Unsetenv(v)
		}
		for _, v := range set {
			os.Setenv(v, filepath.Join(home, v))
		}
		a := registryAdapterAfterEnv(t, row.Adapter)
		sessions, err := sessionRootsFor(a)
		if err != nil {
			return Resolved{}, fmt.Errorf("adapter %q: %w", a.Identity.Name, err)
		}
		hooks, err := hookWritesOf(a)
		if err != nil {
			return Resolved{}, fmt.Errorf("adapter %q: %w", a.Identity.Name, err)
		}
		return Resolved{Home: home, Sessions: sessions, Hooks: hooks}, nil
	}
}

// registryAdapterAfterEnv builds the registry and finds the adapter that
// records one column, and is a separate step because it must run AFTER the
// caller has set the env vars.
//
// That ordering is the property rather than test hygiene. Three of the four
// original rows (copilot, codex, kiro-cli) declare a static Source.Dir computed
// by their package's sessionsDir() at Agent() CONSTRUCTION time, so the watched
// root is frozen when the registry is built; only vibe's DirFunc re-resolves
// lazily. That is exactly why run-cell.sh has to export before
// spawn_record_daemon rather than at any convenient point — a daemon already
// running cannot be relocated. Building the registry first here reported all
// three as "does not relocate", which is true of the object and false of the
// daemon.
//
// The column is routed through shard.DaemonAdapterForSlug: the opencode-v2
// column's adapter is opencode (#2109).
func registryAdapterAfterEnv(t *testing.T, column string) agent.Agent {
	t.Helper()
	want := shard.DaemonAdapterForSlug(column)
	for _, cand := range agents.All() {
		if cand.Identity.Name == want {
			return cand
		}
	}
	t.Fatalf("no registry adapter %q for column %q", want, column)
	return agent.Agent{}
}

// storePathOverrides names the ProcessOwnedStore adapters whose
// Source.PathForPID is a deliberate panic sentinel rather than a resolver, and
// the function that resolves their store instead. opencode is the one:
// agent.go's PathForPID panics "resolver not installed", because the daemon
// constructs opencode's watcher inline (cmd/irrlichd/main.go) and a silent ""
// would have produced no rows; StorePath is what that watcher opens. It is
// named here, by adapter, rather than as a branch in sessionRootsOf's switch,
// so the switch keeps answering only "what does this Source variant declare".
var storePathOverrides = map[string]func() string{
	opencode.AdapterName: opencode.StorePath,
}

// sessionRootsFor is sessionRootsOf, except for the adapters
// storePathOverrides names.
func sessionRootsFor(a agent.Agent) ([]string, error) {
	if resolve, ok := storePathOverrides[a.Identity.Name]; ok {
		return []string{resolve()}, nil
	}
	return sessionRootsOf(a)
}

// sessionRootsOf resolves the paths the daemon watches for one adapter, for
// each Source variant a row can name.
//
// The ProcessOwnedStore branch is #1722's: hermes is the first rowed adapter
// with no transcript files at all, and for it "the session root" is the single
// SQLite store the watcher polls. PathForPID is the resolver the daemon itself
// calls, so grading it asks the same question the FilesUnderRoot branch asks —
// a store that has not moved means the daemon polls the operator's real
// sessions while the driver writes to the scratch home. The pid argument is
// ignored by every ProcessOwnedStore declaration (the store is shared, not
// per-process), which is why passing a placeholder is honest rather than a
// stand-in for something the test could not arrange.
//
// The FilesUnderCWD branch is #1767's: aider is the only adapter using this
// variant today, has no rig-home row (its transcript lives under the
// project's own working directory, not a directory a HOME-style env var can
// relocate), and so this arm has never actually run against it. It is named
// here anyway, with the reason recorded, rather than left to fall through to
// default the way it did before #1767 — the same "one branch per variant,
// with a recorded reason where there is genuinely no sensible answer" shape
// applyWritesNoUserFile and notAManagedFilePath use in core/cmd/irrlichd. A
// future FilesUnderCWD adapter that DOES earn a row gets a clear, named
// failure here instead of the generic one below.
//
// The default case stays a hard failure rather than an empty result: an
// unresolvable Source and a Source with nothing to relocate must not read the
// same way. It should now be unreachable —
// TestSessionRootsOfCoversEveryAgentSourceVariant asserts every agent.Source
// variant declared in core/domain/agent/source.go has a named case above,
// derived from source rather than hand-kept, so a fourth variant fails that
// dedicated, existence-checked test the moment it is introduced rather than
// waiting for some future adapter's relocation test to trip over it.
func sessionRootsOf(a agent.Agent) ([]string, error) {
	switch src := a.Source.(type) {
	case agent.FilesUnderRoot:
		return src.AllRootsFor("darwin"), nil
	case agent.ProcessOwnedStore:
		if src.PathForPID == nil {
			return nil, fmt.Errorf("declares ProcessOwnedStore with no PathForPID, so there is no store path to grade")
		}
		return []string{src.PathForPID(0)}, nil
	case agent.FilesUnderCWD:
		return nil, fmt.Errorf("declares FilesUnderCWD — session data lives under the process's " +
			"own working directory, not a directory a HOME-style env var can relocate, so there is " +
			"no session root here to grade; an adapter using this variant needs its own isolation " +
			"story (see righome.Unisolatable) rather than a rig-home row")
	default:
		return nil, fmt.Errorf("declares Source %T, which this arm cannot resolve — "+
			"TestSessionRootsOfCoversEveryAgentSourceVariant should have failed before this ever "+
			"ran; if it did not, that test's own scan has a bug", a.Source)
	}
}

// TestSessionRootsOfCoversEveryAgentSourceVariant is the existence-checked
// backstop #1767 asks for: it fires the moment core/domain/agent/source.go
// gains a new Source variant, whether or not any adapter using it has earned
// a rig-home row yet — rather than waiting for sessionRootsOf's default
// branch to fatal inside some later adapter's relocation test the way it did
// for hermes (#1722/#1765).
//
// declared and handled are each derived from an AST scan (righome.go), never
// hand-kept, so the two projections cannot silently drift apart the way a
// hand-maintained list could.
func TestSessionRootsOfCoversEveryAgentSourceVariant(t *testing.T) {
	root := repoRoot(t)
	declared, err := sourceVariantsIn(root)
	if err != nil {
		t.Fatalf("deriving agent.Source's declared variants: %v", err)
	}
	handled, err := switchCasesIn(root,
		"tools/onboarding-factory/internal/righome/righome_test.go", "sessionRootsOf")
	if err != nil {
		t.Fatalf("deriving sessionRootsOf's handled variants: %v", err)
	}
	for _, p := range SourceVariantCoverage(declared, handled) {
		t.Error(p)
	}
}

// hookWritesOf resolves every file an adapter's hooks install writes:
// Writes.Path plus every Writes.Also. An adapter with a rig-home row that
// declares no hooks permission is refused rather than graded as "moved
// nothing": the row's promise would only be half checked.
func hookWritesOf(a agent.Agent) ([]string, error) {
	hooks := hookcov.HooksPermission(a)
	if hooks == nil {
		return nil, fmt.Errorf("has a rig home row but declares no hooks permission — the row's second " +
			"half has nothing to grade, so its promise is only half checked; say so in the table " +
			"rather than leaving it implied")
	}
	if hooks.Writes == nil || hooks.Writes.Path == nil {
		return nil, fmt.Errorf("its hooks permission declares no Writes.Path")
	}
	resolvers := append([]func() (string, error){hooks.Writes.Path}, hooks.Writes.Also...)
	out := make([]string, 0, len(resolvers))
	for i, resolve := range resolvers {
		got, err := resolve()
		if err != nil {
			label := "Writes.Path"
			if i > 0 {
				label = "Writes.Also[" + itoa(i-1) + "]"
			}
			return nil, fmt.Errorf("resolving %s: %w", label, err)
		}
		out = append(out, got)
	}
	return out, nil
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

			if countTmuxLaunches(string(src)) == 0 {
				assertServiceDriverChecksTheRow(t, path, string(src), row)
				return
			}
			for _, v := range row.DaemonVars() {
				for _, l := range tmuxLaunchesMissingEnv(string(src), v) {
					t.Errorf("%s:%d launches the agent under tmux without an explicit `env %s=…` "+
						"prefix.\n%s\nThe pane inherits the tmux SERVER's environment, not the "+
						"exported one, so on any machine with a server already running the daemon "+
						"would watch the scratch home while the CLI wrote to the real one.",
						path, l.line, v, l.window)
				}
			}
		})
	}
}

// serviceEnvVarsDecl is the declaration a driver that launches NO agent
// process carries instead of a tmux launch: the variables it checks the
// already-running agent service's own environment for, before it drives
// anything. opencode-v2's driver is the one (#2109): the 2.x service hosts
// every session over HTTP, so there is no pane to hand a home to — the
// service's environment was fixed when it started, and the only honest
// promise is to refuse when it differs from the row's.
const serviceEnvVarsDecl = "SERVICE_ENV_VARS="

// assertServiceDriverChecksTheRow is the no-launch arm of the test above: the
// driver must declare serviceEnvVarsDecl naming every variable of the row,
// driver-only ones included (the driver reads the service's service.json
// from its XDG_STATE_HOME, so a service under another state dir is another
// service). A driver with neither a launch nor the declaration graded
// nothing, and says so.
func assertServiceDriverChecksTheRow(t *testing.T, path, src string, row Row) {
	t.Helper()
	declared, ok := scrapeServiceEnvVars(src)
	if !ok {
		t.Errorf("%s contains no `tmux new-session` and no %q declaration — this arm graded "+
			"nothing", path, serviceEnvVarsDecl)
		return
	}
	for _, v := range row.AllVars() {
		if !declared[v] {
			t.Errorf("%s launches no agent process, and its %s does not name %s — a service "+
				"started under a different %s would be driven while the daemon watched the row's",
				path, serviceEnvVarsDecl, v, v)
		}
	}
}

// scrapeServiceEnvVars reads a top-level SERVICE_ENV_VARS="A B C" line the way
// recipe-lint reads DRIVE_ELICITS: quotes stripped, whitespace-split.
func scrapeServiceEnvVars(src string) (map[string]bool, bool) {
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(line, serviceEnvVarsDecl) {
			continue
		}
		raw := strings.TrimPrefix(line, serviceEnvVarsDecl)
		if i := strings.Index(raw, "#"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.NewReplacer(`"`, "", `'`, "").Replace(raw)
		out := map[string]bool{}
		for _, f := range strings.Fields(raw) {
			out[f] = true
		}
		return out, len(out) > 0
	}
	return nil, false
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
// It is driverteardown.LaunchCount — comments stripped — so the static
// teardown grader and this test agree on which drivers launch nothing (#2109:
// that answer now routes a driver to the no-launch arms).
func countTmuxLaunches(src string) int { return driverteardown.LaunchCount(src) }

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
