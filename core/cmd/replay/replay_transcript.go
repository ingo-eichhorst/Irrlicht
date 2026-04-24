package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// rawEvent is one line from the source transcript paired with its parsed timestamp.
type rawEvent struct {
	Index int
	Bytes []byte // including trailing newline
	Time  time.Time
}

// replay runs the deterministic simulator on a transcript file and returns
// the resulting replayReport. It does not perform any wall-clock sleeps.
func replay(src string, cfg reportSettings) (*replayReport, error) {
	events, err := loadEvents(src)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("transcript is empty: %s", src)
	}

	r, cleanup, err := newTranscriptReplayer(src, cfg, events)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	batches := batchByDebounce(events, cfg.DebounceWindow)
	if err := r.runBatches(batches); err != nil {
		return nil, err
	}

	r.addDuration(r.state, r.report.Summary.LastEventTime.Sub(r.prevTransitionAt))
	finalizeSummary(r.report, r.consumed, r.stateDurations, r.lastMetrics)
	return r.report, nil
}

// transcriptReplayer bundles the mutable replay state (scratch transcript,
// tailer, running report, classifier state) so the batch loop can stay
// readable without a tangle of closures.
type transcriptReplayer struct {
	tmp              *os.File
	tailer           *tailer.TranscriptTailer
	report           *replayReport
	state            string
	prevTransitionAt time.Time
	stateDurations   map[string]time.Duration
	consumed         int
	lastMetrics      *tailer.SessionMetrics
}

// newTranscriptReplayer allocates the scratch transcript mirror, opens the
// tailer, seeds the report summary from the event window, and emits the
// initial-state transition. The returned cleanup removes the scratch dir
// and closes the tailer's file handle.
func newTranscriptReplayer(src string, cfg reportSettings, events []rawEvent) (*transcriptReplayer, func(), error) {
	tmpDir, err := os.MkdirTemp("", "irrlicht-replay-")
	if err != nil {
		return nil, nil, err
	}
	tmpPath := filepath.Join(tmpDir, "transcript.jsonl")
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.RemoveAll(tmpDir)
	}

	adapterName := cfg.Adapter
	if adapterName == "" {
		adapterName = claudecode.AdapterName
	}
	parser := parserFor(adapterName)
	t := tailer.NewTranscriptTailer(tmpPath, parser, adapterName)

	report := &replayReport{
		SchemaVersion:    1,
		SourceTranscript: src,
		GeneratedAt:      time.Now().UTC(),
		Settings:         cfg,
	}
	report.Summary.TotalEvents = len(events)
	report.Summary.FirstEventTime = events[0].Time
	report.Summary.LastEventTime = events[len(events)-1].Time
	report.Summary.WallClockDuration = report.Summary.LastEventTime.Sub(report.Summary.FirstEventTime)

	r := &transcriptReplayer{
		tmp:              tmp,
		tailer:           t,
		report:           report,
		state:            session.StateReady,
		prevTransitionAt: events[0].Time,
		stateDurations:   map[string]time.Duration{},
	}
	r.emit(transition{
		EventIndex:  -1,
		VirtualTime: events[0].Time,
		Cause:       causeInit,
		PrevState:   "",
		NewState:    r.state,
		Reason:      "initial state",
	})
	return r, cleanup, nil
}

// runBatches walks the debounced event groups, writing each batch's bytes to
// the scratch transcript, running the tailer + classifier once per batch,
// and emitting any state transitions the classifier produces.
//
// Batching approximates the daemon's debounce behaviour: inside the live
// SessionDetector each activity event is coalesced into the next
// processActivity call within the debounce window. Without a lifecycle-
// events sidecar we have no way to know where fswatcher split the writes,
// so we fall back to batching by transcript timestamp. The sidecar-driven
// replay path (see replayWithSidecar) gives byte-identical reproduction
// when the sidecar is present.
func (r *transcriptReplayer) runBatches(batches [][]rawEvent) error {
	for bi, batch := range batches {
		nextEventTime := batch[len(batch)-1].Time
		for _, ev := range batch {
			if _, err := r.tmp.Write(ev.Bytes); err != nil {
				return err
			}
			r.consumed++
		}
		metrics, err := r.tailer.TailAndProcess()
		if err != nil {
			return fmt.Errorf("batch %d: %w", bi, err)
		}
		r.lastMetrics = metrics
		cause := causeEvent
		if len(batch) > 1 {
			cause = causeDebounceCoalesce
		}
		r.classifyBatch(batch[len(batch)-1].Index, nextEventTime, cause, tailerToDomain(metrics))
	}
	return nil
}

