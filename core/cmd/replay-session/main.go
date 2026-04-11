// replay-session is an offline simulator that takes a Claude Code transcript
// .jsonl file and replays it through the production tailer + state classifier
// using virtual time (the event timestamps inside the transcript itself).
//
// It exists to reproduce and diagnose issue #102 — long-running Claude Code
// sessions flickering between working and waiting. The replay is fully
// deterministic and runs much faster than realtime: there are no real sleeps
// or debounce timers. Their effects are simulated by inspecting timestamp
// gaps and applying scaled-down thresholds.
//
// Usage:
//
//	go run ./core/cmd/replay-session [flags] TRANSCRIPT.jsonl
//
// Flags:
//
//	--out FILE              Write JSON report to FILE (default stdout).
//	--debounce DURATION     Simulated activity debounce window. Default 2s.
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

// Cause distinguishes why a state evaluation happened — was it triggered by a
// real transcript event, debounce coalescing, or the initial seed state.
type Cause string

const (
	CauseInit             Cause = "init"
	CauseEvent            Cause = "event"
	CauseDebounceCoalesce Cause = "debounce_coalesce"
)

// Transition is a single recorded state change emitted by the replay.
type Transition struct {
	Index         int       `json:"index"`           // monotonic counter, increments per transition
	EventIndex    int       `json:"event_index"`     // index of triggering event in source transcript (-1 for timer)
	VirtualTime   time.Time `json:"virtual_time"`    // synthetic clock at the moment of transition
	Cause         Cause     `json:"cause"`           // event | stale_tool_timer | debounce_coalesce | init
	PrevState     string    `json:"prev_state"`
	NewState      string    `json:"new_state"`
	Reason        string    `json:"reason"`          // ClassifyState's reason string
	LastEventType string    `json:"last_event_type"`
	HasOpenTool   bool      `json:"has_open_tool"`
	OpenToolNames []string  `json:"open_tool_names,omitempty"`
	IsAgentDone   bool      `json:"is_agent_done"`
	NeedsAttn     bool      `json:"needs_user_attention"`
	WaitingQuery  bool      `json:"waiting_for_user_input"`
	LastTextHead  string    `json:"last_assistant_text_head,omitempty"` // first 80 chars
}

// Report is the top-level structure written to the output file.
type Report struct {
	SchemaVersion    int           `json:"schema_version"`
	SourceTranscript string        `json:"source_transcript"`
	GeneratedAt      time.Time     `json:"generated_at"`
	Settings         ReportSettings `json:"settings"`
	Summary          ReportSummary `json:"summary"`
	Transitions      []Transition  `json:"transitions"`

	// ExtendedCheck is populated when a <transcript-basename>.events.jsonl
	// sidecar is present next to the transcript fixture. It diffs the
	// replayed state transitions against the recorded ones so fixtures act
	// as regression tests for the detector.
	ExtendedCheck *ExtendedCheck `json:"extended_check,omitempty"`
}

// ExtendedCheck compares the replayed state transitions against a committed
// lifecycle recording (.events.jsonl sidecar produced by `irrlichd --record`).
//
// When a sidecar is present, replay-session uses `ReplayWithSidecar` to
// drive the tailer from the recorded fswatcher events — each
// transcript_activity event in the sidecar carries a file_size that tells
// the replay exactly how many transcript bytes to feed the tailer at that
// moment. The debounce state machine is then applied over those events
// using the same 2-second window the daemon uses, and process_exited
// events cancel any pending debounce timer the same way the daemon
// tearing down a session does.
//
// The result is a byte-identical reproduction of what the daemon produced
// for the recorded session. Ordered-diff mismatches are therefore real
// regressions — pass --strict-check to fail the process on any drift.
//
// Unique-kind mismatches (a transition prev→new pair that appears in one
// side but not the other) are reported either way.
type ExtendedCheck struct {
	SidecarPath         string               `json:"sidecar_path"`
	RecordedCount       int                  `json:"recorded_transition_count"`
	ReplayedCount       int                  `json:"replayed_transition_count"`
	OrderedMatches      int                  `json:"ordered_matches"`
	OrderedMismatches   []TransitionMismatch `json:"ordered_mismatches,omitempty"`
	RecordedUniqueKinds []string             `json:"recorded_unique_kinds"`
	ReplayedUniqueKinds []string             `json:"replayed_unique_kinds"`
	MissingKinds        []string             `json:"missing_kinds,omitempty"` // kinds in recorded but not replayed
	ExtraKinds          []string             `json:"extra_kinds,omitempty"`   // kinds in replayed but not recorded
}

