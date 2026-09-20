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

	state.State = session.StateWorking
	working := sessionUpdateJSON(t, state)
	assertSessionUpdateMetricPresent(t, working, "task_estimate")
	assertSessionUpdateMetricPresent(t, working, "task_completion_eta")
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

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
