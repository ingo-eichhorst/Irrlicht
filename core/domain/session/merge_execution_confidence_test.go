package session

import "testing"

// TestMergeMetrics_CarriesExecutionConfidence is the #1256 allowlist trap
// aimed at #737's three fields: newMergedMetrics silently zeroes any field its
// literal omits, which would compute the score correctly in the tailer and
// the replay converter and then throw it away on the live merge path.
//
// The fields are copied VERBATIM, nil included: the tailer's accumulator is
// sticky for the session's life and republished every pass, so newM's value is
// always the current verdict — the PendingBackgroundAgentCount / SessionError
// shape, not a carry-forward.
func TestMergeMetrics_CarriesExecutionConfidence(t *testing.T) {
	var acc ExecutionConfidenceAccumulator
	acc.Observe(MeasureHedging("I'm not sure, and the spec is ambiguous. Maybe."))
	newM := &SessionMetrics{LastEventType: "assistant"}
	ApplyExecutionConfidence(newM, acc)
	if newM.ExecutionConfidence == nil || !newM.ExecutionConfidenceLow {
		t.Fatalf("precondition: want a low score on newM, got %+v", newM)
	}

	merged := MergeMetrics(newM, &SessionMetrics{LastEventType: "user"})

	if merged.ExecutionConfidence == nil || *merged.ExecutionConfidence != *newM.ExecutionConfidence {
		t.Fatalf("ExecutionConfidence dropped or changed by the merge: got %v, want %d — "+
			"missing from newMergedMetrics' allowlist", merged.ExecutionConfidence, *newM.ExecutionConfidence)
	}
	if !merged.ExecutionConfidenceLow {
		t.Error("ExecutionConfidenceLow dropped by the merge")
	}
	if merged.ExecutionConfidenceTooltip != newM.ExecutionConfidenceTooltip {
		t.Errorf("ExecutionConfidenceTooltip = %q, want %q", merged.ExecutionConfidenceTooltip, newM.ExecutionConfidenceTooltip)
	}
}
