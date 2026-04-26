package e2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/application/services"
	"irrlicht/core/ports/inbound"
)

// TestScanner_TracksTwoConcurrentProcessesWithSameAgentName starts two
// fake-claude processes simultaneously under a single shared agent name and
// asserts both are tracked as distinct pre-sessions with their own PIDs.
//
// This guards against any future regression where Scanner or SessionDetector
// dedups by agent name (rather than by PID), which would make a second
// concurrent session invisible to the daemon.
func TestScanner_TracksTwoConcurrentProcessesWithSameAgentName(t *testing.T) {
	name := fakeProcessName()
	cmd1, cwd1 := startFakeClaudeProcessNamed(t, name)
	cmd2, cwd2 := startFakeClaudeProcessNamed(t, name)

	if cmd1.Process.Pid == cmd2.Process.Pid {
		t.Fatalf("PIDs collided: %d", cmd1.Process.Pid)
	}
	if cwd1 == cwd2 {
		t.Fatalf("CWDs collided: %s", cwd1)
	}

	scanner := processlifecycle.NewScanner(name, "test", 200*time.Millisecond)
	repo := newMemRepo()

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	id1 := fmt.Sprintf("proc-%d", cmd1.Process.Pid)
	id2 := fmt.Sprintf("proc-%d", cmd2.Process.Pid)

	if !waitForSession(repo, id1, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared", id1)
	}
	if !waitForSession(repo, id2, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared", id2)
	}

	s1, _ := repo.Load(id1)
	s2, _ := repo.Load(id2)
	// PID for pre-sessions is encoded in the session ID (proc-<pid>); the
	// SessionState.PID field is populated later by async PID discovery and
	// is not exercised here. CWD is the per-process discriminator we assert.
	if s1.CWD != cwd1 {
		t.Errorf("session %s CWD: got %q, want %q", id1, s1.CWD, cwd1)
	}
	if s2.CWD != cwd2 {
		t.Errorf("session %s CWD: got %q, want %q", id2, s2.CWD, cwd2)
	}

	// Repo must hold exactly the two pre-sessions (and nothing else from a
	// stray scanner emission).
	all, _ := repo.ListAll()
	if len(all) != 2 {
		var ids []string
		for _, s := range all {
			ids = append(ids, s.SessionID)
		}
		t.Errorf("repo session count: got %d (%v), want 2", len(all), ids)
	}
}
