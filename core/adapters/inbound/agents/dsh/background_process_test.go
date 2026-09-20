package dsh

import (
	"reflect"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// TestParserMapsMeasuredBackgroundJobSignals is red against the pre-fix parser:
// it ignored the measured DSH run_in_background launch, result, and tool-jobs
// terminal notice in the 3.3 recording.
func TestParserMapsMeasuredBackgroundJobSignals(t *testing.T) {
	events := parseFixture(t, "testdata/background-process.jsonl")
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if got := events[1].BackgroundSpawns; !reflect.DeepEqual(got, []tailer.BackgroundSpawn{{BashID: "bash-1"}}) {
		t.Fatalf("background spawns = %+v, want bash-1", got)
	}
	if !events[2].Skip {
		t.Fatalf("terminal plugin notice must be bookkeeping, got %+v", events[2])
	}
	if got := events[2].TerminatedBackgroundTaskIDs; !reflect.DeepEqual(got, []string{"bash-1"}) {
		t.Fatalf("terminal background jobs = %v, want [bash-1]", got)
	}
}

// TestParserRejectsUncorrelatedBackgroundStart is the committed mutation
// fixture: changing the result's callId prevents the matching background call
// from proving that bash-1 was launched.
func TestParserRejectsUncorrelatedBackgroundStart(t *testing.T) {
	events := parseFixture(t, "testdata/background-process-uncorrelated-result.jsonl")
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if got := events[1].BackgroundSpawns; len(got) != 0 {
		t.Fatalf("uncorrelated result created background spawns: %+v", got)
	}
}
