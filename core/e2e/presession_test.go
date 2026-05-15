// Package e2e contains end-to-end tests that exercise the full detection
// pipeline with real OS primitives (pgrep, lsof, kqueue).
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// TestPreSession_DetectedBeforeTranscript starts a real process (symlinked
// from /bin/sleep so pgrep sees a unique name), wires up the full
// Scanner → SessionDetector pipeline, and asserts the session appears in
// the repo as "ready" before any .jsonl transcript file is created.
func TestPreSession_DetectedBeforeTranscript(t *testing.T) {
	// Unique process name so we never collide with real claude instances.
	processName := fmt.Sprintf("irrlicht-e2e-%d", os.Getpid())

	// Symlink /bin/sleep → <tmpdir>/<processName> so the OS reports our
	// chosen name to pgrep -x.
	binDir := realTempDir(t)
	binPath := filepath.Join(binDir, processName)
	if err := os.Symlink("/bin/sleep", binPath); err != nil {
		t.Fatalf("symlink /bin/sleep → %s: %v", binPath, err)
	}

	// Start the fake agent process with a controlled CWD.
	fakeCWD := realTempDir(t)
	cmd := exec.Command(binPath, "60")
	cmd.Dir = fakeCWD
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake process: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	// Wire up: Scanner → SessionDetector (with in-memory stubs).
	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond).WithIdentity(testIdentity)
	repo := newMemRepo()

	detector := services.NewSessionDetector(
		[]inbound.Watcher{scanner},
		nil, // no ProcessWatcher needed
		repo,
		&nopLogger{},
		&stubGit{},
		&stubMetrics{},
		nil, // no broadcaster
		"test", 0, nil, nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	// Poll the repo until the pre-session appears.
	expectedID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout: session %s never appeared in repo", expectedID)
		case <-time.After(100 * time.Millisecond):
		}

		state, _ := repo.Load(expectedID)
		if state == nil {
			continue
		}

		// Session found — verify fields.
		if state.State != session.StateReady {
			t.Errorf("state: got %q, want %q", state.State, session.StateReady)
		}
		if state.CWD != fakeCWD {
			t.Errorf("CWD: got %q, want %q", state.CWD, fakeCWD)
		}
		if state.TranscriptPath != "" {
			t.Errorf("transcript_path should be empty for pre-session, got %q", state.TranscriptPath)
		}
		if state.ProjectName != filepath.Base(fakeCWD) {
			t.Errorf("project_name: got %q, want %q", state.ProjectName, filepath.Base(fakeCWD))
		}
		return // success
	}
}

