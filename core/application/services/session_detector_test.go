package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// --- tests -------------------------------------------------------------------

func TestSessionDetector_NewSession_CreatesState(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "new1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/new1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, err := repo.Load("new1")
	if err != nil {
		t.Fatalf("session not created: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
	if state.TranscriptPath != "/home/.claude/projects/-Users-test-project/new1.jsonl" {
		t.Errorf("transcript_path: got %q", state.TranscriptPath)
	}
	if state.Confidence != "medium" {
		t.Errorf("confidence: got %q, want medium", state.Confidence)
	}
}

func TestSessionDetector_NewSession_SkipsOrphanTranscript(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	// Create a transcript file with an old mtime (orphan).
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "orphan1.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mtime to 10 minutes ago to exceed orphanTranscriptAge.
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(transcriptPath, old, old); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "orphan1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("orphan1")
	if state != nil {
		t.Errorf("orphan session should not be created, but found state %q", state.State)
	}
}

// TestSessionDetector_NewSession_SkipsWhenCWDDeleted is the regression test
// for issue #321. A long-dead session can have its transcript mtime
// refreshed by `claude --resume` from elsewhere; on a daemon restart within
// the 2-minute staleness window the transcript-mtime check admits a ghost.
// A missing cwd directory is unambiguous: no live process can run there.
func TestSessionDetector_NewSession_SkipsWhenCWDDeleted(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	// Fresh transcript — would normally be admitted.
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "zombie.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// CWD points at a directory that no longer exists.
	missingCWD := filepath.Join(tmpDir, "deleted-worktree")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "zombie1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
		CWD:            missingCWD,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("zombie1")
	if state != nil {
		t.Errorf("zombie session with missing cwd should not be created, got state %q", state.State)
	}
}

// TestSessionDetector_NewSession_SkipsNonInteractiveHost is the regression
// test for issue #784: an adapter that opts into RequireKnownHost (currently
// only antigravity) must never create a session for a process whose ancestry
// doesn't resolve to a known terminal or IDE — e.g. a third-party tool like
// CodexBar keeping the agent's CLI running in the background for quota
// polling, with a real, live, but non-interactive PID.
func TestSessionDetector_NewSession_SkipsNonInteractiveHost(t *testing.T) {
	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "antigravity"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	discovers := map[string]agent.PIDDiscoverFunc{
		"antigravity": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			return 62540, nil // real, live PID — just not launched by a terminal/IDE
		},
	}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)
	det.SetHostGate(map[string]bool{"antigravity": true}, func(pid int) bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "ghost1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.gemini/antigravity-cli/brain/conv/transcript.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if state, _ := repo.Load("ghost1"); state != nil {
		t.Errorf("session bound to a non-interactive host should not be created, got state %q", state.State)
	}
}

// TestSessionDetector_NewSession_AdmitsKnownHost is the companion to
// TestSessionDetector_NewSession_SkipsNonInteractiveHost: a real session
// whose process ancestry resolves to a known terminal/IDE must be admitted
// exactly as before — the #784 gate must not delay or block legitimate
// sessions.
func TestSessionDetector_NewSession_AdmitsKnownHost(t *testing.T) {
	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "antigravity"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	discovers := map[string]agent.PIDDiscoverFunc{
		"antigravity": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			return 4242, nil
		},
	}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)
	det.SetHostGate(map[string]bool{"antigravity": true}, func(pid int) bool { return true })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "real1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.gemini/antigravity-cli/brain/conv/transcript.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if state, err := repo.Load("real1"); err != nil || state == nil {
		t.Fatalf("session bound to a known host should be created: err=%v state=%v", err, state)
	}
}

