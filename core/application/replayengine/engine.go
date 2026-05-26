// Package replayengine is the single source of truth for turning a recorded
// transcript into a sequence of classifier-driven state transitions.
//
// Before this package existed there were two parallel implementations of
// "transcript → session state": the replay CLI (which produced the goldens)
// and the agent-onboarding viewer's fallback synthesizer (which fabricated a
// naive ready↔working arc with no waiting/permission semantics, and drifted
// silently whenever services.ClassifyState changed). Both now drive this one
// engine, so the viewer can never show a timeline the goldens wouldn't.
//
// The engine performs no wall-clock sleeps and is deterministic: it mirrors
// the daemon's debounce + force-ready→working + ClassifyState +
// ShouldSynthesizeCollapsedWaiting pipeline against a scratch copy of the
// transcript.
package replayengine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// Cause distinguishes why a state evaluation happened.
type Cause string

const (
	CauseInit             Cause = "init"
	CauseEvent            Cause = "event"
	CauseDebounceCoalesce Cause = "debounce_coalesce"
	// CauseIdleFlush is emitted when the engine forces the parser's
	// idleFlusher hook after all transcript batches have been consumed,
	// simulating the daemon's periodic poll catching the parser past its
	// idle threshold (currently aider-only).
	CauseIdleFlush Cause = "idle_flush"
)

// Transition is one classifier-driven state change.
type Transition struct {
	EventIndex  int
	VirtualTime time.Time
	Cause       Cause
	PrevState   string
	NewState    string
	Reason      string
	// Metrics is the domain metrics snapshot the classifier saw for this
	// transition. Nil for the synthetic initial-state transition (CauseInit).
	Metrics *session.SessionMetrics
}

// Options configures a transcript replay.
type Options struct {
	// Adapter is the canonical adapter name (e.g. "claude-code"); it is
	// passed through to the tailer for adapter-specific behaviour.
	Adapter string
	// Parser is the adapter's transcript parser. Required.
	Parser tailer.TranscriptParser
	// DebounceWindow coalesces transcript writes that land within the window
	// into one classifier pass, approximating the daemon's debounce.
	DebounceWindow time.Duration
	// DisableModelConfigFallback keeps replays reproducible across machines
	// by ignoring the operator's local model config (issue #440).
	DisableModelConfigFallback bool
	// EmitMetricsTimeline records a cumulative metrics snapshot after every
	// batch into Result.MetricsTimeline. Off by default so the transition
	// stream (and the replay CLI goldens) are untouched; the viewer turns it
	// on to animate cost/tokens across a recording's playhead.
	EmitMetricsTimeline bool
}

// MetricsSnapshot is the cumulative SessionMetrics observed up to a point in a
// replayed transcript, tagged with that point's transcript-relative timestamp.
// Emitted only when Options.EmitMetricsTimeline is set.
type MetricsSnapshot struct {
	VirtualTime time.Time
	Metrics     *session.SessionMetrics
}

// Result is the outcome of replaying a transcript.
type Result struct {
	Transitions    []Transition
	TotalEvents    int
	ConsumedEvents int
	FirstEventTime time.Time
	LastEventTime  time.Time
	FinalState     string
	// LastMetrics is the tailer metrics after the final pass, used by the
	// replay CLI to fill the report's cost/token summary.
	LastMetrics *tailer.SessionMetrics
	// MetricsTimeline holds one cumulative snapshot per batch (ascending by
	// VirtualTime) when Options.EmitMetricsTimeline is set; otherwise nil.
	MetricsTimeline []MetricsSnapshot
}

// rawEvent is one line from the source transcript paired with its timestamp.
type rawEvent struct {
	Index int
	Bytes []byte // including trailing newline
	Time  time.Time
}

// ReplayTranscript runs the deterministic simulator over the transcript at
// src and returns its classifier-driven transitions. It performs no
// wall-clock sleeps. Returns (nil, nil) when the transcript has no events.
func ReplayTranscript(src string, opts Options) (*Result, error) {
	events, err := loadEvents(src)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}

	r, cleanup, err := newReplayer(opts, events)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := r.runBatches(batchByDebounce(events, opts.DebounceWindow)); err != nil {
		return nil, err
	}

	r.result.TotalEvents = len(events)
	r.result.ConsumedEvents = r.consumed
	r.result.FirstEventTime = events[0].Time
	r.result.LastEventTime = events[len(events)-1].Time
	r.result.FinalState = r.state
	r.result.LastMetrics = r.lastMetrics
	return r.result, nil
}

