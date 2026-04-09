// Package e2e contains end-to-end tests that exercise the full detection
// pipeline with real OS primitives (pgrep, lsof, kqueue).
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond)
	repo := &memRepo{states: make(map[string]*session.SessionState)}

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner},
		nil, // no ProcessWatcher needed
		repo,
		&nopLogger{},
		&stubGit{},
		&stubMetrics{},
		nil, // no broadcaster
		"test", 0, nil,
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
	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond)
	repo := &memRepo{states: make(map[string]*session.SessionState)}

	// Also wire a mock transcript watcher so we can inject a real session event.
	transcriptWatcher := &mockWatcher{ch: make(chan agent.Event, 4)}

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner, transcriptWatcher},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
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
		Adapter:        "test",
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
// GH #113: opening Claude Code in a project that already has a prior session
// on disk (with a different PID) must still produce a pre-session for the new
// process. Before the fix, the projectDir-only sessionChecker returned true
// for the historical session and scanner.poll() silently skipped pre-session
// creation.
func TestPreSession_CreatedDespiteHistoricalSession(t *testing.T) {
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

	// Pre-seed memRepo with a historical session for the SAME project dir
	// but a DIFFERENT PID — simulating a session from a prior daemon run
	// that's still within MaxSessionAge. Use os.Getpid() (the test process
	// itself) as the historical PID: it's guaranteed alive, so pidMgr.SeedPIDs
	// leaves it in the repo, and it's guaranteed different from the fake
	// claude process PID so the new sessionChecker correctly returns false
	// for the live process. The transcript file does not need to exist on
	// disk; the callback only reads the persisted state.
	projectsRoot := realTempDir(t)
	projectDir := processlifecycle.CWDToProjectDir(fakeCWD)
	histPID := os.Getpid()
	if histPID == cmd.Process.Pid {
		t.Fatalf("test PID unexpectedly matches fake process PID")
	}
	repo := &memRepo{states: make(map[string]*session.SessionState)}
	repo.Save(&session.SessionState{
		SessionID:      "historical-aaaa-bbbb-cccc-dddd",
		State:          session.StateReady,
		PID:            histPID,
		TranscriptPath: filepath.Join(projectsRoot, projectDir, "old.jsonl"),
	})

	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond)
	// Mirror the production sessionChecker in core/cmd/irrlichd/main.go:
	// match on both projectDir and PID so historical sessions don't suppress
	// new pre-sessions for freshly-opened processes.
	scanner.WithSessionChecker(func(pd string, pid int) bool {
		sessions, err := repo.ListAll()
		if err != nil {
			return false
		}
		for _, s := range sessions {
			if len(s.SessionID) >= 5 && s.SessionID[:5] == "proc-" {
				continue
			}
			if s.PID != pid {
				continue
			}
			if s.TranscriptPath != "" &&
				filepath.Base(filepath.Dir(s.TranscriptPath)) == pd {
				return true
			}
		}
		return false
	})

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	// The new pre-session for the live PID must appear even though a
	// historical session exists for the same project.
	expectedID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, expectedID, 5*time.Second) {
		t.Fatalf("regression: pre-session %s never appeared while historical session for project %s (PID=%d) was present", expectedID, projectDir, histPID)
	}

	// The historical session must still be present — the scanner should not
	// touch sessions that don't belong to any live PID it observed.
	if s, _ := repo.Load("historical-aaaa-bbbb-cccc-dddd"); s == nil {
		t.Errorf("historical session should still be in repo, but was removed")
	}
}

