// replay-lifecycle reads a lifecycle recording produced by irrlichd --record
// and generates a timeline report of all session lifecycle events — not just
// transcript-derived state transitions but also process lifecycle, filesystem
// events, debounce behavior, and parent-child linking.
//
// Usage:
//
//	go run ./core/cmd/replay-lifecycle [flags] RECORDING.jsonl
//
// Flags:
//
//	--out FILE        Write JSON report to FILE (default stdout).
//	--session ID      Filter to a single session.
//	--quiet           Suppress progress on stderr.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"irrlicht/core/domain/lifecycle"
)

// SessionTimeline is a per-session summary within the report.
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

// Report is the top-level structure written to the output file.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	SourceFile    string    `json:"source_file"`
	GeneratedAt   time.Time `json:"generated_at"`

	Summary  ReportSummary   `json:"summary"`
	Sessions []SessionTimeline `json:"sessions"`
	Events   []lifecycle.Event `json:"events"`
}

// ReportSummary provides aggregate statistics.
type ReportSummary struct {
	TotalEvents      int       `json:"total_events"`
	TotalSessions    int       `json:"total_sessions"`
	TotalTransitions int       `json:"total_state_transitions"`
	TotalDebounced   int       `json:"total_debounce_coalesced"`
	TotalPIDEvents   int       `json:"total_pid_events"`
	TotalProcessExits int      `json:"total_process_exits"`
	FirstEventTime   time.Time `json:"first_event_time"`
	LastEventTime    time.Time `json:"last_event_time"`
	WallDuration     int64     `json:"wall_duration_ms"`

	EventsByKind map[string]int `json:"events_by_kind"`
}

func main() {
	var (
		outPath    string
		sessionID  string
		quiet      bool
	)
	flag.StringVar(&outPath, "out", "", "output JSON report path (default: stdout)")
	flag.StringVar(&sessionID, "session", "", "filter to a single session ID")
	flag.BoolVar(&quiet, "quiet", false, "suppress progress on stderr")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: replay-lifecycle [flags] RECORDING.jsonl")
		flag.PrintDefaults()
		os.Exit(2)
	}
	src := flag.Arg(0)

	events, err := loadEvents(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(1)
	}
	if len(events) == 0 {
		fmt.Fprintln(os.Stderr, "recording is empty")
		os.Exit(1)
	}

	// Filter by session if requested.
	if sessionID != "" {
		var filtered []lifecycle.Event
		for _, ev := range events {
			if ev.SessionID == sessionID {
				filtered = append(filtered, ev)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "no events for session %q\n", sessionID)
			os.Exit(1)
		}
		events = filtered
	}

	report := buildReport(src, events)

	out := os.Stdout
	if outPath != "" {
		if dir := filepath.Dir(outPath); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0755)
		}
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create output: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(1)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "replay-lifecycle: %d events across %d sessions, %d state transitions, %d debounced\n",
			report.Summary.TotalEvents, report.Summary.TotalSessions,
			report.Summary.TotalTransitions, report.Summary.TotalDebounced)
	}
}

func loadEvents(path string) ([]lifecycle.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var events []lifecycle.Event
	for scanner.Scan() {
		var ev lifecycle.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort by sequence number for deterministic ordering.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Seq < events[j].Seq
	})

	return events, nil
}

func buildReport(src string, events []lifecycle.Event) *Report {
	report := &Report{
		SchemaVersion: 1,
		SourceFile:    src,
		GeneratedAt:   time.Now().UTC(),
		Events:        events,
	}

	report.Summary.TotalEvents = len(events)
	report.Summary.FirstEventTime = events[0].Timestamp
	report.Summary.LastEventTime = events[len(events)-1].Timestamp
	report.Summary.WallDuration = events[len(events)-1].Timestamp.Sub(events[0].Timestamp).Milliseconds()
	report.Summary.EventsByKind = make(map[string]int)

	// Per-session tracking.
	type sessionTracker struct {
		timeline       SessionTimeline
		firstSeen      time.Time
		lastState      string
		lastStateAt    time.Time
		stateDurations map[string]time.Duration
	}
	sessions := make(map[string]*sessionTracker)

	getSession := func(ev lifecycle.Event) *sessionTracker {
		st, ok := sessions[ev.SessionID]
		if !ok {
			st = &sessionTracker{
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
		report.Summary.EventsByKind[string(ev.Kind)]++

		st := getSession(ev)
		st.timeline.EventCount++
		st.timeline.LastSeen = ev.Timestamp

		if st.timeline.Adapter == "" && ev.Adapter != "" {
			st.timeline.Adapter = ev.Adapter
		}

		switch ev.Kind {
		case lifecycle.KindStateTransition:
			report.Summary.TotalTransitions++
			st.timeline.StateChanges++

			// Accumulate time in previous state.
			if st.lastState != "" && !st.lastStateAt.IsZero() {
				dur := ev.Timestamp.Sub(st.lastStateAt)
				st.stateDurations[st.lastState] += dur
			}
			st.lastState = ev.NewState
			st.lastStateAt = ev.Timestamp
			st.timeline.FinalState = ev.NewState

		case lifecycle.KindPIDDiscovered:
			report.Summary.TotalPIDEvents++
			st.timeline.PID = ev.PID
			st.timeline.PIDDiscoveryMs = ev.Timestamp.Sub(st.firstSeen).Milliseconds()

		case lifecycle.KindProcessExited:
			report.Summary.TotalProcessExits++

		case lifecycle.KindDebounceCoalesced:
			report.Summary.TotalDebounced++
			st.timeline.DebounceEvents++

		case lifecycle.KindParentLinked:
			st.timeline.ParentSessionID = ev.ParentSessionID
		}
	}

	// Finalize session timelines.
	var timelines []SessionTimeline
	for _, st := range sessions {
		// Close final state interval.
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

	// Sort sessions by first seen time.
	sort.Slice(timelines, func(i, j int) bool {
		return timelines[i].FirstSeen.Before(timelines[j].FirstSeen)
	})

	report.Sessions = timelines
	report.Summary.TotalSessions = len(timelines)

	return report
}
