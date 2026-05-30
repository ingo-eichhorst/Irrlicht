package replay

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/agentwiring"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/pkg/tailer"
)

// SynthesizeEventsFromTranscript builds a lifecycle event stream from a
// recording's transcript file (transcript.jsonl or transcript.md). Used
// by LoadEventsOrSynthesize when the recording predates the events.jsonl
// recorder — i.e. there is no daemon-produced sidecar to replay.
//
// This is the explicitly-degraded "no sidecar" path. For JSONL
// transcripts it drives core/application/replayengine — the SAME
// transcript→state-transition engine that produces the replay goldens —
// so the synthesized arc carries real waiting/permission semantics and
// can never diverge from what the daemon's classifier would assert.
// (The legacy path here fabricated a naive user→working / assistant→ready
// arc with no waiting state; issue #461 finding #1.)
//
// adapter is the canonical adapter name (e.g. "claude-code"); it selects
// the transcript parser. The aider markdown path keeps a coarse mtime
// approximation because aider transcripts carry no per-line timestamps.
//
// Returns nil if no transcript is present at any expected name.
func SynthesizeEventsFromTranscript(scenarioDir, adapter string) []lifecycle.Event {
	// Try common transcript filenames in order.
	candidates := []string{"transcript.jsonl", "transcript.md"}
	for _, name := range candidates {
		path := filepath.Join(scenarioDir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if strings.HasSuffix(name, ".jsonl") {
			return synthesizeViaEngine(path, adapter)
		}
		return synthesizeFromMarkdown(path)
	}
	return nil
}

// synthesizeViaEngine replays a JSONL transcript through the shared
// classifier engine and maps its transitions to lifecycle events. The
// engine writes the transcript to a scratch file and tails it exactly as
// the daemon would, so claudecode / codex / pi / opencode all work via
// their real parser. Returns nil when the transcript yields no events.
func synthesizeViaEngine(path, adapter string) []lifecycle.Event {
	canonical, parser := resolveParser(adapter)
	res, err := replayengine.ReplayTranscript(path, replayengine.Options{
		Adapter:                    canonical,
		Parser:                     parser,
		DisableModelConfigFallback: true,
	})
	if err != nil || res == nil || len(res.Transitions) == 0 {
		return nil
	}

	sessionID := firstSessionID(path)
	if sessionID == "" {
		sessionID = "synthetic-" + filepath.Base(filepath.Dir(path))
	}

	var (
		out    []lifecycle.Event
		seq    int64 = 1
		prevTs       = res.FirstEventTime
	)
	emit := func(ts time.Time, kind lifecycle.Kind, prev, next, reason string) {
		clamped := clampMonotonic(ts, prevTs)
		out = append(out, lifecycle.Event{
			Seq: seq, Timestamp: clamped, Kind: kind,
			SessionID: sessionID, Adapter: canonical,
			PrevState: prev, NewState: next, Reason: reason,
			TranscriptPath: path,
		})
		seq++
		prevTs = clamped
	}

	emit(res.FirstEventTime, lifecycle.KindTranscriptNew, "", "", "")
	for _, t := range res.Transitions {
		emit(t.VirtualTime, lifecycle.KindStateTransition, t.PrevState, t.NewState, t.Reason)
	}
	emit(res.LastEventTime.Add(50*time.Millisecond), lifecycle.KindTranscriptRemoved, "", "", "")
	return out
}

// resolveParser maps a scenario's agent dir-slug to the canonical adapter
// name and its transcript parser, using the same shared parser map the
// daemon and viewer metrics collector use. Unknown/empty slugs fall back
// to Claude Code, matching the replay CLI's parserFor.
func resolveParser(adapter string) (string, tailer.TranscriptParser) {
	canonical := adapter
	switch adapter {
	case "", "claudecode":
		canonical = claudecode.AdapterName
	}
	if f, ok := agentwiring.ParserFactories(agents.All())[canonical]; ok {
		return canonical, f()
	}
	return claudecode.AdapterName, &claudecode.Parser{}
}

// firstSessionID scans a JSONL transcript for the first session id it can
// find across the adapter-specific field names. Returns "" if none.
func firstSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if sid := extractLineSession(raw); sid != "" {
			return sid
		}
	}
	return ""
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

