package replayengine

import (
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TailerToDomain converts the tailer's metrics struct into the domain type
// consumed by services.ClassifyState. Shared by the replay CLI (both its
// transcript and sidecar paths) and this engine so the conversion has a
// single definition.
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
		LastEventType:                     m.LastEventType,
		LastOpenToolNames:                 copyStrings(m.LastOpenToolNames),
		LastWasUserInterrupt:              m.LastWasUserInterrupt,
		LastWasToolDenial:                 m.LastWasToolDenial,
		EstimatedCostUSD:                  m.EstimatedCostUSD,
		CumInputTokens:                    m.CumInputTokens,
		CumOutputTokens:                   m.CumOutputTokens,
		CumCacheReadTokens:                m.CumCacheReadTokens,
		CumCacheCreationTokens:            m.CumCacheCreationTokens,
		LastAssistantText:                 m.LastAssistantText,
		PermissionMode:                    m.PermissionMode,
		SawUserBlockingToolClosedThisPass: m.SawUserBlockingToolClosedThisPass,
		NoSubstantiveActivity:             m.NoSubstantiveActivity,
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
