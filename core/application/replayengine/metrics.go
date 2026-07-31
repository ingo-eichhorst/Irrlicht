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

// convertSubagentCompletions copies the tailer's subagent completions into
// their domain counterparts, returning nil for an empty input so the domain
// field stays absent rather than becoming an empty slice.
func convertSubagentCompletions(in []tailer.SubagentCompletion) []session.SubagentCompletion {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.SubagentCompletion, len(in))
	for i, c := range in {
		out[i] = session.SubagentCompletion{
			AgentID:   c.AgentID,
			ToolUseID: c.ToolUseID,
			Status:    c.Status,
		}
	}
	return out
}

// convertAppliedTaskDeltas is convertSubagentCompletions' counterpart for the
// task deltas applied during the pass.
func convertAppliedTaskDeltas(in []tailer.AppliedTaskDelta) []session.AppliedTaskDelta {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.AppliedTaskDelta, len(in))
	for i, d := range in {
		out[i] = session.AppliedTaskDelta{
			Op:      d.Op,
			ID:      d.ID,
			Subject: d.Subject,
			Status:  d.Status,
		}
	}
	return out
}

// convertTasks is convertSubagentCompletions' counterpart for the task list.
func convertTasks(in []tailer.Task) []session.Task {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.Task, len(in))
	for i, t := range in {
		out[i] = session.Task{
			ID:          t.ID,
			Subject:     t.Subject,
			Description: t.Description,
			ActiveForm:  t.ActiveForm,
			Status:      t.Status,
			CompletedAt: t.CompletedAt,
		}
	}
	return out
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
		PendingBackgroundAgentCount:       m.PendingBackgroundAgentCount,
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
		PendingWaitingCue:                 m.PendingWaitingCue,
		PermissionMode:                    m.PermissionMode,
		SawUserBlockingToolClosedThisPass: m.SawUserBlockingToolClosedThisPass,
		NoSubstantiveActivity:             m.NoSubstantiveActivity,
		SawManualCompactBoundary:          m.SawManualCompactBoundary,
		SawMidPassTurnBoundary:            m.SawMidPassTurnBoundary,
		SubagentCompletions:               convertSubagentCompletions(m.SubagentCompletions),
		AppliedTaskDeltas:                 convertAppliedTaskDeltas(m.AppliedTaskDeltas),
		Tasks:                             convertTasks(m.Tasks),
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

	// Question source priority (issue #979): the agent's own marker always
	// wins; Claude Code's away_summary recap is a passive, higher-quality
	// upgrade over the raw last-assistant text for adapters/turns where the
	// agent never emitted a marker, but still loses to a marker that did fire.
	questionSource := result.LastAssistantText
	if m.AwaySummary != nil && m.AwaySummary.Text != "" {
		questionSource = m.AwaySummary.Text
	}
	markerAuthored := m.TaskQuestion != nil && m.TaskQuestion.Text != ""
	if markerAuthored {
		questionSource = m.TaskQuestion.Text
		// Only the deliberate irrlicht-question marker feeds the waiting-state
		// classifier (issue #1138) — not the away_summary recap (a passive
		// "here's what I was doing" note, not necessarily a question) and not
		// the LastAssistantText fallback (populated on nearly every turn). See
		// SessionMetrics.PendingQuestionMarker / IsWaitingForUserInput.
		result.PendingQuestionMarker = true
	}
	result.QuestionHeadline = mc.questionHeadline(questionSource, markerAuthored, m.FirstUserText)
	return result
}

// questionHeadline shapes the surfaced pending-question headline as
// "<3–5 word topic>: <question>" (issue #1186).
//
// An agent-authored marker already carries that shape, so it is compacted
// VERBATIM — no sentence selection, which would drop the topic off a
// multi-sentence marker. Otherwise the daemon composes the shape itself: it
// compacts the raw question, then prefixes a topic derived from the session's
// first user prompt, unless the question already leads with one. The prefix is
// a compaction-tier refinement — skipped on the identity path (nil compactor),
// where headlines intentionally carry the raw text, so the replay path is
// unchanged. The join is re-capped through the verbatim kind so the topic and
// question share one rune budget, trimming the question tail rather than the
// topic.
func (mc *MetricsConverter) questionHeadline(source string, markerAuthored bool, firstUserText string) string {
	if markerAuthored {
		return mc.compact(source, outbound.CompactQuestionVerbatim)
	}
	q := mc.compact(source, outbound.CompactQuestion)
	if q == "" || mc == nil || mc.compactor == nil {
		return q
	}
	topic := session.DeriveTopicPrefix(firstUserText)
	if topic == "" || session.QuestionHasTopicPrefix(q, topic) {
		return q
	}
	return mc.compact(topic+": "+q, outbound.CompactQuestionVerbatim)
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