// extractLineText returns the user-visible text content of a transcript
// line, or "" if none. Handles the three shapes we've seen:
//
//	opencode:   {"text": "..."}
//	claudecode: {"message": {"content": "..."}}                       (string)
//	claudecode: {"message": {"content": [{"type": "text", "text": "..."}, ...]}} (block array)
//
// For block arrays we concatenate the first two text blocks; tool_use
// and tool_result blocks are skipped so the lane shows the conversation
// the user actually wrote/read, not tool internals.
func extractLineText(raw map[string]any) string {
	if v, ok := raw["text"].(string); ok && v != "" {
		return v
	}
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return ""
	}
	switch c := msg["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, blk := range c {
			if len(parts) >= 2 {
				break
			}
			b, ok := blk.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := b["type"].(string); t != "" && t != "text" {
				continue
			}
			if s, _ := b["text"].(string); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
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

// clampMonotonic returns ts if ts > floor, else floor+1ms. Used inside
// synthesizeViaEngine to guarantee every event's timestamp strictly
// exceeds the last one — otherwise the state machine treats negative
// deltas as zero-wait and races through synthesized events.
func clampMonotonic(ts, floor time.Time) time.Time {
	if ts.After(floor) {
		return ts
	}
	return floor.Add(time.Millisecond)
}

// TurnMarker is one user prompt or assistant response, placed on the
// viewer's timeline track above the state band. Anchored to the same
// recording start the EventMarker stream uses so the lanes line up
// visually.
type TurnMarker struct {
	OffsetMs  int64  `json:"offset_ms"`
	Role      string `json:"role"` // "user" or "assistant"
	Text      string `json:"text,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// LoadTurnMarkers walks the scenario's transcript and emits one
// TurnMarker per user / assistant line. Offsets are computed against
// `anchor` (the EventMarker anchor) and clamped to >= 0 so a transcript
// line predating events[0] by a few ms still renders at position 0.
//
// Returns nil for transcripts without per-line timestamps (aider's
// transcript.md) and for transcripts with no user/assistant lines.
func LoadTurnMarkers(scenarioDir string, anchor time.Time) []TurnMarker {
	for _, name := range []string{"transcript.jsonl"} {
		path := filepath.Join(scenarioDir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		return loadTurnsFromJSONL(path, anchor)
	}
	return nil
}

const turnTextMax = 240

func loadTurnsFromJSONL(path string, anchor time.Time) []TurnMarker {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []TurnMarker
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		role := extractLineRole(raw)
		if role != "user" && role != "assistant" {
			continue
		}
		ts, ok := extractLineTime(raw)
		if !ok {
			continue
		}
		text := extractLineText(raw)
		if text == "" {
			continue
		}
		offset := ts.Sub(anchor).Milliseconds()
		if offset < 0 {
			offset = 0
		}
		out = append(out, TurnMarker{
			OffsetMs:  offset,
			Role:      role,
			Text:      truncateForTooltip(text, turnTextMax),
			SessionID: extractLineSession(raw),
		})
	}
	return out
}

// truncateForTooltip caps text length and folds whitespace so a long
// multi-line prompt fits a single-line title attribute. Replaces both
// "\r\n" and "\n" with "↵ " so the user can still see paragraph breaks
// in the tooltip without it growing vertically.
func truncateForTooltip(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "↵ ")
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// LoadEventsOrSynthesize returns the daemon-recorded events.jsonl when it
// exists, otherwise synthesizes a degraded stream from the transcript.
//
// The returned degraded flag is true only for the synthesized case: there
// was no daemon-produced sidecar, so the timeline was reconstructed by
// replaying the transcript through the shared classifier engine. The UI
// surfaces this so a reconstructed arc is never mistaken for a recorded
// one. adapter is the scenario's agent dir-slug (selects the parser).
//
// scenarioDir is the directory containing events.jsonl / transcript.jsonl
// / transcript.md. Returns (nil, false, nil) if none exists.
func LoadEventsOrSynthesize(scenarioDir, adapter string) (events []lifecycle.Event, degraded bool, err error) {
	eventsPath := filepath.Join(scenarioDir, "events.jsonl")
	if _, statErr := os.Stat(eventsPath); statErr == nil {
		ev, loadErr := LoadEvents(eventsPath)
		return ev, false, loadErr
	}
	if ev := SynthesizeEventsFromTranscript(scenarioDir, adapter); ev != nil {
		return ev, true, nil
	}
	return nil, false, nil
}
