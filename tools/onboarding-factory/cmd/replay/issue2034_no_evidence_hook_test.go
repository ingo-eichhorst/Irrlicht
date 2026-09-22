package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TestApplyHookEvent_NoTranscriptEvidence_KeepsState is the replay half of
// #2034. The sidecar replay mirrors the daemon's hook pass: applyHookEvent
// re-classifies the last metrics with NoSubstantiveActivity forced false. For
// a session whose transcript holds only skipped lines (Claude Code's
// post-/clear wrappers), that pass reached the ladder's default rung and
// recorded ready → working on no evidence — the same verdict the live daemon
// recorded on session fd0a7643.
//
// The live trigger was Notification/idle_prompt, which the replay does not
// model (session.HookSignal has no row for it). PostToolUse is a hook the
// replay does model and that reaches the same classify pass, so it stands in.
func TestApplyHookEvent_NoTranscriptEvidence_KeepsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := `{"type":"user","isMeta":true,"timestamp":"2026-09-22T20:46:53.168Z","message":{"role":"user","content":"<local-command-caveat>Caveat</local-command-caveat>"}}
{"type":"user","timestamp":"2026-09-22T20:46:53.043Z","message":{"role":"user","content":"<command-name>/clear</command-name>"}}
{"type":"system","subtype":"local_command","timestamp":"2026-09-22T20:46:53.167Z","content":"<local-command-stdout></local-command-stdout>"}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	tt := tailer.NewTranscriptTailer(path, &claudecode.Parser{}, claudecode.AdapterName)
	m, err := tt.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.LastEventType != "" {
		t.Fatalf("precondition failed: LastEventType = %q, want empty — the case needs a transcript with no substantive event", m.LastEventType)
	}

	at := time.Date(2026, 9, 22, 20, 47, 53, 0, time.UTC)
	r := &sidecarReplayer{
		tailer:           tt,
		lastMetrics:      m,
		state:            session.StateReady,
		prevTransitionAt: at.Add(-time.Minute),
		stateDurations:   map[string]time.Duration{},
		signals:          session.NewSignalHolds(),
		report:           &replayReport{},
	}
	r.applyHookEvent(lifecycle.Event{HookName: session.HookPostToolUse, Timestamp: at})

	if r.state != session.StateReady {
		t.Errorf("state after a hook pass with no transcript evidence = %q, want ready", r.state)
	}
	if n := len(r.report.Transitions); n != 0 {
		t.Errorf("recorded %d transitions, want 0: %+v", n, r.report.Transitions)
	}
}
