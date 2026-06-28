package main

import (
	"reflect"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TestFinalizeSummaryTasks pins the task-list copy in finalizeSummary: the
// primary session's tailer.Task list is mapped into Summary.Tasks
// field-for-field and in order, while a nil or empty list leaves the field
// nil (so json:"tasks,omitempty" omits it). The golden fixtures cover the
// end-to-end replay; this guards the conversion itself against a refactor
// that drops the field without disturbing those specific goldens.
func TestFinalizeSummaryTasks(t *testing.T) {
	tests := []struct {
		name    string
		metrics *tailer.SessionMetrics
		want    []session.Task
	}{
		{
			name:    "nil metrics leaves tasks nil",
			metrics: nil,
			want:    nil,
		},
		{
			name:    "empty task list is omitted",
			metrics: &tailer.SessionMetrics{},
			want:    nil,
		},
		{
			name: "mixed statuses copied through in order",
			metrics: &tailer.SessionMetrics{
				Tasks: []tailer.Task{
					{ID: "1", Subject: "read README", Description: "Read the README file", ActiveForm: "Reading README", Status: tailer.TaskStatusCompleted},
					{ID: "2", Subject: "summarize README", Status: tailer.TaskStatusInProgress},
					{ID: "3", Subject: "reply done", Status: tailer.TaskStatusPending},
				},
			},
			want: []session.Task{
				{ID: "1", Subject: "read README", Description: "Read the README file", ActiveForm: "Reading README", Status: tailer.TaskStatusCompleted},
				{ID: "2", Subject: "summarize README", Status: tailer.TaskStatusInProgress},
				{ID: "3", Subject: "reply done", Status: tailer.TaskStatusPending},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := &replayReport{}
			finalizeSummary(report, 0, map[string]time.Duration{}, tc.metrics, "")
			if !reflect.DeepEqual(report.Summary.Tasks, tc.want) {
				t.Fatalf("Summary.Tasks = %+v, want %+v", report.Summary.Tasks, tc.want)
			}
		})
	}
}

// TestFinalizeSummaryStoreDerivedContext pins the #766 store-derived context
// surfacing: antigravity keeps token usage in an out-of-band SQLite store
// (#719), so finalizeSummary lifts TotalTokens/ContextWindow/ContextUtilization
// into the golden summary ONLY for that adapter — keeping every cum-token
// adapter's golden byte-identical. The end-to-end resolution from a captured
// store is proven by the antigravity replaystore test; this guards the gate.
func TestFinalizeSummaryStoreDerivedContext(t *testing.T) {
	storeMetrics := &tailer.SessionMetrics{
		TotalTokens:        16353,
		ContextWindow:      1048576,
		ContextUtilization: 1.56,
		ModelName:          "gemini-3.5-flash",
	}
	tests := []struct {
		name           string
		adapter        string
		metrics        *tailer.SessionMetrics
		wantTotalToks  int64
		wantCtxWindow  int64
		wantCtxUtilPos bool
	}{
		{name: "antigravity surfaces the store vector", adapter: "antigravity", metrics: storeMetrics, wantTotalToks: 16353, wantCtxWindow: 1048576, wantCtxUtilPos: true},
		{name: "other adapter leaves it zero", adapter: "claudecode", metrics: storeMetrics, wantTotalToks: 0, wantCtxWindow: 0, wantCtxUtilPos: false},
		{name: "antigravity with no store stays zero", adapter: "antigravity", metrics: &tailer.SessionMetrics{ModelName: "gemini-3.5-flash"}, wantTotalToks: 0, wantCtxWindow: 0, wantCtxUtilPos: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := &replayReport{}
			finalizeSummary(report, 0, map[string]time.Duration{}, tc.metrics, tc.adapter)
			if report.Summary.TotalTokens != tc.wantTotalToks {
				t.Errorf("TotalTokens = %d, want %d", report.Summary.TotalTokens, tc.wantTotalToks)
			}
			if report.Summary.ContextWindow != tc.wantCtxWindow {
				t.Errorf("ContextWindow = %d, want %d", report.Summary.ContextWindow, tc.wantCtxWindow)
			}
			if (report.Summary.ContextUtilization > 0) != tc.wantCtxUtilPos {
				t.Errorf("ContextUtilization = %g, want positive=%v", report.Summary.ContextUtilization, tc.wantCtxUtilPos)
			}
		})
	}
}