// TransitionMismatch is a single divergence between replayed and recorded
// state transitions.
type TransitionMismatch struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"` // "missing_in_replay" | "extra_in_replay" | "state_differs"
	Recorded string `json:"recorded,omitempty"` // "prev→new"
	Replayed string `json:"replayed,omitempty"` // "prev→new"
}

type ReportSettings struct {
	Adapter            string        `json:"adapter"`
	DebounceWindow     time.Duration `json:"debounce_window"`
	FlickerMaxDuration time.Duration `json:"flicker_max_duration"` // episodes shorter than this are flickers
}

type ReportSummary struct {
	TotalEvents       int                      `json:"total_events"`
	ConsumedEvents    int                      `json:"consumed_events"` // after debounce coalescing
	TotalTransitions  int                      `json:"total_transitions"`
	FirstEventTime    time.Time                `json:"first_event_time"`
	LastEventTime     time.Time                `json:"last_event_time"`
	WallClockDuration time.Duration            `json:"wall_clock_session_duration"` // last - first
	StateDurations    map[string]time.Duration `json:"state_durations"`

	// Flicker accounting — a flicker is a short-lived episode (<FlickerMaxDuration)
	// whose state is different from the state immediately before and after it,
	// i.e. the X → Y → X sandwich pattern. This is the user-visible "bouncing"
	// irrlicht surfaces in notifications.
	FlickerCount       int            `json:"flicker_count"`
	FlickersByCategory map[string]int `json:"flickers_by_category"` // e.g. "working_between_ready": 4501
	FlickersByReason   map[string]int `json:"flickers_by_reason"`

	// Cost metrics — cumulative token totals and estimated cost for the session.
	EstimatedCostUSD       float64 `json:"estimated_cost_usd,omitempty"`
	CumInputTokens         int64   `json:"cum_input_tokens,omitempty"`
	CumOutputTokens        int64   `json:"cum_output_tokens,omitempty"`
	CumCacheReadTokens     int64   `json:"cum_cache_read_tokens,omitempty"`
	CumCacheCreationTokens int64   `json:"cum_cache_creation_tokens,omitempty"`
	ModelName              string  `json:"model_name,omitempty"`
}

