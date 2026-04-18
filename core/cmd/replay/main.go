// replay is an offline simulator that takes a Claude Code transcript .jsonl
// file (or a lifecycle-events sidecar) and replays it through the production
// tailer + state classifier using virtual time.
//
// It consolidates the former replay-session and replay-lifecycle tools into a
// single binary with two replay paths:
//
//   - Sidecar-driven (primary): when a .events.jsonl sidecar is present or
//     passed directly, the replay is driven by the lifecycle recording —
//     fswatcher fires, process events, hook events — for full-fidelity state
//     machine reproduction.
//   - Transcript-only (fallback): when no sidecar exists, events are batched
//     by timestamp and debounced, approximating what the daemon would have
//     seen.
//
// Usage:
//
//	go run ./core/cmd/replay [flags] INPUT.jsonl
//
// INPUT can be a transcript .jsonl or a sidecar .events.jsonl.
//
// Flags:
//
//	--out FILE              Write JSON report to FILE (default stdout).
//	--adapter NAME          Adapter name (claude-code, codex, pi); auto-detected from path if omitted.
//	--session ID            Filter sidecar events to a single session (multi-session recordings).
//	--debounce DURATION     Simulated activity debounce window. Default 2s.
//	--flicker-max DURATION  Episodes shorter than this are counted as flickers. Default 10s.
//	--quiet                 Suppress per-event progress on stderr.
//
// The report is a JSON object containing every state transition (with reason,
// virtual timestamp, event index, and a metric snapshot) plus a flicker
// summary. Pipe through `jq` or feed to the bundled visualizer.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// detectAdapter infers the adapter name from a transcript path by matching
// either the canonical session-storage root for each supported format or the
// repo-relative testdata/replay/<adapter>/ fixture layout.
func detectAdapter(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	switch {
	case strings.Contains(abs, "/.claude/projects/"),
		strings.Contains(abs, "/testdata/replay/claudecode/"):
		return claudecode.AdapterName, nil
	case strings.Contains(abs, "/.codex/sessions/"),
		strings.Contains(abs, "/testdata/replay/codex/"):
		return codex.AdapterName, nil
	case strings.Contains(abs, "/.pi/agent/sessions/"),
		strings.Contains(abs, "/.pi/sessions/"),
		strings.Contains(abs, "/testdata/replay/pi/"):
		return pi.AdapterName, nil
	}
	return "", fmt.Errorf("cannot infer adapter from path %q — pass --adapter claude-code|codex|pi", path)
}

// Cause distinguishes why a state evaluation happened.
type Cause string

const (
	CauseInit             Cause = "init"
	CauseEvent            Cause = "event"
	CauseDebounceCoalesce Cause = "debounce_coalesce"
	CauseHook             Cause = "hook"
)

// Transition is a single recorded state change emitted by the replay.
type Transition struct {
	Index         int       `json:"index"`
	EventIndex    int       `json:"event_index"`
	VirtualTime   time.Time `json:"virtual_time"`
	Cause         Cause     `json:"cause"`
	PrevState     string    `json:"prev_state"`
	NewState      string    `json:"new_state"`
	Reason        string    `json:"reason"`
	LastEventType string    `json:"last_event_type"`
	HasOpenTool   bool      `json:"has_open_tool"`
	OpenToolNames []string  `json:"open_tool_names,omitempty"`
	IsAgentDone   bool      `json:"is_agent_done"`
	NeedsAttn     bool      `json:"needs_user_attention"`
	WaitingQuery  bool      `json:"waiting_for_user_input"`
	LastTextHead  string    `json:"last_assistant_text_head,omitempty"`
}

// transitionFromMetrics builds a Transition populated with classifier snapshot
// fields from domainMetrics. Callers supply the event-specific fields.
func transitionFromMetrics(eventIdx int, virtTime time.Time, cause Cause, prevState, newState, reason string, m *session.SessionMetrics) Transition {
	return Transition{
		EventIndex:    eventIdx,
		VirtualTime:   virtTime,
		Cause:         cause,
		PrevState:     prevState,
		NewState:      newState,
		Reason:        reason,
		LastEventType: m.LastEventType,
		HasOpenTool:   m.HasOpenToolCall,
		OpenToolNames: copyStrings(m.LastOpenToolNames),
		IsAgentDone:   m.IsAgentDone(),
		NeedsAttn:     m.NeedsUserAttention(),
		WaitingQuery:  m.IsWaitingForUserInput(),
		LastTextHead:  head(m.LastAssistantText, 80),
	}
}

