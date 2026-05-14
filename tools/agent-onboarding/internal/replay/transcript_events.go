package replay

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/domain/lifecycle"
)

// SynthesizeEventsFromTranscript builds a plausible lifecycle event
// stream from a recording's transcript file (transcript.jsonl or
// transcript.md). Used by LoadEventsFallback when the recording
// predates Phase 1's events.jsonl recorder — most regression captures
// fall into that bucket.
//
// The synthesized stream is NOT a faithful reproduction of what
// irrlichd would have emitted; it's a "the session existed for this
// long, with N user / assistant turns" approximation that's good
// enough to drive the dashboard's session-row animation. Each user
// message becomes a `working` transition and each assistant message
// becomes a `ready` transition.
//
// Returns nil if no transcript is present at any expected name.
func SynthesizeEventsFromTranscript(scenarioDir string) []lifecycle.Event {
	// Try common transcript filenames in order.
	candidates := []string{"transcript.jsonl", "transcript.md"}
	for _, name := range candidates {
		path := filepath.Join(scenarioDir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if strings.HasSuffix(name, ".jsonl") {
			return synthesizeFromJSONL(path)
		}
		return synthesizeFromMarkdown(path)
	}
	return nil
}

// synthesizeFromJSONL handles claudecode / codex / pi / opencode style
// transcripts where each line is a JSON object describing one message
// or event. Adapter shapes diverge — claudecode uses `timestamp` +
// `sessionId` + top-level `type`; opencode uses `_ts` (unix ms) +
// `_role`; codex/pi vary further. extractLineTime + extractLineRole +
// extractLineSession normalize them.
func synthesizeFromJSONL(path string) []lifecycle.Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var (
		sessionID string
		firstTs   time.Time
		lastTs    time.Time
		userTs    []time.Time
		asstTs    []time.Time
		anyTsSeen bool
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if sessionID == "" {
			if sid := extractLineSession(raw); sid != "" {
				sessionID = sid
			}
		}
		ts, ok := extractLineTime(raw)
		if !ok {
			continue
		}
		if !anyTsSeen {
			firstTs = ts
			anyTsSeen = true
		}
		lastTs = ts
		switch extractLineRole(raw) {
		case "user":
			userTs = append(userTs, ts)
		case "assistant":
			asstTs = append(asstTs, ts)
		}
	}
	if !anyTsSeen {
		return nil
	}
	if sessionID == "" {
		sessionID = "synthetic-" + filepath.Base(filepath.Dir(path))
	}
	return buildArc(sessionID, firstTs, lastTs, userTs, asstTs)
}

// extractLineTime tries the two timestamp conventions we've seen:
// claudecode's RFC3339 `timestamp` and opencode's `_ts` unix-ms number.
func extractLineTime(raw map[string]any) (time.Time, bool) {
	if v, ok := raw["timestamp"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t, true
		}
	}
	if v, ok := raw["_ts"].(float64); ok {
		return time.UnixMilli(int64(v)).UTC(), true
	}
	if v, ok := raw["ts"].(string); ok && v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// extractLineRole returns "user" / "assistant" / "" based on whichever
// adapter-specific role field is populated.
func extractLineRole(raw map[string]any) string {
	for _, key := range []string{"_role"} {
		if v, ok := raw[key].(string); ok {
			switch v {
			case "user":
				return "user"
			case "assistant":
				return "assistant"
			}
		}
	}
	if v, ok := raw["type"].(string); ok {
		switch v {
		case "user":
			return "user"
		case "assistant":
			return "assistant"
		}
	}
	if msg, ok := raw["message"].(map[string]any); ok {
		if r, ok := msg["role"].(string); ok {
			switch r {
			case "user":
				return "user"
			case "assistant":
				return "assistant"
			}
		}
	}
	return ""
}

