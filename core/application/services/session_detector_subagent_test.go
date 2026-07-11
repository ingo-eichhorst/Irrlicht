package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// workflowChildMetrics returns a funcMetrics that reports a live, mid-tool-call
// subagent for any Workflow-tool fan-out path (.../subagents/workflows/...)
// and falls back to nil (no-op merge) for everything else — the parent's own
// fake, non-backed transcript path included. Used by the #889 regression
// tests below to give freshly-discovered children real metrics, since
// mockMetrics.ComputeMetrics always returns nil.
func workflowChildMetrics() *funcMetrics {
	return &funcMetrics{fn: func(path, adapter string) (*session.SessionMetrics, error) {
		if !strings.Contains(path, "subagents/workflows") {
			return nil, nil
		}
		return &session.SessionMetrics{
			LastEventType:     "tool_use",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"Bash"},
		}, nil
	}}
}

// setupWorkflowChildDiscovery reproduces the #889 bug precondition: a parent
// whose turn is already done and sitting at ready (no children discovered
// yet), then fires a new Workflow-tool child transcript for it — path shape
// matching the fan-out layout from issue #565,
// .../<parent>/subagents/workflows/<runID>/<childSessionID>.jsonl — and waits
// for the parent to be held working. Returns the running detector so callers
// can either assert immediately or drive further scenarios (e.g. the
// stale-session sweep) before calling cancel/done themselves.
func setupWorkflowChildDiscovery(t *testing.T, parentID, childSessionID, runID string) (repo *mockRepo, det *services.SessionDetector, cancel context.CancelFunc, done chan error) {
	t.Helper()
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo = newMockRepo()

	det = newDetectorWithMetrics(tw, pw, repo, workflowChildMetrics())

	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID:      parentID,
		State:          session.StateReady,
		TranscriptPath: "/home/.claude/projects/-Users-test/" + parentID + ".jsonl",
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     5,
		Metrics: &session.SessionMetrics{
			LastEventType:     "turn_done",
			HasOpenToolCall:   false,
			LastAssistantText: "The background workflow is running. I'll be notified when it completes.",
		},
	})

	runDir := filepath.Join(t.TempDir(), parentID, "subagents", "workflows", runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(runDir, childSessionID+".jsonl")
	writeOldTranscript(t, childPath, 0)

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      childSessionID,
		ProjectDir:     runID,
		TranscriptPath: childPath,
	}

	waitForSessionState(repo, parentID, session.StateWorking, time.Second)
	return repo, det, cancel, done
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

// TestSessionDetector_ParentHeldWorking_WhenNewChildDiscoveredWhileReady
// reproduces issue #889: a parent running a background Workflow-tool call can
// have its own turn-done fire while it has zero active children — either
// because the workflow's first subagent hasn't been discovered yet, or
// because a pipeline stage boundary left it with none for a moment. With no
// active children, hasActiveChildren finds nothing and the classifier flips
// the parent to ready. Moments later the next stage's subagent transcript
// appears — proof the background job is still running — but before this fix
// nothing re-evaluated the parent at that point: the new child itself starts
// in the generic "ready until proven otherwise" state too, so waiting for its
// own content-based classification left the parent showing ready for as long
// as tens of seconds in production.
//
// holdParentWorkingForNewChild closes this by forcing an already-ready parent
// back to working the instant a new child of it is discovered, rather than
// waiting for either the child's own next activity pass or an incidental
// hook event on the parent to catch it.
func TestSessionDetector_ParentHeldWorking_WhenNewChildDiscoveredWhileReady(t *testing.T) {
	repo, _, cancel, done := setupWorkflowChildDiscovery(t, "parent-wf-889", "agent-review1", "wf_a1b2c3")
	cancel()
	<-done

	parent, _ := repo.Load("parent-wf-889")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateWorking {
		t.Errorf("parent state: got %q, want working — new child discovered while ready must hold it", parent.State)
	}

	child, _ := repo.Load("agent-review1")
	if child == nil {
		t.Fatal("child session should have been created")
	}
	if child.ParentSessionID != "parent-wf-889" {
		t.Errorf("child ParentSessionID: got %q, want %q", child.ParentSessionID, "parent-wf-889")
	}
	if child.State != session.StateWorking {
		t.Errorf("child state: got %q, want working — classified against its own metrics at creation, not left at the generic ready bootstrap", child.State)
	}
}