// TestPreSession_ReplacedByRealSession verifies that when a .jsonl transcript
// file appears (simulating the user's first message), the pre-session is
// deleted and replaced by the real transcript session.
func TestPreSession_ReplacedByRealSession(t *testing.T) {
	processName := fmt.Sprintf("irrlicht-e2e-%d", os.Getpid())

	binDir := realTempDir(t)
	binPath := filepath.Join(binDir, processName)
	if err := os.Symlink("/bin/sleep", binPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	fakeCWD := realTempDir(t)
	cmd := exec.Command(binPath, "60")
	cmd.Dir = fakeCWD
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	projectsRoot := realTempDir(t)
	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond).WithIdentity(testIdentity)
	repo := newMemRepo()

	// Also wire a mock transcript watcher so we can inject a real session event.
	transcriptWatcher := &mockWatcher{
		ch:       make(chan agent.Event, 4),
		identity: testIdentity,
	}

	detector := services.NewSessionDetector(
		[]inbound.Watcher{scanner, transcriptWatcher},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil, nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	// Wait for the pre-session to appear.
	preID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, preID, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared", preID)
	}

	// Simulate Claude Code creating a transcript: create the project dir
	// and .jsonl file, then inject an EventNewSession from the transcript watcher.
	projectDir := processlifecycle.CWDToProjectDir(fakeCWD)
	projPath := filepath.Join(projectsRoot, projectDir)
	if err := os.MkdirAll(projPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	realID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	transcriptPath := filepath.Join(projPath, realID+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	transcriptWatcher.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      realID,
		ProjectDir:     projectDir,
		TranscriptPath: transcriptPath,
	}

	// Wait for the real session to appear.
	if !waitForSession(repo, realID, 5*time.Second) {
		t.Fatalf("timeout: real session %s never appeared", realID)
	}

	// The real session should be ready (initial state before activity).
	realState, _ := repo.Load(realID)
	if realState.State != session.StateReady {
		t.Errorf("real session state: got %q, want %q", realState.State, session.StateReady)
	}

	// The pre-session should be gone (cleaned up by cleanupPreSessionsForProject).
	time.Sleep(300 * time.Millisecond) // allow cleanup to propagate
	preState, _ := repo.Load(preID)
	if preState != nil {
		t.Errorf("pre-session %s should have been deleted, but still exists with state %q", preID, preState.State)
	}
}

// TestPreSession_CreatedDespiteHistoricalSession is the regression test for
// GH #113: a prior session on disk with a different PID must not block
// pre-session creation for a freshly-opened process in the same project.
func TestPreSession_CreatedDespiteHistoricalSession(t *testing.T) {
	cmd, fakeCWD := startFakeClaudeProcess(t)

	// Seed memRepo with a historical session using os.Getpid() as its PID
	// so it's guaranteed alive (pidMgr.SeedPIDs keeps it) AND guaranteed
	// different from the fake claude PID (the new sessionChecker correctly
	// returns false for the live process).
	projectsRoot := realTempDir(t)
	projectDir := processlifecycle.CWDToProjectDir(fakeCWD)
	histPID := os.Getpid()
	if histPID == cmd.Process.Pid {
		t.Fatalf("test PID unexpectedly matches fake process PID")
	}
	repo := newMemRepo()
	repo.Save(&session.SessionState{
		SessionID:      "historical-aaaa-bbbb-cccc-dddd",
		State:          session.StateReady,
		PID:            histPID,
		TranscriptPath: filepath.Join(projectsRoot, projectDir, "old.jsonl"),
	})

	scanner := processlifecycle.NewScanner(fakeProcessName(), "test", 200*time.Millisecond).WithIdentity(testIdentity)
	scanner.WithSessionChecker(realSessionCheckerFor(repo))

	detector := services.NewSessionDetector(
		[]inbound.Watcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil, nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	expectedID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, expectedID, 5*time.Second) {
		t.Fatalf("regression: pre-session %s never appeared while historical session for project %s (PID=%d) was present", expectedID, projectDir, histPID)
	}

	if s, _ := repo.Load("historical-aaaa-bbbb-cccc-dddd"); s == nil {
		t.Errorf("historical session should still be in repo, but was removed")
	}
}

// TestPreSession_SurvivesNeighbourSessionActivity is the regression test for
// the mtime-fallback bug: scanner.hasActiveSession used to fall back to a
// project-wide jsonl mtime check that couldn't discriminate which transcript
// belonged to which PID, so a freshly-written neighbour transcript would
// wrongly mark our pre-session as superseded and SessionDetector.onRemoved
// would delete it.
func TestPreSession_SurvivesNeighbourSessionActivity(t *testing.T) {
	cmd, fakeCWD := startFakeClaudeProcess(t)

	// Simulate another active session in the same project by touching a
	// neighbour .jsonl with a fresh mtime — this is exactly what the old
	// 60s mtime window would pick up and wrongly attribute to our PID.
	projectsRoot := realTempDir(t)
	projectDir := processlifecycle.CWDToProjectDir(fakeCWD)
	projPath := filepath.Join(projectsRoot, projectDir)
	if err := os.MkdirAll(projPath, 0755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	neighbourJSONL := filepath.Join(projPath, "neighbour-aaaa-bbbb-cccc-dddd.jsonl")
	if err := os.WriteFile(neighbourJSONL, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatalf("write neighbour jsonl: %v", err)
	}
	now := time.Now()
	_ = os.Chtimes(neighbourJSONL, now, now)

	repo := newMemRepo()
	scanner := processlifecycle.NewScanner(fakeProcessName(), "test", 200*time.Millisecond).WithIdentity(testIdentity)
	scanner.WithSessionChecker(realSessionCheckerFor(repo))

	detector := services.NewSessionDetector(
		[]inbound.Watcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil, nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	expectedID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, expectedID, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared (initial detection suppressed?)", expectedID)
	}

	// Continuously refresh the neighbour mtime across several scanner polls
	// (200ms interval) to ensure the old 60s window would always be tripped.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = os.Chtimes(neighbourJSONL, time.Now(), time.Now())
		time.Sleep(150 * time.Millisecond)
		if state, _ := repo.Load(expectedID); state == nil {
			t.Fatalf("regression: pre-session %s was removed while neighbour jsonl had fresh mtime — mtime fallback reintroduced", expectedID)
		}
	}
}

// TestPreSession_RemovedOnProcessExit verifies that if the process exits
// without ever creating a transcript, the pre-session is deleted from the repo.
func TestPreSession_RemovedOnProcessExit(t *testing.T) {
	processName := fmt.Sprintf("irrlicht-e2e-%d", os.Getpid())

	binDir := realTempDir(t)
	binPath := filepath.Join(binDir, processName)
	if err := os.Symlink("/bin/sleep", binPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	fakeCWD := realTempDir(t)
	cmd := exec.Command(binPath, "60")
	cmd.Dir = fakeCWD
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond).WithIdentity(testIdentity)
	repo := newMemRepo()

	detector := services.NewSessionDetector(
		[]inbound.Watcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil, nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	preID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, preID, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared", preID)
	}

	// Kill the process — scanner should emit EventRemoved on next poll.
	cmd.Process.Kill()
	cmd.Wait()

	// Wait for the pre-session to be deleted.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout: pre-session %s still exists after process exit", preID)
		case <-time.After(100 * time.Millisecond):
		}

		state, _ := repo.Load(preID)
		if state == nil {
			return // deleted — success
		}
	}
}

