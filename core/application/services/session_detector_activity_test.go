package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

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
