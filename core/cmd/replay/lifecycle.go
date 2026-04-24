package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"irrlicht/core/domain/lifecycle"
)

// sessionTracker carries the running aggregation state for one session as
// its sidecar events are folded into the per-session timeline.
type sessionTracker struct {
	timeline       sessionTimeline
	firstSeen      time.Time
	lastState      string
	lastStateAt    time.Time
	stateDurations map[string]time.Duration
}

// buildSessionTimelines aggregates sidecar events into per-session summaries.
func buildSessionTimelines(events []lifecycle.Event) []sessionTimeline {
	sessions := make(map[string]*sessionTracker)
	for _, ev := range events {
		st, ok := sessions[ev.SessionID]
		if !ok {
			st = newSessionTracker(ev)
			sessions[ev.SessionID] = st
		}
		applyLifecycleEvent(st, ev)
	}

	var timelines []sessionTimeline
	for _, st := range sessions {
		timelines = append(timelines, finalizeSessionTracker(st))
	}
	sort.SliceStable(timelines, func(i, j int) bool {
		return timelines[i].FirstSeen.Before(timelines[j].FirstSeen)
	})
	return timelines
}

// newSessionTracker seeds a fresh tracker from the first event it sees for
// a given sessionID, capturing the adapter hint and the initial timestamp.
func newSessionTracker(ev lifecycle.Event) *sessionTracker {
	return &sessionTracker{
		timeline: sessionTimeline{
			SessionID:      ev.SessionID,
			Adapter:        ev.Adapter,
			StateDurations: make(map[string]int64),
		},
		firstSeen:      ev.Timestamp,
		stateDurations: make(map[string]time.Duration),
	}
}

// applyLifecycleEvent folds one sidecar event into the running tracker,
// dispatching on event kind to update counters, state, PID, or parent link.
func applyLifecycleEvent(st *sessionTracker, ev lifecycle.Event) {
	st.timeline.EventCount++
	st.timeline.LastSeen = ev.Timestamp
	if st.timeline.Adapter == "" && ev.Adapter != "" {
		st.timeline.Adapter = ev.Adapter
	}
	switch ev.Kind {
	case lifecycle.KindStateTransition:
		st.timeline.StateChanges++
		if st.lastState != "" && !st.lastStateAt.IsZero() {
			st.stateDurations[st.lastState] += ev.Timestamp.Sub(st.lastStateAt)
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

// finalizeSessionTracker closes the open state interval against LastSeen,
// converts the per-state Duration map to milliseconds for JSON, and returns
// the completed timeline.
func finalizeSessionTracker(st *sessionTracker) sessionTimeline {
	if st.lastState != "" && !st.lastStateAt.IsZero() {
		st.stateDurations[st.lastState] += st.timeline.LastSeen.Sub(st.lastStateAt)
	}
	st.timeline.FirstSeen = st.firstSeen
	st.timeline.DurationMs = st.timeline.LastSeen.Sub(st.firstSeen).Milliseconds()
	for state, dur := range st.stateDurations {
		st.timeline.StateDurations[state] = dur.Milliseconds()
	}
	return st.timeline
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
