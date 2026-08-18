package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestSessionDetector_StopHook_DrivesTheTurnDoneTransition covers what #1695
// measured to be covered by NOTHING: SessionDetector.HandleStopHook's EFFECT.
//
// The gap was total and easy to mistake for coverage. Every `HandleStopHook`
// reference in a `_test.go` before this file is a mock — claudecode's, codex's
// and copilot's `mockTarget` — so the receiving adapters grade the CALL, and
// deleting the `signals.Hold(…, SignalTurnDone, …)` line from the real handler
// left `go test ./core/...` green apart from an unrelated tmux flake, plus the
// whole replay byte-identity gate green. #1695's fix makes the replay half of
// that red for a recording that carries a Stop; this file is the other half,
// and it is the half no golden can reach, because the payload the daemon
// threads in — the turn's final assistant text and the waiting-cue verdict
// computed over the FULL message (#1150) — is not a field of lifecycle.Event
// and so is absent from every recording on disk.
//
// Both arms were seen red against the deletion above. The second is also red
// against a weaker mutation — dropping the payload and keeping the hold, i.e.
// exactly what the hookSignalEffects row alone delivers — which is why
// HandleStopHook stays a dedicated entry point beside that row rather than
// being replaced by it.
func TestSessionDetector_StopHook_DrivesTheTurnDoneTransition(t *testing.T) {
	tests := []struct {
		name string
		// what the hook's payload carries, i.e. what only the daemon has.
		lastAssistantText string
		waitingCue        bool
		want              string
		why               string
	}{
		{
			name:              "plain turn end → ready",
			lastAssistantText: "Done. All tests pass.",
			waitingCue:        false,
			want:              session.StateReady,
			why: "the Stop hook is the authoritative turn-done signal (#1161); " +
				"without its hold the transcript tail alone says the turn is still running",
		},
		{
			name:              "turn ended on a question → waiting",
			lastAssistantText: "I can take either branch — which one should I target?",
			waitingCue:        true,
			want:              session.StateWaiting,
			why: "the cue was computed over the hook's full message (#1150); the " +
				"transcript tail below carries none, so only the payload can route this to waiting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tw := newMockAgentWatcher()
			pw := newMockProcessWatcher()
			repo := newMockRepo()

			const sid = "stop1"
			const path = "/home/.claude/projects/-Users-test/stop1.jsonl"

			// The transcript says the turn is NOT over: "assistant_message" is
			// codex's spelling and is deliberately outside IsAgentDone's
			// "assistant"/"assistant_output" fallback, so nothing here can
			// reach ready on its own. The tail carries no cue either, so the
			// waiting arm cannot be satisfied by prose the classifier already
			// sees. Every state below is therefore the hook's doing.
			metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
				return &session.SessionMetrics{
					LastEventType:     "assistant_message",
					LastAssistantText: "Working on it.",
				}, nil
			}}

			det := newDetectorWithMetrics(tw, pw, repo, metrics)
			repo.states[sid] = &session.SessionState{
				SessionID:      sid,
				State:          session.StateWorking,
				TranscriptPath: path,
				FirstSeen:      time.Now().Unix(),
				UpdatedAt:      time.Now().Unix(),
				Metrics:        &session.SessionMetrics{LastEventType: "assistant_message"},
			}

			// LIFO: registering the drain first makes teardown cancel() then
			// wait for Run to return, which is what reaps the goroutine.
			done := make(chan error, 1)
			defer func() { <-done }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { done <- det.Run(ctx) }()

			time.Sleep(20 * time.Millisecond) // let seedFromDisk finish

			det.HandleStopHook(sid, path, tt.lastAssistantText, tt.waitingCue)
			waitForSessionState(repo, sid, tt.want, 5*time.Second)

			repo.mu.Lock()
			got := repo.lastSavedState[sid]
			repo.mu.Unlock()
			if got != tt.want {
				t.Fatalf("after Stop hook: state = %q, want %q — %s", got, tt.want, tt.why)
			}
		})
	}
}
