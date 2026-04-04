package process

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestWatch_ProcessExits(t *testing.T) {
	// Start a child process that we can kill.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child: %v", err)
	}
	pid := cmd.Process.Pid

	var (
		mu         sync.Mutex
		gotPID     int
		gotSession string
	)

	w, err := New(func(p int, s string) {
		mu.Lock()
		gotPID = p
		gotSession = s
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	go w.Run(t.Context())

	if err := w.Watch(pid, "test-session-1"); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Kill the child.
	cmd.Process.Kill()
	cmd.Wait()

	// Wait for the handler to fire.
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		done := gotPID != 0
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for exit handler")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPID != pid {
		t.Errorf("got pid %d, want %d", gotPID, pid)
	}
	if gotSession != "test-session-1" {
		t.Errorf("got session %q, want %q", gotSession, "test-session-1")
	}
}

func TestWatch_AlreadyDead(t *testing.T) {
	// Start and immediately kill a process to get a dead PID.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run child: %v", err)
	}
	// PID is now dead (process has exited).
	pid := cmd.ProcessState.Pid()

	var (
		mu         sync.Mutex
		gotPID     int
		gotSession string
	)

	w, err := New(func(p int, s string) {
		mu.Lock()
		gotPID = p
		gotSession = s
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	go w.Run(t.Context())

	// Watching a dead PID should fire the handler immediately.
	if err := w.Watch(pid, "dead-session"); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		done := gotPID != 0
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for exit handler on dead PID")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPID != pid {
		t.Errorf("got pid %d, want %d", gotPID, pid)
	}
	if gotSession != "dead-session" {
		t.Errorf("got session %q, want %q", gotSession, "dead-session")
	}
}

func TestUnwatch(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	called := make(chan struct{}, 1)
	w, err := New(func(int, string) {
		called <- struct{}{}
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	go w.Run(t.Context())

	if err := w.Watch(pid, "s1"); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	w.Unwatch(pid)

	// Kill and verify handler is NOT called.
	cmd.Process.Kill()
	cmd.Wait()

	select {
	case <-called:
		t.Error("handler was called after Unwatch")
	case <-time.After(1 * time.Second):
		// Good — handler was not called.
	}
}

func TestDiscoverPID_FiltersSelf(t *testing.T) {
	// Create a temp file and keep it open — only our own process has it open.
	// DiscoverPID should filter out the caller's own PID (the daemon in prod)
	// and return 0 since no external process has the file open.
	f, err := os.CreateTemp("", "processwatcher-test-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	pid, err := DiscoverPID(f.Name())
	if err != nil {
		t.Fatalf("DiscoverPID: %v", err)
	}
	if pid != 0 {
		t.Errorf("DiscoverPID got pid %d, want 0 (self-PID should be filtered)", pid)
	}
}

func TestDiscoverPID_NoMatch(t *testing.T) {
	// File that no one has open.
	f, err := os.CreateTemp("", "processwatcher-noop-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	name := f.Name()
	f.Close() // Close it so no one has it open.
	defer os.Remove(name)

	pid, err := DiscoverPID(name)
	if err != nil {
		t.Fatalf("DiscoverPID: %v", err)
	}
	if pid != 0 {
		t.Errorf("DiscoverPID got pid %d, want 0 (no match)", pid)
	}
}