// TestSessionDetector_ParentHeldWorking_SurvivesStaleSessionRefresh guards
// the other half of issue #889's fix: holdParentWorkingForNewChild alone only
// flips the parent's State in memory for one instant. If the newly-discovered
// child were left at its generic bootstrap ready state, hasActiveChildren
// would not count it as active, and the periodic stale-session refresh
// (staleWorkingRefreshInterval, 5s) would re-run the classifier against the
// parent's unchanged turn-done metrics, find no active children, and silently
// flip the parent straight back to ready — reproducing #889 a few seconds
// later instead of fixing it. Classifying the child against its own metrics
// at creation (in onNewSession) closes this: the child persists as working,
// so hasActiveChildren keeps holding the parent on every subsequent sweep.
func TestSessionDetector_ParentHeldWorking_SurvivesStaleSessionRefresh(t *testing.T) {
	repo, det, cancel, done := setupWorkflowChildDiscovery(t, "parent-wf-sweep", "agent-review2", "wf_d4e5f6")

	// Age both parent and child well past staleWorkingRefreshInterval (5s) so
	// the sweep's staleness gate doesn't skip them, then run it directly
	// instead of waiting on the real ticker.
	stale := time.Now().Add(-10 * time.Minute).Unix()
	parent, _ := repo.Load("parent-wf-sweep")
	parent.UpdatedAt = stale
	repo.Save(parent)
	child, _ := repo.Load("agent-review2")
	child.UpdatedAt = stale
	repo.Save(child)

	det.RunStaleSessionRefreshForTest()

	// Poll instead of a fixed sleep: the sweep dispatches through the same
	// async event-loop path as a real transcript event.
	waitForCondition(func() bool {
		p, _ := repo.Load("parent-wf-sweep")
		return p != nil && p.State == session.StateWorking
	}, time.Second)

	cancel()
	<-done

	parent, _ = repo.Load("parent-wf-sweep")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateWorking {
		t.Errorf("parent state after stale-session refresh: got %q, want working — the child must still count as active", parent.State)
	}
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

	// Two orphaned children: real stale transcript files (mtime 100s ago)
	// so finishOrphanedChildren's quiet-window check (90s) treats them as
	// silent and promotes them.
	staleMtime := time.Now().Add(-100 * time.Second)
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

	// Poll for the parent reaching ready instead of a fixed sleep — under
	// parallel-load scheduling the event loop may not have finished the
	// processActivity pass within a fixed window, which both flakes the
	// assertion and races the in-place state mutation (issue #606). The
	// children are fast-forwarded and saved within that same pass, before the
	// parent's own ready Save, so this is a sufficient barrier.
	waitForSessionState(repo, "parent-orphans", session.StateReady, time.Second)
	cancel()
	<-done

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
}

// TestSessionDetector_BackgroundSubagentsNotFastForwarded captures the
// 3d506c6e bug: background subagents run asynchronously to the parent.
// The parent's turn can finish while a background agent is mid-stream.
// In that window the child may momentarily have HasOpenToolCall=false
// (between tool calls) — the only safety signal is that its transcript
// is still being written. finishOrphanedChildren must skip any child
// whose transcript mtime is within SubagentQuietWindow of now.
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
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, nil,
		"test", 1*time.Second, nil, nil, nil,
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

	waitForSessionState(repo, "solo1", session.StateReady, 500*time.Millisecond)

	repo.mu.Lock()
	got := repo.lastSavedState["solo1"]
	repo.mu.Unlock()
	if got != session.StateReady {
		t.Errorf("state: got %q, want ready (no children, turn done)", got)
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

	// Poll instead of a fixed sleep: under full-suite -race load the Run
	// loop can take >50ms to process the event, and cancelling too early
	// kills the detector before the transition lands (#578).
	waitForSessionState(repo, childID, session.StateReady, 3*time.Second)
	cancel()
	<-done

	child, _ := repo.Load(childID)
	if child.State != session.StateReady {
		t.Errorf("child state: got %q, want ready (parent task-notification should transition child)", child.State)
	}
}