// Report is the top-level structure written to the output file.
type Report struct {
	SchemaVersion    int            `json:"schema_version"`
	SourceTranscript string         `json:"source_transcript"`
	GeneratedAt      time.Time      `json:"generated_at"`
	Settings         ReportSettings `json:"settings"`
	Summary          ReportSummary  `json:"summary"`
	Transitions      []Transition   `json:"transitions"`

	// Sessions is populated when a sidecar is present and provides per-session
	// aggregate statistics (event counts, state durations, PID discovery lag,
	// debounce stats). Nil for transcript-only replays.
	Sessions []SessionTimeline `json:"sessions,omitempty"`

	// ExtendedCheck diffs the replayed state transitions against the recorded
	// ones so fixtures act as regression tests for the detector.
	ExtendedCheck *ExtendedCheck `json:"extended_check,omitempty"`
}

// SessionTimeline is a per-session summary within the report, populated from
// the lifecycle sidecar when available.
type SessionTimeline struct {
	SessionID       string           `json:"session_id"`
	Adapter         string           `json:"adapter,omitempty"`
	ParentSessionID string           `json:"parent_session_id,omitempty"`
	FirstSeen       time.Time        `json:"first_seen"`
	LastSeen        time.Time        `json:"last_seen"`
	DurationMs      int64            `json:"duration_ms"`
	EventCount      int              `json:"event_count"`
	StateChanges    int              `json:"state_changes"`
	FinalState      string           `json:"final_state,omitempty"`
	PID             int              `json:"pid,omitempty"`
	PIDDiscoveryMs  int64            `json:"pid_discovery_lag_ms,omitempty"`
	DebounceEvents  int              `json:"debounce_coalesced_events"`
	StateDurations  map[string]int64 `json:"state_durations_ms"`
}

// ExtendedCheck compares the replayed state transitions against a committed
// lifecycle recording (.events.jsonl sidecar produced by `irrlichd --record`).
type ExtendedCheck struct {
	SidecarPath         string               `json:"sidecar_path"`
	RecordedCount       int                  `json:"recorded_transition_count"`
	ReplayedCount       int                  `json:"replayed_transition_count"`
	OrderedMatches      int                  `json:"ordered_matches"`
	OrderedMismatches   []TransitionMismatch `json:"ordered_mismatches,omitempty"`
	RecordedUniqueKinds []string             `json:"recorded_unique_kinds"`
	ReplayedUniqueKinds []string             `json:"replayed_unique_kinds"`
	MissingKinds        []string             `json:"missing_kinds,omitempty"`
	ExtraKinds          []string             `json:"extra_kinds,omitempty"`
}

// TransitionMismatch is a single divergence between replayed and recorded
// state transitions.
type TransitionMismatch struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"` // "missing_in_replay" | "extra_in_replay" | "state_differs"
	Recorded string `json:"recorded,omitempty"`
	Replayed string `json:"replayed,omitempty"`
}

type ReportSettings struct {
	Adapter            string        `json:"adapter"`
	SessionFilter      string        `json:"session_filter,omitempty"`
	DebounceWindow     time.Duration `json:"debounce_window"`
	FlickerMaxDuration time.Duration `json:"flicker_max_duration"`
}

type ReportSummary struct {
	TotalEvents       int                      `json:"total_events"`
	ConsumedEvents    int                      `json:"consumed_events"`
	TotalTransitions  int                      `json:"total_transitions"`
	FirstEventTime    time.Time                `json:"first_event_time"`
	LastEventTime     time.Time                `json:"last_event_time"`
	WallClockDuration time.Duration            `json:"wall_clock_session_duration"`
	StateDurations    map[string]time.Duration `json:"state_durations"`

	FlickerCount       int            `json:"flicker_count"`
	FlickersByCategory map[string]int `json:"flickers_by_category"`
	FlickersByReason   map[string]int `json:"flickers_by_reason"`

	EstimatedCostUSD       float64 `json:"estimated_cost_usd,omitempty"`
	CumInputTokens         int64   `json:"cum_input_tokens,omitempty"`
	CumOutputTokens        int64   `json:"cum_output_tokens,omitempty"`
	CumCacheReadTokens     int64   `json:"cum_cache_read_tokens,omitempty"`
	CumCacheCreationTokens int64   `json:"cum_cache_creation_tokens,omitempty"`
	ModelName              string  `json:"model_name,omitempty"`
}

// finalizeSummary fills the report's computed summary fields (consumed events,
// transitions, flickers, cost) from the replay state. Both Replay and
// ReplayWithSidecar call this at the end to avoid duplicating the logic.
func finalizeSummary(report *Report, consumed int, stateDurations map[string]time.Duration, lastMetrics *tailer.SessionMetrics) {
	report.Summary.ConsumedEvents = consumed
	report.Summary.TotalTransitions = len(report.Transitions)
	report.Summary.StateDurations = stateDurations

	flickerCat, flickerReason, flickerTotal := computeFlickers(
		report.Transitions, report.Settings.FlickerMaxDuration)
	report.Summary.FlickerCount = flickerTotal
	report.Summary.FlickersByCategory = flickerCat
	report.Summary.FlickersByReason = flickerReason

	if lastMetrics != nil {
		report.Summary.EstimatedCostUSD = lastMetrics.EstimatedCostUSD
		report.Summary.CumInputTokens = lastMetrics.CumInputTokens
		report.Summary.CumOutputTokens = lastMetrics.CumOutputTokens
		report.Summary.CumCacheReadTokens = lastMetrics.CumCacheReadTokens
		report.Summary.CumCacheCreationTokens = lastMetrics.CumCacheCreationTokens
		report.Summary.ModelName = lastMetrics.ModelName
	}
}