// TestPreSession_SurvivesNeighbourSessionActivity is the regression test for
// the mtime-fallback bug observed live on GH #113: after the PID-aware
// sessionChecker landed, pre-sessions were still being wrongly superseded
// whenever *another* session in the same project dir had a freshly-written
// transcript. scanner.hasActiveSession used to fall back to a project-wide
// jsonl mtime check, which couldn't discriminate which jsonl belonged to
// which PID. The scanner would mark the pre-session superseded and
// SessionDetector.onRemoved would delete it entirely, leaving the user's new
// Claude Code session invisible until they sent their first message.
//
// This test creates a fake claude process, seeds a neighbour .jsonl file
// with a fresh mtime (simulating another active session in the same project
// dir), and verifies the pre-session (a) appears AND (b) is still alive
// after several scanner polls.
func TestPreSession_SurvivesNeighbourSessionActivity(t *testing.T) {
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

	// Create a neighbour .jsonl file with a fresh mtime inside the project
	// dir — this simulates another live Claude Code session writing to its
	// own transcript while our target process has none yet.
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
	// Ensure mtime is "now" so it falls inside the old 60s mtime window.
	now := time.Now()
	_ = os.Chtimes(neighbourJSONL, now, now)

	repo := &memRepo{states: make(map[string]*session.SessionState)}

	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond)
	// Production-equivalent sessionChecker: PID-aware. No non-proc session
	// in the repo has our target PID, so this returns false for every call.
	scanner.WithSessionChecker(func(pd string, pid int) bool {
		sessions, err := repo.ListAll()
		if err != nil {
			return false
		}
		for _, s := range sessions {
			if len(s.SessionID) >= 5 && s.SessionID[:5] == "proc-" {
				continue
			}
			if s.PID != pid {
				continue
			}
			if s.TranscriptPath != "" &&
				filepath.Base(filepath.Dir(s.TranscriptPath)) == pd {
				return true
			}
		}
		return false
	})

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scanner.Watch(ctx)
	go detector.Run(ctx)

	// Pre-session should appear…
	expectedID := fmt.Sprintf("proc-%d", cmd.Process.Pid)
	if !waitForSession(repo, expectedID, 5*time.Second) {
		t.Fatalf("timeout: pre-session %s never appeared (initial detection suppressed?)", expectedID)
	}

	// …and survive several scanner polls despite the neighbour jsonl
	// having a fresh mtime. Poll interval is 200ms, so wait 1.5s → ~7 polls.
	// Continuously refresh the neighbour jsonl's mtime to ensure the old
	// bug's 60s window would always trip.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = os.Chtimes(neighbourJSONL, time.Now(), time.Now())
		time.Sleep(150 * time.Millisecond)
		if state, _ := repo.Load(expectedID); state == nil {
			t.Fatalf("regression: pre-session %s was removed while neighbour jsonl had fresh mtime — scanner's hasActiveSession mtime fallback reintroduced", expectedID)
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

	scanner := processlifecycle.NewScanner(processName, "test", 200*time.Millisecond)
	repo := &memRepo{states: make(map[string]*session.SessionState)}

	detector := services.NewSessionDetector(
		[]inbound.AgentWatcher{scanner},
		nil, repo, &nopLogger{}, &stubGit{}, &stubMetrics{}, nil,
		"test", 0, nil,
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

// --- helpers -----------------------------------------------------------------

// realTempDir returns a temp dir with macOS /var → /private/var symlinks resolved,
// so paths match what lsof reports for process CWDs.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return real
}

func waitForSession(repo *memRepo, id string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return false
		case <-time.After(50 * time.Millisecond):
		}
		if s, _ := repo.Load(id); s != nil {
			return true
		}
	}
}


// --- stubs -------------------------------------------------------------------

type memRepo struct {
	mu     sync.Mutex
	states map[string]*session.SessionState
}

func (r *memRepo) Load(id string) (*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

func (r *memRepo) Save(s *session.SessionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[s.SessionID] = s
	return nil
}

func (r *memRepo) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, id)
	return nil
}

func (r *memRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session.SessionState, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s)
	}
	return out, nil
}

type nopLogger struct{}

func (l *nopLogger) LogInfo(_, _, _ string)                              {}
func (l *nopLogger) LogError(_, _, _ string)                             {}
func (l *nopLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *nopLogger) Close() error                                       { return nil }

type stubGit struct{}

func (g *stubGit) GetBranch(_ string) string               { return "main" }
func (g *stubGit) GetProjectName(dir string) string        { return filepath.Base(dir) }
func (g *stubGit) GetGitRoot(_ string) string              { return "" }
func (g *stubGit) GetBranchFromTranscript(_ string) string { return "" }
func (g *stubGit) GetCWDFromTranscript(_ string) string    { return "" }

type stubMetrics struct{}

func (m *stubMetrics) ComputeMetrics(_, _ string) (*session.SessionMetrics, error) {
	return nil, nil
}

type mockWatcher struct {
	ch chan agent.Event
}

func (w *mockWatcher) Watch(ctx context.Context) error  { <-ctx.Done(); return ctx.Err() }
func (w *mockWatcher) Subscribe() <-chan agent.Event     { return w.ch }
func (w *mockWatcher) Unsubscribe(_ <-chan agent.Event)  {}