// TestSessionDetector_OrphanedChildrenFinish_WhenParentEndsWaitingWithQuestion
// is the core #593 reproduction: a parent whose turn ends by asking a
// question lands in waiting, not ready — before the fix the child
// fast-forward only ran on the ready path, so finished children stayed
// stuck in working until the liveness sweep.
func TestSessionDetector_OrphanedChildrenFinish_WhenParentEndsWaitingWithQuestion(t *testing.T) {
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

	parentPath := filepath.Join(tmpDir, "parent-asks.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	repo.Save(&session.SessionState{
		SessionID:      "parent-asks",
		State:          session.StateWorking,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "All stages recorded. Should I open the PR now?",
		},
	})

	// Orphaned child: stale transcript (100s > SubagentQuietWindow), no open
	// tools — its work is done, only the stop_reason:null quirk keeps it
	// classified as working.
	staleMtime := time.Now().Add(-100 * time.Second)
	childPath := filepath.Join(tmpDir, "child-of-asker.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, staleMtime, staleMtime); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:       "child-of-asker",
		State:           session.StateWorking,
		ParentSessionID: "parent-asks",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-asks",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	parent, _ := repo.Load("parent-asks")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateWaiting {
		t.Errorf("parent state: got %q, want waiting — question/cue turn end", parent.State)
	}

	// No cleanup runs on the waiting path, so the child must still exist —
	// fast-forwarded to ready (the sweep deletes it later).
	child, _ := repo.Load("child-of-asker")
	if child == nil {
		t.Fatal("child should not be deleted on the waiting path")
	}
	if child.State != session.StateReady {
		t.Errorf("child state: got %q, want ready — orphan fast-forward must fire on turn-done waiting", child.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_PermissionWaitingDoesNotFastForwardChildren locks the
// #593 gating constraint: waiting caused by an open user-blocking tool
// (permission prompt, AskUserQuestion) means the parent's turn is NOT done —
// children must be left alone.
func TestSessionDetector_PermissionWaitingDoesNotFastForwardChildren(t *testing.T) {
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

	parentPath := filepath.Join(tmpDir, "parent-perm.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	repo.Save(&session.SessionState{
		SessionID:      "parent-perm",
		State:          session.StateWorking,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "tool_use",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"AskUserQuestion"},
		},
	})

	staleMtime := time.Now().Add(-100 * time.Second)
	childPath := filepath.Join(tmpDir, "child-of-perm.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, staleMtime, staleMtime); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:       "child-of-perm",
		State:           session.StateWorking,
		ParentSessionID: "parent-perm",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-perm",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	parent, _ := repo.Load("parent-perm")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateWaiting {
		t.Errorf("parent state: got %q, want waiting — user-blocking tool open", parent.State)
	}

	child, _ := repo.Load("child-of-perm")
	if child == nil {
		t.Fatal("child should not be deleted")
	}
	if child.State != session.StateWorking {
		t.Errorf("child state: got %q, want working — open-tool waiting must not fast-forward children", child.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_WaitingParent_HeldWorking_WhenBackgroundChildActive
// covers #897: a parent whose turn ends in a waiting cue ("Want a summary
// when it lands?") is held working — not waiting — while a background child
// is still genuinely active (transcript freshly written, within
// SubagentQuietWindow). Surfacing "waiting" here would read as "nothing
// happening" on the dashboard even though the child is still running. The
// child itself is left untouched either way (#593) — this only changes the
// parent's own state.
func TestSessionDetector_WaitingParent_HeldWorking_WhenBackgroundChildActive(t *testing.T) {
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

	parentPath := filepath.Join(tmpDir, "parent-asks-bg.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	repo.Save(&session.SessionState{
		SessionID:      "parent-asks-bg",
		State:          session.StateWorking,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "Kicked off the background review. Want a summary when it lands?",
		},
	})

	// Background child: transcript freshly written (within the 30s quiet
	// window) — must be left alone even though it has no open tool call.
	childPath := filepath.Join(tmpDir, "child-bg.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:       "child-bg",
		State:           session.StateWorking,
		ParentSessionID: "parent-asks-bg",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-asks-bg",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	parent, _ := repo.Load("parent-asks-bg")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateWorking {
		t.Errorf("parent state: got %q, want working — held by the active background child (#897)", parent.State)
	}

	child, _ := repo.Load("child-bg")
	if child == nil {
		t.Fatal("background child should not be deleted")
	}
	if child.State != session.StateWorking {
		t.Errorf("background child state: got %q, want working — quiet-window guard", child.State)
	}

	cancel()
	<-done
}

