//go:build darwin

package processlifecycle

import (
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

	m, err := NewMonitor(func(p int, s string) {
		mu.Lock()
		gotPID = p
		gotSession = s
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	defer m.Close()

	go m.Run(t.Context())

	if err := m.Watch(pid, "test-session-1"); err != nil {
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

	m, err := NewMonitor(func(p int, s string) {
		mu.Lock()
		gotPID = p
		gotSession = s
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	defer m.Close()

	go m.Run(t.Context())

	// Watching a dead PID should fire the handler immediately.
	if err := m.Watch(pid, "dead-session"); err != nil {
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
	m, err := NewMonitor(func(int, string) {
		called <- struct{}{}
	})
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	defer m.Close()

	go m.Run(t.Context())

	if err := m.Watch(pid, "s1"); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	m.Unwatch(pid)

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