func main() {
	var (
		outPath      string
		adapterFlag  string
		sessionFlag  string
		debounceFlag time.Duration
		flickerMax   time.Duration
		quiet        bool
	)
	flag.StringVar(&outPath, "out", "", "output JSON report path (default: stdout)")
	flag.StringVar(&adapterFlag, "adapter", "", "adapter name (claude-code, codex, pi); auto-detected from path if omitted")
	flag.StringVar(&sessionFlag, "session", "", "filter sidecar events to a single session ID")
	flag.DurationVar(&debounceFlag, "debounce", 2*time.Second, "simulated activity debounce window")
	flag.DurationVar(&flickerMax, "flicker-max", 10*time.Second, "episodes shorter than this are counted as flickers (automated agent loops cycle turns in ~25s, so 30s overcounts)")
	flag.BoolVar(&quiet, "quiet", false, "suppress per-event progress on stderr")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay [flags] INPUT.jsonl")
		flag.PrintDefaults()
		os.Exit(2)
	}
	src := flag.Arg(0)

	// Resolve input: the argument can be a transcript .jsonl or a sidecar
	// .events.jsonl. When a sidecar is given directly, derive the transcript
	// path; when a transcript is given, auto-detect a sibling sidecar.
	var transcriptPath, sidecarPath string
	useSidecar := false

	if strings.HasSuffix(src, ".events.jsonl") {
		sidecarPath = src
		transcriptPath = strings.TrimSuffix(src, ".events.jsonl") + ".jsonl"
		useSidecar = true
	} else {
		transcriptPath = src
		candidate := strings.TrimSuffix(src, ".jsonl") + ".events.jsonl"
		if _, err := os.Stat(candidate); err == nil {
			sidecarPath = candidate
			useSidecar = true
		}
	}

	adapterName := adapterFlag
	if adapterName == "" {
		var err error
		adapterName, err = detectAdapter(transcriptPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	cfg := ReportSettings{
		Adapter:            adapterName,
		SessionFilter:      sessionFlag,
		DebounceWindow:     debounceFlag,
		FlickerMaxDuration: flickerMax,
	}

	var (
		report    *Report
		replayErr error
	)
	if useSidecar {
		report, replayErr = ReplayWithSidecar(transcriptPath, sidecarPath, cfg)
	} else {
		if sessionFlag != "" {
			fmt.Fprintln(os.Stderr, "--session requires a sidecar (.events.jsonl); no sidecar found")
			os.Exit(2)
		}
		report, replayErr = Replay(transcriptPath, cfg)
	}
	if replayErr != nil {
		fmt.Fprintln(os.Stderr, "replay failed:", replayErr)
		os.Exit(1)
	}

	if useSidecar {
		check, err := runExtendedCheck(sidecarPath, report.Transitions)
		if err != nil {
			fmt.Fprintln(os.Stderr, "extended check failed:", err)
			os.Exit(1)
		}
		report.ExtendedCheck = check
	}

	enc := json.NewEncoder(chooseOutput(outPath))
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}

	if !quiet {
		s := report.Summary
		fmt.Fprintf(os.Stderr,
			"replay: %d events → %d transitions, %d flickers (ww=%d wr=%d rw=%d)",
			s.TotalEvents, s.TotalTransitions, s.FlickerCount,
			s.FlickersByCategory["working_between_waiting"]+s.FlickersByCategory["waiting_between_working"],
			s.FlickersByCategory["working_between_ready"]+s.FlickersByCategory["ready_between_working"],
			s.FlickersByCategory["ready_between_waiting"]+s.FlickersByCategory["waiting_between_ready"])
		if c := report.ExtendedCheck; c != nil {
			kindsMark := "ok"
			if len(c.MissingKinds) > 0 || len(c.ExtraKinds) > 0 {
				kindsMark = "FAIL"
			}
			orderMark := "ok"
			if len(c.OrderedMismatches) > 0 {
				orderMark = "FAIL"
			}
			fmt.Fprintf(os.Stderr, " [extended-check: kinds %s ordered %d/%d %s]",
				kindsMark, c.OrderedMatches, c.RecordedCount, orderMark)
		}
		fmt.Fprintln(os.Stderr)
	}

	if c := report.ExtendedCheck; c != nil {
		if len(c.OrderedMismatches) > 0 || len(c.MissingKinds) > 0 || len(c.ExtraKinds) > 0 {
			os.Exit(1)
		}
	}
}

