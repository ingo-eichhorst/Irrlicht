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
	return result
}

func copyStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}
