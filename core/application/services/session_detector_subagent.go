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
		if !d.isOrphanedChild(s, parentID) {
			continue
		}
		d.promoteOrphanedChild(s, parentID, now)
	}
}

// isOrphanedChild reports whether s is a child of parentID that finished its
// work but never received a proper end_turn (the in-process-subagent quirk
// described in finishOrphanedChildren's doc), and is safe to promote: no
// open tool call, and quiet for at least SubagentQuietWindow so a
// still-running background agent isn't mistaken for orphaned mid-write.
func (d *SessionDetector) isOrphanedChild(s *session.SessionState, parentID string) bool {
	if s.ParentSessionID != parentID {
		return false
	}
	if s.State != session.StateWorking && s.State != session.StateWaiting {
		return false
	}
	if s.Metrics == nil || s.Metrics.HasOpenToolCall {
		return false
	}
	// Safety: a child whose transcript has been written in the last
	// SubagentQuietWindow is a background agent still mid-run — we don't
	// know whether the parent's "done" means "finished the subagents" or
	// "kicked off async background work". Leaving active children alone
	// avoids the bug where background agents are promoted and deleted
	// while still writing. If the stat fails (missing file), treat it as
	// not-quiet (conservative) — the liveness sweep will fall back to its
	// 2-minute window for anything we miss here.
	info, err := os.Stat(s.TranscriptPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) >= SubagentQuietWindow
}

// childReadyMessages carries the wording that differs between
// promoteOrphanedChild and applyOneSubagentCompletion — the lifecycle
// event's Reason, the save-error log format (one %v verb for the error),
// and the success log format (two %s verbs, in order: the child's
// prior state and parentID) — so promoteChildToReady can share their
// otherwise-identical mutate/save/broadcast tail.
type childReadyMessages struct {
	Reason      string
	ErrorFormat string
	InfoFormat  string
}

// promoteChildToReady transitions child session s to ready and persists +
// broadcasts the change, sharing the tail duplicated between
// promoteOrphanedChild and applyOneSubagentCompletion (both promote a
// finished child; they differ only in the recorded reason and log
// wording — see childReadyMessages). now is injected so every child
// promoted in the same caller pass gets an identical timestamp.
func (d *SessionDetector) promoteChildToReady(s *session.SessionState, parentID string, now int64, msgs childReadyMessages) {
	prev := s.State
	s.State = session.StateReady
	s.UpdatedAt = now
	s.WaitingStartTime = nil
	d.record(lifecycle.Event{
		Kind:      lifecycle.KindStateTransition,
		SessionID: s.SessionID,
		PrevState: prev,
		NewState:  session.StateReady,
		Reason:    msgs.Reason,
	})
	if err := d.repo.Save(s); err != nil {
		d.log.LogError(logComponentSessionDetector, s.SessionID,
			fmt.Sprintf(msgs.ErrorFormat, err))
		return
	}
	d.log.LogInfo(logComponentSessionDetector, s.SessionID,
		fmt.Sprintf(msgs.InfoFormat, prev, parentID))
	d.broadcast(outbound.PushTypeUpdated, s)
}

// promoteOrphanedChild transitions an orphaned child (see isOrphanedChild)
// to ready. now is injected so every child promoted in the same
// finishOrphanedChildren pass gets an identical timestamp.
func (d *SessionDetector) promoteOrphanedChild(s *session.SessionState, parentID string, now int64) {
	d.promoteChildToReady(s, parentID, now, childReadyMessages{
		Reason:      "subagent orphaned (parent turn done, no open tools)",
		ErrorFormat: "failed to finish orphaned child: %v",
		InfoFormat:  "finished orphaned subagent (%s → ready) — parent %s turn done",
	})
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
		if target := d.findCompletionTarget(states, parentID, suffix); target != nil {
			d.applyOneSubagentCompletion(target, parentID, now)
		}
	}
}

// findCompletionTarget locates the child session a subagent-completion
// notification refers to: a child of parentID whose transcript path ends in
// the agent's file suffix (agent-<AgentID>.jsonl — the layout Claude Code
// uses for subagent transcripts) that hasn't already left working/waiting.
// Returns nil if none matches — the no-op is safe, since
// finishOrphanedChildren still acts as a defensive fallback later.
func (d *SessionDetector) findCompletionTarget(states []*session.SessionState, parentID, suffix string) *session.SessionState {
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
		return s
	}
	return nil
}

