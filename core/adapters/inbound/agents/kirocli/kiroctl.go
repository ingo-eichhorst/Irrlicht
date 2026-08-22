// kiroctl.go shells out to the live kiro-cli binary for the two operations
// the #1716 audit established have no file-surgery equivalent: materializing
// a full agent config from the built-in default, and switching
// chat.defaultAgent. See hookinstaller.go's package comment for why both are
// necessary — kiro-cli only fires hooks for an explicitly selected agent, and
// this machine's user has none.
//
// Two things below are load-bearing and were both measured live against an
// isolated KIRO_HOME with kiro-cli 2.6.0, never against the real ~/.kiro.
//
//   - `kiro-cli agent create <name> --from <src>` opens $EDITOR (vim by
//     default) to let the user review the materialized config before saving —
//     with no controlling terminal this fails ("Editor process did not exit
//     with success", exit 1) even though the file was already written to
//     disk before the editor ran. EDITOR=true in the child's environment
//     turns that into a clean exit 0: `true` returns immediately, the file is
//     unchanged, and nothing opens a window. Without this override, running
//     the daemon's own Apply closure under launchd — no TTY, no display —
//     would report the install failed on every grant despite the file being
//     correct on disk.
//   - `kiro-cli agent create` refuses outright (exit 1, "already exists")
//     when the target file is already there, so existence is checked with
//     os.Stat before invoking it rather than by parsing that message.
package kirocli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"irrlicht/core/pkg/cliversion"
	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/shellout"
)

// kiroCLIBinary is the executable name resolved on every shellout below, and
// the argv[0] the declared VersionGate.Probe uses (agent.go) — one constant,
// so the binary this installer runs and the binary the version floor checks
// cannot name two different things.
//
// It is deliberately NOT "kiro" — /usr/local/bin/kiro on this machine is the
// Kiro IDE (a VS Code fork, `kiro --version` prints "0.12.224"), a different
// product from the CLI chat agent this adapter monitors
// (~/.local/bin/kiro-cli, `kiro-cli --version` prints "kiro-cli 2.6.0").
// #1716's own audit records finding this confused in the issue body it was
// filed from.
const kiroCLIBinary = "kiro-cli"

// kiroCLITimeout bounds one live kiro-cli invocation. This runs on the
// consent-grant path — synchronous with a user watching the grant complete —
// so it has to lose quickly rather than wedge the wizard. Longer than
// cliprobe's 2s read-only version probe: this is a write (materializing a
// full agent config on disk, or splicing a settings file) rather than a
// stdout read, and deserves a little more headroom for a cold binary launch.
const kiroCLITimeout = 5 * time.Second

// maxKiroCLIOutput caps how much of a kiro-cli invocation's stdout/stderr is
// retained for an error message. A few hundred bytes covers every message
// seen live; this only bounds a misbehaving child from making the daemon
// hold an unbounded buffer.
const maxKiroCLIOutput = 64 << 10

// kiroCLIPath resolves the kiro-cli binary to invoke. A package-level var,
// not a bare call to pathutil.MustResolve inline in runKiroCLI, so tests can
// substitute a stand-in binary.
//
// That substitution needs a seam of its own, and PATH-prepending — the trick
// core/pkg/cliprobe's own tests use for a decoy binary — does not reach this
// call: pathutil.Resolve checks a fixed, unwriteable set of trusted
// directories (/usr/bin, /bin, /usr/local/bin, /opt/homebrew/bin, ...)
// BEFORE ever consulting $PATH, and returns the absolute match immediately
// when one exists. Measured on the machine this was developed on: kiro-cli is
// ALSO reachable at /opt/homebrew/bin/kiro-cli (a Homebrew install,
// independent of the ~/.local/bin/kiro-cli symlink adapter.go's doc
// describes), so pathutil.MustResolve("kiro-cli") returns that path directly
// and a fake binary placed earlier on $PATH is never reached — verified by
// instrumenting this exact call. That is pathutil's security property
// working as designed (SonarQube go:S4036: a local attacker's writable PATH
// entry must not be able to shadow a trusted binary), not a defect, so the
// fix is a seam here rather than a workaround in pathutil.
var kiroCLIPath = func() string { return pathutil.MustResolve(kiroCLIBinary) }

// runKiroCLI runs `kiro-cli <args...>` and reports whether it exited zero,
// wrapping stderr into the error when it did not.
func runKiroCLI(ctx context.Context, args ...string) error {
	_, err := runKiroCLIOutput(ctx, kiroCLITimeout, args...)
	return err
}

