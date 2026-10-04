package muse

import (
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// A retained frame folds several child events into one ParsedEvent; their
// execution-confidence samples must sum into that one event rather than the
// last child's sample winning (#737).
func TestMergeChildEvents_SumsHedgeSamples(t *testing.T) {
	merged := mergeChildEvents([]*tailer.ParsedEvent{
		{Hedge: &session.HedgeSample{Weight: 3, Words: 10}},
		{},
		{Hedge: &session.HedgeSample{Weight: 1, Words: 20}},
	})
	want := session.HedgeSample{Weight: 4, Words: 30}
	if merged.Hedge == nil {
		t.Fatal("merged Hedge = nil, want {4 30}")
	}
	if *merged.Hedge != want {
		t.Errorf("merged Hedge = %+v, want {4 30}", merged.Hedge)
	}
}