// replayer bundles the mutable replay state so the batch loop stays readable.
type replayer struct {
	tmp         *os.File
	tailer      *tailer.TranscriptTailer
	result       *Result
	state        string
	consumed     int
	lastMetrics  *tailer.SessionMetrics
	emitTimeline bool
}

func newReplayer(opts Options, events []rawEvent) (*replayer, func(), error) {
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

	t := tailer.NewTranscriptTailer(tmpPath, opts.Parser, opts.Adapter)
	if opts.DisableModelConfigFallback {
		t.DisableModelConfigFallback()
	}

	r := &replayer{
		tmp:          tmp,
		tailer:       t,
		result:       &Result{},
		state:        session.StateReady,
		emitTimeline: opts.EmitMetricsTimeline,
	}
	// Synthetic initial-state transition mirrors the live detector seeding
	// a session as ready before any activity.
	r.result.Transitions = append(r.result.Transitions, Transition{
		EventIndex:  -1,
		VirtualTime: events[0].Time,
		Cause:       CauseInit,
		PrevState:   "",
		NewState:    r.state,
		Reason:      "initial state",
	})
	return r, cleanup, nil
}

// runBatches walks the debounced event groups, writing each batch's bytes to
// the scratch transcript, running the tailer + classifier once per batch, and
// recording any state transitions the classifier produces.
func (r *replayer) runBatches(batches [][]rawEvent) error {
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
		if r.emitTimeline {
			// TailerToDomain allocates a fresh struct per call, so the snapshot
			// never aliases the tailer's mutable cumulative state.
			r.result.MetricsTimeline = append(r.result.MetricsTimeline, MetricsSnapshot{
				VirtualTime: nextEventTime,
				Metrics:     TailerToDomain(metrics),
			})
		}
		cause := CauseEvent
		if len(batch) > 1 {
			cause = CauseDebounceCoalesce
		}
		r.classifyBatch(batch[len(batch)-1].Index, nextEventTime, cause, TailerToDomain(metrics))
	}

	// Force the parser's idle-flush hook after the final batch, mirroring the
	// daemon's periodic poll eventually crossing the parser's idle threshold.
	// No-op for parsers that don't implement idleFlusher (claudecode/codex/pi).
	if len(batches) > 0 {
		if metrics, flushed := r.tailer.FlushIdle(); flushed {
			r.lastMetrics = metrics
			last := batches[len(batches)-1]
			if r.emitTimeline {
				r.result.MetricsTimeline = append(r.result.MetricsTimeline, MetricsSnapshot{
					VirtualTime: last[len(last)-1].Time,
					Metrics:     TailerToDomain(metrics),
				})
			}
			r.classifyBatch(last[len(last)-1].Index, last[len(last)-1].Time, CauseIdleFlush, TailerToDomain(metrics))
		}
	}
	return nil
}

// classifyBatch applies the state classifier to one batch's domain metrics,
// mirroring SessionDetector.processActivity's force-r→w + ClassifyState
// pattern. Any emitted transition shares the batch's metrics snapshot.
func (r *replayer) classifyBatch(eventIdx int, virtTime time.Time, cause Cause, m *session.SessionMetrics) {
	if m == nil || m.NoSubstantiveActivity {
		return
	}
	if r.state == session.StateReady && m.LastEventType != "" {
		r.emit(eventIdx, virtTime, cause, r.state, session.StateWorking, services.ForceReadyToWorkingReason, m)
		r.state = session.StateWorking
	}
	newState, reason := services.ClassifyState(r.state, m)
	if services.ShouldSynthesizeCollapsedWaiting(r.state, newState, m) {
		r.emit(eventIdx, virtTime, cause, r.state, session.StateWaiting, services.SyntheticWaitingReason, m)
		r.state = session.StateWaiting
		newState, reason = services.ClassifyState(r.state, m)
	}
	if newState != r.state {
		r.emit(eventIdx, virtTime, cause, r.state, newState, reason, m)
		r.state = newState
	}
}

func (r *replayer) emit(eventIdx int, virtTime time.Time, cause Cause, prev, next, reason string, m *session.SessionMetrics) {
	r.result.Transitions = append(r.result.Transitions, Transition{
		EventIndex:  eventIdx,
		VirtualTime: virtTime,
		Cause:       cause,
		PrevState:   prev,
		NewState:    next,
		Reason:      reason,
		Metrics:     m,
	})
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
			} else if v, ok := raw["_ts"].(float64); ok && v > 0 {
				// OpenCode transcripts carry _ts as Unix milliseconds.
				ts = time.UnixMilli(int64(v)).UTC()
			}
		}

		out = append(out, rawEvent{Index: idx, Bytes: line, Time: ts})
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