// TestSessionDetector_NewSession_HostGateResolvesCWDFromTranscript is the
// regression test for the #791 review finding that the host-ancestry gate was
// a structural no-op for antigravity's normal transcript-driven path: fswatcher
// never sets Event.CWD, so DiscoverPID's cwd-based lookup would always
// short-circuit to (0, nil) and AllowsSession would fail open before ever
// attempting an ancestry check. The gate must fall back to the transcript-
// derived cwd (mirroring the existing #576 stale-rescue pattern) so discovery
// actually gets a usable cwd.
func TestSessionDetector_NewSession_HostGateResolvesCWDFromTranscript(t *testing.T) {
	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "antigravity"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const wantCWD = "/Users/ghost/.gemini/antigravity-cli"
	discoverCalls := 0
	discovers := map[string]agent.PIDDiscoverFunc{
		"antigravity": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			discoverCalls++
			if cwd != wantCWD {
				t.Errorf("discover called with cwd %q, want %q (transcript-derived fallback cwd wasn't threaded through)", cwd, wantCWD)
			}
			return 62540, nil
		},
	}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &cwdGit{cwd: wantCWD}, &mockMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)
	det.SetHostGate(map[string]bool{"antigravity": true}, func(pid int) bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "ghost-empty-cwd",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.gemini/antigravity-cli/brain/conv/transcript.jsonl",
		// CWD deliberately left empty — matches production fswatcher events.
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if discoverCalls == 0 {
		t.Fatal("discover was never called — the host gate never attempted PID discovery at all")
	}
	if state, _ := repo.Load("ghost-empty-cwd"); state != nil {
		t.Errorf("session should have been rejected once discovered via the transcript-derived cwd, got state %q", state.State)
	}
}

// TestSessionDetector_NewSession_HostGateRejectionSurvivesIdentityLessRetry is
// the regression test for the #791 review finding that a session rejected by
// the host gate could still be created via the debounce-coalesce path: a
// second activity event within the 2s debounce window is coalesced and
// re-dispatched through processActivityWithoutIdentity, which calls
// onNewSession with an empty Identity — without the hostGateRejected cache,
// AllowsSession("", ...) would no-op on the unrecognized adapter name and
// admit the retry. Drives the real debounce timer (NewSessionDetector panics
// if a watcher itself has no Identity, so there's no way to synthesize this
// via a second watcher — the empty identity only ever arises internally, via
// processActivityWithoutIdentity) rather than a synthetic shortcut.
func TestSessionDetector_NewSession_HostGateRejectionSurvivesIdentityLessRetry(t *testing.T) {
	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "antigravity"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	discovers := map[string]agent.PIDDiscoverFunc{
		"antigravity": func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
			return 62540, nil
		},
	}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &cwdGit{cwd: "/Users/ghost"}, &mockMetrics{}, nil,
		"test", 0, discovers, nil, nil,
	)
	det.SetHostGate(map[string]bool{"antigravity": true}, func(pid int) bool { return false })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	transcriptPath := "/home/.gemini/antigravity-cli/brain/conv/transcript.jsonl"

	// First event: proper identity, rejected by the host gate.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "ghost-retry",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}
	time.Sleep(30 * time.Millisecond)

	// First EventActivity for this session ID: onActivity fires it
	// immediately (with proper identity) and starts the debounce window —
	// state is still nil, so this re-enters onNewSession and is rejected
	// again, this time via the hostGateRejected cache.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "ghost-retry",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}
	time.Sleep(30 * time.Millisecond)

	// Second EventActivity within the debounce window: coalesced, and
	// re-dispatched after activityDebounceWindow via
	// processActivityWithoutIdentity — the empty-identity call this test
	// targets.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "ghost-retry",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}
	// activityDebounceWindow (session_detector.go) is 2s and unexported;
	// this external test package can't reference it directly.
	time.Sleep(2*time.Second + 200*time.Millisecond)
	cancel()
	<-done

	if state, _ := repo.Load("ghost-retry"); state != nil {
		t.Errorf("session rejected by the host gate should stay rejected on a coalesced, identity-less retry, got state %q", state.State)
	}
}

// --- stale-transcript rescue (issue #576) -------------------------------------
//
// When observe consent is granted after agent sessions are already running,
// the backfill sweep sees transcripts idle beyond orphanTranscriptAge whose
// process is still alive. These tests cover the rescue path that creates the
// session anyway when a live process owns the transcript's cwd.

