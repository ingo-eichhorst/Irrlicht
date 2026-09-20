package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func TestSessionUpdatedTaskEstimateVisibilityFollowsState(t *testing.T) {
	state := taskEstimateReadyFixtureSession(t)
	ready := sessionUpdateJSON(t, state)
	assertSessionUpdateMetricAbsent(t, ready, "task_estimate")
	assertSessionUpdateMetricAbsent(t, ready, "task_completion_eta")
	if state.Metrics.TaskEstimate == nil || state.Metrics.TaskCompletionEta == nil {
		t.Fatal("ready session update mutated the source metrics")
	}

	state.State = session.StateWorking
	working := sessionUpdateJSON(t, state)
	assertSessionUpdateMetricPresent(t, working, "task_estimate")
	assertSessionUpdateMetricPresent(t, working, "task_completion_eta")
}

func TestDashboardReadyTaskEstimateVisibilityFollowsState(t *testing.T) {
	state := taskEstimateReadyFixtureSession(t)
	ready := dashboardJSON(t, state)
	assertDashboardMetricAbsent(t, ready, "task_estimate")
	assertDashboardMetricAbsent(t, ready, "task_completion_eta")
	if state.Metrics.TaskEstimate == nil || state.Metrics.TaskCompletionEta == nil {
		t.Fatal("ready sessions response mutated the source metrics")
	}

	state.State = session.StateWorking
	working := dashboardJSON(t, state)
	assertDashboardMetricPresent(t, working, "task_estimate")
	assertDashboardMetricPresent(t, working, "task_completion_eta")
}

func TestDashboardReadyParentHidesSubagentTaskEstimate(t *testing.T) {
	parent := taskEstimateReadyFixtureSession(t)
	parent.SessionID = "ready-parent"
	parent.Metrics.TaskEstimate = nil
	parent.Metrics.TaskCompletionEta = nil

	child := taskEstimateReadyFixtureSession(t)
	child.SessionID = "working-child"
	child.ParentSessionID = parent.SessionID
	child.State = session.StateWorking
	eta := int64(1_763_636_400)
	child.Metrics.TaskEstimate = &session.TaskEstimate{
		TotalRounds:     4,
		CompletedRounds: 1,
		Source:          "marker",
		UpdatedAt:       1_763_636_000,
	}
	child.Metrics.TaskCompletionEta = &eta

	b, err := json.Marshal(sessionsResponse{Groups: session.BuildDashboard([]*session.SessionState{parent, child}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(b, &response); err != nil {
		t.Fatal(err)
	}
	assertDashboardMetricAbsent(t, response, "task_estimate")
	assertDashboardMetricAbsent(t, response, "task_completion_eta")
	if parent.Metrics.TaskEstimate != nil || parent.Metrics.TaskCompletionEta != nil {
		t.Fatal("dashboard subagent enrichment mutated the ready parent source metrics")
	}
}

func taskEstimateReadyFixtureSession(t *testing.T) *session.SessionState {
	t.Helper()
	path := filepath.Join("..", "..", "..", "replaydata", "agents", "deepseek-harness", "scenarios", "5-8_task-estimate-marker", "recordings", "2026-09-19-04-55-38_irrlichd-0.6.4+e6443f6", "session_updates.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var frame outbound.PushMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == outbound.PushTypeUpdated && frame.Session != nil && frame.Session.State == session.StateReady && frame.Session.Metrics != nil && frame.Session.Metrics.TaskEstimate != nil && frame.Session.Metrics.TaskCompletionEta != nil {
			return frame.Session
		}
	}
	t.Fatal("session-update fixture has no ready frame with task estimate and completion ETA")
	return nil
}

func sessionUpdateJSON(t *testing.T, state *session.SessionState) map[string]any {
	t.Helper()
	b, err := json.Marshal(outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: state})
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := json.Unmarshal(b, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func dashboardJSON(t *testing.T, state *session.SessionState) map[string]any {
	t.Helper()
	b, err := json.Marshal(sessionsResponse{Groups: session.BuildDashboard([]*session.SessionState{state}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(b, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func assertSessionUpdateMetricAbsent(t *testing.T, frame map[string]any, field string) {
	t.Helper()
	metrics := sessionUpdateMetrics(t, frame)
	if _, ok := metrics[field]; ok {
		t.Fatalf("ready session_updated exposes metrics.%s: %s", field, mustJSON(t, frame))
	}
}

func assertSessionUpdateMetricPresent(t *testing.T, frame map[string]any, field string) {
	t.Helper()
	metrics := sessionUpdateMetrics(t, frame)
	if _, ok := metrics[field]; !ok {
		t.Fatalf("working session_updated omits metrics.%s: %s", field, mustJSON(t, frame))
	}
}

func sessionUpdateMetrics(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	state, ok := frame["session"].(map[string]any)
	if !ok {
		t.Fatalf("session_updated has no session: %s", mustJSON(t, frame))
	}
	metrics, ok := state["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("session_updated has no metrics: %s", mustJSON(t, frame))
	}
	return metrics
}

func assertDashboardMetricAbsent(t *testing.T, response map[string]any, field string) {
	t.Helper()
	metrics := dashboardMetrics(t, response)
	if _, ok := metrics[field]; ok {
		t.Fatalf("ready sessions response exposes metrics.%s: %s", field, mustJSON(t, response))
	}
}

func assertDashboardMetricPresent(t *testing.T, response map[string]any, field string) {
	t.Helper()
	metrics := dashboardMetrics(t, response)
	if _, ok := metrics[field]; !ok {
		t.Fatalf("working sessions response omits metrics.%s: %s", field, mustJSON(t, response))
	}
}

func dashboardMetrics(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	groups, ok := response["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("sessions response groups = %s", mustJSON(t, response))
	}
	group, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("sessions response group = %s", mustJSON(t, response))
	}
	agents, ok := group["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("sessions response agents = %s", mustJSON(t, response))
	}
	agent, ok := agents[0].(map[string]any)
	if !ok {
		t.Fatalf("sessions response agent = %s", mustJSON(t, response))
	}
	metrics, ok := agent["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("sessions response metrics = %s", mustJSON(t, response))
	}
	return metrics
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
