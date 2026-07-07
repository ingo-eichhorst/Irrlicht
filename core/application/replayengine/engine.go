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
	"strings"
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
//
// TaskEstimate/TaskEstimateBase carry the raw tailer marker state (which
// TailerToDomain deliberately drops as a live-only enrichment) so the metrics
// adapter can layer the SAME completion forecast onto the timeline that the
// live path computes — only anchored at VirtualTime instead of wall-clock, so
// a replayed ETA is reproducible from the transcript alone (#753).
type MetricsSnapshot struct {
	VirtualTime      time.Time
	Metrics          *session.SessionMetrics
	TaskEstimate     *tailer.TaskEstimate
	TaskEstimateBase *tailer.TaskEstimate
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

	r, cleanup, err := newReplayer(src, opts, events)
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
	tmp          *os.File
	tailer       *tailer.TranscriptTailer
	result       *Result
	state        string
	consumed     int
	lastMetrics  *tailer.SessionMetrics
	emitTimeline bool
}

func newReplayer(src string, opts Options, events []rawEvent) (*replayer, func(), error) {
	tmpDir, err := os.MkdirTemp("", "irrlicht-replay-")
	if err != nil {
		return nil, nil, err
	}
	tmpPath := filepath.Join(tmpDir, "transcript.jsonl")
	// A parser whose live store sits outside the transcript tree (Antigravity's
	// conversations/<conv>.db, #766) rebuilds its expected layout under tmpDir
	// from a store captured next to the recorded transcript, and returns where
	// the transcript must live so its unchanged path-resolution finds the store.
	// Best-effort, like the sidecar staging below: any failure falls back to the
	// flat path, replaying storeless (the pre-#719 state).
	if st, ok := opts.Parser.(tailer.ReplayStoreStager); ok {
		if relocated, err := st.StageReplayStore(tmpDir, filepath.Dir(src)); err == nil && relocated != "" {
			tmpPath = relocated
		}
	}
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.RemoveAll(tmpDir)
	}

	// Stage the transcript's metadata sidecar (<base>.json) next to the temp
	// copy when one exists, so sidecar-reading parsers (Kiro CLI, #599) see
	// the same fields during replay as they do live.
	if strings.HasSuffix(src, ".jsonl") {
		sidecarSrc := strings.TrimSuffix(src, ".jsonl") + ".json"
		if body, err := os.ReadFile(sidecarSrc); err == nil {
			// Best-effort: a failed write just means replay runs sidecar-less,
			// exactly like a live session whose sidecar is missing.
			_ = os.WriteFile(filepath.Join(tmpDir, "transcript.json"), body, 0o644)
		}
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
		if err := r.writeBatch(batch); err != nil {
			return err
		}
		metrics, err := r.tailer.TailAndProcess()
		if err != nil {
			return fmt.Errorf("batch %d: %w", bi, err)
		}
		r.lastMetrics = metrics
		r.recordMetricsSnapshot(nextEventTime, metrics)
		cause := CauseEvent
		if len(batch) > 1 {
			cause = CauseDebounceCoalesce
		}
		r.classifyBatch(batch[len(batch)-1].Index, nextEventTime, cause, TailerToDomain(metrics))
	}

	r.flushIdleTail(batches)
	return nil
}

// writeBatch appends one debounced batch's raw transcript bytes to the
// scratch file, tracking how many source events have been consumed.
func (r *replayer) writeBatch(batch []rawEvent) error {
	for _, ev := range batch {
		if _, err := r.tmp.Write(ev.Bytes); err != nil {
			return err
		}
		r.consumed++
	}
	return nil
}

// recordMetricsSnapshot appends a cumulative metrics snapshot to the result's
// timeline when Options.EmitMetricsTimeline is set; otherwise it's a no-op.
// TailerToDomain allocates a fresh struct per call, so the snapshot never
// aliases the tailer's mutable cumulative state; the estimate pointers are
// copied for the same reason.
func (r *replayer) recordMetricsSnapshot(virtTime time.Time, metrics *tailer.SessionMetrics) {
	if !r.emitTimeline {
		return
	}
	r.result.MetricsTimeline = append(r.result.MetricsTimeline, MetricsSnapshot{
		VirtualTime:      virtTime,
		Metrics:          TailerToDomain(metrics),
		TaskEstimate:     copyTailerTaskEstimate(metrics.TaskEstimate),
		TaskEstimateBase: copyTailerTaskEstimate(metrics.TaskEstimateBase),
	})
}

// flushIdleTail forces the parser's idle-flush hook after the final batch,
// mirroring the daemon's periodic poll eventually crossing the parser's idle
// threshold. No-op for parsers that don't implement idleFlusher
// (claudecode/codex/pi), and when there were no batches to begin with.
func (r *replayer) flushIdleTail(batches [][]rawEvent) {
	if len(batches) == 0 {
		return
	}
	metrics, flushed := r.tailer.FlushIdle()
	if !flushed {
		return
	}
	r.lastMetrics = metrics
	last := batches[len(batches)-1]
	r.recordMetricsSnapshot(last[len(last)-1].Time, metrics)
	r.classifyBatch(last[len(last)-1].Index, last[len(last)-1].Time, CauseIdleFlush, TailerToDomain(metrics))
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

		out = append(out, rawEvent{Index: idx, Bytes: line, Time: parseEventTimestamp(scanner.Bytes())})
		idx++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	fillMissingTimestamps(out)

	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	for i := range out {
		out[i].Index = i
	}
	return out, nil
}

// parseEventTimestamp extracts one transcript line's explicit timestamp from
// its known field shapes. Returns the zero time when the line is unparsable
// JSON or carries none of the known fields — do NOT fall back to
// tailer.ParseTimestamp/time.Now() here, since that would pollute the sorted
// virtual timeline with wall-clock values for metadata lines.
func parseEventTimestamp(lineBytes []byte) time.Time {
	var raw map[string]any
	if err := json.Unmarshal(lineBytes, &raw); err != nil {
		return time.Time{}
	}

	if v, ok := raw["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
		if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", v); err == nil {
			return parsed
		}
		return time.Time{}
	}
	if v, ok := raw["_ts"].(float64); ok && v > 0 {
		// OpenCode transcripts carry _ts as Unix milliseconds.
		return time.UnixMilli(int64(v)).UTC()
	}
	if v, ok := raw["created_at"].(string); ok {
		// Antigravity steps carry an RFC3339 created_at.
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// fillMissingTimestamps resolves null-timestamp lines (summary / metadata) in
// place so they process in-file-order alongside the surrounding real events:
// each takes the most recent preceding real timestamp, and any run of
// zero-timestamp lines at the very start takes the first real timestamp in
// the slice.
func fillMissingTimestamps(events []rawEvent) {
	var lastTS time.Time
	for i := range events {
		if events[i].Time.IsZero() {
			events[i].Time = lastTS
		} else {
			lastTS = events[i].Time
		}
	}
	var firstTS time.Time
	for _, e := range events {
		if !e.Time.IsZero() {
			firstTS = e.Time
			break
		}
	}
	for i := range events {
		if events[i].Time.IsZero() {
			events[i].Time = firstTS
		}
	}
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
