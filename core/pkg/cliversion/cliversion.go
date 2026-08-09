// Package cliversion parses, compares and probes upstream agent-CLI version
// strings — the shared half of issue #1365's declared minimum version per
// hook-capable adapter.
//
// It exists because the alternative is the thing #1365 is about: Codex grew a
// bespoke parseCodexVersion/codexSupportsHooks pair inside its own adapter,
// Claude Code grew nothing at all, and a third adapter would have copied one or
// the other. The comparison is the same arithmetic for every CLI; only the
// version *string* differs, so only the version string is declared.
//
// Every agent CLI decorates its version differently and none of them are
// promising not to change it — "2.1.226 (Claude Code)", "codex-cli 0.146.1",
// "rust-v0.114.0", "1.29.8". Parse therefore looks for the first bare
// major.minor.patch triple anywhere in the string rather than trying to model
// each CLI's banner.
package cliversion

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProbeTimeout bounds how long a version probe may take. A version check runs
// on the consent path — the user has just clicked "grant" and is watching —
// so a CLI that hangs must lose quickly rather than wedge the grant.
const ProbeTimeout = 2 * time.Second

// probeWaitDelay bounds the extra time Probe waits for the child's output
// pipes to close after its context expires. Killing the process is not enough
// on its own: exec waits for stdout to reach EOF, and a grandchild that
// inherited the pipe holds it open after its parent dies, so a plain
// CommandContext kill can still block indefinitely. This is the difference
// between "fails fast" as an intention and as a guarantee.
const probeWaitDelay = 500 * time.Millisecond

// versionTriple matches a bare major.minor.patch run of digits anywhere in a
// version banner. Requiring all three fields is deliberate: every agent CLI in
// play ships three (2.1.226, 0.146.1, 1.0.26, 1.29.8), and treating a
// two-field string as a version would invent a patch level nobody stated.
// Unparseable input is not an error condition here — it resolves to "unknown",
// which fails open (see AtLeast).
var versionTriple = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// Version is a parsed semantic-ish version. Pre-release and build suffixes are
// discarded: no agent CLI gates a hook event on an -rc tag, and comparing them
// would mean picking a precedence rule none of these CLIs document.
type Version struct {
	Major, Minor, Patch int
}

// Parse extracts the first major.minor.patch triple from s. ok is false when s
// contains no such triple, which callers must treat as "unknown version", not
// as "version zero".
func Parse(s string) (Version, bool) {
	m := versionTriple.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	// Each group is \d+ and so parses, but a version long enough to overflow
	// an int is not a version — fall back to unknown rather than to a
	// wrapped-around number that would compare wrong.
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	patch, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return Version{}, false
	}
	return Version{Major: major, Minor: minor, Patch: patch}, true
}

// String renders the version in bare major.minor.patch form.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare returns -1, 0 or +1 as v sorts before, equal to, or after o.
func (v Version) Compare(o Version) int {
	switch {
	case v.Major != o.Major:
		return sign(v.Major - o.Major)
	case v.Minor != o.Minor:
		return sign(v.Minor - o.Minor)
	default:
		return sign(v.Patch - o.Patch)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// AtLeast reports whether installed is at or above minimum.
//
// known is false when either string is missing or unparseable, and in that
// case ok is reported as true — unknown fails OPEN. That direction is chosen,
// not inherited: the harm this gate prevents is writing config into a CLI too
// old to understand it, and "I could not read a version" is not evidence that
// the CLI is old. Failing closed on unknown would silently disable hooks for
// every user whose binary is installed somewhere the daemon's PATH does not
// reach — the daemon runs under launchd with a minimal PATH, while `claude`
// commonly lives in ~/.local/bin — which is the same "granted, but the channel
// never fires" outcome the gate exists to abolish, just arrived at from the
// other side. Only a version we successfully read AND successfully parsed AND
// found to be below the floor is allowed to block an install.
func AtLeast(installed, minimum string) (ok, known bool) {
	got, gotOK := Parse(installed)
	floor, floorOK := Parse(minimum)
	if !gotOK || !floorOK {
		return true, false
	}
	return got.Compare(floor) >= 0, true
}

// Probe runs argv and returns the first version triple it prints on stdout.
//
// All three failure modes an external binary can present — missing, hung,
// unintelligible — collapse to the same result: an error, which callers turn
// into "unknown", which AtLeast fails open on.
//
//   - Missing binary: exec's PATH lookup fails before anything is spawned, so
//     this returns immediately rather than after the timeout.
//   - Hung binary: bounded by ProbeTimeout, and by probeWaitDelay against a
//     grandchild holding the output pipe open past the kill.
//   - Unintelligible output: reported as an error here and as unknown by Parse.
//
// argv comes from an adapter's compile-time declaration, never from a session,
// a config file, or anything else an attacker could reach; it is passed to
// exec as separate arguments with no shell in between.
func Probe(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("cliversion: no probe command declared")
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = probeWaitDelay
	// A version probe must never block on a prompt, and must never inherit the
	// daemon's stdin.
	cmd.Stdin = nil

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cliversion: probing %q: %w", strings.Join(argv, " "), err)
	}
	version, ok := Parse(string(out))
	if !ok {
		return "", fmt.Errorf("cliversion: %q printed no recognizable version", strings.Join(argv, " "))
	}
	return version.String(), nil
}