func chooseOutput(path string) *os.File {
	if path == "" {
		return os.Stdout
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create output:", err)
		os.Exit(1)
	}
	return f
}

// rawEvent is one line from the source transcript paired with its parsed timestamp.
type rawEvent struct {
	Index int
	Bytes []byte // including trailing newline
	Time  time.Time
}

// Replay runs the deterministic simulator on a transcript file and returns the
// resulting Report. It does not perform any wall-clock sleeps.
func Replay(src string, cfg ReportSettings) (*Report, error) {
	events, err := loadEvents(src)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("transcript is empty: %s", src)
	}

	// Group events into "batches" using the debounce window. Inside the
	// SessionDetector each activity event would be coalesced into the next
	// processActivity call within the debounce window. We mimic that here so
	// the tailer/classifier sees the same compressed event stream.
	//
	// Note: this is a coarse approximation of the daemon's real behavior.
	// The daemon processes one fswatcher event at a time, and fswatcher
	// may coalesce multiple transcript-line writes into a single fire.
	// Without a lifecycle-events sidecar we have no way to know where
	// fswatcher split the writes, so we fall back to batching by transcript
	// timestamp. A sidecar-driven replay path (see ReplayWithSidecar) is
	// used when the sidecar is present, giving byte-identical reproduction.
	batches := batchByDebounce(events, cfg.DebounceWindow)

	tmpDir, err := os.MkdirTemp("", "irrlicht-replay-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	tmpPath := filepath.Join(tmpDir, "transcript.jsonl")
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer tmp.Close()

	adapterName := cfg.Adapter
	if adapterName == "" {
		adapterName = claudecode.AdapterName
	}
	parser := agents.ParserFor(adapterName)
	t := tailer.NewTranscriptTailer(tmpPath, parser, adapterName)

	report := &Report{
		SchemaVersion:    1,
		SourceTranscript: src,
		GeneratedAt:      time.Now().UTC(),
		Settings:         cfg,
	}
	report.Summary.TotalEvents = len(events)
	report.Summary.FirstEventTime = events[0].Time
	report.Summary.LastEventTime = events[len(events)-1].Time
	report.Summary.WallClockDuration =
		report.Summary.LastEventTime.Sub(report.Summary.FirstEventTime)

	state := session.StateReady
	prevTransitionAt := events[0].Time
	stateDurations := map[string]time.Duration{}
	addDuration := func(s string, d time.Duration) {
		if d > 0 {
			stateDurations[s] += d
		}
	}

	emit := func(tr Transition) {
		tr.Index = len(report.Transitions)
		report.Transitions = append(report.Transitions, tr)
		addDuration(tr.PrevState, tr.VirtualTime.Sub(prevTransitionAt))
		prevTransitionAt = tr.VirtualTime
	}

	emit(Transition{
		EventIndex:  -1,
		VirtualTime: events[0].Time,
		Cause:       CauseInit,
		PrevState:   "",
		NewState:    state,
		Reason:      "initial state",
	})

	consumed := 0
	var lastMetrics *tailer.SessionMetrics
	for bi, batch := range batches {
		nextEventTime := batch[len(batch)-1].Time

		for _, ev := range batch {
			if _, err := tmp.Write(ev.Bytes); err != nil {
				return nil, err
			}
			consumed++
		}

		metrics, err := t.TailAndProcess()
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", bi, err)
		}
		lastMetrics = metrics

		domainMetrics := tailerToDomain(metrics)

		cause := CauseEvent
		if len(batch) > 1 {
			cause = CauseDebounceCoalesce
		}

		if state == session.StateReady && domainMetrics.LastEventType != "" {
			emit(transitionFromMetrics(batch[len(batch)-1].Index, nextEventTime, cause,
				state, session.StateWorking, "force ready→working on first activity", domainMetrics))
			state = session.StateWorking
		}

		newState, reason := services.ClassifyState(state, domainMetrics)
		if services.ShouldSynthesizeCollapsedWaiting(state, newState, domainMetrics) {
			emit(transitionFromMetrics(batch[len(batch)-1].Index, nextEventTime, cause,
				state, session.StateWaiting, services.SyntheticWaitingReason, domainMetrics))
			state = session.StateWaiting
			newState, reason = services.ClassifyState(state, domainMetrics)
		}
		if newState != state {
			emit(transitionFromMetrics(batch[len(batch)-1].Index, nextEventTime, cause,
				state, newState, reason, domainMetrics))
			state = newState
		}
	}

	addDuration(state, report.Summary.LastEventTime.Sub(prevTransitionAt))
	finalizeSummary(report, consumed, stateDurations, lastMetrics)

	return report, nil
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