// runKiroCLIOutput is runKiroCLI plus the child's stdout, under a caller-chosen
// bound. Split out for kiroCLIVersion, which needs the output and wants the
// tighter budget a read-only probe deserves; every write verb keeps
// kiroCLITimeout by going through runKiroCLI.
func runKiroCLIOutput(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, kiroCLIPath(), args...)
	// EDITOR=true (and VISUAL, which some CLIs consult first) — see the
	// package comment. Appended onto the inherited environment rather than
	// replacing it, so KIRO_HOME and every other override the daemon's own
	// process carries still reaches the child.
	cmd.Env = append(os.Environ(), "EDITOR=true", "VISUAL=true")
	cmd.Stdin = nil
	cmd.WaitDelay = shellout.WaitDelay

	var out shellout.CappedBuffer
	out.Limit = maxKiroCLIOutput
	cmd.Stdout = &out
	var errOut shellout.CappedBuffer
	errOut.Limit = maxKiroCLIOutput
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(strings.Join([]string{out.String(), errOut.String()}, " "))
		return "", fmt.Errorf("kirocli: %s %s: %w: %s", kiroCLIBinary, strings.Join(args, " "), err, combined)
	}
	return out.String(), nil
}

// kiroVersionTimeout bounds `kiro-cli --version`. Shorter than kiroCLITimeout
// for exactly the reason core/pkg/cliprobe.Timeout is 2s: reading a banner off
// stdout is not a write, it runs on the consent path with a user watching, and
// a CLI that hangs has to lose quickly. Kept as its own constant rather than
// importing cliprobe's, because this probe deliberately does NOT go through
// cliprobe — see kiroCLIVersion.
const kiroVersionTimeout = 2 * time.Second

// kiroCLIVersion reads the installed kiro-cli's version, as a bare
// major.minor.patch string. Empty string plus an error when it cannot be read
// for any reason (binary missing, hung, non-zero exit, unrecognizable banner).
//
// It deliberately does NOT call core/pkg/cliprobe, even though that package
// exists to run this exact question, and the reason is the one thing that makes
// the refresh's fail-closed direction defensible (agentrefresh.go):
//
//   - cliprobe resolves argv[0] through pathutil's TRUSTED DIRECTORIES first,
//     which on the machine this adapter was developed on selects
//     /opt/homebrew/bin/kiro-cli — a DIFFERENT binary from the one this package
//     actually shells out to, which goes through the kiroCLIPath seam
//     (measured; see kiroCLIPath's own doc comment).
//   - A refresh regenerates the agent config by running `kiro-cli agent create
//     --from kiro_default` through kiroCLIPath. Deciding "has kiro-cli been
//     upgraded?" from a binary we are not going to run would make the trigger
//     and the capability two different things that can disagree.
//
// Going through the same seam makes "we can read the version" and "we can
// regenerate" the SAME capability by construction, which is what
// TestRefreshTriggerAndRegenerationUseTheSameBinarySeam pins.
func kiroCLIVersion(ctx context.Context) (string, error) {
	out, err := runKiroCLIOutput(ctx, kiroVersionTimeout, "--version")
	if err != nil {
		return "", err
	}
	v, ok := cliversion.Parse(out)
	if !ok {
		return "", fmt.Errorf("kirocli: %s --version printed no recognizable version: %q",
			kiroCLIBinary, strings.TrimSpace(out))
	}
	return v.String(), nil
}

// kiroAgentCreateFromDefault materializes name as a full copy of the given
// source agent (kiroBuiltinDefaultAgent, ordinarily) via
// `kiro-cli agent create <name> --from <src>`. Errors, including "already
// exists", are returned verbatim for the caller to interpret; EnsureHooksInstalled
// checks existence itself before calling this, so an "already exists" here
// would mean a concurrent writer.
func kiroAgentCreateFromDefault(ctx context.Context, name, from string) error {
	return runKiroCLI(ctx, "agent", "create", name, "--from", from)
}

// kiroAgentSetDefault points chat.defaultAgent at name via
// `kiro-cli agent set-default <name>`, which splices settings/cli.json in
// place and — measured live — preserves every other key already there.
func kiroAgentSetDefault(ctx context.Context, name string) error {
	return runKiroCLI(ctx, "agent", "set-default", name)
}
