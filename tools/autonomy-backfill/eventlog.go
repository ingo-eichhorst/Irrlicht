package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

// The daemon's own event log (<data-dir>/logs/events.log*) is the higher-
// fidelity of the two sources: its `session-detector` entries record the
// transition that ACTUALLY happened, so a span reconstructed from it carries a
// real end reason — measured at the time, read back later.
//
// It is also the shorter of the two. The log rotates, so it reaches back only
// as far as the retained files do; everything older falls to the cost log.

// eventLogGlob is where the daemon writes and rotates its event log, relative
// to the data dir.
const eventLogGlob = "logs/events.log*"

// detectorEventType is the event_type the session detector stamps on every
// entry it writes, including every state transition.
const detectorEventType = "session-detector"

// startupEventType is the event_type the daemon stamps while booting. Its
// timestamps are the restart boundaries: a reconstructed span that crosses one
// is not one run but several, merged because the transitions in between were
// never logged (see dropRestartStraddlers).
const startupEventType = "startup"

// restartClusterSeconds is how close two `startup` entries have to be to count
// as one boot. A boot emits its whole startup banner in well under a second;
// 30 s is generous enough to absorb a slow one without merging two genuinely
// separate restarts, which on this machine are minutes apart at the closest.
const restartClusterSeconds int64 = 30

// arrowPattern matches a transition message's destination state.
//
// DERIVED from session.CanonicalStates() rather than typed out, per AGENTS.md
// — and here the derivation is not just hygiene. The census of real messages
// this was built against covers nineteen distinct shapes, from
// "agent finished turn → ready" to "force ready→working on first activity"
// (no space before the arrow) to
// "finished orphaned subagent (working → ready) — parent <id> turn done"
// (an arrow with a state on BOTH sides). A hand-typed alternation would be
// silently incomplete the day a fifth state ships, and the tool would quietly
// reconstruct fewer runs rather than fail.
//
// `\s*` covers the no-space spelling. The LAST match on the line is the
// destination: every two-state message reads "<from> → <to>".
var arrowPattern = regexp.MustCompile(`→\s*(` + strings.Join(session.CanonicalStates(), "|") + `)`)

