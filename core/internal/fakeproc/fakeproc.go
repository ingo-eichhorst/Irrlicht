// Package fakeproc starts a live stand-in for an agent process under a name
// the test chooses, for tests that run process discovery against the real OS
// (pgrep / kern.procargs2 on macOS, /proc on Linux).
//
// The stand-in is the running test binary itself, behind a symlink carrying
// the chosen name and told by an environment variable to sleep instead of
// running tests. The switch lives in this package's init, which Go runs
// before the importing package's TestMain and tests, so importing the
// package is all a test binary needs — there is no call to forget, and so no
// way for a stand-in to run the suite and start stand-ins of its own.
//
// Why not the system sleep: these fixtures used to symlink /bin/sleep, which
// only works while sleep ignores the name it was started under. GitHub's
// ubuntu-latest moved from ubuntu-24.04 (linux.yml run 40599334423 on
// 2026-10-27, green) to ubuntu-26.04 (run 41284168180 on 2026-11-01, red),
// and every fixture built that way failed from then on: the stand-in was not
// there by the time discovery looked. The likely mechanism — assumed, not
// checked on the runner — is that Ubuntu's default coreutils are now uutils,
// a multicall binary whose main (src/bin/coreutils.rs, read 2026-11-07)
// picks the utility argv[0]'s basename ends with and otherwise exits through
// validation::not_found, which `opencode2` or `irrlicht-e2e-N` would hit.
//
// The package lives under core/internal/ so production code cannot import it.
package fakeproc

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// sleepEnv carries how long a stand-in sleeps. Its presence is what makes a
// test binary a stand-in.
const sleepEnv = "IRRLICHT_FAKEPROC_SLEEP"

// Lifetime is how long a stand-in lives unless the test kills it first —
// the 60s the /bin/sleep fixtures passed, so a stand-in a crashed test never
// reaps still goes away on its own.
const Lifetime = 60 * time.Second

// init turns this process into a stand-in when Command launched it: it
// sleeps out the requested lifetime and exits without returning. In any
// other process it returns at once.
func init() {
	v, ok := os.LookupEnv(sleepEnv)
	if !ok {
		return
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		os.Exit(2)
	}
	time.Sleep(d)
	os.Exit(0)
}

// ceiling bounds a stand-in from the parent's side as well (#1543): past
// it, or when the test ends, its context kills it even if the stand-in's
// own Lifetime somehow did not end it.
const ceiling = Lifetime + 30*time.Second

// Command links binPath to the running test binary and returns a command
// that runs it as a stand-in, with argv exactly binPath followed by args —
// the argv a discovery matcher sees. binPath's directory must exist and
// binPath must not; the caller sets Dir and starts it. The stand-in is
// killed when the test ends at the latest.
func Command(t testing.TB, binPath string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("fakeproc: locate the test binary: %v", err)
	}
	if err := os.Symlink(exe, binPath); err != nil {
		t.Fatalf("fakeproc: symlink %s → %s: %v", binPath, exe, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ceiling)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = append(os.Environ(), sleepEnv+"="+Lifetime.String())
	return cmd
}
