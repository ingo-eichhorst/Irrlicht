//go:build linux

package processlifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestObserverHelper is the throwaway child process the conformance test
// spawns. It runs only under GO_WANT_OBSERVER_HELPER=1: it chdir's into a
// known directory, opens a transcript file for writing and keeps the fd open,
// then idles until the parent kills it. This gives the parent a real process
// with a known name, cwd, and held-open transcript to observe via /proc —
// no agent CLI involved.
func TestObserverHelper(t *testing.T) {
	if os.Getenv("GO_WANT_OBSERVER_HELPER") != "1" {
		return
	}
	dir := os.Getenv("OBSERVER_HELPER_DIR")
	file := os.Getenv("OBSERVER_HELPER_FILE")
	if err := os.Chdir(dir); err != nil {
		os.Exit(2)
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		os.Exit(3)
	}
	defer f.Close()
	if _, err := f.WriteString("ready\n"); err != nil {
		os.Exit(4)
	}
	_ = f.Sync()
	// Hold the fd open and stay alive until the parent kills us.
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// TestLinuxObserverConformance is the Stage-1 sensor regression gate: it
// asserts the live Linux ProcessObserver + pidfd exit-watcher observe a real
// process correctly through /proc — find-by-name, cwd, transcript-writer, and
// exit. Deterministic and CLI-free, so it runs in CI / the Docker harness.
func TestLinuxObserverConformance(t *testing.T) {
	tmp := t.TempDir()
	// Resolve symlinks up front: /proc/<pid>/cwd is fully resolved, so the
	// expected path must be too (e.g. if TMPDIR sits under a symlink).
	tmpResolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", tmp, err)
	}
	transcript := filepath.Join(tmpResolved, "transcript.jsonl")

	cmd := exec.Command(os.Args[0], "-test.run=^TestObserverHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"GO_WANT_OBSERVER_HELPER=1",
		"OBSERVER_HELPER_DIR="+tmpResolved,
		"OBSERVER_HELPER_FILE="+transcript,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	pid := cmd.Process.Pid
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})

	// WriterOf becomes true only once the child has chdir'd and opened the
	// transcript — use it as the readiness signal (bounded poll).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if w, _ := osProc.WriterOf(transcript); w == pid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("WriterOf(%q) never returned helper pid %d", transcript, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// FindByName: the helper's comm (the test binary, truncated to 15 chars)
	// must be discoverable, and the result must include the helper pid.
	commBytes, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		t.Fatalf("read helper comm: %v", err)
	}
	comm := strings.TrimRight(string(commBytes), "\n") // tolerate empty/short reads
	pids, err := osProc.FindByName(comm)
	if err != nil {
		t.Fatalf("FindByName(%q): %v", comm, err)
	}
	if !slices.Contains(pids, pid) {
		t.Errorf("FindByName(%q) = %v, want it to include helper pid %d", comm, pids, pid)
	}

	// CWDOf: the helper chdir'd to the temp dir.
	cwd, err := osProc.CWDOf(pid)
	if err != nil {
		t.Fatalf("CWDOf(%d): %v", pid, err)
	}
	if cwd != tmpResolved {
		t.Errorf("CWDOf(%d) = %q, want %q", pid, cwd, tmpResolved)
	}

	// Exit detection via pidfd: Watch the helper, kill it, expect the handler.
	exited := make(chan int, 1)
	mon, err := NewMonitor(func(p int, _ string) { exited <- p })
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	defer mon.Close()
	go mon.Run(t.Context())
	if err := mon.Watch(pid, "conformance-session"); err != nil {
		t.Fatalf("Watch(%d): %v", pid, err)
	}

	killed = true
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_, _ = cmd.Process.Wait()

	select {
	case got := <-exited:
		if got != pid {
			t.Errorf("exit handler fired for pid %d, want %d", got, pid)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("exit handler did not fire for pid %d within 3s", pid)
	}
}
