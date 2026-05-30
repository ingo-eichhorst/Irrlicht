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
			finalizeSummary(report, 0, map[string]time.Duration{}, tc.metrics)
			if !reflect.DeepEqual(report.Summary.Tasks, tc.want) {
				t.Fatalf("Summary.Tasks = %+v, want %+v", report.Summary.Tasks, tc.want)
			}
		})
	}
}
