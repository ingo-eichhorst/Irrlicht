// install.go is the install-side half of the beacon: rendering the command an
// adapter writes into an agent's config, and deciding when an already-installed
// one has gone stale.
//
// Nothing installs it yet — #1373 builds the mechanism and each adapter adopts it
// separately — so this file is written to be wired into hookjson.Config without
// modification:
//
//	hookjson.Config{
//	    Sentinel:    hookbeacon.Sentinel(segment),
//	    Entry:       func() map[string]interface{} { ... "command": cmd ... },
//	    IsCanonical: func(h map[string]interface{}) bool {
//	        c, _ := h["command"].(string)
//	        return hookbeacon.IsCanonical(c, segment)
//	    },
//	}
package hookbeacon

import (
	"os"
	"path/filepath"
	"strings"
)

// LegacyGuardToken is appended to every installed beacon command line, and it is
// the reason this file exists rather than a one-line fmt.Sprintf.
//
// irrlichd does not parse its arguments. main() is a chain of exact-match
// hasFlag scans over os.Args that falls through to runDaemon() on anything it
// does not recognize, so an irrlichd predating this subcommand, handed
// `hook-post gemini-cli`, does not error — it STARTS A DAEMON. On every tool
// call. And that is not merely wasteful: the startup path calls os.Remove on the
// unix socket before listening, so a rogue start actively breaks the live
// daemon's socket before failing to bind the TCP port.
//
// That hazard cannot be fixed in the old binary, so it is fixed in the command
// line the new one installs. --version is the guard because it is the FIRST
// branch of that chain and has been present since the commit that first named
// the binary irrlichd (04fc6265, the irrlichtd rename) — so every irrlichd that
// has ever existed handles it by printing two lines and exiting 0. An old binary
// invoked as a beacon therefore no-ops instead of starting a daemon.
//
// It is inert on a current binary because irrlichd dispatches the subcommand on
// os.Args[1] BEFORE any flag check (see selectAction in core/cmd/irrlichd), and
// because adapterFrom reads past any "-" token. Both halves are locked by tests;
// reordering the dispatch so --version wins would silently turn every installed
// beacon into a version banner.
const LegacyGuardToken = "--version"

// stdoutRedirect keeps the guard token's own output off the hook's stdout, which
// gemini-cli and Claude Code both read as a decision channel. It only takes
// effect when the entry is run through a shell — which is how `type: "command"`
// hooks are run today (Codex's installed entry is `curl ... || true`, which is
// shell syntax). If some adapter ever execs the command directly the redirect
// becomes an inert extra argument that adapterFrom skips over, and the residual
// cost is two lines of stdout from an out-of-date binary. That is the intended
// failure ordering: the redirect is defence in depth for the guard, not the
// guard.
const stdoutRedirect = ">/dev/null"

// Command renders the shell command an adapter installs for one hook event.
//
// binaryPath must be absolute — the daemon runs under launchd with a minimal
// PATH (/usr/bin:/bin:/usr/sbin:/sbin) and the agent CLIs inherit no better, so
// a bare "irrlichd" is the silent no-op #1161 removed, wearing a new hat. Callers
// get it from BinaryPath.
//
// The rendered line carries no port and no host. That is the beacon's structural
// payoff over a curl entry: there is nothing address-shaped in the user's config
// that can go stale, so the entire #1178 class stops being a thing that has to be
// re-fixed per adapter rather than being fixed once more.
func Command(binaryPath, adapter string) string {
	return strings.Join([]string{
		shellQuote(binaryPath), Subcommand, adapter, LegacyGuardToken, stdoutRedirect,
	}, " ")
}

// Sentinel is the substring hookjson uses to recognize an installed entry as
// ours across every shape we have ever written for this adapter.
//
// It deliberately excludes the binary path and the guard token, the two parts of
// the command that are allowed to change: an entry naming a binary that has since
// moved must still be recognized as ours, because recognizing it is the
// precondition for rewriting it in place instead of appending a duplicate beside
// it. It is the same role HookEndpointPath plays for the curl and http shapes —
// identity is the part that does not vary.
//
// A caveat for the first adapter that migrates an EXISTING install to the beacon:
// hookjson's sentinel has to match every shape ever installed for that adapter,
// and this one does not appear in a curl line. Such an adapter must keep its
// endpoint-path sentinel, not adopt this one, or its old entries are orphaned and
// `irrlichd --uninstall-hooks` stops finding them. No adapter is in that position
// today — claudecode and codex are the only installers and neither is in scope
// for #1373.
func Sentinel(adapter string) string {
	return Subcommand + " " + adapter
}

// BinaryPath returns the absolute path of the running irrlichd, for embedding in
// an installed entry.
//
// No symlink resolution: the invoked path is what the user's installation
// intends, and resolving it would bake a version-specific real path into the
// config for any install laid out like Claude Code's versions/<ver> tree, so
// every upgrade would look like drift. (On Linux os.Executable reads
// /proc/self/exe and is symlink-resolved by the kernel regardless. That
// asymmetry is tolerable precisely because Inspect treats a changed path as
// ordinary drift and EnsureInstalled rewrites it in place on the next daemon
// start — a wrong-but-detected path self-heals, which is the property #1161
// lacked.)
func BinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}

