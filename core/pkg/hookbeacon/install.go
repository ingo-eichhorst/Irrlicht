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
//
// One thing the adopting adapter owes: `segment` is typed in three independent
// places — the value it passes here, the route registerHookRoutes registers
// (core/cmd/irrlichd/startup.go), and the row in beaconroute_test.go that pins
// the two against each other. Export it as ONE constant from the adapter and
// reference that constant from all three, the way claudecode and codex already
// share a single HookEndpointPath between their route and their installer. A
// mismatch is a permanent silent 404: the beacon exits 0 by design, so nothing
// anywhere reports it.
package hookbeacon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LegacyGuardToken leads every installed beacon command line, and it is the
// reason this file exists rather than a one-line fmt.Sprintf.
//
// irrlichd did not parse its arguments until #1417. An irrlichd predating this
// subcommand, handed `hook-post gemini-cli`, does not error — it STARTS A
// DAEMON. On every tool call. (A current binary rejects an unknown flag and an
// unknown positional, but that cannot help here: the binary in the field is
// whichever one the user already has, which is the whole reason for the guard.)
// And that is not merely wasteful: the startup path
// calls os.Remove on the unix socket before listening (setupUnixSocket), so a
// rogue start actively breaks the live daemon's socket before failing to bind
// the TCP port.
//
// That hazard cannot be fixed in the old binary, so it is fixed in the command
// line the new one installs: --version is a token every irrlichd exits 0 on
// without side effects, and it is placed FIRST because irrlichd has had two
// different dispatch shapes and only the leading position satisfies both.
// Verified with `git show <tag>:core/cmd/irrlichd/main.go`:
//
//   - v0.2.0 through v0.3.2 dispatch POSITIONALLY:
//     `if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v")`.
//     These match only in argv[1]. A trailing guard token would miss them
//     entirely and they would start a daemon — so "every irrlichd handles
//     --version anywhere in argv" is false, and an earlier draft of this file
//     said exactly that.
//   - Later versions scan the whole command line (hasFlag), and match a guard
//     in any position.
//
// Leading satisfies both, so the guard covers every irrlichd ever released.
//
// It is inert on a current binary because IsInvocation accepts the guarded form
// explicitly and irrlichd dispatches the subcommand BEFORE any flag check (see
// selectAction in core/cmd/irrlichd), and because adapterFrom reads past any
// "-" token. Both halves are locked by tests; reordering the dispatch so
// --version wins would silently turn every installed beacon into a version
// banner.
const LegacyGuardToken = "--version"

// failOpenSuffix keeps the SHELL's exit code at 0 when the command itself never
// runs, which is the one failure the beacon newly introduces and the one its
// own exit-0 contract cannot reach: an absolute path that has stopped existing
// exits 127, and a path that lost its execute bit exits 126, both from the
// shell, before any Go code runs. Measured:
//
//	$ sh -c "'/gone/irrlichd' hook-post x --version >/dev/null"; echo $?   -> 127
//	$ sh -c "'/gone/irrlichd' hook-post x --version >/dev/null || true"    -> 0
//
// A fail-closed pre-tool hook turns that 127 into a denied tool call, which is
// precisely what this package exists to prevent — so the guarantee has to hold
// even when irrlicht has been uninstalled and no daemon is left to reconcile the
// stale entry. Codex's installed curl line carries the same `|| true` for the
// same reason (codex/hookinstaller.go).
const failOpenSuffix = "|| true"

const stdoutRedirect = ">/dev/null"