// classifyBatch applies the state classifier to one batch's domain metrics,
// mirroring SessionDetector.processActivity's force-r→w + ClassifyState
// pattern. Any emitted transition is appended to the report.
func (r *transcriptReplayer) classifyBatch(eventIdx int, virtTime time.Time, cause transitionCause, domainMetrics *session.SessionMetrics) {
	if r.state == session.StateReady && domainMetrics.LastEventType != "" {
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, session.StateWorking, "force ready→working on first activity", domainMetrics))
		r.state = session.StateWorking
	}
	newState, reason := services.ClassifyState(r.state, domainMetrics)
	if services.ShouldSynthesizeCollapsedWaiting(r.state, newState, domainMetrics) {
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, session.StateWaiting, services.SyntheticWaitingReason, domainMetrics))
		r.state = session.StateWaiting
		newState, reason = services.ClassifyState(r.state, domainMetrics)
	}
	if newState != r.state {
		r.emit(transitionFromMetrics(eventIdx, virtTime, cause,
			r.state, newState, reason, domainMetrics))
		r.state = newState
	}
}

// emit appends a transition to the report and updates the running
// prev-state duration counter. Index is assigned here so Transitions is
// always densely numbered in emission order.
func (r *transcriptReplayer) emit(tr transition) {
	tr.Index = len(r.report.Transitions)
	r.report.Transitions = append(r.report.Transitions, tr)
	r.addDuration(tr.PrevState, tr.VirtualTime.Sub(r.prevTransitionAt))
	r.prevTransitionAt = tr.VirtualTime
}

// addDuration accumulates state-duration time against s, ignoring negative
// or zero deltas (which can occur when two batches share a timestamp).
func (r *transcriptReplayer) addDuration(s string, d time.Duration) {
	if d > 0 {
		r.stateDurations[s] += d
	}
}

func loadEvents(path string) ([]rawEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var out []rawEvent
	idx := 0
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		line = append(line, '\n')

		// Explicit timestamp only — do NOT use tailer.ParseTimestamp here
		// because it falls back to time.Now() when the field is missing,
		// which would pollute the sorted virtual timeline with wall-clock
		// values for metadata lines.
		var raw map[string]any
		ts := time.Time{}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err == nil {
			if v, ok := raw["timestamp"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339, v); err == nil {
					ts = parsed
				} else if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", v); err == nil {
					ts = parsed
				}
			}
		}

		out = append(out, rawEvent{
			Index: idx,
			Bytes: line,
			Time:  ts,
		})
		idx++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Resolve null-timestamp lines (summary / metadata) so they process
	// in-file-order alongside the surrounding real events.
	var lastTS time.Time
	for i := range out {
		if out[i].Time.IsZero() {
			out[i].Time = lastTS
		} else {
			lastTS = out[i].Time
		}
	}
	var firstTS time.Time
	for _, e := range out {
		if !e.Time.IsZero() {
			firstTS = e.Time
			break
		}
	}
	for i := range out {
		if out[i].Time.IsZero() {
			out[i].Time = firstTS
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	for i := range out {
		out[i].Index = i
	}
	return out, nil
}

func batchByDebounce(events []rawEvent, window time.Duration) [][]rawEvent {
	if window <= 0 || len(events) == 0 {
		out := make([][]rawEvent, len(events))
		for i, e := range events {
			out[i] = []rawEvent{e}
		}
		return out
	}

	var batches [][]rawEvent
	current := []rawEvent{events[0]}
	for i := 1; i < len(events); i++ {
		gap := events[i].Time.Sub(current[len(current)-1].Time)
		if gap < window {
			current = append(current, events[i])
		} else {
			batches = append(batches, current)
			current = []rawEvent{events[i]}
		}
	}
	batches = append(batches, current)
	return batches
}
