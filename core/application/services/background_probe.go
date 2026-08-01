package services

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"irrlicht/core/pkg/pathutil"
)

// lsofPath is resolved once from a fixed set of trusted directories rather
// than trusted PATH, per go:S4036.
var lsofPath = pathutil.MustResolve("lsof")

// lsofTimeout bounds a single lsof invocation. A package var rather than a
// const so tests can drive the timeout path without waiting seconds.
var lsofTimeout = 2 * time.Second

// backgroundProbe answers "does any of these output files still have a live
// writer?" — the daemon's authoritative liveness check for Claude Code Bash
// background processes (run_in_background). Claude Code reports a background
// process's exit only on demand (when the agent polls via BashOutput), so the
// transcript alone can't tell us when one dies; this probe walks the live
// process state instead. Modeled as a field so tests can inject a fake.
// See issue #445.
//
// The second return is "was this conclusive?", not a second liveness bit. It
// is false only when the probe could not find out — see anyLiveOutputWriter —
// and a false there must never be read as "dead", because the caller's
// response to death is a durable purge (issue #1299).
type backgroundProbe func(outputPaths []string) (alive, conclusive bool)

// anyLiveOutputWriter reports whether any of the given background output files
// is currently held open by a live process. Each backgrounded command's
// stdout/stderr is redirected to its tasks/<bash_id>.output file, so the file
// is held open exactly as long as the process (or its shell) is alive — `lsof`
// listing a holder means the process is still running, an empty result means
// it has exited. The output paths are unique per session, so any holder is the
// session's own background process.
//
// A single `lsof -t -- path…` over all paths returns the union of holder PIDs
// on stdout; if any PID is printed, at least one background process is alive.
//
// Crucially we read STDOUT regardless of lsof's exit code. lsof exits 1 when
// ANY named path has no open holder (a background process that already exited
// but whose .output file remains, or a stale/deleted path) — yet it still
// prints the holder PIDs of the OTHER paths to stdout. Branching on the error
// would discard those PIDs and wrongly report a still-running process as dead,
// flipping the session to `ready` while it's alive (issue #445 review). lsof's
// diagnostics and usage text go to stderr (dropped by .Output()), and `-t`
// prints only PIDs to stdout, so we additionally require an all-digit line to
// guard against any unexpected stdout noise.
//
// A TIMEOUT is reported as inconclusive rather than as "no live writer". lsof
// walks every process on the machine, so it is slowest exactly when the machine
// is busiest — which is also when long-running background work is most likely
// to be in flight. Reading that silence as death was not the harmless "don't
// pin forever" degradation this comment used to claim: the caller responds to
// death by purging the background process from the tailer AND persisting that
// through saveLedger, so one slow lsof could permanently drop a still-running
// process and flip a live session to `ready` with no way back — the same
// false-negative the exit-code handling above exists to prevent, reached by a
// different route (issue #1299).
//
// Any OTHER failure — most plausibly a missing lsof, since MustResolve falls
// back to the bare name — still degrades to a conclusive "no live writer".
// Retrying cannot fix those, so treating them as inconclusive would pin every
// affected session `working` forever, which is the failure the old degradation
// was protecting against. Only the transient case gets the retry.
func anyLiveOutputWriter(outputPaths []string) (alive, conclusive bool) {
	if len(outputPaths) == 0 {
		return false, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), lsofTimeout)
	defer cancel()
	args := append([]string{"-t", "--"}, outputPaths...)
	out, _ := exec.CommandContext(ctx, lsofPath, args...).Output() //nolint:errcheck — exit 1 (some path unheld) still carries live PIDs on stdout
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := strconv.Atoi(line); err == nil {
			return true, true
		}
	}
	// Checked only once stdout has yielded no PID: lsof can be killed by the
	// deadline after already printing a holder, and that PID is still proof of
	// life worth keeping.
	if ctx.Err() == context.DeadlineExceeded {
		return false, false
	}
	return false, true
}

// backgroundPIDProbe answers "is any of these PIDs still a live process?" — the
// daemon's authoritative liveness check for adapters that report a backgrounded
// command's PID rather than an output file (Gemini CLI hides the output and
// surfaces only "(PID: N)"), so the lsof-on-output-file probe has nothing to
// inspect. Modeled as a field so tests can inject a fake. See issue #661.
type backgroundPIDProbe func(pids []string) bool

// pidLivenessSignal is the seam for the kill(pid, 0) existence check, factored
// out so tests can drive anyLivePID's EPERM-alive branch without a foreign-user
// process. Defaults to syscall.Kill. Mirrors the backgroundPIDProbe field seam.
var pidLivenessSignal = syscall.Kill

// anyLivePID reports whether any of the given PIDs is a currently live process.
// `kill(pid, 0)` performs the kernel's existence-and-permission check without
// delivering a signal: a nil error (or EPERM — the process exists but is owned
// by another user) means alive; ESRCH means gone. A non-numeric or non-positive
// entry is skipped. An empty list is "nothing live", the safe degradation that
// never pins a session `working` forever.
//
// Bounded PID-reuse window: between probes a reported PID could be recycled to
// an unrelated live process and read as alive, holding the session `working`
// until the next 5s re-probe reconciles it — the same staleness class as the
// lsof-on-output-file path above (a reused .output path), and self-correcting.
func anyLivePID(pids []string) bool {
	for _, s := range pids {
		pid, err := strconv.Atoi(s)
		if err != nil || pid <= 0 {
			continue
		}
		switch err := pidLivenessSignal(pid, 0); err {
		case nil, syscall.EPERM:
			return true
		}
	}
	return false
}
