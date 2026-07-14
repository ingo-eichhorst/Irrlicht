package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_CodexMetadataChildrenStayNested verifies the complete
// lifecycle path exercised by Codex's flat rollout metadata: three children
// arrive with explicit ParentSessionID values, never become dashboard roots,
// hold their completed parent working, and release it only after the final
// child is ready.
func TestSessionDetector_CodexMetadataChildrenStayNested(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	metrics := &funcMetrics{fn: func(path, _ string) (*session.SessionMetrics, error) {
		if strings.Contains(path, "codex-child-") {
			return &session.SessionMetrics{LastEventType: "tool_use", HasOpenToolCall: true}, nil
		}
		return nil, nil
	}}
	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	const parentID = "019f6249-055d-76d3-b381-cd9d3eb99189"
	now := time.Now().Unix()
	repo.Save(&session.SessionState{
		SessionID: parentID, State: session.StateReady, ProjectName: "codex-project",
		TranscriptPath: filepath.Join(t.TempDir(), "parent.jsonl"), FirstSeen: now, UpdatedAt: now,
		Metrics: &session.SessionMetrics{LastEventType: "turn_done"},
	})

	childIDs := []string{
		"019f624a-cc95-7c50-b9fa-1db381270b73",
		"019f624b-cc95-7c50-b9fa-1db381270b74",
		"019f624c-cc95-7c50-b9fa-1db381270b75",
	}
	for _, childID := range childIDs {
		path := filepath.Join(t.TempDir(), "codex-child-"+childID+".jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: childID, ParentSessionID: parentID, TranscriptPath: path}
	}

	waitForSessionState(repo, parentID, session.StateWorking, time.Second)
	waitForCondition(func() bool {
		states, _ := repo.ListAll()
		linked := 0
		for _, state := range states {
			if state.ParentSessionID == parentID {
				linked++
			}
		}
		return linked == len(childIDs)
	}, time.Second)

	states, _ := repo.ListAll()
	groups := session.BuildDashboard(states, nil)
	if len(groups) != 1 || len(groups[0].Agents) != 1 {
		t.Fatalf("dashboard roots = %#v, want exactly one parent", groups)
	}
	if got := len(groups[0].Agents[0].Children); got != len(childIDs) {
		t.Errorf("nested child count = %d, want %d", got, len(childIDs))
	}

	for _, childID := range childIDs[:2] {
		child, _ := repo.Load(childID)
		child.State = session.StateReady
		repo.Save(child)
	}
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: parentID, TranscriptPath: filepath.Join(t.TempDir(), "parent.jsonl")}
	time.Sleep(50 * time.Millisecond)
	if parent, _ := repo.Load(parentID); parent.State != session.StateWorking {
		t.Errorf("parent State after two children complete = %q, want working", parent.State)
	}

	last, _ := repo.Load(childIDs[2])
	last.State = session.StateReady
	repo.Save(last)
	tw.ch <- agent.Event{Type: agent.EventActivity, SessionID: parentID, TranscriptPath: filepath.Join(t.TempDir(), "parent.jsonl")}
	waitForSessionState(repo, parentID, session.StateReady, time.Second)

	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Errorf("detector Run() = %v", err)
	}
}