func main() {
	var (
		outPath      string
		adapterFlag  string
		debounceFlag time.Duration
		flickerMax   time.Duration
		quiet        bool
		strictCheck  bool
	)
	flag.StringVar(&outPath, "out", "", "output JSON report path (default: stdout)")
	flag.StringVar(&adapterFlag, "adapter", "", "adapter name (claude-code, codex, pi); auto-detected from path if omitted")
	flag.DurationVar(&debounceFlag, "debounce", 2*time.Second, "simulated activity debounce window")
	flag.DurationVar(&flickerMax, "flicker-max", 10*time.Second, "episodes shorter than this are counted as flickers (automated agent loops cycle turns in ~25s, so 30s overcounts)")
	flag.BoolVar(&quiet, "quiet", false, "suppress per-event progress on stderr")
	flag.BoolVar(&strictCheck, "strict-check", false, "exit non-zero on any ordered-diff mismatch in the extended check (default: only unique-kind regressions fail)")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay-session [flags] TRANSCRIPT.jsonl")
		flag.PrintDefaults()
		os.Exit(2)
	}
	src := flag.Arg(0)

	adapterName := adapterFlag
	if adapterName == "" {
		var err error
		adapterName, err = detectAdapter(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	// Sidecar-driven replay: when a <transcript-basename>.events.jsonl
	// sidecar is present, the replay is driven by the lifecycle recording
	// instead of the transcript's own line timestamps. The sidecar records
	// one transcript_activity event per fswatcher fire, each with a
	// file_size. Feeding the tailer bytes up to each recorded file_size
	// gives a byte-identical reproduction of what the daemon observed —
	// no more batching-vs-fswatcher drift.
	sidecarPath := strings.TrimSuffix(src, ".jsonl") + ".events.jsonl"
	useSidecar := false
	if _, err := os.Stat(sidecarPath); err == nil {
		useSidecar = true
	}

	var (
		report    *Report
		replayErr error
	)
	if useSidecar {
		report, replayErr = ReplayWithSidecar(src, sidecarPath, ReportSettings{
			Adapter:            adapterName,
			DebounceWindow:     debounceFlag,
			FlickerMaxDuration: flickerMax,
		})
	} else {
		report, replayErr = Replay(src, ReportSettings{
			Adapter:            adapterName,
			DebounceWindow:     debounceFlag,
			FlickerMaxDuration: flickerMax,
		})
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
			kindsMark := "✓"
			if len(c.MissingKinds) > 0 || len(c.ExtraKinds) > 0 {
				kindsMark = "✗"
			}
			orderMark := "✓"
			if len(c.OrderedMismatches) > 0 {
				orderMark = "✗"
			}
			fmt.Fprintf(os.Stderr, " [extended-check: kinds %s ordered %d/%d %s]",
				kindsMark, c.OrderedMatches, c.RecordedCount, orderMark)
		}
		fmt.Fprintln(os.Stderr)
	}

	// Exit policy: the extended check is informational by default. Pass
	// --strict-check when you expect byte-identical reproduction and want
	// the process to fail on any drift.
	if strictCheck && report.ExtendedCheck != nil {
		c := report.ExtendedCheck
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
	Index    int
	Bytes    []byte // including trailing newline
	Time     time.Time
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

	// Set up the production tailer + parser writing into a temp transcript
	// file. We rebuild the temp file as we go so the tailer's incremental
	// offset logic processes one batch at a time.
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

	// Seed the initial state row so consumers see "started in ready" too.
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

		// Append every line in the batch to the temp transcript and let
		// the tailer process them in one call.
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

		if state == session.StateReady && domainMetrics.LastEventType != "" {
			cause := CauseEvent
			if len(batch) > 1 {
				cause = CauseDebounceCoalesce
			}
			emit(Transition{
				EventIndex:    batch[len(batch)-1].Index,
				VirtualTime:   nextEventTime,
				Cause:         cause,
				PrevState:     state,
				NewState:      session.StateWorking,
				Reason:        "force ready→working on first activity",
				LastEventType: domainMetrics.LastEventType,
				HasOpenTool:   domainMetrics.HasOpenToolCall,
				OpenToolNames: copyStrings(domainMetrics.LastOpenToolNames),
				IsAgentDone:   domainMetrics.IsAgentDone(),
				NeedsAttn:     domainMetrics.NeedsUserAttention(),
				WaitingQuery:  domainMetrics.IsWaitingForUserInput(),
				LastTextHead:  head(domainMetrics.LastAssistantText, 80),
			})
			state = session.StateWorking
		}

		newState, reason := services.ClassifyState(state, domainMetrics)
		if newState != state {
			cause := CauseEvent
			if len(batch) > 1 {
				cause = CauseDebounceCoalesce
			}
			emit(Transition{
				EventIndex:    batch[len(batch)-1].Index,
				VirtualTime:   nextEventTime,
				Cause:         cause,
				PrevState:     state,
				NewState:      newState,
				Reason:        reason,
				LastEventType: domainMetrics.LastEventType,
				HasOpenTool:   domainMetrics.HasOpenToolCall,
				OpenToolNames: copyStrings(domainMetrics.LastOpenToolNames),
				IsAgentDone:   domainMetrics.IsAgentDone(),
				NeedsAttn:     domainMetrics.NeedsUserAttention(),
				WaitingQuery:  domainMetrics.IsWaitingForUserInput(),
				LastTextHead:  head(domainMetrics.LastAssistantText, 80),
			})
			state = newState
		}
	}

	// Close the final state interval against the last event time.
	addDuration(state, report.Summary.LastEventTime.Sub(prevTransitionAt))

	report.Summary.ConsumedEvents = consumed
	report.Summary.TotalTransitions = len(report.Transitions)
	report.Summary.StateDurations = stateDurations

	// Flicker accounting — compute from finalised transition list, using
	// the sandwich metric: a short episode (<FlickerMaxDuration) whose state
	// differs from the state immediately before and after.
	flickerCat, flickerReason, flickerTotal := computeFlickers(
		report.Transitions, cfg.FlickerMaxDuration)
	report.Summary.FlickerCount = flickerTotal
	report.Summary.FlickersByCategory = flickerCat
	report.Summary.FlickersByReason = flickerReason

	// Cost metrics from the final tailer state.
	if lastMetrics != nil {
		report.Summary.EstimatedCostUSD = lastMetrics.EstimatedCostUSD
		report.Summary.CumInputTokens = lastMetrics.CumInputTokens
		report.Summary.CumOutputTokens = lastMetrics.CumOutputTokens
		report.Summary.CumCacheReadTokens = lastMetrics.CumCacheReadTokens
		report.Summary.CumCacheCreationTokens = lastMetrics.CumCacheCreationTokens
		report.Summary.ModelName = lastMetrics.ModelName
	}

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
		// Reattach the trailing newline so the tailer sees a complete JSONL.
		line = append(line, '\n')

		// Explicit timestamp only — do NOT use tailer.ParseTimestamp here
		// because it falls back to time.Now() when the field is missing,
		// which would pollute the sorted virtual timeline with wall-clock
		// values for metadata lines (permission-mode, file-history-snapshot,
		// last-prompt, etc.). Those lines inherit from neighbours instead.
		var raw map[string]interface{}
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
	// in-file-order alongside the surrounding real events. First pass:
	// fill forward from the last real timestamp. Second pass: fill
	// backward for leading nulls that had no prior real timestamp.
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

	// Stable sort by timestamp. Stable preserves insertion order for ties,
	// which is important: multi-line writes with slightly-out-of-order
	// timestamps and our now-inherited nulls rely on physical order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	for i := range out {
		out[i].Index = i
	}
	return out, nil
}

