package tailer

import "maps"

// GetLedgerState returns the durable accumulation state of the tailer so it
// can be persisted to disk and rehydrated after a daemon restart.
func (t *TranscriptTailer) GetLedgerState() LedgerState {
	s := LedgerState{
		SchemaVersion:               LedgerSchemaVersion,
		LastOffset:                  t.lastOffset,
		CumProviderCostUSD:          t.cumProviderCostUSD,
		ModelName:                   t.metrics.ModelName,
		AgentVersion:                t.metrics.AgentVersion,
		LastEventType:               t.metrics.LastEventType,
		LastAssistantText:           t.lastAssistantText,
		PendingBackgroundAgentCount: t.lastPendingBackgroundAgentCount,
	}
	if len(t.cumByModel) > 0 {
		// Direct assignment is safe: the caller JSON-marshals immediately
		// while holding the per-tailer lock, so TailAndProcess cannot run
		// concurrently and mutate the map during the marshal.
		s.CumByModel = t.cumByModel
	}
	if pp, ok := t.parser.(ParserStateProvider); ok {
		pl := pp.GetParserLedger()
		s.ParserState = &pl
	}
	if len(t.tasks) > 0 {
		s.Tasks = append([]Task(nil), t.tasks...)
	}
	s.TaskSeq = t.taskSeq
	if len(t.pendingTaskCreates) > 0 {
		ptc := make(map[string]string, len(t.pendingTaskCreates))
		maps.Copy(ptc, t.pendingTaskCreates)
		s.PendingTaskCreates = ptc
	}
	if len(t.openBackgroundProcs) > 0 {
		bp := make(map[string]string, len(t.openBackgroundProcs))
		maps.Copy(bp, t.openBackgroundProcs)
		s.BackgroundProcs = bp
	}
	if len(t.pendingBashPolls) > 0 {
		pp := make(map[string]string, len(t.pendingBashPolls))
		maps.Copy(pp, t.pendingBashPolls)
		s.PendingBashPolls = pp
	}
	// Estimate pointers are only ever reassigned (fresh allocations from
	// ScanTaskEstimate), never mutated in place, so direct assignment is
	// safe under the same marshal-while-locked guarantee as CumByModel.
	s.LastTaskEstimate = t.lastTaskEstimate
	s.FirstTaskEstimate = t.firstTaskEstimate
	s.LastTaskSummary = t.lastTaskSummary
	s.FirstUserText = t.firstUserText
	s.LastTaskQuestion = t.lastTaskQuestion
	return s
}

// SetLedgerState rehydrates accumulation state from a previously persisted
// ledger. Must be called before the first TailAndProcess; a no-op if the
// tailer has already processed any lines.
func (t *TranscriptTailer) SetLedgerState(s LedgerState) {
	if t.lastOffset != 0 {
		return
	}
	t.lastOffset = s.LastOffset
	t.restoreCumByModel(s.CumByModel)
	t.cumProviderCostUSD = s.CumProviderCostUSD
	if s.ModelName != "" {
		t.metrics.ModelName = s.ModelName
	}
	if s.AgentVersion != "" {
		t.metrics.AgentVersion = s.AgentVersion
	}
	// Restore the classification anchor: a resume-at-EOF pass processes no
	// events, and IsAgentDone needs the pre-restart event type to recognise
	// a finished turn (issue #649).
	if s.LastEventType != "" {
		t.metrics.LastEventType = s.LastEventType
	}
	// Restore the question text the same way: a resume-at-EOF pass would
	// otherwise leave it empty and IsWaitingForUserInput would mis-classify a
	// persisted `waiting` turn as `ready` (issue #705). Set both — t.metrics
	// directly so the value survives computeMetrics' empty-MessageHistory early
	// return on this resume-at-EOF pass (which skips the lastAssistantText
	// copy), and the private field so it stays the source of truth for the next
	// ledger write and for computeMetrics on later passes that carry new lines.
	if s.LastAssistantText != "" {
		t.lastAssistantText = s.LastAssistantText
		t.metrics.LastAssistantText = s.LastAssistantText
	}
	// Restore the background-agent hold: a resume-at-EOF pass reads no
	// turn_duration, so without this the #1037 guard goes inert on every
	// restart and the parent flips `ready` while agents are still running
	// (issue #1076). Only the private field needs setting — unlike
	// LastAssistantText above, surfaceSporadicMetrics copies it onto
	// t.metrics on every pass, above computeMetrics' empty-MessageHistory
	// early return.
	t.lastPendingBackgroundAgentCount = s.PendingBackgroundAgentCount
	t.restoreParserState(s.ParserState)
	if len(s.Tasks) > 0 {
		t.tasks = append([]Task(nil), s.Tasks...)
	}
	// Restore the provisional-ID counter from the persisted value, falling
	// back to len(Tasks) only for pre-#615 ledgers that didn't carry TaskSeq.
	// len(Tasks) alone understates the counter once completed batches have
	// been pruned, desyncing every later TaskCreate from Claude's monotonic
	// numbering (issue #615).
	t.taskSeq = max(s.TaskSeq, len(t.tasks))
	restoreStringMap(&t.pendingTaskCreates, s.PendingTaskCreates)
	restoreStringMap(&t.openBackgroundProcs, s.BackgroundProcs)
	restoreStringMap(&t.pendingBashPolls, s.PendingBashPolls)
	t.lastTaskEstimate = s.LastTaskEstimate
	t.firstTaskEstimate = s.FirstTaskEstimate
	t.lastTaskSummary = s.LastTaskSummary
	t.firstUserText = s.FirstUserText
	t.lastTaskQuestion = s.LastTaskQuestion
}

// restoreCumByModel deep-copies a persisted per-model usage breakdown into
// the tailer's live map so it doesn't alias the caller's.
func (t *TranscriptTailer) restoreCumByModel(m map[string]*UsageBreakdown) {
	if len(m) == 0 {
		return
	}
	t.cumByModel = make(map[string]*UsageBreakdown, len(m))
	for k, v := range m {
		if v != nil {
			copied := *v
			t.cumByModel[k] = &copied
		}
	}
}

// restoreParserState hands a persisted parser ledger back to a stateful
// parser, if it implements ParserStateProvider.
func (t *TranscriptTailer) restoreParserState(ps *ParserLedger) {
	if ps == nil {
		return
	}
	if pp, ok := t.parser.(ParserStateProvider); ok {
		pp.SetParserLedger(*ps)
	}
}

// restoreStringMap deep-copies src into a freshly allocated map assigned to
// *dst, leaving *dst untouched when src is empty. Shared by SetLedgerState's
// three identically-shaped string-to-string ledger maps.
func restoreStringMap(dst *map[string]string, src map[string]string) {
	if len(src) == 0 {
		return
	}
	*dst = make(map[string]string, len(src))
	maps.Copy(*dst, src)
}
