package opencode

import (
	"strings"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TestComputeMetrics_V2Store is #2106's red-first metrics test: on an opencode
// 2.x store ComputeMetrics reads the session's totals from session_v2, its
// model and context from the assistant rows, its open tools from content[]'s
// tool state, and its turn end and failure from the idle row. On main it
// returned nil for every 2.x store.
func TestComputeMetrics_V2Store(t *testing.T) {
	metricsFor := func(t *testing.T, w *Watcher, id string) *session.SessionMetrics {
		t.Helper()
		m, err := ComputeMetrics(w.transcriptPathFor(id), "")
		if err != nil {
			t.Fatalf("ComputeMetrics(%s): %v", id, err)
		}
		if m == nil {
			t.Fatalf("ComputeMetrics(%s) = nil, want the session's metrics", id)
		}
		return m
	}

	t.Run("completed_turn_totals_come_from_session_v2", func(t *testing.T) {
		w, db := newV2Store(t)
		loadV2Fixture(t, db, v2StoreRows)
		// The fixture's local model bills nothing; a cost written here proves
		// the column is what ComputeMetrics reports.
		execAll(t, db, `UPDATE session_v2 SET cost = 0.0421 WHERE id = '`+sesToolTurn+`'`)

		m := metricsFor(t, w, sesToolTurn)
		// session_v2 row: tokens_input 2254, tokens_output 370,
		// tokens_cache_read 23040 — the session's own totals, which include
		// the title call the assistant rows do not carry (the three rows sum
		// to 2219 input).
		if m.CumInputTokens != 2254 || m.CumOutputTokens != 370 || m.CumCacheReadTokens != 23040 {
			t.Errorf("cumulative tokens = in %d / out %d / cache-read %d, want 2254 / 370 / 23040",
				m.CumInputTokens, m.CumOutputTokens, m.CumCacheReadTokens)
		}
		if m.EstimatedCostUSD != 0.0421 {
			t.Errorf("EstimatedCostUSD = %v, want session_v2.cost 0.0421", m.EstimatedCostUSD)
		}
		if want := tailer.NormalizeModelName("qwen3.8-27b-splash"); m.ModelName != want {
			t.Errorf("ModelName = %q, want %q (the assistant row's data.model.id)", m.ModelName, want)
		}
		// The last assistant row's tokens: input 2024 + output 232 +
		// reasoning 389 + cache.read 7616 + cache.write 0.
		if m.TotalTokens != 10261 {
			t.Errorf("TotalTokens = %d, want 10261 (the latest step's context)", m.TotalTokens)
		}
		if m.LastEventType != "turn_done" || m.HasOpenToolCall || m.SessionError != nil {
			t.Errorf("LastEventType %q, HasOpenToolCall %t, SessionError %+v; want turn_done, false, nil",
				m.LastEventType, m.HasOpenToolCall, m.SessionError)
		}
		if !strings.Contains(m.LastAssistantText, "couldn't run `date` directly") {
			t.Errorf("LastAssistantText = %q, want the last step's text", m.LastAssistantText)
		}
		if m.LastCWD != fixtureDir {
			t.Errorf("LastCWD = %q, want %q", m.LastCWD, fixtureDir)
		}
	})

	t.Run("tool_held_on_a_permission_is_open", func(t *testing.T) {
		w, db := newV2Store(t)
		loadV2Fixture(t, db, v2StoreRows)
		holdBackAfter(t, db, sesPermission, 4)
		setToRunningTool(t, db, sesPermission)

		m := metricsFor(t, w, sesPermission)
		if !m.HasOpenToolCall || m.OpenToolCallCount != 1 || len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "shell" {
			t.Errorf("open tools = %t / %d / %v, want true / 1 / [shell]",
				m.HasOpenToolCall, m.OpenToolCallCount, m.LastOpenToolNames)
		}
		if m.LastEventType != "assistant_message" {
			t.Errorf("LastEventType = %q mid-step, want assistant_message", m.LastEventType)
		}
	})

	t.Run("failed_outcome_reports_the_idle_rows_error", func(t *testing.T) {
		w, db := newV2Store(t)
		loadV2Fixture(t, db, v2StoreRows)

		m := metricsFor(t, w, sesFailed)
		if m.LastEventType != "turn_done" {
			t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
		}
		se := m.SessionError
		if se == nil || se.Class != "provider.no-route" ||
			se.Message != "Model unavailable: local-splash/fleet-2106-no-such-model" ||
			se.Phase != session.ErrorPhaseTerminal {
			t.Errorf("SessionError = %+v, want the idle row's provider.no-route error, terminal", se)
		}
		// No assistant row exists; the model is the session row's.
		if want := tailer.NormalizeModelName("fleet-2106-no-such-model"); m.ModelName != want {
			t.Errorf("ModelName = %q, want %q (session_v2.model)", m.ModelName, want)
		}
	})

	t.Run("interrupted_outcome_ends_the_turn_without_an_error", func(t *testing.T) {
		w, db := newV2Store(t)
		loadV2Fixture(t, db, v2StoreRows)

		m := metricsFor(t, w, sesInterrupted)
		if m.LastEventType != "turn_done" || m.SessionError != nil {
			t.Errorf("LastEventType %q, SessionError %+v; want turn_done and no error (an interrupt is not a failure)",
				m.LastEventType, m.SessionError)
		}
	})

	t.Run("unknown_session_reads_nothing", func(t *testing.T) {
		w, db := newV2Store(t)
		loadV2Fixture(t, db, v2StoreRows)
		if m, err := ComputeMetrics(w.transcriptPathFor("ses_not_in_the_store"), ""); m != nil || err != nil {
			t.Errorf("ComputeMetrics(unknown) = %+v, %v; want nil, nil", m, err)
		}
	})
}
