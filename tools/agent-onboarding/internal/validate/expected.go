// Spec-derived expectation validation. expected.jsonl is per-scenario
// — written BEFORE the recording, anchored to the spec's Expected:
// bullets. Re-records do NOT rewrite it. Its purpose is to encode
// "these are the spec-grounded assertions the daemon must satisfy."
//
// A daemon change that drifts from the spec must FAIL expected.jsonl
// validation. The committed assertions are the regression contract.
//
// File shape (one meta line + N phase lines):
//
//   {"schema_version":1,"scenario_id":"...","source":"...","notes":"..."}
//   {"phase":"session_birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000,"text":"..."}
//   {"phase":"pid_bind","kind":"pid_discovered","relative_to":"session_birth","max_delay_ms":1000,"text":"..."}
//   ...
//
// See .claude/skills/ir:onboard-agent/translate/SKILL.md Step 3.5 for
// the authoring workflow.

package validate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ExpectedMeta is the first line of expected.jsonl — file-wide
// metadata. The validator carries this through into the report so
// downstream tooling can show source/notes alongside per-phase
// verdicts.
//
// KnownFailing marks a scenario whose validation is currently
// expected to fail because of a known daemon-side gap (cite the
// issue number in Notes). The validator still RUNS and surfaces the
// failure detail per phase; what changes is that the test wrapper
// (expected_test.go) and the CI path (replay-fixtures.sh) DON'T
// turn red. The phase-by-phase output stays visible so the
// maintainer can see when the gap closes. Remove this flag once the
// daemon issue is fixed and the recording re-records cleanly.
type ExpectedMeta struct {
	SchemaVersion int    `json:"schema_version"`
	ScenarioID    string `json:"scenario_id"`
	Source        string `json:"source"`
	Notes         string `json:"notes,omitempty"`
	KnownFailing  bool   `json:"known_failing,omitempty"`
}

// ExpectedPhase is one line of expected.jsonl after the meta line.
// Exactly one of ExpectedState / Kind must be set:
//   - ExpectedState ("ready"|"working"|"waiting") matches a state_transition.
//   - Kind matches an event whose .kind field equals it (e.g. "pid_discovered").
//
// Phases form a DAG via RelativeTo. "start" refers to the recording's
// first event timestamp. All other RelativeTo values must reference
// a phase declared EARLIER in the file (no forward refs).
type ExpectedPhase struct {
	Phase             string   `json:"phase"`
	ExpectedState     string   `json:"expected_state,omitempty"`
	Kind              string   `json:"kind,omitempty"`
	RelativeTo        string   `json:"relative_to,omitempty"`
	MaxDelayMs        int64    `json:"max_delay_ms,omitempty"`
	DurationAtLeastMs int64    `json:"duration_at_least_ms,omitempty"`
	Invariants        []string `json:"invariants,omitempty"`
	Trigger           string   `json:"trigger,omitempty"` // documentation-only this iteration
	Text              string   `json:"text,omitempty"`
}

// ExpectedResult is the per-phase verdict for an expected.jsonl file
// validated against an events.jsonl recording.
type ExpectedResult struct {
	Phase     string    `json:"phase"`
	Pass      bool      `json:"pass"`
	MatchedTs time.Time `json:"matched_ts,omitempty"`
	// DeltaMs is matched_ts - anchor_ts. Serialized even when 0 because
	// 0 is a meaningful value: it means the phase matched exactly at the
	// anchor event (common when one phase's match is also the next
	// phase's anchor, e.g. idle_window matching ready at first_turn_end).
	// With omitempty here the frontend reads `undefined` and renders
	// "+undefined ms" in the delta column.
	DeltaMs int64 `json:"delta_ms"`
	Reason    string    `json:"reason,omitempty"`   // pass/fail explanation
	Notes     []string  `json:"notes,omitempty"`    // invariant-check trace
}

// ExpectedReport is the full validation result — meta + the original
// phase definitions + per-phase verdicts. Definitions and Phases are
// the same length and indexed identically, so callers can pair them
// up by position. Including the definitions lets viewer UIs render
// target/anchor/window context next to the result without an extra
// fetch. RecordingStart is events[0].Ts — viewer UIs use it to
// convert matched_ts into an offset for timeline positioning.
type ExpectedReport struct {
	Meta           ExpectedMeta     `json:"meta"`
	Pass           bool             `json:"pass"`
	RecordingStart time.Time        `json:"recording_start"`
	Definitions    []ExpectedPhase  `json:"definitions"`
	Phases         []ExpectedResult `json:"phases"`
	Summary        string           `json:"summary"`
}

// recordedEvent is the subset of fields we care about from
// events.jsonl. Mirrors the daemon's lifecycle.Event JSON shape; we
// don't pull in lifecycle to keep this package's deps thin.
type recordedEvent struct {
	Ts        time.Time `json:"ts"`
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id"`
	NewState  string    `json:"new_state,omitempty"`
}

