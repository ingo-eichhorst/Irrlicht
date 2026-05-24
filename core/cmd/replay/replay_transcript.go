package main

import (
	"fmt"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
)

// replay runs the deterministic simulator on a transcript file and returns
// the resulting replayReport. It does not perform any wall-clock sleeps.
//
// The transcript → state-transition logic lives in core/application/
// replayengine, the single source of truth shared with the agent-onboarding
// viewer. This function only decorates the engine's transitions into the
// richer report shape (per-transition classifier snapshot, state durations,
// flicker + cost summary).
func replay(src string, cfg reportSettings) (*replayReport, error) {
	adapterName := cfg.Adapter
	if adapterName == "" {
		adapterName = claudecode.AdapterName
	}

	res, err := replayengine.ReplayTranscript(src, replayengine.Options{
		Adapter: adapterName,
		Parser:  parserFor(adapterName),
		// Replay must reflect only the transcript, never the operator's
		// local config, so goldens stay reproducible across machines (#440).
		DisableModelConfigFallback: true,
		DebounceWindow:             cfg.DebounceWindow,
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("transcript is empty: %s", src)
	}

	report := &replayReport{
		SchemaVersion:    1,
		SourceTranscript: src,
		GeneratedAt:      time.Now().UTC(),
		Settings:         cfg,
	}
	report.Summary.TotalEvents = res.TotalEvents
	report.Summary.FirstEventTime = res.FirstEventTime
	report.Summary.LastEventTime = res.LastEventTime
	report.Summary.WallClockDuration = res.LastEventTime.Sub(res.FirstEventTime)

	b := newReportBuilder(report, res.FirstEventTime)
	for _, t := range res.Transitions {
		b.add(t)
	}
	// Credit the final state with the tail of the session, matching the live
	// daemon's "duration since last transition" accounting.
	b.addDuration(res.FinalState, res.LastEventTime.Sub(b.prevTransitionAt))
	finalizeSummary(report, res.ConsumedEvents, b.stateDurations, res.LastMetrics)
	return report, nil
}

// reportBuilder turns the engine's ordered transitions into report rows,
// assigning dense indices and accumulating per-state durations exactly as the
// previous inline transcript replayer did.
type reportBuilder struct {
	report           *replayReport
	prevTransitionAt time.Time
	stateDurations   map[string]time.Duration
}

func newReportBuilder(report *replayReport, firstEventTime time.Time) *reportBuilder {
	return &reportBuilder{
		report:           report,
		prevTransitionAt: firstEventTime,
		stateDurations:   map[string]time.Duration{},
	}
}

// add maps one engine transition to a report row. The synthetic initial-state
// transition carries no metrics; every other transition carries the classifier
// snapshot fields via transitionFromMetrics.
func (b *reportBuilder) add(t replayengine.Transition) {
	var tr transition
	if t.Cause == replayengine.CauseInit {
		tr = transition{
			EventIndex:  t.EventIndex,
			VirtualTime: t.VirtualTime,
			Cause:       mapCause(t.Cause),
			PrevState:   t.PrevState,
			NewState:    t.NewState,
			Reason:      t.Reason,
		}
	} else {
		tr = transitionFromMetrics(t.EventIndex, t.VirtualTime, mapCause(t.Cause),
			t.PrevState, t.NewState, t.Reason, t.Metrics)
	}
	b.emit(tr)
}

// emit appends a transition to the report and updates the running prev-state
// duration counter. Index is assigned here so Transitions is always densely
// numbered in emission order.
func (b *reportBuilder) emit(tr transition) {
	tr.Index = len(b.report.Transitions)
	b.report.Transitions = append(b.report.Transitions, tr)
	b.addDuration(tr.PrevState, tr.VirtualTime.Sub(b.prevTransitionAt))
	b.prevTransitionAt = tr.VirtualTime
}

// addDuration accumulates state-duration time against s, ignoring negative or
// zero deltas (which can occur when two batches share a timestamp).
func (b *reportBuilder) addDuration(s string, d time.Duration) {
	if d > 0 {
		b.stateDurations[s] += d
	}
}

// mapCause translates an engine cause into the report's transitionCause.
// The string values are identical (so goldens are unaffected); the explicit
// switch keeps the report's cause enum the single documented contract.
func mapCause(c replayengine.Cause) transitionCause {
	switch c {
	case replayengine.CauseInit:
		return causeInit
	case replayengine.CauseDebounceCoalesce:
		return causeDebounceCoalesce
	case replayengine.CauseIdleFlush:
		return causeIdleFlush
	default:
		return causeEvent
	}
}
