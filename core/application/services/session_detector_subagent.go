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
	session.ApplySubagentTaskEstimate(state, children, time.Now())
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
			d.log.LogError(logComponentSessionDetector, s.SessionID,
				fmt.Sprintf("failed to finish orphaned child: %v", err))
			continue
		}
		d.log.LogInfo(logComponentSessionDetector, s.SessionID,
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
				d.log.LogError(logComponentSessionDetector, s.SessionID,
					fmt.Sprintf("failed to apply subagent completion: %v", err))
				continue
			}
			d.log.LogInfo(logComponentSessionDetector, s.SessionID,
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

// holdParentWorkingForNewChild forces a parent session that is sitting at
// ready back to working the moment a new child of it is discovered.
//
// A background Workflow-tool run can legitimately have zero active children
// for a stretch: before its first subagent's transcript appears, or between
// pipeline stages (a stage's children finish and are cleaned up before the
// next stage's children are discovered). If the parent's own turn-done fires
// during one of those windows, hasActiveChildren finds nothing and the
// classifier flips the parent to ready — even though the child just
// discovered here proves the background job is still running. The child
// itself starts in the generic "ready until content proves otherwise" state
// too, so waiting for it to be classified as working is too slow; its mere
// existence as this parent's child is already sufficient evidence.
//
// This is the mirror image of reevaluateParent, which only ever pulls a
// held-working parent down to ready — it never pushes a wrongly-ready parent
// back up. See issue #889.
//
// Runs under WithSessionStateLock, matching every other load-modify-save of
// an already-existing SessionState in this package: the parent may have its
// own PID-discovery goroutine in flight (assignPIDLocked writes state.PID/
// UpdatedAt on the same shared pointer), and without the lock this would
// race it (issue #606).
func (d *SessionDetector) holdParentWorkingForNewChild(parentID string) {
	d.pidMgr.WithSessionStateLock(func() {
		parent, err := d.repo.Load(parentID)
		if err != nil || parent == nil || parent.State != session.StateReady {
			return
		}
		d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: parentID, PrevState: parent.State, NewState: session.StateWorking, Reason: "new child discovered while ready — holding working"})
		parent.State = session.StateWorking
		parent.UpdatedAt = time.Now().Unix()
		d.refreshSubagentSummary(parent)
		if err := d.repo.Save(parent); err != nil {
			d.log.LogError(logComponentSessionDetector, parentID,
				fmt.Sprintf("failed to hold parent working for new child: %v", err))
			return
		}
		d.log.LogInfo(logComponentSessionDetector, parentID,
			"holding parent working — new child discovered while ready")
		d.broadcast(outbound.PushTypeUpdated, parent)
	})
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
	// A child just changed state or was deleted: the parent's persisted
	// badge may be stale even when the parent itself won't transition —
	// e.g. the liveness sweep removing a child of a waiting/ready parent
	// (#593). Refresh, persist, and re-push, gated on the summary actually
	// changing so routine child events add no push traffic.
	//
	// Gate coupling: when this runs from a child's processActivity pass,
	// the child broadcast (session_detector_helpers.go) has already
	// refreshed this parent's summary in place — the repo shares
	// SessionState pointers — AND re-pushed the parent, so prevSummary
	// compares fresh-vs-fresh and the gate skips the duplicate push. The
	// skip path therefore depends on that child-broadcast parent refresh
	// staying in place (locked by the NoRedundantParentBroadcast storm-
	// guard test). With a deep-copy repo the gate would instead compare
	// against the persisted summary and fire on staleness — correct in
	// both worlds.
	prevSummary := parent.Subagents
	d.refreshSubagentSummary(parent)
	if !parent.Subagents.Equal(prevSummary) {
		if err := d.repo.Save(parent); err != nil {
			d.log.LogError(logComponentSessionDetector, parentID,
				fmt.Sprintf("failed to persist refreshed subagent summary: %v", err))
		}
		d.broadcast(outbound.PushTypeUpdated, parent)
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
		d.log.LogInfo(logComponentSessionDetector, parentID,
			fmt.Sprintf("children done, parent re-evaluated: %s", reason))
	}
	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: parentID, PrevState: parent.State, NewState: newState, Reason: reason})
	parent.State = newState
	parent.UpdatedAt = now
	if newState == session.StateWaiting {
		parent.WaitingStartTime = &now
	}

	if err := d.repo.Save(parent); err != nil {
		d.log.LogError(logComponentSessionDetector, parentID,
			fmt.Sprintf("failed to save parent re-evaluation: %v", err))
		return
	}
	d.broadcast(outbound.PushTypeUpdated, parent)

	// If the parent transitioned to ready, clean up its children — then
	// refresh, persist, and re-push the now-cleared summary so the final
	// parent message doesn't count children deleted a moment ago (#593).
	if parent.State == session.StateReady {
		prev := parent.Subagents
		d.pidMgr.cleanupChildren(parentID)
		d.refreshSubagentSummary(parent)
		if !parent.Subagents.Equal(prev) {
			if err := d.repo.Save(parent); err != nil {
				d.log.LogError(logComponentSessionDetector, parentID,
					fmt.Sprintf("failed to persist cleared subagent summary: %v", err))
			}
			d.broadcast(outbound.PushTypeUpdated, parent)
		}
	}
}