// applyOneSubagentCompletion transitions a child session located by
// findCompletionTarget to ready.
func (d *SessionDetector) applyOneSubagentCompletion(s *session.SessionState, parentID string, now int64) {
	d.promoteChildToReady(s, parentID, now, childReadyMessages{
		Reason:      "subagent completed (parent task-notification)",
		ErrorFormat: "failed to apply subagent completion: %v",
		InfoFormat:  "subagent completed via parent task-notification (%s → ready, parent %s)",
	})
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

// holdIfChildrenActive fast-forwards any orphaned children of sessionID
// (see finishOrphanedChildren) and reports whether a genuinely active one
// remains. Shared by processActivity's ready and turn-done-waiting branches,
// which both hold the parent working identically when it does (#897) — a
// single call site keeps that hold logic from drifting out of sync between
// the two.
func (d *SessionDetector) holdIfChildrenActive(sessionID string) bool {
	d.finishOrphanedChildren(sessionID)
	return d.hasActiveChildren(sessionID)
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
	d.refreshParentSummaryIfChanged(parent, parentID)

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
	d.transitionParentAfterChildrenDone(parent, parentID)
}

// refreshParentSummaryIfChanged refreshes parentID's subagent summary and
// re-persists/re-broadcasts it if it changed. A child just changed state or
// was deleted: the parent's persisted badge may be stale even when the
// parent itself won't transition — e.g. the liveness sweep removing a child
// of a waiting/ready parent (#593). Gated on the summary actually changing
// so routine child events add no push traffic.
//
// Gate coupling: when this runs from a child's processActivity pass, the
// child broadcast (session_detector_helpers.go) has already refreshed this
// parent's summary in place — the repo shares SessionState pointers — AND
// re-pushed the parent, so prevSummary compares fresh-vs-fresh and the gate
// skips the duplicate push. The skip path therefore depends on that
// child-broadcast parent refresh staying in place (locked by the
// NoRedundantParentBroadcast storm-guard test). With a deep-copy repo the
// gate would instead compare against the persisted summary and fire on
// staleness — correct in both worlds.
func (d *SessionDetector) refreshParentSummaryIfChanged(parent *session.SessionState, parentID string) {
	d.persistSummaryIfChanged(parent, parentID, nil, "failed to persist refreshed subagent summary: %v")
}

// persistSummaryIfChanged snapshots parent.Subagents, runs an optional extra
// step (before, e.g. deleting finished children) followed by
// refreshSubagentSummary, and persists + broadcasts only if the summary
// actually changed — shared by refreshParentSummaryIfChanged and
// cleanupParentChildrenOnReady, which differ only in whether they first
// clean up children and in their save-error log wording (errFormat takes a
// single %v verb for the error). Neither caller treats a save failure as
// fatal to the broadcast — matching both functions' original behavior of
// still pushing the in-memory update even if persistence failed.
func (d *SessionDetector) persistSummaryIfChanged(parent *session.SessionState, parentID string, before func(), errFormat string) {
	prevSummary := parent.Subagents
	if before != nil {
		before()
	}
	d.refreshSubagentSummary(parent)
	if parent.Subagents.Equal(prevSummary) {
		return
	}
	if err := d.repo.Save(parent); err != nil {
		d.log.LogError(logComponentSessionDetector, parentID,
			fmt.Sprintf(errFormat, err))
	}
	d.broadcast(outbound.PushTypeUpdated, parent)
}

// transitionParentAfterChildrenDone applies the parent's own state
// transition once its turn is done and no active children remain, then — if
// it lands on ready — cleans up its now-finished children.
func (d *SessionDetector) transitionParentAfterChildrenDone(parent *session.SessionState, parentID string) {
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
		d.cleanupParentChildrenOnReady(parent, parentID)
	}
}

// cleanupParentChildrenOnReady deletes parentID's now-finished children and
// re-persists/re-broadcasts the cleared subagent summary, so the parent's
// final message doesn't count children that were just deleted (#593).
func (d *SessionDetector) cleanupParentChildrenOnReady(parent *session.SessionState, parentID string) {
	d.persistSummaryIfChanged(parent, parentID, func() { d.pidMgr.cleanupChildren(parentID) }, "failed to persist cleared subagent summary: %v")
}
