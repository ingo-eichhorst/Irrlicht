package replayengine

import (
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TailerToDomain converts the tailer's metrics struct into the domain type
// consumed by services.ClassifyState. THE single tailer→domain plain-copy:
// shared by the replay CLI (both its transcript and sidecar paths), this
// engine, and the live metrics adapter — which layers its live-only
// enrichments (subagent counter, rate-limit + ETA forecasts, presentation
// defaults) on top. Plain field copies belong here so the live and replay
// paths can't drift (#604 review).
func TailerToDomain(m *tailer.SessionMetrics) *session.SessionMetrics {
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
	if snippet := session.ExtractQuestionSnippet(result.LastAssistantText); snippet != "" {
		result.LastAssistantText = snippet
	}
	// Task summary (issue #738): the agent's in-band marker wins; the first
	// user message is the heuristic fallback for agents that emit none. Both
	// are wall-clock independent, so the selection lives in this shared
	// plain-copy and surfaces identically in live and replay paths.
	if m.TaskSummary != nil && m.TaskSummary.Text != "" {
		result.TaskSummary = m.TaskSummary.Text
	} else {
		result.TaskSummary = m.FirstUserText
	}
	return result
}

// copyTailerTaskEstimate deep-copies a tailer task estimate so a timeline
// snapshot never aliases the tailer's mutable cumulative state (#753). The
// Confidence pointer is shared — it's read-only once parsed.
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