// Command renders the shell command an adapter installs for one hook event.
//
// The token order is load-bearing and is documented on LegacyGuardToken and
// failOpenSuffix: guard first so every irrlichd release exits 0 rather than
// starting a daemon, `|| true` last so the shell exits 0 even when the binary
// is gone.
//
// binaryPath must be absolute — the daemon runs under launchd with a minimal
// PATH (/usr/bin:/bin:/usr/sbin:/sbin) and the agent CLIs inherit no better, so
// a bare "irrlichd" is the silent no-op #1161 removed, wearing a new hat.
// Callers get it from BinaryPath.
//
// The rendered line carries no port and no host. That is the beacon's structural
// payoff over a curl entry: there is nothing address-shaped in the user's config
// that can go stale, so the entire #1178 class stops being a thing that has to be
// re-fixed per adapter rather than being fixed once more.
//
// It returns an error rather than panicking on a bad argument because this text
// is written into a user's config file and executed by a shell: the read side
// (adapterFrom) already refuses an adapter that is not a safe path segment, and
// a write side that did not would be the asymmetry that stops holding the day a
// segment becomes config-derived rather than a compile-time literal.
func Command(binaryPath, adapter string) (string, error) {
	if !filepath.IsAbs(binaryPath) {
		return "", fmt.Errorf("hookbeacon: binary path %q is not absolute; a relative path is the un-PATHed no-op #1161 removed", binaryPath)
	}
	if !adapterSegment.MatchString(adapter) {
		return "", fmt.Errorf("hookbeacon: %q is not a valid adapter segment", adapter)
	}
	return strings.Join([]string{
		shellQuote(binaryPath), LegacyGuardToken, Subcommand, adapter,
		stdoutRedirect, failOpenSuffix,
	}, " "), nil
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
//
// The trailing space is not cosmetic. hookjson matches a sentinel with
// strings.Contains, so an unterminated "hook-post gemini" would also match an
// installed "hook-post gemini-cli" entry: one adapter's EnsureInstalled would
// claim the other's entry and rewrite it, and its Uninstall would delete it. No
// current segment is a prefix of another, so this is latent rather than live —
// which is exactly the kind of thing that stops being latent when a segment is
// added.
func Sentinel(adapter string) string {
	return Subcommand + " " + adapter + " "
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
	return inspectAgainst(command, adapter, live)
}

// inspectAgainst is Inspect with the running binary's path injected, so the
// cases that depend on OUR binary being absent are testable without arranging
// for os.Executable to fail.
func inspectAgainst(command, adapter, live string) Drift {
	want, err := Command(live, adapter)
	if err != nil {
		return DriftUnresolvable
	}
	return driftAgainst(command, adapter, live, want)
}

// driftAgainst takes the line we would write as a parameter so a caller that
// has already rendered it does not render it again.
func driftAgainst(command, adapter, live, want string) Drift {
	if command == want {
		if !isExecutableFile(live) {
			return DriftBinaryMissing
		}
		return DriftNone
	}
	return driftOfOtherEntry(command, adapter, live)
}

// driftOfOtherEntry classifies an entry that is not the line we would write now.
//
// The order is the point. A missing binary is checked before a differing one
// because it is the drift that is otherwise undetectable from the config alone —
// a path that merely differs still runs, while a path that is gone makes the
// SHELL exit 127 before any of our code runs (which is what failOpenSuffix is
// for). And an entry naming the binary we are actually running is shape drift,
// not path drift, or every rendering change would be misreported as a move.
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

// IsCanonical reports whether an installed command should be left alone. It is
// the hookjson.Config.IsCanonical for a beacon entry.
//
// It is deliberately NOT `Inspect(...) == DriftNone`. False is what makes
// hookjson replace the entry and mark the settings file modified, and hookjson
// has no "the bytes did not change, skip the write" branch — so reporting a
// drift that a rewrite cannot repair means rewriting the user's config on every
// daemon start, forever, without ever converging. Two such drifts exist:
//
//   - DriftUnresolvable: we could not work out what we would write, so we have
//     no replacement to offer. Its own doc says "must not rewrite it", and
//     returning false here would do exactly that.
//   - A dead binary that the rewrite would name again — either the entry is
//     already byte-identical to what we would write, or our own binary is gone
//     so every candidate line is equally dead.
//
// Both are still reported by Inspect, which is where the installer logs them.
// Detection and repair are separate questions, and only repair belongs here.
func IsCanonical(command, adapter string) bool {
	live, err := BinaryPath()
	if err != nil {
		return true
	}
	return canonicalAgainst(command, adapter, live)
}

func canonicalAgainst(command, adapter, live string) bool {
	want, err := Command(live, adapter)
	if err != nil {
		return true
	}
	switch driftAgainst(command, adapter, live, want) {
	case DriftNone, DriftUnresolvable:
		return true
	}
	// A rewrite repairs the entry only if it would actually change the line AND
	// the binary it would name can be run.
	return command == want || !isExecutableFile(live)
}

// binaryPathOf recovers the binary path from an installed beacon command, so a
// stale entry can be described ("was /old/irrlichd") and not merely replaced.
// Returns false when the command is not a beacon line for this adapter.
func binaryPathOf(command, adapter string) (string, bool) {
	if !strings.Contains(command, Sentinel(adapter)) {
		return "", false
	}
	return leadingArg(command)
}

// leadingArg extracts the first shell word of a command line, undoing
// shellQuote. It is the inverse of the rendering in Command, and it has to
// understand the '\” escape or a path containing a quote would be truncated at
// that quote and then read as a missing binary.
func leadingArg(command string) (string, bool) {
	s := strings.TrimLeft(command, " ")
	if strings.HasPrefix(s, "'") {
		return unquoteLeading(s)
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// unquoteLeading reads one single-quoted shell word from the front of s,
// undoing shellQuote's '\” escape. It reports false on an unterminated quote,
// which is not a line we rendered.
func unquoteLeading(s string) (string, bool) {
	var out strings.Builder
	for i := 1; i < len(s); {
		if s[i] != '\'' {
			out.WriteByte(s[i])
			i++
			continue
		}
		// A quote is either the '\'' escape sequence or the terminator.
		if strings.HasPrefix(s[i:], `'\''`) {
			out.WriteByte('\'')
			i += 4
			continue
		}
		return out.String(), true
	}
	return "", false
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
