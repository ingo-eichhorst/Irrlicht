package replayengine

import (
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// MetricsConverter performs THE single tailer→domain conversion, shared by the
// replay CLI (both its transcript and sidecar paths), this engine, and the live
// metrics adapter — which layers its live-only enrichments (subagent counter,
// rate-limit + ETA forecasts, presentation defaults) on top. Plain field copies
// belong here so the live and replay paths can't drift (#604 review).
//
// It holds a TextCompactor (issue #759) used to derive the one-line
// intent/question headlines. A nil compactor (or the nil-receiver free function
// below) means identity compaction — the headlines carry the full text, which
// is what the replay paths and tests use.
type MetricsConverter struct {
	compactor outbound.TextCompactor
}

// NewMetricsConverter returns a converter that compacts headlines via c. Pass
// nil for identity (no compaction).
func NewMetricsConverter(c outbound.TextCompactor) *MetricsConverter {
	return &MetricsConverter{compactor: c}
}

// TailerToDomain is the identity-compaction convenience wrapper kept so replay
// call sites that don't compact headlines (engine, replay CLI, tests) compile
// unchanged. The live metrics adapter uses a NewMetricsConverter with the
// deterministic compactor instead.
func TailerToDomain(m *tailer.SessionMetrics) *session.SessionMetrics {
	return (&MetricsConverter{}).Convert(m)
}

// compact applies the converter's compactor, falling back to identity when the
// converter or its compactor is nil.
func (mc *MetricsConverter) compact(text string, kind outbound.CompactKind) string {
	if mc == nil || mc.compactor == nil {
		return text
	}
	return mc.compactor.Compact(text, kind)
}

// Convert maps the tailer's metrics struct into the domain type consumed by
// services.ClassifyState.
func (mc *MetricsConverter) Convert(m *tailer.SessionMetrics) *session.SessionMetrics {
	if m == nil {
		return nil
	}
	result := &session.SessionMetrics{
		ElapsedSeconds:                    m.ElapsedSeconds,
		TotalTokens:                       m.TotalTokens,
		ModelName:                         m.ModelName,
		AgentVersion:                      m.AgentVersion,
		ContextWindow:                     m.ContextWindow,
		ContextUtilization:                m.ContextUtilization,
		PressureLevel:                     m.PressureLevel,
		ContextWindowUnknown:              m.ContextWindowUnknown,
		HasOpenToolCall:                   m.HasOpenToolCall,
		OpenToolCallCount:                 m.OpenToolCallCount,
		BackgroundProcessCount:            m.BackgroundProcessCount,
		BackgroundProcessOutputs:          m.BackgroundProcessOutputs,
		BackgroundProcessPIDs:             m.BackgroundProcessPIDs,
		LastEventType:                     m.LastEventType,
		LastOpenToolNames:                 copyStrings(m.LastOpenToolNames),
		LastWasUserInterrupt:              m.LastWasUserInterrupt,
		LastWasToolDenial:                 m.LastWasToolDenial,
		EstimatedCostUSD:                  m.EstimatedCostUSD,
		EstimatedCO2Grams:                 m.EstimatedCO2Grams,
		CO2Tier:                           m.CO2Tier,
		CumInputTokens:                    m.CumInputTokens,
		CumOutputTokens:                   m.CumOutputTokens,
		CumCacheReadTokens:                m.CumCacheReadTokens,
		CumCacheCreationTokens:            m.CumCacheCreationTokens,
		LastCWD:                           m.LastCWD,
		LastAssistantText:                 m.LastAssistantText,
		PermissionMode:                    m.PermissionMode,
		SawUserBlockingToolClosedThisPass: m.SawUserBlockingToolClosedThisPass,
		NoSubstantiveActivity:             m.NoSubstantiveActivity,
		SawManualCompactBoundary:          m.SawManualCompactBoundary,
		SawMidPassTurnBoundary:            m.SawMidPassTurnBoundary,
	}
	if len(m.SubagentCompletions) > 0 {
		result.SubagentCompletions = make([]session.SubagentCompletion, len(m.SubagentCompletions))
		for i, c := range m.SubagentCompletions {
			result.SubagentCompletions[i] = session.SubagentCompletion{
				AgentID:   c.AgentID,
				ToolUseID: c.ToolUseID,
				Status:    c.Status,
			}
		}
	}
	if len(m.AppliedTaskDeltas) > 0 {
		result.AppliedTaskDeltas = make([]session.AppliedTaskDelta, len(m.AppliedTaskDeltas))
		for i, d := range m.AppliedTaskDeltas {
			result.AppliedTaskDeltas[i] = session.AppliedTaskDelta{
				Op:      d.Op,
				ID:      d.ID,
				Subject: d.Subject,
				Status:  d.Status,
			}
		}
	}
	if len(m.Tasks) > 0 {
		result.Tasks = make([]session.Task, len(m.Tasks))
		for i, t := range m.Tasks {
			result.Tasks[i] = session.Task{
				ID:          t.ID,
				Subject:     t.Subject,
				Description: t.Description,
				ActiveForm:  t.ActiveForm,
				Status:      t.Status,
				CompletedAt: t.CompletedAt,
			}
		}
	}
	// Task summary (issue #738): the agent's in-band marker wins; the first
	// user message is the heuristic fallback for agents that emit none. Both
	// are wall-clock independent, so the selection lives in this shared
	// plain-copy and surfaces identically in live and replay paths. Kept as the
	// full text (the sidebar tooltip); IntentHeadline is its compacted form.
	if m.TaskSummary != nil && m.TaskSummary.Text != "" {
		result.TaskSummary = m.TaskSummary.Text
	} else {
		result.TaskSummary = m.FirstUserText
	}

	// Headlines (issue #759): the terse one-line sidebar text. The full
	// TaskSummary / LastAssistantText are kept above for the hover tooltips —
	// this no longer overwrites LastAssistantText with the question snippet, so
	// the waiting-state classifier and tooltips see the complete text.
	result.IntentHeadline = mc.compact(result.TaskSummary, outbound.CompactIntent)
	questionSource := result.LastAssistantText
	if m.TaskQuestion != nil && m.TaskQuestion.Text != "" {
		questionSource = m.TaskQuestion.Text
	}
	result.QuestionHeadline = mc.compact(questionSource, outbound.CompactQuestion)
	return result
}

// copyTailerTaskEstimate copies a tailer task estimate struct so a timeline
// snapshot never aliases the tailer's mutable cumulative state (#753). Not a
// deep clone — the Confidence pointer is shared, which is safe: it's read-only
// once parsed.
func copyTailerTaskEstimate(e *tailer.TaskEstimate) *tailer.TaskEstimate {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

func copyStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}