// TestPreSession_DoesNotEvictNeighborSessionWithSharedCWD is the regression
// test for issue #345. When a second claude process starts in VS Code while
// another is already running, the scanner can catch it during the brief
// pre-`cd` window where its CWD still reads as the parent (the repo root).
// Pre-fix, PIDManager called the adapter's CWD-based discovery for the new
// `proc-<NEW>` pre-session, which could return the *neighbor's* PID — then
// HandlePIDAssigned's same-PID cleanup evicted the legitimate neighbor.
//
// The fix short-circuits TryDiscoverPID for `proc-<pid>` sessions: the PID
// is encoded in the ID, so adapter-level discovery is skipped entirely.
// This test fails pre-fix because the registered discoverFn returns the
// neighbor's PID.
func TestPreSession_DoesNotEvictNeighborSessionWithSharedCWD(t *testing.T) {
	cmd, _ := startFakeClaudeProcess(t)

	// Pre-seed a neighbor session whose PID is alive (use os.Getpid() — the
	// test binary itself — so PID liveness checks pass) and is NOT the fake
	// process's PID.
	neighborPID := os.Getpid()
	if neighborPID == cmd.Process.Pid {
		t.Fatalf("test PID unexpectedly matches fake process PID")
	}

	repo := newMemRepo()
	repo.Save(&session.SessionState{
		SessionID:      "neighbor-aaaa-bbbb-cccc-dddd",
		State:          session.StateReady,
		Adapter:        "test",
		PID:            neighborPID,
		TranscriptPath: filepath.Join(realTempDir(t), "neighbor.jsonl"),
		UpdatedAt:      time.Now().Unix(),
	})

	// Adversarial discoverFn: returns the neighbor's PID, simulating the bug
	// where CWD-based discovery misattributes the new proc-<pid> pre-session
	// to a sibling process in the same CWD. With the fix, this function MUST
	// NOT be called for proc-<pid> sessions.
	var discoverCalls atomic.Int32
	discovers := map[string]agent.PIDDiscoverFunc{
		"test": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			discoverCalls.Add(1)
			return neighborPID, nil
		},
	}

	scanner := processlifecycle.NewScanner(fakeProcessName(), "test", 200*time.Millisecond).WithIdentity(testIdentity)
	scanner.WithSessionChecker(realSessionCheckerFor(repo))

	detector := services.NewSessionDetector(
		[]inbound.Watcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	preID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, preID, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared", preID)
	}

	// Wait past the first DiscoverPIDWithRetry tick (immediate + 500ms +
	// 1s) so any erroneous discoverFn invocation has a chance to fire.
	time.Sleep(2 * time.Second)

	if n := discoverCalls.Load(); n != 0 {
		t.Errorf("adapter discoverFn was called %d times for proc-<pid> session — issue #345 regression", n)
	}

	pre, _ := repo.Load(preID)
	if pre == nil {
		t.Fatalf("pre-session %s was deleted", preID)
	}
	if pre.PID != cmd.Process.Pid {
		t.Errorf("pre-session PID = %d, want %d (encoded from session ID)", pre.PID, cmd.Process.Pid)
	}

	if s, _ := repo.Load("neighbor-aaaa-bbbb-cccc-dddd"); s == nil {
		t.Fatal("neighbor session was evicted by same-PID cleanup — issue #345 regression")
	}
}