// extractLineSession picks up the session id from the various places
// adapters put it. Returns "" if none found (caller falls back to a
// synthetic id).
func extractLineSession(raw map[string]any) string {
	for _, key := range []string{"sessionId", "session_id"} {
		if v, ok := raw[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// synthesizeFromMarkdown handles aider's transcript.md. Aider lacks
// per-line JSON metadata, so we use the file's mtime range as a coarse
// approximation and emit one working→ready cycle.
func synthesizeFromMarkdown(path string) []lifecycle.Event {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	end := st.ModTime()
	// Aider transcripts often span 30s–5min; without per-line ts we
	// guess a 60-second window so the user sees something animate.
	start := end.Add(-60 * time.Second)
	sessionID := "synthetic-" + filepath.Base(filepath.Dir(path))
	return []lifecycle.Event{
		{Seq: 1, Timestamp: start, Kind: lifecycle.KindTranscriptNew, SessionID: sessionID, Adapter: "aider", TranscriptPath: path},
		{Seq: 2, Timestamp: start.Add(100 * time.Millisecond), Kind: lifecycle.KindStateTransition, SessionID: sessionID, NewState: "ready", Reason: "synthetic"},
		{Seq: 3, Timestamp: start.Add(2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: sessionID, PrevState: "ready", NewState: "working", Reason: "synthetic user turn"},
		{Seq: 4, Timestamp: end.Add(-2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: sessionID, PrevState: "working", NewState: "ready", Reason: "synthetic assistant turn"},
		{Seq: 5, Timestamp: end, Kind: lifecycle.KindTranscriptRemoved, SessionID: sessionID},
	}
}

// buildArc converts a (firstTs, lastTs, userTs, asstTs) summary into a
// lifecycle.Event slice. Always emits at least transcript_new + an
// initial ready transition + transcript_removed at end.
//
// Critical invariant: every emitted event's timestamp must be >= the
// previous event's timestamp. The state machine computes inter-event
// deltas and races through any event whose delta is non-positive (it
// treats negative as "no wait"). The clampMonotonic helper enforces
// this — when a transcript line's timestamp would be earlier than the
// previous synthesized event (e.g. the first user line shares firstTs
// with the synthesized opener), nudge it forward by 1ms.
func buildArc(sessionID string, firstTs, lastTs time.Time, userTs, asstTs []time.Time) []lifecycle.Event {
	var (
		out     []lifecycle.Event
		seq     int64 = 1
		prevTs        = firstTs
	)
	emit := func(ts time.Time, kind lifecycle.Kind, prev, next, reason string) {
		clamped := clampMonotonic(ts, prevTs)
		out = append(out, lifecycle.Event{
			Seq: seq, Timestamp: clamped, Kind: kind,
			SessionID: sessionID, PrevState: prev, NewState: next, Reason: reason,
			Adapter: "claude-code",
		})
		seq++
		prevTs = clamped
	}
	emit(firstTs, lifecycle.KindTranscriptNew, "", "", "")
	emit(firstTs.Add(50*time.Millisecond), lifecycle.KindStateTransition, "", "ready", "synthetic: session start")

	state := "ready"
	idxU, idxA := 0, 0
	for idxU < len(userTs) || idxA < len(asstTs) {
		var nextTs time.Time
		var nextKind string
		switch {
		case idxU < len(userTs) && idxA < len(asstTs):
			if userTs[idxU].Before(asstTs[idxA]) {
				nextTs = userTs[idxU]
				nextKind = "user"
				idxU++
			} else {
				nextTs = asstTs[idxA]
				nextKind = "assistant"
				idxA++
			}
		case idxU < len(userTs):
			nextTs = userTs[idxU]
			nextKind = "user"
			idxU++
		default:
			nextTs = asstTs[idxA]
			nextKind = "assistant"
			idxA++
		}
		want := state
		switch nextKind {
		case "user":
			want = "working"
		case "assistant":
			want = "ready"
		}
		if want != state {
			emit(nextTs, lifecycle.KindStateTransition, state, want, "synthetic: transcript "+nextKind)
			state = want
		}
	}
	if state != "ready" {
		emit(lastTs, lifecycle.KindStateTransition, state, "ready", "synthetic: end of transcript")
	}
	emit(lastTs.Add(50*time.Millisecond), lifecycle.KindTranscriptRemoved, "", "", "")
	return out
}

// clampMonotonic returns ts if ts > floor, else floor+1ms. Used inside
// buildArc to guarantee every event's timestamp strictly exceeds the
// last one — otherwise the state machine treats negative deltas as
// zero-wait and races through synthesized events.
func clampMonotonic(ts, floor time.Time) time.Time {
	if ts.After(floor) {
		return ts
	}
	return floor.Add(time.Millisecond)
}

// LoadEventsOrSynthesize returns events.jsonl contents if they exist,
// otherwise synthesizes a stream from the scenario's transcript file.
// scenarioDir is the directory containing events.jsonl / transcript.jsonl
// / transcript.md. Returns nil if neither exists.
func LoadEventsOrSynthesize(scenarioDir string) ([]lifecycle.Event, error) {
	eventsPath := filepath.Join(scenarioDir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err == nil {
		return LoadEvents(eventsPath)
	}
	if events := SynthesizeEventsFromTranscript(scenarioDir); events != nil {
		return events, nil
	}
	return nil, nil
}