// writeOldTranscript writes a .jsonl transcript whose mtime is age in the past.
func writeOldTranscript(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// liveCWDSet returns a LiveCWDsFunc reporting the given cwds as owned by
// live processes, regardless of binary name.
func liveCWDSet(cwds ...string) services.LiveCWDsFunc {
	return func(string) (map[string]struct{}, error) {
		set := make(map[string]struct{}, len(cwds))
		for _, c := range cwds {
			set[c] = struct{}{}
		}
		return set, nil
	}
}

// claudeProcessNames maps the default test watcher identity to a binary name
// so HasLiveProcessInCWD's processNames lookup succeeds.
func claudeProcessNames() map[string]string {
	return map[string]string{"claude-code": "claude"}
}

// rescueCWD returns an existing directory in OS-canonical form — LiveCWDs
// builds its set from symlink-resolved CWDOf paths, so the test set must be
// keyed the same way (t.TempDir is a symlink on macOS: /var → /private/var).
func rescueCWD(t *testing.T) string {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestSessionDetector_NewSession_RescuesStaleTranscriptWithLiveProcess(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := rescueCWD(t)
	det := newDetectorWithLiveCWDs(tw, pw, repo, nil, claudeProcessNames(), liveCWDSet(cwd))

	transcriptPath := filepath.Join(t.TempDir(), "idle1.jsonl")
	writeOldTranscript(t, transcriptPath, 10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "idle1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, err := repo.Load("idle1")
	if err != nil {
		t.Fatalf("stale transcript with live process should be rescued: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
}

func TestSessionDetector_NewSession_NoRescueWithoutLiveProcess(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := rescueCWD(t)
	det := newDetectorWithLiveCWDs(tw, pw, repo, nil, claudeProcessNames(), liveCWDSet( /* none */ ))

	transcriptPath := filepath.Join(t.TempDir(), "idle2.jsonl")
	writeOldTranscript(t, transcriptPath, 10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "idle2",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if state, _ := repo.Load("idle2"); state != nil {
		t.Errorf("stale transcript without live process should be skipped, got state %q", state.State)
	}
}

// Only the newest transcript in a project directory may be rescued — the
// watcher's initial scan emits every transcript younger than MaxSessionAge,
// and a single live process corresponds to at most the most recent one.
// Older stale siblings rescued alongside it would be ghost sessions.
func TestSessionDetector_NewSession_NoRescueWhenNotNewestInDir(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := rescueCWD(t)
	det := newDetectorWithLiveCWDs(tw, pw, repo, nil, claudeProcessNames(), liveCWDSet(cwd))

	projectDir := t.TempDir()
	olderPath := filepath.Join(projectDir, "older.jsonl")
	writeOldTranscript(t, olderPath, 10*time.Minute)
	newerPath := filepath.Join(projectDir, "newer.jsonl")
	writeOldTranscript(t, newerPath, 5*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "older",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: olderPath,
		CWD:            cwd,
	}
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "newer",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: newerPath,
		CWD:            cwd,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if state, _ := repo.Load("older"); state != nil {
		t.Errorf("older stale sibling should not be rescued, got state %q", state.State)
	}
	if _, err := repo.Load("newer"); err != nil {
		t.Errorf("newest stale transcript with live process should be rescued: %v", err)
	}
}

func TestSessionDetector_NewSession_NoRescueForSubagentTranscript(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := rescueCWD(t)
	det := newDetectorWithLiveCWDs(tw, pw, repo, nil, claudeProcessNames(), liveCWDSet(cwd))

	subagentsDir := filepath.Join(t.TempDir(), "parent-1", "subagents")
	if err := os.MkdirAll(subagentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(subagentsDir, "sub1.jsonl")
	writeOldTranscript(t, transcriptPath, 10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "sub1",
		ProjectDir:     "subagents",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if state, _ := repo.Load("sub1"); state != nil {
		t.Errorf("stale subagent transcript should not be rescued, got state %q", state.State)
	}
}

// In production, fswatcher events carry no CWD — the rescue must fall back to
// extracting the cwd from transcript content (GetCWDFromTranscript).
func TestSessionDetector_NewSession_RescueUsesGetCWDFromTranscript(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	cwd := rescueCWD(t)
	det := newDetectorWithLiveCWDs(tw, pw, repo, &cwdGit{cwd: cwd},
		claudeProcessNames(), liveCWDSet(cwd))

	transcriptPath := filepath.Join(t.TempDir(), "idle3.jsonl")
	writeOldTranscript(t, transcriptPath, 10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "idle3",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
		// CWD intentionally empty — the fswatcher never sets it.
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if _, err := repo.Load("idle3"); err != nil {
		t.Fatalf("rescue should fall back to transcript-derived cwd: %v", err)
	}
}
