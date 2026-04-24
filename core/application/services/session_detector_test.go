package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
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

func TestSessionDetector_Activity_TransitionsToWaiting_WhenToolUseOpen(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["act1"] = &session.SessionState{
		SessionID:      "act1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/act1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     1,
		Metrics: &session.SessionMetrics{
			LastEventType:     "tool_use",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"AskUserQuestion"},
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "act1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/act1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("act1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (open tool call blocks on user)", state.State)
	}
}

// TestSessionDetector_Activity_SamePassUserBlocking_EmitsSyntheticWaiting
// is the regression test for issue #150. When the tailer sees an
// AskUserQuestion / ExitPlanMode tool_use and its matching tool_result
// in a single pass (fswatcher coalesced the writes), HasOpenToolCall
// is already false by the time the classifier runs and the brief
// waiting episode is invisible. The daemon must emit a synthetic
// working→waiting so observers (UI, replay) see the collapsed window.
func TestSessionDetector_Activity_SamePassUserBlocking_EmitsSyntheticWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	rec := &mockRecorder{}
	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Let seedFromDisk complete before injecting the session — otherwise
	// seedFromDisk's own re-evaluation would apply rule 3 and transition
	// the session to ready before our activity event arrives.
	time.Sleep(20 * time.Millisecond)

	// Metrics as if the tailer just processed tool_use(AskUserQuestion) +
	// tool_result(is_error=true) + denial text in one pass. Denial flag is
	// still set (the user text "[Request interrupted by user for tool use]"
	// was the last user event in the batch), so the classifier's rule 3
	// would return ready and skip waiting without the synthetic emit.
	repo.Save(&session.SessionState{
		SessionID:      "pass1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/pass1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     1,
		Metrics: &session.SessionMetrics{
			LastEventType:                     "user",
			HasOpenToolCall:                   false,
			LastWasToolDenial:                 true,
			SawUserBlockingToolClosedThisPass: true,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "pass1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/pass1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("pass1")
	if state.State != session.StateReady {
		t.Errorf("final state: got %q, want ready (classifier's original ruling after synth)", state.State)
	}

	// Assert the lifecycle recorder saw a working→waiting→ready pair,
	// not a direct working→ready.
	var prevs, news []string
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindStateTransition {
			prevs = append(prevs, ev.PrevState)
			news = append(news, ev.NewState)
		}
	}
	wantPrevs := []string{session.StateWorking, session.StateWaiting}
	wantNews := []string{session.StateWaiting, session.StateReady}
	if len(prevs) != len(wantPrevs) {
		t.Fatalf("state transitions: got %d (%v→%v), want %d (%v→%v)",
			len(prevs), prevs, news, len(wantPrevs), wantPrevs, wantNews)
	}
	for i := range prevs {
		if prevs[i] != wantPrevs[i] || news[i] != wantNews[i] {
			t.Errorf("transition %d: got %s→%s, want %s→%s",
				i, prevs[i], news[i], wantPrevs[i], wantNews[i])
		}
	}
}

// TestSessionDetector_Activity_SamePassUserBlocking_RespectsParentHold
// guards the parent-hold invariant against the same-pass synthesis path
// from issue #150. A parent with active children must stay working even
// when its own metrics would otherwise classify ready (rule 3 denial).
// Without the parentHeldWorking guard, the synth path would flip the
// parent to waiting, reclassify, and let rule 3 fire → parent goes to
// ready despite the child still running. This test locks down the fix.
func TestSessionDetector_Activity_SamePassUserBlocking_RespectsParentHold(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	rec := &mockRecorder{}
	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Let seedFromDisk complete before injecting sessions — otherwise
	// it re-evaluates them and may transition the parent before our
	// activity event arrives.
	time.Sleep(20 * time.Millisecond)

	// Active child: keeps the parent held working. HasOpenToolCall=true
	// makes finishOrphanedChildren skip it without needing a real
	// transcript file.
	repo.Save(&session.SessionState{
		SessionID:       "child1",
		ParentSessionID: "parentA",
		State:           session.StateWorking,
		TranscriptPath:  "/home/.claude/projects/-Users-test/child1.jsonl",
		FirstSeen:       time.Now().Unix(),
		UpdatedAt:       time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant",
			HasOpenToolCall: true,
		},
	})

	// Parent session: metrics identical to
	// TestSessionDetector_Activity_SamePassUserBlocking_EmitsSyntheticWaiting
	// — same-pass collapse of AskUserQuestion with a sticky denial marker.
	// Classifier rule 3 wants to return ready; parent-hold must veto that
	// and the synth path must not fire.
	repo.Save(&session.SessionState{
		SessionID:      "parentA",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parentA.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     1,
		Metrics: &session.SessionMetrics{
			LastEventType:                     "user",
			HasOpenToolCall:                   false,
			LastWasToolDenial:                 true,
			SawUserBlockingToolClosedThisPass: true,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parentA",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parentA.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	parent, _ := repo.Load("parentA")
	if parent.State != session.StateWorking {
		t.Errorf("parent final state: got %q, want working (child still active)", parent.State)
	}

	// No lifecycle transition on the parent should mention the synthetic
	// reason. The parent should stay in working throughout this event.
	for _, ev := range rec.snapshot() {
		if ev.Kind != lifecycle.KindStateTransition || ev.SessionID != "parentA" {
			continue
		}
		if ev.Reason == services.SyntheticWaitingReason {
			t.Errorf("synthesis fired on held parent: %+v", ev)
		}
		if ev.NewState == session.StateWaiting || ev.NewState == session.StateReady {
			t.Errorf("parent transitioned to %q while child still active: %+v", ev.NewState, ev)
		}
	}
}

func TestSessionDetector_Activity_TransitionsToReady_WhenAgentDone(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["wait1"] = &session.SessionState{
		SessionID:      "wait1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/wait1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "turn_done",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "wait1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/wait1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("wait1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (turn_done signal, no open tools)", state.State)
	}
}

func TestSessionDetector_Activity_StaysWorking_WhenAssistantStreaming(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Mid-turn: Claude Code streaming message (no stop_reason) emits
	// "assistant_streaming" which should NOT trigger IsAgentDone().
	repo.states["nosys1"] = &session.SessionState{
		SessionID:      "nosys1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/nosys1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "nosys1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/nosys1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("nosys1")
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (assistant_streaming should not trigger ready)", state.State)
	}
}

func TestSessionDetector_Activity_TransitionsToWaiting_WhenAssistantButOpenTools(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["otc1"] = &session.SessionState{
		SessionID:      "otc1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/otc1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"ExitPlanMode"},
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "otc1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/otc1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("otc1")
	if state.State != session.StateWaiting {
		t.Errorf("state: got %q, want waiting (open tool call blocks on user)", state.State)
	}
}

func TestSessionDetector_Activity_CancellationFromWorking_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk to complete, then inject the session.
	// This avoids seedFromDisk re-evaluating the state before onActivity.
	time.Sleep(20 * time.Millisecond)

	// Simulate post-ESC state: session was working, user cancelled via ESC.
	// Claude Code writes "[Request interrupted by user]" as the text content
	// of a user event — the parser flags this as LastWasUserInterrupt. Tool
	// result errors alone are NOT enough (issue #102 Bug B), and tool
	// denials ("[Request interrupted by user for tool use]") set a separate
	// LastWasToolDenial flag that the cancellation rule does NOT consume.
	repo.Save(&session.SessionState{
		SessionID:      "esc1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/esc1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:        "user",
			HasOpenToolCall:      false,
			LastWasUserInterrupt: true,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "esc1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/esc1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("esc1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (ESC cancellation: user event while working, no open tools)", state.State)
	}
}

func TestSessionDetector_Activity_CancellationFromWaiting_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk to complete, then inject the session.
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:        "wake1",
		State:            session.StateWaiting,
		TranscriptPath:   "/home/.claude/projects/-Users-test/wake1.jsonl",
		FirstSeen:        now,
		UpdatedAt:        now,
		WaitingStartTime: &now,
		EventCount:       3,
		Metrics: &session.SessionMetrics{
			LastEventType:        "user",
			HasOpenToolCall:      false,
			LastWasUserInterrupt: true,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "wake1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/wake1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("wake1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (ESC from permission prompt)", state.State)
	}
}

func TestSessionDetector_Activity_NormalToolCompletion_StaysWorking(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	// Simulate mid-turn state: last tool_result completed normally
	// (is_error=false). Agent is still working between tool calls.
	repo.Save(&session.SessionState{
		SessionID:      "fp1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/fp1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "user",
			HasOpenToolCall: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "fp1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/fp1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("fp1")
	if state.State != session.StateWorking {
		t.Errorf("state: got %q, want working (normal tool completion should not transition to ready)", state.State)
	}
}

func TestSessionDetector_Removed_TransitionsToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["rm1"] = &session.SessionState{
		SessionID:      "rm1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/rm1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:      agent.EventRemoved,
		SessionID: "rm1",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("rm1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
}

func TestSessionDetector_Removed_SkipsTerminalState(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["rm2"] = &session.SessionState{
		SessionID:      "rm2",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/rm2.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:      agent.EventRemoved,
		SessionID: "rm2",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("rm2")
	if state.State != session.StateReady {
		t.Errorf("state should remain ready, got %q", state.State)
	}
}

func TestSessionDetector_ExistingSession_UpdatesTranscriptPath(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["hook1"] = &session.SessionState{
		SessionID: "hook1",
		State:     session.StateWorking,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "hook1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/hook1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, _ := repo.Load("hook1")
	if state.TranscriptPath != "/home/.claude/projects/-Users-test/hook1.jsonl" {
		t.Errorf("transcript_path should be updated, got %q", state.TranscriptPath)
	}
}

func TestSessionDetector_ContextCancel_StopsGracefully(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	cancel()
	err := <-done

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSessionDetector_Activity_UnknownSession_TreatedAsNew(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "unknown1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/unknown1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	state, err := repo.Load("unknown1")
	if err != nil {
		t.Fatalf("session should have been created: %v", err)
	}
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready", state.State)
	}
}

func TestSessionDetector_HandleProcessExit_DeletesSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["exit1"] = &session.SessionState{
		SessionID: "exit1",
		State:     session.StateWorking,
		PID:       12345,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(12345, "exit1")

	state, _ := repo.Load("exit1")
	if state != nil {
		t.Errorf("session should be deleted, but still exists with state %q", state.State)
	}
}

func TestSessionDetector_HandleProcessExit_DeletesReadySession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["exit2"] = &session.SessionState{
		SessionID: "exit2",
		State:     session.StateReady,
		PID:       12345,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(12345, "exit2")

	state, _ := repo.Load("exit2")
	if state != nil {
		t.Errorf("ready session should be deleted on process exit, but still exists")
	}
}

func TestSessionDetector_ContinueSession_RecreatableAfterProcessExit(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Session exists with a PID.
	repo.states["cont1"] = &session.SessionState{
		SessionID:      "cont1",
		State:          session.StateWorking,
		PID:            12345,
		TranscriptPath: "/tmp/test-cont1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)
	det.SetDeletedCooldown(0) // allow immediate re-creation

	// Process exits — session is deleted and added to deletedSessions.
	det.HandleProcessExit(12345, "cont1")

	state, _ := repo.Load("cont1")
	if state != nil {
		t.Fatal("session should be deleted after process exit")
	}

	// Start the event loop.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // wait for seedFromDisk

	// Create a fresh transcript file (simulating --continue writing to it).
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "cont1.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Activity event for the deleted session with a fresh transcript.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "cont1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Session should be re-created (--continue with fresh transcript).
	state, err := repo.Load("cont1")
	if err != nil || state == nil {
		t.Fatal("session should be re-created after --continue (fresh transcript)")
	}
	if state.TranscriptPath != transcriptPath {
		t.Errorf("transcript_path: got %q, want %q", state.TranscriptPath, transcriptPath)
	}
}

func TestSessionDetector_LateWriteAfterQuit_NoGhostSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	repo.states["ghost1"] = &session.SessionState{
		SessionID:      "ghost1",
		State:          session.StateWorking,
		PID:            12345,
		TranscriptPath: "/tmp/test-ghost1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)
	// Keep default 10s cooldown — late writes happen within milliseconds.

	// Process exits — session deleted.
	det.HandleProcessExit(12345, "ghost1")

	state, _ := repo.Load("ghost1")
	if state != nil {
		t.Fatal("session should be deleted after process exit")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	// Late-arriving write from the dying process (within cooldown).
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "ghost1.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"assistant"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "ghost1",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: transcriptPath,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Session should NOT be re-created — still within cooldown.
	state, _ = repo.Load("ghost1")
	if state != nil {
		t.Error("session should NOT be re-created from late writes after quit (within cooldown)")
	}
}

func TestSessionDetector_HandleProcessExit_UnknownSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	// Should not panic for unknown session.
	det.HandleProcessExit(99999, "nonexistent")
}

// TestSessionDetector_SeedFromDisk_PersistsRefreshedMetrics is a regression
// test for irrlicht-qha: after PR #110 fixed the codex parser to read the
// per-turn last_token_usage, persisted sessions from the pre-fix daemon
// still served the stale cumulative count. seedFromDisk called RefreshMetrics
// and mutated the in-memory state, but only Save()'d when the classified
// state transitioned — so idle ready sessions kept the bad numbers on disk
// indefinitely. The fix is to persist after RefreshMetrics regardless of
// whether the state changed.
func TestSessionDetector_SeedFromDisk_PersistsRefreshedMetrics(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete the session as dead.
	myPID := os.Getpid()

	// Stale persisted state: cumulative token count from the buggy daemon,
	// already-ready state (so the state transition path would not fire).
	repo.states["rollout-stale"] = &session.SessionState{
		SessionID:      "rollout-stale",
		State:          session.StateReady,
		Adapter:        "codex",
		PID:            myPID,
		TranscriptPath: "/tmp/rollout-stale.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			TotalTokens:        2282896, // stale cumulative
			ContextWindow:      258400,
			ContextUtilization: 883.47,
			ModelName:          "gpt-5.4",
			PressureLevel:      "critical",
		},
	}

	// Fresh tailer output: per-turn snapshot. seedFromDisk should merge this
	// into state.Metrics and then persist, overwriting the stale cumulative.
	freshMetrics := &session.SessionMetrics{
		TotalTokens:        123496,
		ContextWindow:      258400,
		ContextUtilization: 47.79,
		ModelName:          "gpt-5.4",
		PressureLevel:      "safe",
	}
	metrics := &funcMetrics{
		fn: func(path, adapter string) (*session.SessionMetrics, error) {
			if path == "/tmp/rollout-stale.jsonl" {
				// Return a fresh copy so the detector can mutate without
				// affecting subsequent calls.
				cp := *freshMetrics
				return &cp, nil
			}
			return nil, nil
		},
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	// Record the save count before Run: seedFromDisk must call Save() for
	// this session even though its classified state (ready) is unchanged,
	// otherwise the refreshed metrics would never reach disk. In the real
	// filesystem repo, an un-saved in-memory mutation is lost because Load
	// deep-copies from disk; the mockRepo hands back the same pointer, so
	// we assert on the Save call count, not the loaded state.
	repo.mu.Lock()
	savesBefore := repo.saves
	repo.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	repo.mu.Lock()
	savesAfter := repo.saves
	repo.mu.Unlock()
	if savesAfter <= savesBefore {
		t.Errorf("expected Save() to be called during seedFromDisk after "+
			"RefreshMetrics, but saves count did not increase (before=%d after=%d)",
			savesBefore, savesAfter)
	}

	state, err := repo.Load("rollout-stale")
	if err != nil || state == nil {
		t.Fatalf("rollout-stale should still exist after seed: err=%v state=%v", err, state)
	}
	if state.Metrics == nil {
		t.Fatal("state.Metrics is nil after seed")
	}
	if state.Metrics.TotalTokens != 123496 {
		t.Errorf("TotalTokens = %d, want 123496 (fresh). Stale cumulative leak?",
			state.Metrics.TotalTokens)
	}
	if state.Metrics.ContextUtilization < 40 || state.Metrics.ContextUtilization > 55 {
		t.Errorf("ContextUtilization = %.2f, want ~47.79", state.Metrics.ContextUtilization)
	}
}

func TestSessionDetector_SeedFromDisk_DeletesDeadPIDs(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// PIDs 42 and 99 don't exist as real processes, so syscall.Kill(pid, 0)
	// returns ESRCH. All sessions with dead PIDs should be deleted.
	repo.states["seed1"] = &session.SessionState{
		SessionID:      "seed1",
		State:          session.StateWorking,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/seed1.jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}
	repo.states["seed2"] = &session.SessionState{
		SessionID: "seed2",
		State:     session.StateReady,
		PID:       99,
		FirstSeen: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// seed1 has a dead PID — should be deleted.
	if state, _ := repo.Load("seed1"); state != nil {
		t.Error("seed1 should be deleted (PID 42 is dead)")
	}
	// seed2 has a dead PID — should be deleted.
	if state, _ := repo.Load("seed2"); state != nil {
		t.Error("seed2 should be deleted (PID 99 is dead)")
	}

	// Dead PIDs should NOT be registered with ProcessWatcher.
	if _, ok := pw.watched[42]; ok {
		t.Error("PID 42 should not be watched (dead process)")
	}
	if _, ok := pw.watched[99]; ok {
		t.Error("PID 99 should not be watched (dead process)")
	}
}

func TestNeedsUserAttention(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"no open tools", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: false}, false},
		{"open tool call (Bash)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}, false},
		{"open tool call (Write)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Write"}}, false},
		{"open tool call (Agent)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Agent"}}, false},
		{"open tool call (mcp__tool)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"mcp__claude-in-chrome__navigate"}}, false},
		{"open tool call (AskUserQuestion)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"AskUserQuestion"}}, true},
		{"open tool call (ExitPlanMode)", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"ExitPlanMode"}}, true},
		{"mixed tools with AskUserQuestion", &session.SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash", "AskUserQuestion"}}, true},
		{"open tool call, no names", &session.SessionMetrics{HasOpenToolCall: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metrics.NeedsUserAttention()
			if got != tt.want {
				t.Errorf("NeedsUserAttention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionDetector_PIDAssigned_CleansUpOldSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Old session: real transcript session with known PID (previous /clear victim).
	repo.states["old-session"] = &session.SessionState{
		SessionID:      "old-session",
		State:          session.StateReady,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/old-session.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// New session: just created after /clear, PID not yet discovered.
	repo.states["new-session"] = &session.SessionState{
		SessionID:      "new-session",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/new-session.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// Simulate PID discovery for the new session — same PID as old session.
	det.HandlePIDAssigned(42, "new-session")

	// Old session should be deleted (replaced by /clear).
	if state, _ := repo.Load("old-session"); state != nil {
		t.Errorf("old session should be deleted, but still exists with state %q", state.State)
	}

	// New session should have PID assigned.
	newState, _ := repo.Load("new-session")
	if newState == nil {
		t.Fatal("new session should exist")
	}
	if newState.PID != 42 {
		t.Errorf("new session PID: got %d, want 42", newState.PID)
	}

	// ProcessWatcher should track the PID for the new session.
	if pw.watched[42] != "new-session" {
		t.Errorf("ProcessWatcher: got %q for PID 42, want new-session", pw.watched[42])
	}
}

func TestSessionDetector_PIDAssigned_CapturesLauncher(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()
	repo.states["s"] = &session.SessionState{
		SessionID: "s",
		State:     session.StateWorking,
		FirstSeen: now,
		UpdatedAt: now,
	}

	det := newDetector(tw, pw, repo)
	var calledPID int
	det.SetLauncherEnvReader(func(pid int) *session.Launcher {
		calledPID = pid
		return &session.Launcher{
			TermProgram:    "iTerm.app",
			ITermSessionID: "w0t0p0",
		}
	})

	det.HandlePIDAssigned(4242, "s")

	if calledPID != 4242 {
		t.Errorf("launcherEnvReader pid: got %d, want 4242", calledPID)
	}
	state, _ := repo.Load("s")
	if state == nil || state.Launcher == nil {
		t.Fatal("expected Launcher to be set after HandlePIDAssigned")
	}
	if state.Launcher.TermProgram != "iTerm.app" {
		t.Errorf("Launcher.TermProgram: got %q, want iTerm.app", state.Launcher.TermProgram)
	}

	// Subsequent PID assignment with the same PID must not clobber — the
	// guard `state.PID == pid` bails before we re-read env, so the reader
	// should not even be invoked again.
	calledPID = 0
	det.HandlePIDAssigned(4242, "s")
	if calledPID != 0 {
		t.Errorf("launcherEnvReader invoked again for same PID: got %d", calledPID)
	}
}

func TestSessionDetector_PIDAssigned_SkipsSubagents(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Parent session with known PID.
	repo.states["parent"] = &session.SessionState{
		SessionID:      "parent",
		State:          session.StateWorking,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// Subagent session — shares parent's PID but has ParentSessionID set.
	repo.states["subagent"] = &session.SessionState{
		SessionID:       "subagent",
		State:           session.StateWorking,
		ParentSessionID: "parent",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent/subagents/subagent.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
	}

	det := newDetector(tw, pw, repo)

	// Assign same PID to subagent — should NOT delete parent.
	det.HandlePIDAssigned(42, "subagent")

	if state, _ := repo.Load("parent"); state == nil {
		t.Error("parent session should NOT be deleted when subagent gets same PID")
	}
}

func TestSessionDetector_CWDFallback_DoesNotAssignDuplicatePID(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Mock CWD discovery: always returns the same two candidate PIDs.
	cwdFn := func(cwd string, disambiguate func([]int) int) (int, error) {
		return disambiguate([]int{1000, 1001}), nil
	}

	det := newDetectorWithCWDDiscovery(tw, pw, repo, cwdFn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Send two new sessions in the same project (same CWD).
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		Adapter:        "claude-code",
		SessionID:      "sess-a",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/sess-a.jsonl",
		CWD:            "/Users/test/project",
	}

	// Wait for first session's PID discovery retry goroutine to complete.
	time.Sleep(200 * time.Millisecond)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		Adapter:        "claude-code",
		SessionID:      "sess-b",
		ProjectDir:     "-Users-test-project",
		TranscriptPath: "/home/.claude/projects/-Users-test-project/sess-b.jsonl",
		CWD:            "/Users/test/project",
	}

	// Wait for second session's PID discovery.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	stateA, _ := repo.Load("sess-a")
	stateB, _ := repo.Load("sess-b")

	if stateA == nil {
		t.Fatal("sess-a should still exist (must not be deleted by sess-b's PID assignment)")
	}
	if stateB == nil {
		t.Fatal("sess-b should exist")
	}

	// Both sessions should exist and have different PIDs.
	if stateA.PID == stateB.PID {
		t.Errorf("sessions should have different PIDs, both got %d", stateA.PID)
	}
	if stateA.PID != 1001 {
		t.Errorf("sess-a PID: got %d, want 1001 (highest unclaimed)", stateA.PID)
	}
	if stateB.PID != 1000 {
		t.Errorf("sess-b PID: got %d, want 1000 (1001 already claimed)", stateB.PID)
	}
}

func TestSessionDetector_CWDFallback_CleansUpOldSessionOnClear(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete sess-a as a dead process.
	myPID := os.Getpid()

	// Mock CWD discovery returns only our PID — simulates the /clear scenario
	// where the same process starts a new transcript. The new session should
	// claim the PID and clean up the old session.
	cwdFn := func(cwd string, disambiguate func([]int) int) (int, error) {
		return disambiguate([]int{myPID}), nil
	}

	det := newDetectorWithCWDDiscovery(tw, pw, repo, cwdFn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk, then inject sessions.
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Session A already has a PID assigned (discovered earlier).
	repo.Save(&session.SessionState{
		SessionID:      "sess-a",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            myPID,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-a.jsonl",
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Session B has no PID yet (new transcript after /clear).
	repo.Save(&session.SessionState{
		SessionID:      "sess-b",
		Adapter:        "claude-code",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Trigger activity on sess-b to initiate PID discovery.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "sess-b",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-b.jsonl",
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// sess-a should be deleted — replaced by sess-b (same PID, /clear).
	stateA, _ := repo.Load("sess-a")
	if stateA != nil {
		t.Error("sess-a should be deleted (replaced by sess-b with same PID)")
	}

	// sess-b should have the PID.
	stateB, _ := repo.Load("sess-b")
	if stateB == nil {
		t.Fatal("sess-b should exist")
	}
	if stateB.PID != myPID {
		t.Errorf("sess-b PID: got %d, want %d", stateB.PID, myPID)
	}
}

// TestSessionDetector_ClearWithStaleMetadata_DeletesOldSessionImmediately is the
// end-to-end regression for #169. It drives the real claudecode.DiscoverPID
// with a stale ~/.claude/sessions/<pid>.json pointing at the old session and
// asserts the full pipeline — DiscoverPIDWithRetry → HandlePIDAssigned →
// same-PID cleanup — deletes the old session within the retry window.
func TestSessionDetector_ClearWithStaleMetadata_DeletesOldSessionImmediately(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// Use our own PID so seedFromDisk doesn't delete sess-old as a dead process.
	myPID := os.Getpid()

	// Install a fake sessionsDir with a stale metadata file that points at
	// the OLD sessionId — simulating Claude's post-/clear behaviour where
	// <pid>.json lingers on the previous session for up to ~2 min.
	sessionsDir := t.TempDir()
	if err := claudecode.WriteSessionMetaForTest(sessionsDir, myPID, "sess-old", time.Now().Add(-30*time.Second)); err != nil {
		t.Fatalf("write stale meta: %v", err)
	}

	// Real transcript file so DiscoverPID can stat its mtime (fresh, > stale
	// metadata + staleMetaSlack). Without a real file, the mtime gate would
	// be inert and current negative-filter behaviour would keep applying.
	transcriptDir := t.TempDir()
	newTranscript := filepath.Join(transcriptDir, "sess-new.jsonl")
	if err := os.WriteFile(newTranscript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	restore := claudecode.ReplaceTestDeps(
		sessionsDir,
		func(pid int) bool { return pid == myPID },
		func(_ string, _ string, disambiguate func([]int) int) (int, error) {
			return disambiguate([]int{myPID}), nil
		},
	)
	defer restore()

	discovers := map[string]services.PIDDiscoverFunc{
		"claude-code": claudecode.DiscoverPID,
	}
	det := services.NewSessionDetector(
		[]inbound.AgentWatcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 0, discovers,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// Wait for seedFromDisk before injecting state.
	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Old session from before /clear — holds the live PID.
	repo.Save(&session.SessionState{
		SessionID:      "sess-old",
		Adapter:        "claude-code",
		State:          session.StateWorking,
		PID:            myPID,
		TranscriptPath: "/home/.claude/projects/-Users-test/sess-old.jsonl",
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// New session from after /clear — PID not yet discovered, fresh transcript.
	repo.Save(&session.SessionState{
		SessionID:      "sess-new",
		Adapter:        "claude-code",
		State:          session.StateReady,
		TranscriptPath: newTranscript,
		CWD:            "/Users/test/project",
		FirstSeen:      now,
		UpdatedAt:      now,
	})

	// Activity on sess-new triggers PID discovery. The real DiscoverPID must
	// see the stale metadata, skip the negative filter (mtime gate), return
	// myPID via the CWD stub, and fire HandlePIDAssigned's same-PID cleanup.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "sess-new",
		ProjectDir:     "-Users-test",
		TranscriptPath: newTranscript,
	}

	// 300ms covers the immediate synchronous discovery attempt; retries at
	// 500ms/1s/2s aren't needed — DiscoverPID must succeed on the first try
	// once the mtime gate lets it past the stale metadata.
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	if stateOld, _ := repo.Load("sess-old"); stateOld != nil {
		t.Error("sess-old should be deleted (stale metadata must not block /clear cleanup)")
	}
	stateNew, _ := repo.Load("sess-new")
	if stateNew == nil {
		t.Fatal("sess-new should exist")
	}
	if stateNew.PID != myPID {
		t.Errorf("sess-new PID: got %d, want %d", stateNew.PID, myPID)
	}
}

func TestSessionDetector_HandlePIDAssigned_CleansUpOldSession(t *testing.T) {
	// Verify that HandlePIDAssigned cleans up old sessions with the same PID
	// (the /clear scenario).
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	now := time.Now().Unix()

	// Old session with PID 42 (from before /clear).
	repo.states["old"] = &session.SessionState{
		SessionID:      "old",
		State:          session.StateReady,
		PID:            42,
		TranscriptPath: "/home/.claude/projects/-Users-test/old.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	// New session after /clear, PID not yet discovered.
	repo.states["new"] = &session.SessionState{
		SessionID:      "new",
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/new.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
	}

	det := newDetector(tw, pw, repo)

	// PID assignment should clean up old session.
	det.HandlePIDAssigned(42, "new")

	if state, _ := repo.Load("old"); state != nil {
		t.Error("old session should be deleted by /clear cleanup")
	}
	newState, _ := repo.Load("new")
	if newState == nil {
		t.Fatal("new session should exist")
	}
	if newState.PID != 42 {
		t.Errorf("new session PID: got %d, want 42", newState.PID)
	}
}

func TestIsAgentDone(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"turn_done", &session.SessionMetrics{LastEventType: "turn_done"}, true},
		{"turn_done, open tools (subagent running)", &session.SessionMetrics{LastEventType: "turn_done", HasOpenToolCall: true}, false},
		{"assistant with stop_reason (end_turn)", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: false}, true},
		{"assistant_message, no open tools (Codex preliminary msg — NOT done)", &session.SessionMetrics{LastEventType: "assistant_message", HasOpenToolCall: false}, false},
		{"assistant_output, no open tools (Codex)", &session.SessionMetrics{LastEventType: "assistant_output", HasOpenToolCall: false}, true},
		{"assistant_streaming (no stop_reason — NOT done)", &session.SessionMetrics{LastEventType: "assistant_streaming", HasOpenToolCall: false}, false},
		{"assistant, open tools", &session.SessionMetrics{LastEventType: "assistant", HasOpenToolCall: true}, false},
		{"user, no open tools", &session.SessionMetrics{LastEventType: "user", HasOpenToolCall: false}, false},
		{"empty", &session.SessionMetrics{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metrics.IsAgentDone()
			if got != tt.want {
				t.Errorf("IsAgentDone() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- parent-child state propagation tests ------------------------------------

func TestSessionDetector_ParentHeldWorking_WhenChildrenActive(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Parent session: turn is done, no open tool calls.
	// Without children this would transition to ready.
	repo.Save(&session.SessionState{
		SessionID:      "parent1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "Done.",
		},
	})

	// Child session: still working.
	repo.Save(&session.SessionState{
		SessionID:       "child1",
		State:           session.StateWorking,
		ParentSessionID: "parent1",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent1/subagents/child1.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"Bash"},
		},
	})

	// Trigger parent activity — should be held in working because child is active.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	state, _ := repo.Load("parent1")
	if state.State != session.StateWorking {
		t.Errorf("parent state: got %q, want working (child still active)", state.State)
	}

	cancel()
	<-done
}

func TestSessionDetector_ParentReleasedToReady_WhenChildFinishes(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Parent: turn done, held in working because of child.
	repo.Save(&session.SessionState{
		SessionID:      "parent2",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "Done.",
		},
	})

	// Child: still working.
	repo.Save(&session.SessionState{
		SessionID:       "child2",
		State:           session.StateWorking,
		ParentSessionID: "parent2",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent2/subagents/child2.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "turn_done",
			HasOpenToolCall: false,
		},
	})

	// Trigger child activity — child now has turn_done, transitions to ready.
	// This should trigger parent re-evaluation → parent also goes ready.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "child2",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent2/subagents/child2.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	parent, _ := repo.Load("parent2")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateReady {
		t.Errorf("parent state: got %q, want ready (child finished, parent turn was done)", parent.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_OrphanedSubagentsFinishWhenParentTurnDone
// reproduces the bug where in-process Explore/Plan subagents leave
// their transcripts with stop_reason: null and no terminal event.
// The classifier correctly treats this as assistant_streaming (not
// done), so the child stays in working. The parent, whose own turn
// IS done, is held in working by the active children.
//
// The fix is finishOrphanedChildren: when processing the parent's
// last activity event and its classifier verdict is ready, walk the
// children and promote any that have no open tool calls to ready.
// Their work must be complete because the parent's final message
// already incorporated their results.
func TestSessionDetector_OrphanedSubagentsFinishWhenParentTurnDone(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	tmpDir := t.TempDir()

	// Parent transcript: real file, mtime = now (so force-promotion
	// triggered by EventActivity reads a real file).
	parentPath := filepath.Join(tmpDir, "parent-orphans.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	repo.Save(&session.SessionState{
		SessionID:      "parent-orphans",
		State:          session.StateWorking,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "All waves complete.",
		},
	})

	// Two orphaned children: real stale transcript files (mtime 60s ago)
	// so finishOrphanedChildren's quiet-window check (30s) treats them as
	// silent and promotes them.
	staleMtime := time.Now().Add(-60 * time.Second)
	for _, childID := range []string{"child-orphan-a", "child-orphan-b"} {
		childPath := filepath.Join(tmpDir, childID+".jsonl")
		if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(childPath, staleMtime, staleMtime); err != nil {
			t.Fatal(err)
		}
		repo.Save(&session.SessionState{
			SessionID:       childID,
			State:           session.StateWorking,
			ParentSessionID: "parent-orphans",
			TranscriptPath:  childPath,
			FirstSeen:       now,
			UpdatedAt:       now,
			EventCount:      5,
			Metrics: &session.SessionMetrics{
				LastEventType:   "assistant_streaming",
				HasOpenToolCall: false,
			},
		})
	}

	// Trigger the parent's processActivity. The classifier will say
	// ready, finishOrphanedChildren should promote both children,
	// hasActiveChildren should then return false, and the parent
	// should land in ready.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-orphans",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	parent, _ := repo.Load("parent-orphans")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateReady {
		t.Errorf("parent state: got %q, want ready — orphaned children should have been fast-forwarded", parent.State)
	}

	for _, childID := range []string{"child-orphan-a", "child-orphan-b"} {
		child, _ := repo.Load(childID)
		if child == nil {
			continue // parent-ready cleanup may have deleted it
		}
		if child.State != session.StateReady {
			t.Errorf("child %q state: got %q, want ready", childID, child.State)
		}
	}

	cancel()
	<-done
}

// TestSessionDetector_BackgroundSubagentsNotFastForwarded captures the
// 3d506c6e bug: background subagents run asynchronously to the parent.
// The parent's turn can finish while a background agent is mid-stream.
// In that window the child may momentarily have HasOpenToolCall=false
// (between tool calls) — the only safety signal is that its transcript
// is still being written. finishOrphanedChildren must skip any child
// whose transcript mtime is within subagentQuietWindow of now.
//
// Scenario: parent turn done, child has no open tools, but child's
// transcript was just written. The child must stay in working and the
// parent must be held in working.
func TestSessionDetector_BackgroundSubagentsNotFastForwarded(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	tmpDir := t.TempDir()

	parentPath := filepath.Join(tmpDir, "parent-bg.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:      "parent-bg",
		State:          session.StateWorking,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "All 3 background agents launched.",
		},
	})

	// Background child: no open tools (between tool calls) but its
	// transcript is fresh — mtime = now, indicating it's still being
	// actively written.
	childPath := filepath.Join(tmpDir, "child-bg.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	// Keep the default mtime (just now) — this is the point: a
	// still-running background agent has a fresh mtime.
	repo.Save(&session.SessionState{
		SessionID:       "child-bg",
		State:           session.StateWorking,
		ParentSessionID: "parent-bg",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-bg",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	// Parent must be held in working because the child is still active.
	parent, _ := repo.Load("parent-bg")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateWorking {
		t.Errorf("parent state: got %q, want working — background child has fresh mtime and must hold the parent", parent.State)
	}

	// Child must NOT have been promoted.
	child, _ := repo.Load("child-bg")
	if child == nil {
		t.Fatal("child session should still exist")
	}
	if child.State != session.StateWorking {
		t.Errorf("child state: got %q, want working — fresh mtime indicates active background agent", child.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_ActiveSubagentsNotPromoted_ByOrphanFinish guards
// against a false-positive in finishOrphanedChildren: a child that has
// an open tool call (genuinely still running) must NOT be promoted just
// because the parent's turn is done.
func TestSessionDetector_ActiveSubagentsNotPromoted_ByOrphanFinish(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	repo.Save(&session.SessionState{
		SessionID:      "parent-active",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent-active.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "Waiting for subagent.",
		},
	})

	// Child has an open tool call — genuinely still running.
	repo.Save(&session.SessionState{
		SessionID:       "child-active",
		State:           session.StateWorking,
		ParentSessionID: "parent-active",
		TranscriptPath:  "/home/.claude/projects/-Users-test/parent-active/subagents/child-active.jsonl",
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"Bash"},
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-active",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/parent-active.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	// Parent should be held in working because the child genuinely
	// has a tool open — finishOrphanedChildren must NOT touch it.
	parent, _ := repo.Load("parent-active")
	if parent.State != session.StateWorking {
		t.Errorf("parent state: got %q, want working (child has open tool — should be held)", parent.State)
	}
	child, _ := repo.Load("child-active")
	if child == nil {
		t.Fatal("child should still exist")
	}
	if child.State != session.StateWorking {
		t.Errorf("child state: got %q, want working (has open tool)", child.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_ParentReleasedToReady_WhenChildSweptByLiveness
// reproduces the bug where a parent session got stuck in `working` after
// the liveness sweep deleted its last child.
//
// Scenario: user launches 3 parallel foreground agents. The parent's own
// turn finishes but it's held in `working` because the children are still
// in the repo. The children's transcripts stop updating (the agents are
// done but Claude Code doesn't write a final turn_done for foreground
// agents), so CheckPIDLiveness eventually deletes them as stale. Before
// this fix the parent was never re-evaluated, so it sat in `working`
// forever.
func TestSessionDetector_ParentReleasedToReady_WhenChildSweptByLiveness(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// The child-sweep path in PIDManager is gated on readyTTL > 0,
	// so the default newDetector (readyTTL=0) would skip it entirely.
	// Use a tiny TTL so the sweep actually runs its child-cleanup loop.
	det := services.NewSessionDetector(
		[]inbound.AgentWatcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 1*time.Second, nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Parent: turn is done, no open tools, held in working because of an
	// active child. This matches the production state observed for
	// session 57323e2d-4a55-4e00-85de-e9ed21b42171.
	repo.Save(&session.SessionState{
		SessionID:      "parent-swept",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/parent-swept.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "All waves complete.",
		},
	})

	// Child: stuck in working, transcript went stale 5+ minutes ago so
	// isStaleTranscript() returns true when the sweep checks it.
	staleTime := time.Now().Add(-5 * time.Minute)
	staleTranscriptPath := filepath.Join(t.TempDir(), "stale-child.jsonl")
	if err := os.WriteFile(staleTranscriptPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleTranscriptPath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:       "child-swept",
		State:           session.StateWorking,
		ParentSessionID: "parent-swept",
		TranscriptPath:  staleTranscriptPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType: "tool_use",
		},
	})

	// Trigger the sweep directly instead of waiting the real 5s ticker.
	det.RunPIDLivenessSweepForTest()

	// Give the parent re-evaluation time to land.
	time.Sleep(30 * time.Millisecond)

	parent, _ := repo.Load("parent-swept")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateReady {
		t.Errorf("parent state: got %q, want ready (child was swept, parent should release)", parent.State)
	}

	// Child should be gone.
	if child, _ := repo.Load("child-swept"); child != nil {
		t.Errorf("child should have been deleted by the sweep, got %+v", child)
	}

	cancel()
	<-done
}

func TestSessionDetector_ParentNotAffected_WhenNoChildren(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()

	// Session with no children, turn done → should transition to ready normally.
	repo.Save(&session.SessionState{
		SessionID:      "solo1",
		State:          session.StateWorking,
		TranscriptPath: "/home/.claude/projects/-Users-test/solo1.jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "All done.",
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "solo1",
		ProjectDir:     "-Users-test",
		TranscriptPath: "/home/.claude/projects/-Users-test/solo1.jsonl",
	}

	time.Sleep(50 * time.Millisecond)

	state, _ := repo.Load("solo1")
	if state.State != session.StateReady {
		t.Errorf("state: got %q, want ready (no children, turn done)", state.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_Activity_SubagentCompletion_TransitionsChildToReady is
// the issue #134 regression: a parent activity event whose metrics carry a
// SubagentCompletion (parsed from origin.kind="task-notification") must
// transition the matching child session to ready immediately, without
// depending on the time-gated finishOrphanedChildren fallback.
func TestSessionDetector_Activity_SubagentCompletion_TransitionsChildToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const parentID = "8a525d27-37a4-4a12-8523-a3ea345290cf"
	const childID = "child-af7bf8be"
	const agentID = "af7bf8be5a1b511e4"
	parentTranscript := "/home/.claude/projects/-Users-test/" + parentID + ".jsonl"
	childTranscript := "/home/.claude/projects/-Users-test/" + parentID + "/subagents/agent-" + agentID + ".jsonl"

	// Parent: turn still in flight (working). Pre-populate the completion
	// signal on metrics — the mock metrics collector returns nil from
	// ComputeMetrics, so MergeMetrics(nil, oldM) keeps these values.
	repo.states[parentID] = &session.SessionState{
		SessionID:      parentID,
		State:          session.StateWorking,
		TranscriptPath: parentTranscript,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
			SubagentCompletions: []session.SubagentCompletion{
				{AgentID: agentID, ToolUseID: "toolu_01Wf", Status: "completed"},
			},
		},
	}

	// Child: stuck in working with stop_reason=null (the bug condition).
	repo.states[childID] = &session.SessionState{
		SessionID:       childID,
		ParentSessionID: parentID,
		State:           session.StateWorking,
		TranscriptPath:  childTranscript,
		FirstSeen:       time.Now().Unix(),
		UpdatedAt:       time.Now().Unix(),
		EventCount:      8,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	}

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      parentID,
		ProjectDir:     "-Users-test",
		TranscriptPath: parentTranscript,
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	child, _ := repo.Load(childID)
	if child.State != session.StateReady {
		t.Errorf("child state: got %q, want ready (parent task-notification should transition child)", child.State)
	}
}
