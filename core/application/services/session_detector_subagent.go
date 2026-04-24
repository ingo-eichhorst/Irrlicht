package services

import (
	"fmt"
	"os"
	"strings"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func (d *SessionDetector) refreshSubagentSummary(state *session.SessionState) {
	if state == nil {
		return
	}
	children, err := d.repo.ListAll()
	if err != nil {
		children = nil
	}
	state.Subagents = session.ComputeSubagentSummary(state, children)
}

// finishOrphanedChildren walks the child sessions of parentID and promotes
// each one to ready if it has no open tool calls. Called when the parent's
// own turn is done and we're about to hold the parent in working on
// behalf of the children.
//
// This handles a Claude Code quirk: in-process subagent transcripts
// (Explore/Plan tools under <parent>/subagents/<agent-id>.jsonl) never
// emit a proper end_turn event — their final assistant message is written
// with stop_reason: null, which the classifier correctly treats as
// streaming. Without this fast-forward the child stays in working until
// the 2-minute stale-transcript sweep catches it, and the parent is
// held working for that entire window.
//
// Safety: we only promote children whose metrics show no open tool calls.
// A child that is genuinely still running a tool keeps an entry in the
// FIFO and is left alone.
func (d *SessionDetector) finishOrphanedChildren(parentID string) {
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, s := range states {
		if s.ParentSessionID != parentID {
			continue
		}
		if s.State != session.StateWorking && s.State != session.StateWaiting {
			continue
		}
		if s.Metrics == nil || s.Metrics.HasOpenToolCall {
			continue
		}
		// Safety: a child whose transcript has been written in the last
		// SubagentQuietWindow is a background agent still mid-run — we
		// don't know whether the parent's "done" means "finished the
		// subagents" or "kicked off async background work". Leaving active
		// children alone avoids the bug where background agents are
		// promoted and deleted while still writing. If the stat fails
		// (missing file), skip to be conservative — the liveness sweep
		// will fall back to its 2-minute window for anything we miss here.
		info, err := os.Stat(s.TranscriptPath)
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < SubagentQuietWindow {
			continue
		}

		prev := s.State
		s.State = session.StateReady
		s.UpdatedAt = now
		s.WaitingStartTime = nil
		d.record(lifecycle.Event{
			Kind:      lifecycle.KindStateTransition,
			SessionID: s.SessionID,
			PrevState: prev,
			NewState:  session.StateReady,
			Reason:    "subagent orphaned (parent turn done, no open tools)",
		})
		if err := d.repo.Save(s); err != nil {
			d.log.LogError("session-detector", s.SessionID,
				fmt.Sprintf("failed to finish orphaned child: %v", err))
			continue
		}
		d.log.LogInfo("session-detector", s.SessionID,
			fmt.Sprintf("finished orphaned subagent (%s → ready) — parent %s turn done", prev, parentID))
		d.broadcast(outbound.PushTypeUpdated, s)
	}
}

// applySubagentCompletions resolves each completion to a child session and
// transitions it to ready. Match is by ParentSessionID + transcript filename
// suffix (agent-<AgentID>.jsonl) — the latter mirrors the subagent transcript
// layout Claude Code uses on disk. If no child is indexed yet, the no-op is
// safe: finishOrphanedChildren still acts as a defensive fallback later.
// See issue #134.
func (d *SessionDetector) applySubagentCompletions(parentID string, completions []session.SubagentCompletion) {
	if len(completions) == 0 {
		return
	}
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, c := range completions {
		if c.AgentID == "" {
			continue
		}
		suffix := "agent-" + c.AgentID + ".jsonl"
		for _, s := range states {
			if s.ParentSessionID != parentID {
				continue
			}
			if !strings.HasSuffix(s.TranscriptPath, suffix) {
				continue
			}
			if s.State != session.StateWorking && s.State != session.StateWaiting {
				continue
			}
			prev := s.State
			s.State = session.StateReady
			s.UpdatedAt = now
			s.WaitingStartTime = nil
			d.record(lifecycle.Event{
				Kind:      lifecycle.KindStateTransition,
				SessionID: s.SessionID,
				PrevState: prev,
				NewState:  session.StateReady,
				Reason:    "subagent completed (parent task-notification)",
			})
			if err := d.repo.Save(s); err != nil {
				d.log.LogError("session-detector", s.SessionID,
					fmt.Sprintf("failed to apply subagent completion: %v", err))
				continue
			}
			d.log.LogInfo("session-detector", s.SessionID,
				fmt.Sprintf("subagent completed via parent task-notification (%s → ready, parent %s)", prev, parentID))
			d.broadcast(outbound.PushTypeUpdated, s)
			break
		}
	}
}

// hasActiveChildren returns true if any child session of the given parent is
// still working or waiting. Used to prevent a parent from transitioning to
// ready while background/foreground subagents are still processing.
func (d *SessionDetector) hasActiveChildren(parentID string) bool {
	states, err := d.repo.ListAll()
	if err != nil {
		return false
	}
	for _, s := range states {
		if s.ParentSessionID == parentID &&
			(s.State == session.StateWorking || s.State == session.StateWaiting) {
			return true
		}
	}
	return false
}

// reevaluateParent checks whether a parent session should transition now that
// a child's state has changed. If the parent's own turn is done (IsAgentDone)
// and no more active children remain, the parent transitions to ready (or
// waiting if ending with a question).
func (d *SessionDetector) reevaluateParent(parentID string) {
	parent, err := d.repo.Load(parentID)
	if err != nil || parent == nil {
		return
	}
	// Only re-evaluate parents that are being held in working.
	if parent.State != session.StateWorking {
		return
	}
	// The parent's own turn must be done for it to transition.
	if parent.Metrics == nil || !parent.Metrics.IsAgentDone() {
		return
	}
	// Fast-forward orphaned subagents (finished work but no stop_reason)
	// — see finishOrphanedChildren doc for rationale.
	d.finishOrphanedChildren(parentID)

	// Still have active children — stay working.
	if d.hasActiveChildren(parentID) {
		return
	}

	// All children are done and the parent's turn is complete.
	newState, reason := ClassifyState(parent.State, parent.Metrics)
	if newState == parent.State {
		return
	}

	now := time.Now().Unix()
	if reason != "" {
		d.log.LogInfo("session-detector", parentID,
			fmt.Sprintf("children done, parent re-evaluated: %s", reason))
	}
	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: parentID, PrevState: parent.State, NewState: newState, Reason: reason})
	parent.State = newState
	parent.UpdatedAt = now
	if newState == session.StateWaiting {
		parent.WaitingStartTime = &now
	}

	if err := d.repo.Save(parent); err != nil {
		d.log.LogError("session-detector", parentID,
			fmt.Sprintf("failed to save parent re-evaluation: %v", err))
		return
	}
	d.broadcast(outbound.PushTypeUpdated, parent)

	// If the parent transitioned to ready, clean up its children.
	if parent.State == session.StateReady {
		d.pidMgr.cleanupChildren(parentID)
	}
}
