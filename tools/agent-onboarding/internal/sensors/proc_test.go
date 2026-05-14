package sensors

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestProc_detectsSpawnAndExit launches a sleep subprocess under a parent
// we control (this test's process), waits for the sensor to emit a spawn,
// kills the subprocess, waits for the exit. Skips if `ps` isn't available.
func TestProc_detectsSpawnAndExit(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available")
	}
	rootPID := os.Getpid()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &Proc{RootPID: rootPID, PollInterval: 100 * time.Millisecond}
	ch := s.Run(ctx)

	// Spawn a known-named subprocess as a descendant of this test.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Wait for the spawn signal naming our child PID.
	saw := waitFor(t, ch, time.Second*3, func(sig Signal) bool {
		if sig.Kind != "spawn" {
			return false
		}
		var e procEntry
		if err := json.Unmarshal(sig.Payload, &e); err != nil {
			return false
		}
		return e.PID == childPID
	})
	if !saw {
		t.Fatal("did not observe spawn signal for child")
	}

	// Kill the child and wait for exit signal.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	saw = waitFor(t, ch, time.Second*3, func(sig Signal) bool {
		if sig.Kind != "exit" {
			return false
		}
		var e procEntry
		if err := json.Unmarshal(sig.Payload, &e); err != nil {
			return false
		}
		return e.PID == childPID
	})
	if !saw {
		t.Fatal("did not observe exit signal for child")
	}
}

func waitFor(t *testing.T, ch <-chan Signal, dur time.Duration, pred func(Signal) bool) bool {
	t.Helper()
	deadline := time.After(dur)
	for {
		select {
		case sig, ok := <-ch:
			if !ok {
				return false
			}
			if pred(sig) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

