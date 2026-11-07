package fakeproc

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// started runs cmd and reports whether it is still running after d. It
// always reaps cmd before the test ends.
func started(t *testing.T, cmd *exec.Cmd, d time.Duration) bool {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stand-in: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	select {
	case <-done:
		return false
	case <-time.After(d):
		return true
	}
}

// The property every caller depends on: a stand-in started under a name no
// system utility answers to is still there when discovery looks. The
// /bin/sleep fixtures this package replaced lost it on ubuntu-26.04 — the
// stand-in was gone before the first discovery pass.
func TestCommand_StandInOutlivesItsStartUnderAForeignName(t *testing.T) {
	cmd := Command(t, filepath.Join(t.TempDir(), "opencode2"), "60")
	if !started(t, cmd, time.Second) {
		t.Fatal("stand-in exited within 1s of starting; want it alive for its Lifetime")
	}
}

// The committed mutation for the check above: a stand-in whose init bails
// out (an unparseable lifetime takes the os.Exit(2) path) vanishes at once,
// as the /bin/sleep stand-ins did on ubuntu-26.04, and started must report
// that rather than time out into a pass.
func TestStarted_SeesAStandInThatExitsAtOnce(t *testing.T) {
	cmd := Command(t, filepath.Join(t.TempDir(), "opencode2"), "60")
	cmd.Env = append(cmd.Env, sleepEnv+"=not-a-duration")
	if started(t, cmd, 10*time.Second) {
		t.Fatal("a stand-in with an unparseable lifetime was still running after 10s; want it to exit at once")
	}
}
