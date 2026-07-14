package services_test

import (
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// newIdleUnresolvedSession persists an idle session fixture with an
// unresolved ProjectName — the #1021 precondition (a metadata sidecar, e.g.
// mistral-vibe's meta.json, wasn't readable yet when the transcript went
// quiet). Shared by the retry-cap tests below.
func newIdleUnresolvedSession(t *testing.T, repo *mockRepo, sessionID, state string) {
	t.Helper()
	transcriptPath := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeOldTranscript(t, transcriptPath, 0)
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      sessionID,
		State:          state,
		Adapter:        "mistral-vibe",
		TranscriptPath: transcriptPath,
		PID:            4242,
		FirstSeen:      now,
		UpdatedAt:      now,
	})
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

	git := &cwdGit{cwd: t.TempDir(), failFor: 2}
	det := newDetectorWithLiveCWDs(tw, pw, repo, git, nil, nil)
	newIdleUnresolvedSession(t, repo, "vibe1", session.StateWaiting)

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

	git := &cwdGit{failFor: 1000} // never resolves within this test
	det := newDetectorWithLiveCWDs(tw, pw, repo, git, nil, nil)
	newIdleUnresolvedSession(t, repo, "vibe2", session.StateReady)

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
