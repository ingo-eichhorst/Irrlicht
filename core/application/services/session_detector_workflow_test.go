package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
)

// Workflow-tool fan-out (issue #565): agents write transcripts one level
// deeper than plain Task subagents — .../<parent>/subagents/workflows/<run>/
// — so they must link to the parent session exactly like plain subagents,
// and the run dir's journal.jsonl bookkeeping file must never surface as a
// session.

func TestSessionDetector_WorkflowAgent_LinkedToParent(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	runDir := filepath.Join(t.TempDir(), "parent-wf", "subagents", "workflows", "wf_854deede-0ff")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(runDir, "agent-a1.jsonl")
	writeOldTranscript(t, transcriptPath, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "agent-a1",
		ProjectDir:     "wf_854deede-0ff",
		TranscriptPath: transcriptPath,
	}

	waitForCondition(func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		s, ok := repo.states["agent-a1"]
		return ok && s.ParentSessionID == "parent-wf"
	}, time.Second)

	cancel()
	<-done

	state, _ := repo.Load("agent-a1")
	if state == nil {
		t.Fatal("workflow agent session not created")
	}
	if state.ParentSessionID != "parent-wf" {
		t.Errorf("ParentSessionID = %q, want %q", state.ParentSessionID, "parent-wf")
	}

	// The lifecycle stream must carry the parent link, and the recorded
	// transcript_new must use the stable layout label instead of the
	// ephemeral wf_<run-id> directory name.
	linked, transcriptNew, transcriptNewProjectDir := findWorkflowLinkEvents(rec.snapshot(), "agent-a1", "parent-wf")
	if !linked {
		t.Error("no parent_linked lifecycle event recorded for agent-a1 → parent-wf")
	}
	if !transcriptNew {
		t.Error("no transcript_new lifecycle event recorded for agent-a1")
	} else if transcriptNewProjectDir != "subagents/workflows" {
		t.Errorf("transcript_new ProjectDir = %q, want %q", transcriptNewProjectDir, "subagents/workflows")
	}
}

// findWorkflowLinkEvents scans recorded lifecycle events for the two entries
// the workflow-agent fan-out test cares about: a parent_linked event for
// sessionID pointing at parentID, and a transcript_new event for sessionID
// (returning its ProjectDir so the caller can assert on it).
func findWorkflowLinkEvents(events []lifecycle.Event, sessionID, parentID string) (linked, transcriptNew bool, transcriptNewProjectDir string) {
	for _, lev := range events {
		if lev.SessionID != sessionID {
			continue
		}
		switch lev.Kind {
		case lifecycle.KindParentLinked:
			if lev.ParentSessionID == parentID {
				linked = true
			}
		case lifecycle.KindTranscriptNew:
			transcriptNew = true
			transcriptNewProjectDir = lev.ProjectDir
		}
	}
	return linked, transcriptNew, transcriptNewProjectDir
}

func TestSessionDetector_WorkflowJournal_NotASession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	runDir := filepath.Join(t.TempDir(), "parent-wf", "subagents", "workflows", "wf_854deede-0ff")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(runDir, "journal.jsonl")
	writeOldTranscript(t, journalPath, 0)
	agentPath := filepath.Join(runDir, "agent-a2.jsonl")
	writeOldTranscript(t, agentPath, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "journal",
		ProjectDir:     "wf_854deede-0ff",
		TranscriptPath: journalPath,
	}
	// A sibling agent event sent after the journal acts as a barrier: events
	// drain in order, so once agent-a2 is observable the journal event has
	// been processed too.
	tw.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      "agent-a2",
		ProjectDir:     "wf_854deede-0ff",
		TranscriptPath: agentPath,
	}

	waitForCondition(func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		_, ok := repo.states["agent-a2"]
		return ok
	}, time.Second)

	cancel()
	<-done

	if state, _ := repo.Load("journal"); state != nil {
		t.Errorf("journal.jsonl must not create a session, got state %q", state.State)
	}
	for _, ev := range rec.snapshot() {
		if ev.SessionID == "journal" {
			t.Errorf("journal.jsonl must not record lifecycle events, got kind %q", ev.Kind)
		}
	}
}