// Drift names why an installed entry is not the one we would write now. It is
// the beacon's equivalent of the port comparison inside claudecode's
// hookEntryIsCanonical, generalized to the failure this mechanism newly admits:
// the binary path.
type Drift string

const (
	// DriftNone means the entry is current and its binary is present.
	DriftNone Drift = ""

	// DriftShape is an entry that is ours by sentinel but not in the current
	// command form — a flag added, the guard token dropped, an older
	// rendering.
	DriftShape Drift = "shape"

	// DriftBinaryPath is an entry naming a DIFFERENT irrlichd than the one
	// running. A second install, a move out of /Applications, a dev binary
	// that installed and then a release binary that took over.
	DriftBinaryPath Drift = "binary_path"

	// DriftBinaryMissing is an entry naming a path that is no longer there at
	// all. This is the trap #1373 names: the beacon trades curl's PATH
	// dependency for an absolute-path dependency, and an absolute path that
	// stops existing fails exactly as quietly as a curl that was never on
	// PATH — the agent runs it, the exec fails, no state is ever reported and
	// nothing anywhere says why.
	DriftBinaryMissing Drift = "binary_missing"

	// DriftUnresolvable means we could not determine our own path, so we
	// cannot judge the entry and must not rewrite it. Reported rather than
	// swallowed: silently leaving an install alone is how the previous entry
	// in this list stays invisible.
	DriftUnresolvable Drift = "unresolvable"
)

// Inspect is the ONE decision point for whether an installed beacon command is
// current, and it returns WHY rather than a boolean so the installer can log the
// reason. IsCanonical is the boolean hookjson.Config wants.
//
// Unlike the delivery reasons above there is no counter here, and the difference
// is not an oversight: delivery runs in a one-shot process where a stderr line
// can be discarded by the CLI, while reconciliation runs inside the daemon on
// every start, where a log line is read by the same operator reading everything
// else. Detection is the requirement; a counter would be a second surface for a
// fact already on the first.
func Inspect(command, adapter string) Drift {
	live, err := BinaryPath()
	if err != nil {
		return DriftUnresolvable
	}
	if command != Command(live, adapter) {
		return driftOfOtherEntry(command, adapter, live)
	}
	if !isExecutableFile(live) {
		return DriftBinaryMissing
	}
	return DriftNone
}

// driftOfOtherEntry classifies an entry that is not the line we would write now.
//
// The order is the point. A missing binary is checked before a differing one
// because it is the drift that is otherwise undetectable from the config alone —
// a path that merely differs still runs, while a path that is gone fails exactly
// as quietly as the un-PATHed curl #1161 removed. And an entry naming the binary
// we are actually running is shape drift, not path drift, or every rendering
// change would be misreported as a move.
func driftOfOtherEntry(command, adapter, live string) Drift {
	path, ok := binaryPathOf(command, adapter)
	if !ok {
		return DriftShape
	}
	if !isExecutableFile(path) {
		return DriftBinaryMissing
	}
	if path != live {
		return DriftBinaryPath
	}
	return DriftShape
}

// IsCanonical reports whether an installed command needs no rewrite. It is the
// hookjson.Config.IsCanonical for a beacon entry.
//
// A false here is what makes hookjson rewrite the entry IN PLACE — replacing the
// element rather than appending a group beside it — so a moved or deleted binary
// is repaired on the next daemon start rather than failing quietly forever.
func IsCanonical(command, adapter string) bool {
	return Inspect(command, adapter) == DriftNone
}

// binaryPathOf recovers the binary path from an installed beacon command, so a
// stale entry can be described ("was /old/irrlichd") and not merely replaced.
// Returns false when the command is not a beacon line for this adapter.
func binaryPathOf(command, adapter string) (string, bool) {
	idx := strings.Index(command, " "+Sentinel(adapter))
	if idx <= 0 {
		return "", false
	}
	return shellUnquote(strings.TrimSpace(command[:idx])), true
}

// isExecutableFile reports whether path is a regular file with an execute bit.
// A directory or a mode-0644 leftover is as unrunnable as an absent file, and
// all three have to read the same way here or the drift they cause is the
// invisible one.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// shellQuote wraps s in single quotes so a path containing a space, a $ or a
// semicolon is one argument and not several. The embedded-quote form
// ('\”) is the portable one — it closes the literal, escapes a bare quote and
// reopens — and it is what every POSIX shell accepts.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellUnquote is shellQuote's inverse, for reading a path back out of an
// installed entry.
func shellUnquote(s string) string {
	if len(s) < 2 || !strings.HasPrefix(s, "'") || !strings.HasSuffix(s, "'") {
		return s
	}
	return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
}