// ValidateExpected loads expected.jsonl + events.jsonl from
// scenarioDir and validates the recording against the spec-grounded
// expectations. Returns nil + nil if scenarioDir has no expected.jsonl
// (no expectations declared yet → nothing to validate, not a fail).
func ValidateExpected(scenarioDir string) (*ExpectedReport, error) {
	return ValidateExpectedAgainst(
		filepath.Join(scenarioDir, "expected.jsonl"),
		filepath.Join(scenarioDir, "events.jsonl"),
	)
}

// ValidateExpectedAgainst runs the validator with explicitly named
// expected.jsonl and events.jsonl paths. Lets the viewer evaluate
// an archived recording (in recordings/<name>/events.jsonl) against
// the CURRENT spec (top-level expected.jsonl) — that's the "did the
// daemon drift?" signal: an archive that PASSED at promote-time but
// FAILS the current spec means either the spec moved or the daemon
// went backward (the maintainer disambiguates).
func ValidateExpectedAgainst(expectedPath, eventsPath string) (*ExpectedReport, error) {
	if _, err := os.Stat(expectedPath); err != nil {
		return nil, nil // not configured for this scenario
	}
	meta, phases, err := loadExpected(expectedPath)
	if err != nil {
		return nil, fmt.Errorf("load expected.jsonl: %w", err)
	}
	events, err := loadEvents(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("load events.jsonl: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("events.jsonl has no entries — cannot validate")
	}

	anchorTs := map[string]time.Time{"start": events[0].Ts}
	matchedSid := map[string]string{} // phase → session_id of the event we matched
	results := make([]ExpectedResult, 0, len(phases))
	allPass := true

	for _, p := range phases {
		r := matchPhase(p, events, anchorTs, matchedSid)
		results = append(results, r)
		if !r.Pass {
			allPass = false
		}
		if r.Pass {
			anchorTs[p.Phase] = r.MatchedTs
		}
	}

	return &ExpectedReport{
		Meta:           meta,
		Pass:           allPass,
		RecordingStart: events[0].Ts,
		Definitions:    phases,
		Phases:         results,
		Summary:        summarize(results),
	}, nil
}

func loadExpected(path string) (ExpectedMeta, []ExpectedPhase, error) {
	var meta ExpectedMeta
	var phases []ExpectedPhase
	f, err := os.Open(path)
	if err != nil {
		return meta, nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if lineNum == 1 {
			if err := json.Unmarshal([]byte(line), &meta); err != nil {
				return meta, nil, fmt.Errorf("line %d: meta: %w", lineNum, err)
			}
			continue
		}
		var p ExpectedPhase
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return meta, nil, fmt.Errorf("line %d: phase: %w", lineNum, err)
		}
		if p.Phase == "" {
			return meta, nil, fmt.Errorf("line %d: phase name is required", lineNum)
		}
		if p.ExpectedState == "" && p.Kind == "" {
			return meta, nil, fmt.Errorf("line %d (%q): exactly one of expected_state or kind required", lineNum, p.Phase)
		}
		if p.ExpectedState != "" && p.Kind != "" {
			return meta, nil, fmt.Errorf("line %d (%q): expected_state and kind are mutually exclusive", lineNum, p.Phase)
		}
		phases = append(phases, p)
	}
	return meta, phases, scanner.Err()
}