// TestSessionDetector_ParentBadgeCleared_AfterReadyCleanup locks the #593
// ordering fix: the turn's FINAL parent push must carry the post-cleanup
// (empty) subagent summary, and the repo copy must be cleared too — the
// pre-fix order was refresh → broadcast → cleanupChildren, so the last
// message of the turn counted children deleted a moment later and the
// stale summary stayed persisted for hook-path re-broadcasts.
func TestSessionDetector_ParentBadgeCleared_AfterReadyCleanup(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det, bc := newDetectorWithBroadcaster(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	tmpDir := t.TempDir()

	parentPath := filepath.Join(tmpDir, "parent-clears.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:      "parent-clears",
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

	staleMtime := time.Now().Add(-100 * time.Second)
	childPath := filepath.Join(tmpDir, "child-cleared.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, staleMtime, staleMtime); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:       "child-cleared",
		State:           session.StateWorking,
		ParentSessionID: "parent-clears",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	})

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-clears",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	// Quiesce the detector before reading shared state.
	cancel()
	<-done

	parent, _ := repo.Load("parent-clears")
	if parent == nil {
		t.Fatal("parent session should still exist")
	}
	if parent.State != session.StateReady {
		t.Fatalf("parent state: got %q, want ready", parent.State)
	}
	if parent.Subagents != nil {
		t.Errorf("persisted parent summary: got %+v, want nil after cleanup", parent.Subagents)
	}
	if child, _ := repo.Load("child-cleared"); child != nil {
		t.Errorf("child should be deleted by parent-ready cleanup, still in state %q", child.State)
	}

	// The LAST broadcast for the parent must carry the cleared summary.
	var last *outbound.PushMessage
	for _, m := range bc.messages() {
		if m.Type == outbound.PushTypeUpdated && m.Session != nil && m.Session.SessionID == "parent-clears" {
			mm := m
			last = &mm
		}
	}
	if last == nil {
		t.Fatal("no session_updated broadcast for parent")
	}
	if last.Session.Subagents != nil {
		t.Errorf("final parent push summary: got %+v, want nil", last.Session.Subagents)
	}
}

// TestSessionDetector_ParentBadgeCleared_WhenChildSweptWhileParentWaiting
// locks the #593 sweep-case fix: when the liveness sweep deletes a stuck
// child of a parent that is NOT held in working (it's waiting on the user),
// the parent's persisted summary must still be refreshed and re-pushed.
// Pre-fix, reevaluateParent early-returned before touching the summary, so
// a waiting parent kept its stale badge until its own next transcript event.
func TestSessionDetector_ParentBadgeCleared_WhenChildSweptWhileParentWaiting(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	// The child-sweep path in PIDManager is gated on readyTTL > 0; wire a
	// capturing broadcaster to assert the corrective parent push.
	bc := &mockBroadcaster{}
	det := services.NewSessionDetector(
		[]inbound.Watcher{tw}, pw, repo,
		&mockLogger{}, &mockGit{}, &mockMetrics{}, bc,
		"test", 1*time.Second, nil, nil, nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	tmpDir := t.TempDir()

	parentPath := filepath.Join(tmpDir, "parent-waiting.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	parent := &session.SessionState{
		SessionID:      "parent-waiting",
		State:          session.StateWaiting,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "Should I continue?",
		},
	}

	// Stuck child: transcript went stale 5+ minutes ago, so the sweep
	// deletes it on its next pass.
	staleTime := time.Now().Add(-5 * time.Minute)
	childPath := filepath.Join(tmpDir, "stuck-child.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	child := &session.SessionState{
		SessionID:       "stuck-child",
		State:           session.StateWorking,
		ParentSessionID: "parent-waiting",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      3,
		Metrics: &session.SessionMetrics{
			LastEventType: "tool_use",
		},
	}

	// The parent's persisted badge counts the stuck child.
	parent.Subagents = session.ComputeSubagentSummary(parent, []*session.SessionState{child})
	repo.Save(parent)
	repo.Save(child)

	// Trigger the sweep directly instead of waiting the real ticker.
	det.RunPIDLivenessSweepForTest()

	// Poll for the sweep's effects instead of a fixed sleep — the tight
	// readyTTL (1s) can be outrun by scheduling starvation under parallel
	// load, so a fixed window flakes (issue #624). The corrective parent
	// re-evaluation clears the badge and deletes the child; wait on both via
	// race-free repo probes before quiescing.
	waitForSessionDeleted(repo, "stuck-child", time.Second)
	waitForCondition(func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		p, ok := repo.states["parent-waiting"]
		return ok && p.Subagents == nil
	}, time.Second)

	// Quiesce the detector before reading shared state.
	cancel()
	<-done

	got, _ := repo.Load("parent-waiting")
	if got == nil {
		t.Fatal("parent session should still exist")
	}
	if got.State != session.StateWaiting {
		t.Errorf("parent state: got %q, want waiting (no transition)", got.State)
	}
	if got.Subagents != nil {
		t.Errorf("persisted parent summary: got %+v, want nil after child swept", got.Subagents)
	}
	if c, _ := repo.Load("stuck-child"); c != nil {
		t.Errorf("child should have been swept, still in state %q", c.State)
	}

	// The corrective parent push must carry the cleared summary.
	var last *outbound.PushMessage
	for _, m := range bc.messages() {
		if m.Type == outbound.PushTypeUpdated && m.Session != nil && m.Session.SessionID == "parent-waiting" {
			mm := m
			last = &mm
		}
	}
	if last == nil {
		t.Fatal("no corrective session_updated broadcast for the waiting parent")
	}
	if last.Session.Subagents != nil {
		t.Errorf("corrective parent push summary: got %+v, want nil", last.Session.Subagents)
	}
}

