package services_test

import (
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// delayedCWDGit fails to resolve a cwd for the first failFor calls to
// GetCWDFromTranscript, then returns cwd — modeling an adapter sidecar (e.g.
// mistral-vibe's meta.json) that's lazily created a few ticks after the
// transcript goes idle (#1021).
type delayedCWDGit struct {
	mockGit
	calls   int
	failFor int
	cwd     string
}

func (g *delayedCWDGit) GetCWDFromTranscript(path string) string {
	g.calls++
	if g.calls <= g.failFor {
		return ""
	}
	return g.cwd
}

// TestSessionDetector_IdleUnresolvedProject_RetriesUntilSidecarAppears
// reproduces #1021: an idle (waiting) session whose ProjectName never
// resolved because the metadata sidecar wasn't readable yet at the moment
// the transcript went quiet. Nothing else revisits an idle session, so
// refreshStaleSessions must keep retrying CWD/project resolution on its own
// ticker until the sidecar appears, instead of leaving the session in the
// "unknown" project group forever.
func TestSessionDetector_IdleUnresolvedProject_RetriesUntilSidecarAppears(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	git := &delayedCWDGit{failFor: 2, cwd: t.TempDir()}
	det := newDetectorWithLiveCWDs(tw, pw, repo, git, nil, nil)

	transcriptPath := filepath.Join(t.TempDir(), "vibe1.jsonl")
	writeOldTranscript(t, transcriptPath, 0)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "vibe1",
		State:          session.StateWaiting,
		Adapter:        "mistral-vibe",
		TranscriptPath: transcriptPath,
		PID:            4242,
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	for range 3 {
		det.RunStaleSessionRefreshForTest()
	}

	state, err := repo.Load("vibe1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if state.ProjectName == "" {
		t.Fatalf("expected ProjectName resolved once the sidecar appears, got %q (cwd=%q)", state.ProjectName, state.CWD)
	}
}

// TestSessionDetector_IdleUnresolvedProject_StopsRetryingAfterCap covers the
// other half of #1021's ask: a session whose cwd is genuinely unresolvable
// (non-git cwd, or an adapter with no sidecar at all) must not be re-read by
// refreshStaleSessions forever. Runs the ticker well past any reasonable
// retry budget and asserts the resolution attempts stop growing.
func TestSessionDetector_IdleUnresolvedProject_StopsRetryingAfterCap(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	git := &delayedCWDGit{failFor: 1000} // never resolves within this test
	det := newDetectorWithLiveCWDs(tw, pw, repo, git, nil, nil)

	transcriptPath := filepath.Join(t.TempDir(), "vibe2.jsonl")
	writeOldTranscript(t, transcriptPath, 0)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      "vibe2",
		State:          session.StateReady,
		Adapter:        "mistral-vibe",
		TranscriptPath: transcriptPath,
		PID:            4242,
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	for range 20 {
		det.RunStaleSessionRefreshForTest()
	}
	callsAfter20 := git.calls
	if callsAfter20 == 0 {
		t.Fatal("expected at least one resolution attempt")
	}

	for range 20 {
		det.RunStaleSessionRefreshForTest()
	}
	if git.calls != callsAfter20 {
		t.Fatalf("expected retries to stop after a bounded cap, got %d calls after 20 ticks then %d after 40", callsAfter20, git.calls)
	}

	if state, _ := repo.Load("vibe2"); state.ProjectName != "" {
		t.Fatalf("expected ProjectName to remain unresolved, got %q", state.ProjectName)
	}
}