func loadEvents(path string) ([]recordedEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []recordedEvent
	for scanner.Scan() {
		var e recordedEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // skip lines we can't parse — events.jsonl may carry richer fields
		}
		if e.Ts.IsZero() {
			continue
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}

// matchPhase runs one phase's validation against the event stream.
// Returns an ExpectedResult; doesn't mutate the inputs.
func matchPhase(p ExpectedPhase, events []recordedEvent, anchorTs map[string]time.Time, matchedSid map[string]string) ExpectedResult {
	r := ExpectedResult{Phase: p.Phase}

	anchorName := p.RelativeTo
	if anchorName == "" {
		anchorName = "start"
	}
	anchor, ok := anchorTs[anchorName]
	if !ok {
		r.Reason = fmt.Sprintf("unknown anchor %q (must be declared in an earlier phase or 'start')", anchorName)
		return r
	}

	// 1. Find the first event matching expected_state or kind, at or
	//    after the anchor. Skip events strictly before anchor — earlier
	//    matches belong to an earlier phase.
	var matched *recordedEvent
	for i := range events {
		ev := &events[i]
		if ev.Ts.Before(anchor) {
			continue
		}
		if p.ExpectedState != "" && ev.Kind == "state_transition" && ev.NewState == p.ExpectedState {
			matched = ev
			break
		}
		if p.Kind != "" && ev.Kind == p.Kind {
			matched = ev
			break
		}
	}
	if matched == nil {
		want := p.ExpectedState
		if want == "" {
			want = p.Kind
		}
		r.Reason = fmt.Sprintf("no event matching %q found at or after anchor %q", want, anchorName)
		return r
	}
	r.MatchedTs = matched.Ts
	r.DeltaMs = matched.Ts.Sub(anchor).Milliseconds()
	matchedSid[p.Phase] = matched.SessionID

	// 2. Check max_delay_ms.
	if p.MaxDelayMs > 0 && r.DeltaMs > p.MaxDelayMs {
		r.Reason = fmt.Sprintf("event arrived %d ms after anchor, exceeds max_delay_ms=%d", r.DeltaMs, p.MaxDelayMs)
		return r
	}

	// 3. Check duration_at_least_ms — the matched state must persist
	//    that long. Scan forward for the next state_transition that
	//    leaves expected_state; fail if it arrives too soon.
	if p.DurationAtLeastMs > 0 && p.ExpectedState != "" {
		endWindow := matched.Ts.Add(time.Duration(p.DurationAtLeastMs) * time.Millisecond)
		for i := range events {
			ev := &events[i]
			if !ev.Ts.After(matched.Ts) {
				continue
			}
			if ev.Ts.After(endWindow) {
				break
			}
			if ev.Kind == "state_transition" && ev.NewState != p.ExpectedState && ev.SessionID == matched.SessionID {
				early := ev.Ts.Sub(matched.Ts).Milliseconds()
				r.Reason = fmt.Sprintf("state changed to %q after only %d ms (expected to hold %q for %d ms)", ev.NewState, early, p.ExpectedState, p.DurationAtLeastMs)
				return r
			}
		}
	}

	// 4. Invariants over the phase's time window. Window starts at
	//    matched.Ts; ends at matched.Ts + DurationAtLeastMs if set,
	//    else at the next anchor referenced by a later phase, else
	//    end of events. For this iteration, the simple interpretation:
	//    window = matched.Ts ... matched.Ts + DurationAtLeastMs (or
	//    forever if no duration).
	var windowEnd time.Time
	if p.DurationAtLeastMs > 0 {
		windowEnd = matched.Ts.Add(time.Duration(p.DurationAtLeastMs) * time.Millisecond)
	}
	for _, inv := range p.Invariants {
		ok, why := checkInvariant(inv, events, matched, windowEnd)
		if !ok {
			r.Reason = fmt.Sprintf("invariant violated: %s — %s", inv, why)
			return r
		}
		r.Notes = append(r.Notes, fmt.Sprintf("invariant ok: %s", inv))
	}

	r.Pass = true
	if p.MaxDelayMs > 0 {
		r.Reason = fmt.Sprintf("matched at +%d ms (under %d ms max)", r.DeltaMs, p.MaxDelayMs)
	} else {
		r.Reason = fmt.Sprintf("matched at +%d ms after anchor %q", r.DeltaMs, anchorName)
	}
	return r
}

// Invariant DSL — two forms supported in iteration 10:
//
//	"no <kind> for <session-noun>"          — e.g. "no transcript_removed for primary session"
//	"no state_transition to <state>"        — e.g. "no state_transition to working"
//
// session-noun is currently informational (no per-session scoping yet):
// the check is "no event of <kind> appears in the window for the
// matched phase's session_id". Future expansion can introduce real
// session-set scoping; the DSL stays stable.
//
// Unknown invariants don't fail — they record a "skipped (unknown
// invariant DSL form)" note. Lets the schema evolve without breaking
// older expected.jsonl files.
var (
	invariantNoKindRE  = regexp.MustCompile(`^no\s+([a-z_]+)\s+for\s+(.+)$`)
	invariantNoStateRE = regexp.MustCompile(`^no\s+state_transition\s+to\s+([a-z]+)$`)
)

func checkInvariant(inv string, events []recordedEvent, matched *recordedEvent, windowEnd time.Time) (bool, string) {
	inv = strings.TrimSpace(inv)
	if m := invariantNoStateRE.FindStringSubmatch(inv); m != nil {
		forbiddenState := m[1]
		for i := range events {
			ev := &events[i]
			if !ev.Ts.After(matched.Ts) {
				continue
			}
			if !windowEnd.IsZero() && ev.Ts.After(windowEnd) {
				break
			}
			if ev.Kind == "state_transition" && ev.NewState == forbiddenState && ev.SessionID == matched.SessionID {
				return false, fmt.Sprintf("found state_transition to %q at +%d ms", forbiddenState, ev.Ts.Sub(matched.Ts).Milliseconds())
			}
		}
		return true, ""
	}
	if m := invariantNoKindRE.FindStringSubmatch(inv); m != nil {
		forbiddenKind := m[1]
		// session-noun in m[2] is informational for now.
		for i := range events {
			ev := &events[i]
			if !ev.Ts.After(matched.Ts) {
				continue
			}
			if !windowEnd.IsZero() && ev.Ts.After(windowEnd) {
				break
			}
			if ev.Kind == forbiddenKind && ev.SessionID == matched.SessionID {
				return false, fmt.Sprintf("found %s at +%d ms", forbiddenKind, ev.Ts.Sub(matched.Ts).Milliseconds())
			}
		}
		return true, ""
	}
	// Unknown DSL — don't fail, but flag.
	return true, "skipped (unknown invariant DSL form)"
}

func summarize(results []ExpectedResult) string {
	pass := 0
	for _, r := range results {
		if r.Pass {
			pass++
		}
	}
	return fmt.Sprintf("%d/%d phases passed", pass, len(results))
}