// TestSessionDetector_NoRedundantParentBroadcast_OnUnchangedSummary is the
// #593 storm guard: a child event that doesn't change the parent's badge
// must not add a second parent push on top of the existing child-broadcast
// parent refresh (session_detector_helpers.go).
func TestSessionDetector_NoRedundantParentBroadcast_OnUnchangedSummary(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det, bc := newDetectorWithBroadcaster(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	tmpDir := t.TempDir()

	parentPath := filepath.Join(tmpDir, "parent-stable.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	parent := &session.SessionState{
		SessionID:      "parent-stable",
		State:          session.StateWaiting,
		TranscriptPath: parentPath,
		FirstSeen:      now,
		UpdatedAt:      now,
		EventCount:     10,
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   false,
			LastAssistantText: "Should I continue?",
		},
	}

	childPath := filepath.Join(tmpDir, "busy-child.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	child := &session.SessionState{
		SessionID:       "busy-child",
		State:           session.StateWorking,
		ParentSessionID: "parent-stable",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "tool_use",
			HasOpenToolCall: true,
		},
	}

	// Persisted badge already matches reality: one working child.
	parent.Subagents = session.ComputeSubagentSummary(parent, []*session.SessionState{child})
	repo.Save(parent)
	repo.Save(child)

	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "busy-child",
		ProjectDir:     "-Users-test",
		TranscriptPath: childPath,
	}

	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	parentPushes := 0
	for _, m := range bc.messages() {
		if m.Type == outbound.PushTypeUpdated && m.Session != nil && m.Session.SessionID == "parent-stable" {
			parentPushes++
		}
	}
	if parentPushes != 1 {
		t.Errorf("parent pushes after one no-change child event: got %d, want 1 (child-broadcast refresh only)", parentPushes)
	}
}

// TestSessionDetector_ChildDeletionIsRecorded: child cleanup must leave a
// lifecycle trace (#593) — pre-fix, cleanupChildren and the liveness sweep
// deleted silently, so recordings showed children that "leaked" forever.
func TestSessionDetector_ChildDeletionIsRecorded(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	det := newDetector(tw, pw, repo)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)

	now := time.Now().Unix()
	tmpDir := t.TempDir()

	parentPath := filepath.Join(tmpDir, "parent-records.jsonl")
	if err := os.WriteFile(parentPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:      "parent-records",
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

	staleMtime := time.Now().Add(-100 * time.Second)
	childPath := filepath.Join(tmpDir, "child-recorded.jsonl")
	if err := os.WriteFile(childPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, staleMtime, staleMtime); err != nil {
		t.Fatal(err)
	}
	repo.Save(&session.SessionState{
		SessionID:       "child-recorded",
		State:           session.StateWorking,
		ParentSessionID: "parent-records",
		TranscriptPath:  childPath,
		FirstSeen:       now,
		UpdatedAt:       now,
		EventCount:      5,
		Metrics: &session.SessionMetrics{
			LastEventType:   "assistant_streaming",
			HasOpenToolCall: false,
		},
	})

	// Parent finishes its turn → ready → cleanupChildren deletes the child.
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      "parent-records",
		ProjectDir:     "-Users-test",
		TranscriptPath: parentPath,
	}

	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	if child, _ := repo.Load("child-recorded"); child != nil {
		t.Fatalf("child should be deleted, still in state %q", child.State)
	}

	found := false
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindTranscriptRemoved && ev.SessionID == "child-recorded" {
			found = true
			if ev.Reason == "" {
				t.Error("recorded child deletion should carry a reason")
			}
		}
	}
	if !found {
		t.Error("no transcript_removed lifecycle event recorded for the cleaned-up child")
	}
}