// eventLogLine is the subset of a daemon log entry this tool reads.
type eventLogLine struct {
	Timestamp string `json:"timestamp"`
	EventType string `json:"event_type"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// transition is one logged state change: when it happened, to which session,
// and which state it landed in.
type transition struct {
	TS      int64
	Session string
	State   string
}

// eventLog is everything the reconstruction needs out of the daemon's log.
type eventLog struct {
	// Transitions are every parsed state change, ordered by timestamp.
	Transitions []transition

	// Restarts are the daemon's boot instants, ordered, one per boot.
	//
	// A single boot writes MANY `startup` lines — this machine's log holds 400
	// of them across 88 boots — so they are clustered (see
	// restartClusterSeconds) rather than counted one for one. The clustering
	// does not change which spans get dropped, since every line of one boot
	// falls inside the same gap; it changes the number the report prints, and
	// a restart count four times too high is a figure that documents nothing.
	Restarts []int64

	// Stats is the parse census. It exists so an unreadable log fails LOUDLY
	// rather than yielding fewer spans (AGENTS.md: absence of a finding and
	// inability to look must never produce the same output).
	Stats parseStats
}

// parseStats is one source's parse census.
//
// Malformed is the load-bearing field: a source this tool cannot read is the
// LAST place to quietly carry on, because "the log held no transitions" and
// "the log was unreadable" produce identical span counts otherwise. main
// refuses to --apply when the malformed share crosses malformedLimit.
type parseStats struct {
	Files     int
	Lines     int
	Parsed    int // lines that decoded into a JSON object with a usable timestamp
	Malformed int // non-empty lines that did not
	Relevant  int // parsed lines this source actually used
}

// MalformedShare is the fraction of non-empty lines that would not parse.
func (s parseStats) MalformedShare() float64 {
	if s.Lines == 0 {
		return 0
	}
	return float64(s.Malformed) / float64(s.Lines)
}

func (s parseStats) String() string {
	return fmt.Sprintf("%d file(s), %d lines, %d parsed, %d used, %d malformed (%.3f%%)",
		s.Files, s.Lines, s.Parsed, s.Relevant, s.Malformed, s.MalformedShare()*100)
}

// readEventLog parses every retained event-log file under dataDir.
//
// Returns an error only when the log is missing or unreadable as a whole. An
// individual line that will not decode is COUNTED, never fatal: the newest
// file's tail is routinely a half-written line, and one truncated record must
// not blind the reconstruction to everything before it. The count is what
// makes the difference visible.
func readEventLog(dataDir string) (*eventLog, error) {
	paths, err := filepath.Glob(filepath.Join(dataDir, eventLogGlob))
	if err != nil {
		return nil, fmt.Errorf("glob event log: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no event log under %s — this machine has nothing to reconstruct from",
			filepath.Join(dataDir, eventLogGlob))
	}
	sort.Strings(paths)

	out := &eventLog{Stats: parseStats{Files: len(paths)}}
	var startups []int64
	for _, p := range paths {
		if err := scanEventLogFile(p, out, &startups); err != nil {
			return nil, err
		}
	}
	out.Restarts = clusterRestarts(startups, restartClusterSeconds)
	// Rotated files are not in timestamp order by name (events.log is the
	// NEWEST, events.log.1 the one before it), so the merge has to be sorted
	// rather than assumed. A stable sort keeps two transitions stamped in the
	// same second in the order they were written, which is the order the state
	// machine has to see them in.
	sort.SliceStable(out.Transitions, func(i, j int) bool { return out.Transitions[i].TS < out.Transitions[j].TS })
	return out, nil
}

// clusterRestarts collapses a boot's burst of `startup` entries into the ONE
// instant the daemon came back, taking the earliest entry of each cluster —
// the daemon was down before that timestamp, not after it.
func clusterRestarts(startups []int64, within int64) []int64 {
	if len(startups) == 0 {
		return nil
	}
	sort.Slice(startups, func(i, j int) bool { return startups[i] < startups[j] })
	out := []int64{startups[0]}
	last := startups[0]
	for _, ts := range startups[1:] {
		if ts-last > within {
			out = append(out, ts)
		}
		last = ts
	}
	return out
}

// scanEventLogFile folds one log file into out.
func scanEventLogFile(path string, out *eventLog, startups *[]int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, scanBufInitial), scanBufMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		out.Stats.Lines++
		var e eventLogLine
		if err := json.Unmarshal(line, &e); err != nil {
			out.Stats.Malformed++
			continue
		}
		ts, ok := parseLogTimestamp(e.Timestamp)
		if !ok {
			out.Stats.Malformed++
			continue
		}
		out.Stats.Parsed++
		switch e.EventType {
		case startupEventType:
			*startups = append(*startups, ts)
			out.Stats.Relevant++
		case detectorEventType:
			state, ok := destinationState(e.Message)
			if !ok || e.SessionID == "" {
				continue
			}
			out.Transitions = append(out.Transitions, transition{TS: ts, Session: e.SessionID, State: state})
			out.Stats.Relevant++
		}
	}
	if err := sc.Err(); err != nil {
		// A scan error is NOT a malformed line: it means the read itself
		// failed, and carrying on would silently truncate the source.
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// destinationState extracts the state a transition message lands in.
func destinationState(message string) (string, bool) {
	m := arrowPattern.FindAllStringSubmatch(message, -1)
	if len(m) == 0 {
		return "", false
	}
	return m[len(m)-1][1], true
}

// parseLogTimestamp reads the daemon's RFC3339 log timestamp into unix
// seconds. ok is false for anything it cannot read, which the caller counts as
// malformed rather than treating as the zero time (1970 would land every such
// line at the very start of the window and produce spans out of nothing).
func parseLogTimestamp(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}
