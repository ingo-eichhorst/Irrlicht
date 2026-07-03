package tailer

import "maps"

// GetLedgerState returns the durable accumulation state of the tailer so it
// can be persisted to disk and rehydrated after a daemon restart.
func (t *TranscriptTailer) GetLedgerState() LedgerState {
	s := LedgerState{
		SchemaVersion:      LedgerSchemaVersion,
		LastOffset:         t.lastOffset,
		CumProviderCostUSD: t.cumProviderCostUSD,
		ModelName:          t.metrics.ModelName,
		AgentVersion:       t.metrics.AgentVersion,
		LastEventType:      t.metrics.LastEventType,
		LastAssistantText:  t.lastAssistantText,
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
	if len(s.CumByModel) > 0 {
		// Deep-copy so the caller's map doesn't alias the tailer's.
		t.cumByModel = make(map[string]*UsageBreakdown, len(s.CumByModel))
		for k, v := range s.CumByModel {
			if v != nil {
				copied := *v
				t.cumByModel[k] = &copied
			}
		}
	}
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
	if s.ParserState != nil {
		if pp, ok := t.parser.(ParserStateProvider); ok {
			pp.SetParserLedger(*s.ParserState)
		}
	}
	if len(s.Tasks) > 0 {
		t.tasks = append([]Task(nil), s.Tasks...)
	}
	// Restore the provisional-ID counter from the persisted value, falling
	// back to len(Tasks) only for pre-#615 ledgers that didn't carry TaskSeq.
	// len(Tasks) alone understates the counter once completed batches have
	// been pruned, desyncing every later TaskCreate from Claude's monotonic
	// numbering (issue #615).
	t.taskSeq = max(s.TaskSeq, len(t.tasks))
	if len(s.PendingTaskCreates) > 0 {
		t.pendingTaskCreates = make(map[string]string, len(s.PendingTaskCreates))
		maps.Copy(t.pendingTaskCreates, s.PendingTaskCreates)
	}
	if len(s.BackgroundProcs) > 0 {
		t.openBackgroundProcs = make(map[string]string, len(s.BackgroundProcs))
		maps.Copy(t.openBackgroundProcs, s.BackgroundProcs)
	}
	if len(s.PendingBashPolls) > 0 {
		t.pendingBashPolls = make(map[string]string, len(s.PendingBashPolls))
		maps.Copy(t.pendingBashPolls, s.PendingBashPolls)
	}
	t.lastTaskEstimate = s.LastTaskEstimate
	t.firstTaskEstimate = s.FirstTaskEstimate
	t.lastTaskSummary = s.LastTaskSummary
	t.firstUserText = s.FirstUserText
	t.lastTaskQuestion = s.LastTaskQuestion
}