// ReplayWithSidecar runs a deterministic replay driven by a lifecycle-events
// sidecar. Each transcript_activity event in the sidecar is one fswatcher
// fire the daemon observed; we feed the tailer the exact bytes the daemon
// had at that moment and call the classifier. The result is byte-identical
// to the daemon's behavior for the recorded session.
//
// Returns a Report with the same shape as Replay() — the only difference is
// the virtual timeline, which tracks sidecar event timestamps instead of
// transcript-line timestamps, and the classifier call granularity.
func ReplayWithSidecar(transcriptPath, sidecarPath string, cfg ReportSettings) (*Report, error) {
	// Load the transcript as raw bytes so we can slice it by file_size.
	srcBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	// Load sidecar events and walk them in sequence order. We need the
	// transcript_activity stream (fswatcher fires with file_size), plus
	// process_exited events (which cause the daemon to tear down the
	// session, cancelling any pending debounce timer).
	sidecarEvents, err := loadAllLifecycleEvents(sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("load sidecar: %w", err)
	}
	var fswatches []lifecycle.Event
	var processExitAt time.Time
	for _, ev := range sidecarEvents {
		switch ev.Kind {
		case lifecycle.KindTranscriptActivity:
			if ev.FileSize > 0 {
				fswatches = append(fswatches, ev)
			}
		case lifecycle.KindProcessExited:
			if processExitAt.IsZero() {
				processExitAt = ev.Timestamp
			}
		}
	}
	if len(fswatches) == 0 {
		return nil, fmt.Errorf("sidecar has no transcript_activity events with file_size: %s", sidecarPath)
	}

	// Set up a temp file and tailer. We append bytes incrementally so the
	// tailer's offset-based incremental read produces the same metrics the
	// daemon computed at each fswatcher fire.
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
		target := fileSize
		if target > int64(len(srcBytes)) {
			target = int64(len(srcBytes))
		}
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
			emit(Transition{
				EventIndex:    eventIdx,
				VirtualTime:   virtTime,
				Cause:         cause,
				PrevState:     state,
				NewState:      session.StateWorking,
				Reason:        "force ready→working on first activity",
				LastEventType: domainMetrics.LastEventType,
				HasOpenTool:   domainMetrics.HasOpenToolCall,
				OpenToolNames: copyStrings(domainMetrics.LastOpenToolNames),
				IsAgentDone:   domainMetrics.IsAgentDone(),
				NeedsAttn:     domainMetrics.NeedsUserAttention(),
				WaitingQuery:  domainMetrics.IsWaitingForUserInput(),
				LastTextHead:  head(domainMetrics.LastAssistantText, 80),
			})
			state = session.StateWorking
		}

		newState, reason := services.ClassifyState(state, domainMetrics)
		if newState != state {
			emit(Transition{
				EventIndex:    eventIdx,
				VirtualTime:   virtTime,
				Cause:         cause,
				PrevState:     state,
				NewState:      newState,
				Reason:        reason,
				LastEventType: domainMetrics.LastEventType,
				HasOpenTool:   domainMetrics.HasOpenToolCall,
				OpenToolNames: copyStrings(domainMetrics.LastOpenToolNames),
				IsAgentDone:   domainMetrics.IsAgentDone(),
				NeedsAttn:     domainMetrics.NeedsUserAttention(),
				WaitingQuery:  domainMetrics.IsWaitingForUserInput(),
				LastTextHead:  head(domainMetrics.LastAssistantText, 80),
			})
			state = newState
		}
		return nil
	}

	// Apply the daemon's debounce state machine over the sidecar fswatcher
	// events. The daemon (session_detector.go:308 onActivity):
	//   - First event in a quiet period → processActivity fires synchronously
	//   - Subsequent event within the timer window → coalesce, reset timer
	//   - Timer fires (2s after last event) → processActivity fires on latest,
	//     but only if at least one event coalesced (the `pending` flag)
	//   - A process exit cancels any pending timer (the session is torn down)
	debounce := cfg.DebounceWindow
	if debounce <= 0 {
		debounce = 2 * time.Second
	}

	debouncePending := false
	coalescedSinceFire := false
	var windowDeadline time.Time
	var pendingSize int64 // file_size of the latest coalesced event
	var pendingIdx int

	for i, fsev := range fswatches {
		// If the debounce window for the previous event has expired before
		// this event arrives, the timer would have fired. Fire it now if
		// events coalesced, then start a fresh window for this event.
		if debouncePending && !fsev.Timestamp.Before(windowDeadline) {
			if coalescedSinceFire {
				// Don't fire if the process exited before the timer would have.
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
			// First event of a new quiet period: fire immediately.
			if err := classifyAtSidecar(fsev.FileSize, fsev.Timestamp, i, CauseEvent); err != nil {
				return nil, fmt.Errorf("classify fsev %d: %w", i, err)
			}
			debouncePending = true
			windowDeadline = fsev.Timestamp.Add(debounce)
			continue
		}

		// Subsequent event in the same debounce window: coalesce, reset timer.
		coalescedSinceFire = true
		windowDeadline = fsev.Timestamp.Add(debounce)
		pendingSize = fsev.FileSize
		pendingIdx = i
	}

	// End of stream: fire the final debounce timer if events were pending
	// AND the process hadn't exited before it would fire.
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

	report.Summary.ConsumedEvents = len(fswatches)
	report.Summary.TotalTransitions = len(report.Transitions)
	report.Summary.StateDurations = stateDurations

	flickerCat, flickerReason, flickerTotal := computeFlickers(
		report.Transitions, cfg.FlickerMaxDuration)
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

	return report, nil
}

