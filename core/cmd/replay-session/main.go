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
}

func main() {
	var (
		outPath      string
		adapterFlag  string
		debounceFlag time.Duration
		flickerMax   time.Duration
		quiet        bool
	)
	flag.StringVar(&outPath, "out", "", "output JSON report path (default: stdout)")
	flag.StringVar(&adapterFlag, "adapter", "", "adapter name (claude-code, codex, pi); auto-detected from path if omitted")
	flag.DurationVar(&debounceFlag, "debounce", 2*time.Second, "simulated activity debounce window")
	flag.DurationVar(&flickerMax, "flicker-max", 10*time.Second, "episodes shorter than this are counted as flickers (automated agent loops cycle turns in ~25s, so 30s overcounts)")
	flag.BoolVar(&quiet, "quiet", false, "suppress per-event progress on stderr")
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

	report, err := Replay(src, ReportSettings{
		Adapter:            adapterName,
		DebounceWindow:     debounceFlag,
		FlickerMaxDuration: flickerMax,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay failed:", err)
		os.Exit(1)
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
			"replay: %d events → %d transitions, %d flickers (ww=%d wr=%d rw=%d)\n",
			s.TotalEvents, s.TotalTransitions, s.FlickerCount,
			s.FlickersByCategory["working_between_waiting"]+s.FlickersByCategory["waiting_between_working"],
			s.FlickersByCategory["working_between_ready"]+s.FlickersByCategory["ready_between_working"],
			s.FlickersByCategory["ready_between_waiting"]+s.FlickersByCategory["waiting_between_ready"])
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
		// No fsync needed: the tailer reads from the same process via the
		// OS page cache and sees the written bytes immediately.

		metrics, err := t.TailAndProcess()
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", bi, err)
		}

		// Convert tailer.SessionMetrics → session.SessionMetrics for the
		// classifier (the classifier consumes the domain type).
		domainMetrics := tailerToDomain(metrics)

		// Mirror SessionDetector.processActivity: force ready→working when
		// metrics show any event activity, so the classifier can later detect
		// working→ready properly.
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

		var raw map[string]interface{}
		ts := time.Time{}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err == nil {
			ts = tailer.ParseTimestamp(raw)
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

	// Sort by timestamp so out-of-order writes (rare but possible) don't
	// confuse the simulator.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	for i := range out {
		out[i].Index = i
	}
	return out, nil
}

func batchByDebounce(events []rawEvent, window time.Duration) [][]rawEvent {
	if window <= 0 || len(events) == 0 {
		// Each event is its own batch.
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
		EstimatedCostUSD:       m.EstimatedCostUSD,
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