// ReplayWithSidecar runs a deterministic replay driven by a lifecycle-events
// sidecar. Each transcript_activity event in the sidecar is one fswatcher
// fire the daemon observed; we feed the tailer the exact bytes the daemon
// had at that moment and call the classifier. Hook events (KindHookReceived)
// are interleaved by timestamp — when a permission-request hook fires, we
// emit a working→waiting transition without a tailer call, mirroring the
// daemon's behavior where a permission request pauses the agent.
func ReplayWithSidecar(transcriptPath, sidecarPath string, cfg ReportSettings) (*Report, error) {
	srcBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	sidecarEvents, err := loadAllLifecycleEvents(sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("load sidecar: %w", err)
	}

	// Identify the primary session: either the --session flag or auto-detected.
	var primarySessionID string
	if cfg.SessionFilter != "" {
		primarySessionID = cfg.SessionFilter
	} else {
		primarySessionID = findPrimarySessionID(sidecarEvents)
	}
	if primarySessionID == "" {
		return nil, fmt.Errorf("sidecar %s has no transcript_new event — cannot identify the primary session", sidecarPath)
	}

	// Single walk: collect fswatcher events, hook events, and first process exit.
	var fswatches []lifecycle.Event
	var hookEvents []lifecycle.Event
	var processExitAt time.Time
	for _, ev := range sidecarEvents {
		if ev.SessionID != primarySessionID {
			continue
		}
		switch ev.Kind {
		case lifecycle.KindTranscriptActivity:
			if ev.FileSize > 0 {
				fswatches = append(fswatches, ev)
			}
		case lifecycle.KindProcessExited:
			if processExitAt.IsZero() {
				processExitAt = ev.Timestamp
			}
		case lifecycle.KindHookReceived:
			hookEvents = append(hookEvents, ev)
		}
	}
	if len(fswatches) == 0 {
		return nil, fmt.Errorf("sidecar has no transcript_activity events with file_size for primary session %s: %s", primarySessionID, sidecarPath)
	}

	tmpDir, err := os.MkdirTemp("", "irrlicht-replay-sidecar-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	tmpPath := filepath.Join(tmpDir, "transcript.jsonl")
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer tmp.Close()

	adapterName := cfg.Adapter
	if adapterName == "" {
		adapterName = claudecode.AdapterName
	}
	parser := agents.ParserFor(adapterName)
	t := tailer.NewTranscriptTailer(tmpPath, parser, adapterName)

	report := &Report{
		SchemaVersion:    1,
		SourceTranscript: transcriptPath,
		GeneratedAt:      time.Now().UTC(),
		Settings:         cfg,
	}
	report.Summary.TotalEvents = len(fswatches)
	report.Summary.FirstEventTime = fswatches[0].Timestamp
	report.Summary.LastEventTime = fswatches[len(fswatches)-1].Timestamp
	report.Summary.WallClockDuration =
		report.Summary.LastEventTime.Sub(report.Summary.FirstEventTime)

	state := session.StateReady
	prevTransitionAt := fswatches[0].Timestamp
	stateDurations := map[string]time.Duration{}
	addDuration := func(s string, d time.Duration) {
		if d > 0 {
			stateDurations[s] += d
		}
	}
	emit := func(tr Transition) {
		tr.Index = len(report.Transitions)
		report.Transitions = append(report.Transitions, tr)
		addDuration(tr.PrevState, tr.VirtualTime.Sub(prevTransitionAt))
		prevTransitionAt = tr.VirtualTime
	}

	emit(Transition{
		EventIndex:  -1,
		VirtualTime: fswatches[0].Timestamp,
		Cause:       CauseInit,
		PrevState:   "",
		NewState:    state,
		Reason:      "initial state",
	})

	var lastMetrics *tailer.SessionMetrics
	var lastSize int64

	// classifyAtSidecar writes transcript bytes up to the given file_size,
	// then runs the tailer + classifier (mirroring SessionDetector.processActivity
	// for the force-r→w + ClassifyState pattern).
	classifyAtSidecar := func(fileSize int64, virtTime time.Time, eventIdx int, cause Cause) error {
		target := min(fileSize, int64(len(srcBytes)))
		if target > lastSize {
			if _, err := tmp.Write(srcBytes[lastSize:target]); err != nil {
				return err
			}
			lastSize = target
		}

		metrics, err := t.TailAndProcess()
		if err != nil {
			return err
		}
		lastMetrics = metrics
		domainMetrics := tailerToDomain(metrics)

		if state == session.StateReady && domainMetrics.LastEventType != "" {
			emit(transitionFromMetrics(eventIdx, virtTime, cause,
				state, session.StateWorking, "force ready→working on first activity", domainMetrics))
			state = session.StateWorking
		}

		newState, reason := services.ClassifyState(state, domainMetrics)
		if services.ShouldSynthesizeCollapsedWaiting(state, newState, domainMetrics) {
			emit(transitionFromMetrics(eventIdx, virtTime, cause,
				state, session.StateWaiting, services.SyntheticWaitingReason, domainMetrics))
			state = session.StateWaiting
			newState, reason = services.ClassifyState(state, domainMetrics)
		}
		if newState != state {
			emit(transitionFromMetrics(eventIdx, virtTime, cause,
				state, newState, reason, domainMetrics))
			state = newState
		}
		return nil
	}

	// applyHookEvent processes a hook_received event. Permission-request
	// hooks (e.g. PreToolUse) pause the agent, producing a working→waiting
	// transition. The transcript doesn't change at hook time — only the
	// state machine does.
	applyHookEvent := func(hookEv lifecycle.Event) {
		if state != session.StateWorking {
			return
		}
		emit(Transition{
			EventIndex:  -1,
			VirtualTime: hookEv.Timestamp,
			Cause:       CauseHook,
			PrevState:   state,
			NewState:    session.StateWaiting,
			Reason:      fmt.Sprintf("hook: %s (permission pending)", hookEv.HookName),
		})
		state = session.StateWaiting
	}

	// Build a merged timeline of fswatcher events and hook events, ordered
	// by sidecar sequence number.
	type timelineEntry struct {
		isHook  bool
		fsIdx   int
		hookIdx int
		seq     int64
	}
	timeline := make([]timelineEntry, 0, len(fswatches)+len(hookEvents))
	for i, ev := range fswatches {
		timeline = append(timeline, timelineEntry{fsIdx: i, seq: ev.Seq})
	}
	for i, ev := range hookEvents {
		timeline = append(timeline, timelineEntry{isHook: true, hookIdx: i, seq: ev.Seq})
	}
	sort.SliceStable(timeline, func(i, j int) bool {
		return timeline[i].seq < timeline[j].seq
	})

	// Apply the daemon's debounce state machine over the merged timeline.
	// Hook events bypass debounce — they fire immediately regardless of
	// the debounce window state, matching how the daemon handles them.
	debounce := cfg.DebounceWindow
	if debounce <= 0 {
		debounce = 2 * time.Second
	}

	debouncePending := false
	coalescedSinceFire := false
	var windowDeadline time.Time
	var pendingSize int64
	var pendingIdx int

	for _, entry := range timeline {
		if entry.isHook {
			applyHookEvent(hookEvents[entry.hookIdx])
			continue
		}

		i := entry.fsIdx
		fsev := fswatches[i]

		if debouncePending && !fsev.Timestamp.Before(windowDeadline) {
			if coalescedSinceFire {
				if processExitAt.IsZero() || windowDeadline.Before(processExitAt) {
					if err := classifyAtSidecar(pendingSize, windowDeadline, pendingIdx, CauseDebounceCoalesce); err != nil {
						return nil, fmt.Errorf("flush timer at fsev %d: %w", i, err)
					}
				}
			}
			debouncePending = false
			coalescedSinceFire = false
		}

		if !debouncePending {
			if err := classifyAtSidecar(fsev.FileSize, fsev.Timestamp, i, CauseEvent); err != nil {
				return nil, fmt.Errorf("classify fsev %d: %w", i, err)
			}
			debouncePending = true
			windowDeadline = fsev.Timestamp.Add(debounce)
			continue
		}

		coalescedSinceFire = true
		windowDeadline = fsev.Timestamp.Add(debounce)
		pendingSize = fsev.FileSize
		pendingIdx = i
	}

	if debouncePending && coalescedSinceFire {
		lastFs := fswatches[len(fswatches)-1]
		fireTime := lastFs.Timestamp.Add(debounce)
		if processExitAt.IsZero() || fireTime.Before(processExitAt) {
			if err := classifyAtSidecar(pendingSize, fireTime, pendingIdx, CauseDebounceCoalesce); err != nil {
				return nil, fmt.Errorf("final flush: %w", err)
			}
		}
	}
	addDuration(state, report.Summary.LastEventTime.Sub(prevTransitionAt))

	finalizeSummary(report, len(fswatches), stateDurations, lastMetrics)
	report.Sessions = buildSessionTimelines(sidecarEvents)

	return report, nil
}

// buildSessionTimelines aggregates sidecar events into per-session summaries.
func buildSessionTimelines(events []lifecycle.Event) []SessionTimeline {
	type tracker struct {
		timeline       SessionTimeline
		firstSeen      time.Time
		lastState      string
		lastStateAt    time.Time
		stateDurations map[string]time.Duration
	}
	sessions := make(map[string]*tracker)

	getSession := func(ev lifecycle.Event) *tracker {
		st, ok := sessions[ev.SessionID]
		if !ok {
			st = &tracker{
				timeline: SessionTimeline{
					SessionID:      ev.SessionID,
					Adapter:        ev.Adapter,
					StateDurations: make(map[string]int64),
				},
				firstSeen:      ev.Timestamp,
				stateDurations: make(map[string]time.Duration),
			}
			sessions[ev.SessionID] = st
		}
		return st
	}

	for _, ev := range events {
		st := getSession(ev)
		st.timeline.EventCount++
		st.timeline.LastSeen = ev.Timestamp

		if st.timeline.Adapter == "" && ev.Adapter != "" {
			st.timeline.Adapter = ev.Adapter
		}

		switch ev.Kind {
		case lifecycle.KindStateTransition:
			st.timeline.StateChanges++
			if st.lastState != "" && !st.lastStateAt.IsZero() {
				dur := ev.Timestamp.Sub(st.lastStateAt)
				st.stateDurations[st.lastState] += dur
			}
			st.lastState = ev.NewState
			st.lastStateAt = ev.Timestamp
			st.timeline.FinalState = ev.NewState

		case lifecycle.KindPIDDiscovered:
			st.timeline.PID = ev.PID
			st.timeline.PIDDiscoveryMs = ev.Timestamp.Sub(st.firstSeen).Milliseconds()

		case lifecycle.KindDebounceCoalesced:
			st.timeline.DebounceEvents++

		case lifecycle.KindParentLinked:
			st.timeline.ParentSessionID = ev.ParentSessionID
		}
	}

	var timelines []SessionTimeline
	for _, st := range sessions {
		if st.lastState != "" && !st.lastStateAt.IsZero() {
			dur := st.timeline.LastSeen.Sub(st.lastStateAt)
			st.stateDurations[st.lastState] += dur
		}

		st.timeline.FirstSeen = st.firstSeen
		st.timeline.DurationMs = st.timeline.LastSeen.Sub(st.firstSeen).Milliseconds()

		for state, dur := range st.stateDurations {
			st.timeline.StateDurations[state] = dur.Milliseconds()
		}

		timelines = append(timelines, st.timeline)
	}

	sort.SliceStable(timelines, func(i, j int) bool {
		return timelines[i].FirstSeen.Before(timelines[j].FirstSeen)
	})

	return timelines
}

// findPrimarySessionID returns the session ID of the first non-proc
// transcript_new event, which identifies the primary (parent) session.
func findPrimarySessionID(events []lifecycle.Event) string {
	for _, ev := range events {
		if ev.Kind == lifecycle.KindTranscriptNew && ev.SessionID != "" && !strings.HasPrefix(ev.SessionID, "proc-") {
			return ev.SessionID
		}
	}
	return ""
}

// loadAllLifecycleEvents reads a lifecycle sidecar file and returns every
// event sorted by sequence number. Malformed lines are logged to stderr and
// skipped so a partial file doesn't silently produce bogus replay output.
func loadAllLifecycleEvents(path string) ([]lifecycle.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var out []lifecycle.Event
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		var ev lifecycle.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			fmt.Fprintf(os.Stderr, "replay: skipping malformed sidecar line %d in %s: %v\n", lineNum, path, err)
			continue
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// filterStateTransitions extracts state_transition events for a given session
// from an already-loaded event slice, skipping rows with empty prev_state
// (session-creation markers).
func filterStateTransitions(events []lifecycle.Event, primarySessionID string) []lifecycle.Event {
	out := make([]lifecycle.Event, 0, len(events))
	for _, ev := range events {
		if ev.Kind != lifecycle.KindStateTransition {
			continue
		}
		if ev.PrevState == "" {
			continue
		}
		if primarySessionID != "" && ev.SessionID != primarySessionID {
			continue
		}
		out = append(out, ev)
	}
	return out
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

// tailerToDomain converts the tailer's metrics struct into the domain type
// consumed by ClassifyState.
func tailerToDomain(m *tailer.SessionMetrics) *session.SessionMetrics {
	if m == nil {
		return nil
	}
	return &session.SessionMetrics{
		ElapsedSeconds:         m.ElapsedSeconds,
		TotalTokens:            m.TotalTokens,
		ModelName:              m.ModelName,
		ContextWindow:          m.ContextWindow,
		ContextUtilization:     m.ContextUtilization,
		PressureLevel:          m.PressureLevel,
		HasOpenToolCall:        m.HasOpenToolCall,
		OpenToolCallCount:      m.OpenToolCallCount,
		LastEventType:          m.LastEventType,
		LastOpenToolNames:      copyStrings(m.LastOpenToolNames),
		LastWasUserInterrupt:   m.LastWasUserInterrupt,
		LastWasToolDenial:      m.LastWasToolDenial,
		EstimatedCostUSD:       m.EstimatedCostUSD,
		CumInputTokens:        m.CumInputTokens,
		CumOutputTokens:       m.CumOutputTokens,
		CumCacheReadTokens:    m.CumCacheReadTokens,
		CumCacheCreationTokens: m.CumCacheCreationTokens,
		LastAssistantText:      m.LastAssistantText,
		PermissionMode:         m.PermissionMode,
		SawUserBlockingToolClosedThisPass: m.SawUserBlockingToolClosedThisPass,
	}
}

// computeFlickers counts short-lived X→Y→X sandwich patterns.
func computeFlickers(trs []Transition, maxDur time.Duration) (map[string]int, map[string]int, int) {
	byCategory := map[string]int{}
	byReason := map[string]int{}
	if len(trs) < 3 || maxDur <= 0 {
		return byCategory, byReason, 0
	}
	total := 0
	for i := 1; i < len(trs)-1; i++ {
		prev, cur, next := trs[i-1], trs[i], trs[i+1]
		if prev.NewState == cur.NewState || cur.NewState == next.NewState {
			continue
		}
		if prev.NewState != next.NewState {
			continue
		}
		dur := next.VirtualTime.Sub(cur.VirtualTime)
		// Zero-duration sandwiches are same-batch replay artifacts — skip so
		// flicker counts reflect what the UI actually sees.
		if dur <= 0 || dur >= maxDur {
			continue
		}
		byCategory[cur.NewState+"_between_"+prev.NewState]++
		reason := cur.Reason
		if reason == "" {
			reason = "(no reason)"
		}
		byReason[reason]++
		total++
	}
	return byCategory, byReason, total
}

func copyStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// runExtendedCheck compares the replayed state transitions against the sidecar's
// recorded transitions.
func runExtendedCheck(sidecarPath string, replayed []Transition) (*ExtendedCheck, error) {
	all, err := loadAllLifecycleEvents(sidecarPath)
	if err != nil {
		return nil, err
	}

	primaryID := findPrimarySessionID(all)
	recorded := filterStateTransitions(all, primaryID)

	replayedReal := make([]Transition, 0, len(replayed))
	for _, t := range replayed {
		if t.PrevState == "" {
			continue
		}
		replayedReal = append(replayedReal, t)
	}

	check := &ExtendedCheck{
		SidecarPath:   sidecarPath,
		RecordedCount: len(recorded),
		ReplayedCount: len(replayedReal),
	}

	n := min(len(recorded), len(replayedReal))
	for i := 0; i < n; i++ {
		r := recorded[i]
		p := replayedReal[i]
		if r.PrevState == p.PrevState && r.NewState == p.NewState {
			check.OrderedMatches++
			continue
		}
		check.OrderedMismatches = append(check.OrderedMismatches, TransitionMismatch{
			Index:    i,
			Kind:     "state_differs",
			Recorded: r.PrevState + "→" + r.NewState,
			Replayed: p.PrevState + "→" + p.NewState,
		})
	}
	for i := n; i < len(recorded); i++ {
		r := recorded[i]
		check.OrderedMismatches = append(check.OrderedMismatches, TransitionMismatch{
			Index:    i,
			Kind:     "missing_in_replay",
			Recorded: r.PrevState + "→" + r.NewState,
		})
	}
	for i := n; i < len(replayedReal); i++ {
		p := replayedReal[i]
		check.OrderedMismatches = append(check.OrderedMismatches, TransitionMismatch{
			Index:    i,
			Kind:     "extra_in_replay",
			Replayed: p.PrevState + "→" + p.NewState,
		})
	}

	recordedKinds := uniqueTransitionKinds(recorded, func(e lifecycle.Event) (string, string) { return e.PrevState, e.NewState })
	replayedKinds := uniqueTransitionKinds(replayedReal, func(t Transition) (string, string) { return t.PrevState, t.NewState })
	check.RecordedUniqueKinds = sortedKinds(recordedKinds)
	check.ReplayedUniqueKinds = sortedKinds(replayedKinds)
	for k := range recordedKinds {
		if !replayedKinds[k] {
			check.MissingKinds = append(check.MissingKinds, k)
		}
	}
	for k := range replayedKinds {
		if !recordedKinds[k] {
			check.ExtraKinds = append(check.ExtraKinds, k)
		}
	}
	sort.Strings(check.MissingKinds)
	sort.Strings(check.ExtraKinds)

	return check, nil
}

// uniqueTransitionKinds returns the set of "prev→new" strings in a slice.
func uniqueTransitionKinds[T any](items []T, fields func(T) (prev, next string)) map[string]bool {
	out := make(map[string]bool)
	for _, it := range items {
		prev, next := fields(it)
		out[prev+"→"+next] = true
	}
	return out
}

func sortedKinds(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
