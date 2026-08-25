package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSIGTERMRightAfterAddrPublishLeavesNoAddrFile is the #1808 defect test.
//
// main() used to publish the addr file (the "daemon is up" signal) before
// calling signal.Notify to install the SIGTERM handler. A SIGTERM landing in
// that window got the default disposition — the process died immediately and
// os.Remove(addrPath) never ran — even though every reader of the addr file
// treats its existence as "the daemon is alive and will clean up after
// itself."
//
// This races a SIGTERM in as fast as possible after the addr file appears,
// bypassing bootSmokeDaemon's 20ms-poll waitForAddr (whose latency alone can
// be enough to let the window close first) and grant-all's synchronous
// consent effects (the real work — installing hooks/statuslines across every
// declared permission, exactly what TestGrantAllLeavesAgentConfigsAlone
// exercises) so there is something occupying the window for the SIGTERM to
// land inside, the same shape that produced the flake on a loaded Linux CI
// runner.
//
// Seen red before the fix (`go test -run TestSIGTERMRightAfterAddrPublishLeavesNoAddrFile -race -count=1 -v ./core/cmd/irrlichd/`):
//
//	addr file <dir>/irrlichd.addr should be removed after a SIGTERM landing right after it was published, stat err = <nil>
func TestSIGTERMRightAfterAddrPublishLeavesNoAddrFile(t *testing.T) {
	bin := buildIrrlichd(t)
	// shortTempDir, not t.TempDir(): IRRLICHT_HOME holds the unix socket and a
	// long test name pushes sun_path past its 104-byte limit — see its comment.
	homeDir := shortTempDir(t)
	stateDir := shortTempDir(t)

	cmd := exec.Command(bin)
	// grant-all, not the smoke test's default "ask": PermService.Start only
	// does real synchronous work — installing every declared consent
	// effect — in grant-all mode. That work is what occupies the window
	// between publishAddrFile and signal.Notify for long enough to hit
	// reliably; "ask" mode has nothing granted to apply and returns from
	// startBackgroundLoops almost immediately.
	cmd.Env = append(sanitizedChildEnv(homeDir, stateDir),
		"IRRLICHT_BIND_ADDR=127.0.0.1:0", "IRRLICHT_PERMISSION_MODE=grant-all")

	exited := make(chan struct{})
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	// A single reaper goroutine owns cmd.Wait(), same discipline as
	// bootSmokeDaemonIn, so `go test -race` never sees Wait called twice.
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	addrPath := filepath.Join(stateDir, "irrlichd.addr")
	// waitForAddrFileTight, not waitForAddr's 20ms poll: the point is to
	// detect the addr file, and fire the SIGTERM, as close to the instant
	// publishAddrFile's os.Rename lands as this process can manage — a
	// looser poll would let the daemon's remaining startup work finish
	// before the signal is even sent, which would prove nothing about the
	// window this test exists to close.
	waitForAddrFileTight(t, addrPath, time.Now().Add(15*time.Second))
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case <-exited:
	case <-time.After(6 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatalf("daemon did not exit within 6s of SIGTERM")
	}

	if _, err := os.Stat(addrPath); !os.IsNotExist(err) {
		t.Errorf("addr file %s should be removed after a SIGTERM landing right after it was published, stat err = %v", addrPath, err)
	}
}
