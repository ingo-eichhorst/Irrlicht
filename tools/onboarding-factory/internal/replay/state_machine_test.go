package replay

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// fakeBroadcaster captures every PushMessage so tests can assert order
// and content without spinning up the real pushService.
type fakeBroadcaster struct {
	mu  sync.Mutex
	msg []outbound.PushMessage
}

func (b *fakeBroadcaster) Broadcast(m outbound.PushMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msg = append(b.msg, m)
}
func (b *fakeBroadcaster) Subscribe() chan outbound.PushMessage {
	return make(chan outbound.PushMessage)
}
func (b *fakeBroadcaster) Unsubscribe(ch chan outbound.PushMessage) {}
func (b *fakeBroadcaster) messages() []outbound.PushMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]outbound.PushMessage, len(b.msg))
	copy(out, b.msg)
	return out
}

func TestStateMachine_basicSessionLifecycle(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: start, Kind: lifecycle.KindTranscriptNew, SessionID: "s1", Adapter: "claudecode"},
		{Seq: 2, Timestamp: start.Add(time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: "working"},
		{Seq: 3, Timestamp: start.Add(2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: "ready"},
	}
	b := &fakeBroadcaster{}
	// Crank speed up so the test doesn't wait real seconds.
	sm := New(events, b, 1000.0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sm.Run(ctx)

	msgs := b.messages()
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Type != outbound.PushTypeCreated || msgs[0].Session.SessionID != "s1" {
		t.Errorf("msg0 wrong: %+v", msgs[0])
	}
	if msgs[0].Session.State != "ready" {
		t.Errorf("initial state should be ready, got %s", msgs[0].Session.State)
	}
	if msgs[1].Type != outbound.PushTypeUpdated || msgs[1].Session.State != "working" {
		t.Errorf("msg1 wrong: %+v", msgs[1])
	}
	if msgs[2].Type != outbound.PushTypeUpdated || msgs[2].Session.State != "ready" {
		t.Errorf("msg2 wrong: %+v", msgs[2])
	}
}

func TestStateMachine_processExitDeletes(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: start, Kind: lifecycle.KindTranscriptNew, SessionID: "s1"},
		{Seq: 2, Timestamp: start.Add(time.Second), Kind: lifecycle.KindProcessExited, SessionID: "s1"},
	}
	b := &fakeBroadcaster{}
	sm := New(events, b, 1000.0)
	sm.Run(context.Background())
	msgs := b.messages()
	if len(msgs) != 2 || msgs[1].Type != outbound.PushTypeDeleted {
		t.Errorf("expected created→deleted; got %+v", msgs)
	}
}

func TestStateMachine_pidDiscoverySetsPID(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: start, Kind: lifecycle.KindTranscriptNew, SessionID: "s1"},
		{Seq: 2, Timestamp: start.Add(time.Second), Kind: lifecycle.KindPIDDiscovered, SessionID: "s1", PID: 12345},
	}
	b := &fakeBroadcaster{}
	sm := New(events, b, 1000.0)
	sm.Run(context.Background())
	msgs := b.messages()
	if len(msgs) != 2 || msgs[1].Session.PID != 12345 {
		t.Errorf("PID not set on session: %+v", msgs[1].Session)
	}
}

func TestStateMachine_pauseResumeStops(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: start, Kind: lifecycle.KindTranscriptNew, SessionID: "s1"},
		{Seq: 2, Timestamp: start.Add(time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: "working"},
		{Seq: 3, Timestamp: start.Add(2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: "ready"},
	}
	b := &fakeBroadcaster{}
	// Slow speed so we have room to pause mid-stream.
	sm := New(events, b, 10.0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sm.Run(ctx)
	// Wait for first emit.
	time.Sleep(50 * time.Millisecond)
	sm.Pause()
	time.Sleep(50 * time.Millisecond)
	beforePauseCount := len(b.messages())
	// Confirm no progress while paused.
	time.Sleep(150 * time.Millisecond)
	if got := len(b.messages()); got != beforePauseCount {
		t.Errorf("paused machine emitted %d more messages", got-beforePauseCount)
	}
	sm.Resume()
	// Let it finish.
	select {
	case <-sm.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("state machine didn't complete after resume")
	}
	if got := len(b.messages()); got != 3 {
		t.Errorf("expected 3 messages total, got %d", got)
	}
}

func TestStateMachine_seekForwardReplaysIntermediateState(t *testing.T) {
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: start, Kind: lifecycle.KindTranscriptNew, SessionID: "s1"},
		{Seq: 2, Timestamp: start.Add(5 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: "working"},
		{Seq: 3, Timestamp: start.Add(10 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s1", NewState: "ready"},
	}
	b := &fakeBroadcaster{}
	sm := New(events, b, 0.5) // slow — never reaches event 2 organically
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sm.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	sm.SeekToOffset(11000) // past the end
	time.Sleep(100 * time.Millisecond)
	// State after seek should reflect the final ready state.
	snap := sm.Snapshot()
	if len(snap) != 1 || snap[0].State != "ready" {
		t.Errorf("seek did not produce final state: %+v", snap)
	}
}

func TestStateMachine_seedScenarioProducesPlausibleTimeline(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "replaydata", "agents",
		"claudecode", "scenarios", "multi-turn-conversation", "events.jsonl")
	events, err := LoadEvents(path)
	if err != nil {
		t.Skipf("seed corpus not present: %v", err)
	}
	b := &fakeBroadcaster{}
	sm := New(events, b, 10000.0)
	sm.Run(context.Background())
	msgs := b.messages()
	if len(msgs) == 0 {
		t.Fatal("expected some messages from seed scenario")
	}
	// At least one session should reach the "working" state through the
	// recording.
	sawWorking := false
	for _, m := range msgs {
		if m.Session != nil && m.Session.State == "working" {
			sawWorking = true
			break
		}
	}
	if !sawWorking {
		t.Error("seed scenario should produce a session in working state at some point")
	}
}

// _ ensure we link session package via the type so the import compiles
// even after the test set above evolves.
var _ = session.SessionState{}