// loadAllLifecycleEvents reads a lifecycle sidecar file and returns every
// event sorted by sequence number (the fields that make sidecar replay
// deterministic). Unlike loadLifecycleStateTransitions it does not filter
// by Kind.
func loadAllLifecycleEvents(path string) ([]lifecycle.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var out []lifecycle.Event
	for scanner.Scan() {
		var ev lifecycle.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
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
// consumed by ClassifyState. We copy the fields the classifier reads.
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
	}
}

// computeFlickers walks the transition list and counts "true flickers": short
// episodes (<maxDur) whose state differs from the state immediately before and
// after — the X→Y→X sandwich pattern that a user perceives as "bouncing".
//
// Returns a category breakdown (e.g. "working_between_ready": 4501), a reason
// breakdown keyed by the classifier's reason string, and the total count.
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
		// Zero-duration sandwiches are same-batch replay artifacts: the
		// force-promotion + classifier revert happen inside one
		// processActivity call and coalesce into a single production
		// broadcast. Skip so flicker counts reflect what the UI actually
		// sees, not intermediate classifier evaluations.
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

// runExtendedCheck loads a lifecycle-events sidecar, extracts its state
// transitions, and compares them to the replayed transitions. Transitions
// with an empty prev_state are dropped on both sides (the sidecar's "new
// session created" row and the replay's synthetic init row).
func runExtendedCheck(sidecarPath string, replayed []Transition) (*ExtendedCheck, error) {
	recorded, err := loadLifecycleStateTransitions(sidecarPath)
	if err != nil {
		return nil, err
	}

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

	// Ordered diff.
	n := len(recorded)
	if len(replayedReal) < n {
		n = len(replayedReal)
	}
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

	// Unique-kinds diff (the strict correctness check).
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

// uniqueTransitionKinds is a small generic helper that returns the set of
// "prev→new" strings appearing in a slice of transition-like records.
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

// loadLifecycleStateTransitions reads a JSONL lifecycle recording and
// returns only the state_transition events that carry a non-empty
// prev_state (dropping "new session created" which the replay does not
// reproduce). Events are returned in sequence order.
func loadLifecycleStateTransitions(path string) ([]lifecycle.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var out []lifecycle.Event
	for scanner.Scan() {
		var ev lifecycle.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Kind != lifecycle.KindStateTransition {
			continue
		}
		if ev.PrevState == "" {
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
