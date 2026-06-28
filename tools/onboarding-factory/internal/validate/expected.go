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
// See .claude/skills/ir:onboarding-factory/assess/SKILL.md for the
// spec-authoring workflow.
//
// # Relationship to tools/onboarding-factory/cmd/replay's extended check
//
// There are two validators in this repo and they are orthogonal — neither
// overrides the other, because they answer different questions:
//
//   - THIS validator (spec-intent): "does the daemon's recorded behaviour
//     satisfy the spec-grounded assertions?" It compares events.jsonl
//     against the hand-authored expected.jsonl. A failure here means the
//     daemon drifted from the spec — a behaviour regression.
//   - tools/onboarding-factory/cmd/replay/extended_check.go (recording-fidelity): "does the
//     deterministic replay reproduce the same state transitions the live
//     daemon recorded in the sidecar?" It compares replayed transitions
//     against the recorded ones. A failure here means the replay engine
//     drifted from the daemon — a replay-fidelity bug, not necessarily a
//     behaviour regression.
//
// When they disagree: this validator is authoritative for "is the
// behaviour correct" (spec is the contract); extended_check is
// authoritative for "is replay faithful to the recording." A spec failure
// is fixed in the daemon (or, if intended, by updating the spec); an
// extended-check-only failure is fixed in the replay engine.

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
	// Observations carries the go-test-style assertions over the recording's
	// metric vector (model / cost / tokens), beyond the lifecycle-state phases.
	// Absent → no hard metric assertions (the soft-diff vs the prior recording
	// still runs in ValidateObservations).
	Observations *ObservationSpec `json:"observations,omitempty"`
}

// ObservationSpec is the optional metric-assertion block of an expected.jsonl
// meta line. Categorical fields (model) assert exact equality; numeric fields
// assert nonzero. The full vector is additionally soft-diffed against the prior
// recording within TolerancePct — see ValidateObservations.
type ObservationSpec struct {
	Model         string  `json:"model,omitempty"`          // exact-match assertion (categorical)
	CostNonzero   bool    `json:"cost_nonzero,omitempty"`   // estimated_cost_usd > 0
	TokensNonzero bool    `json:"tokens_nonzero,omitempty"` // cum_input+output > 0
	TolerancePct  float64 `json:"tolerance_pct,omitempty"`  // soft-diff band vs prior (default 50)

	// Store-derived context/token assertions (#766) for adapters whose usage
	// lives in an out-of-band store rather than the transcript (antigravity's
	// conversations/<conv>.db, #719). These read the golden's store-derived
	// vector, which is distinct from the cum_input+output TokensNonzero covers.
	TotalTokensNonzero        bool `json:"total_tokens_nonzero,omitempty"`        // summary.total_tokens > 0
	ContextWindowNonzero      bool `json:"context_window_nonzero,omitempty"`      // summary.context_window > 0
	ContextUtilizationNonzero bool `json:"context_utilization_nonzero,omitempty"` // summary.context_utilization_percentage > 0
}

// ExpectedPhase is one line of expected.jsonl after the meta line.
// Exactly one of ExpectedState / Kind must be set:
//   - ExpectedState ("ready"|"working"|"waiting") matches a state_transition.
//   - Kind matches an event whose .kind field equals it (e.g. "pid_discovered").
//
// Phases form a DAG via RelativeTo. "start" refers to the recording's
// first event timestamp. All other RelativeTo values must reference
// a phase declared EARLIER in the file (no forward refs).
//
// Session-identity predicates (optional, mutually exclusive):
//   - SameSessionAs: <phase name> — the matched event's session_id
//     must equal the session_id matched by an earlier-named phase.
//     Use this to pin a phase to a specific session row (e.g. assert
//     v2_turn_start fires on the SAME session as v2_session_birth,
//     not on a leftover transition from v1).
//   - NewSession: true — the matched event's session_id must NOT
//     equal any previously-matched phase's session_id. Use this to
//     assert "this phase happens on a brand-new session" (e.g. the
//     post-/clear UUID after session-reset).
type ExpectedPhase struct {
	Phase             string   `json:"phase"`
	ExpectedState     string   `json:"expected_state,omitempty"`
	Kind              string   `json:"kind,omitempty"`
	RelativeTo        string   `json:"relative_to,omitempty"`
	MaxDelayMs        int64    `json:"max_delay_ms,omitempty"`
	DurationAtLeastMs int64    `json:"duration_at_least_ms,omitempty"`
	SameSessionAs     string   `json:"same_session_as,omitempty"`
	NewSession        bool     `json:"new_session,omitempty"`
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
	DeltaMs int64    `json:"delta_ms"`
	Reason  string   `json:"reason,omitempty"` // pass/fail explanation
	Notes   []string `json:"notes,omitempty"`  // invariant-check trace
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

// NewestRecordingDir returns the path to the newest recording under
// scenarioDir/recordings/ — the lexicographically-greatest entry name, which
// (recording names are timestamp-prefixed) is the most recent. ok is false
// when there is no recordings/ dir or it holds no subdirectories.
func NewestRecordingDir(scenarioDir string) (string, bool) {
	entries, err := os.ReadDir(filepath.Join(scenarioDir, "recordings"))
	if err != nil {
		return "", false
	}
	newest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return "", false
	}
	return filepath.Join(scenarioDir, "recordings", newest), true
}

// RecordingComplete reports problems with a single recording directory as a
// list of human-readable findings (nil when the recording is complete). The
// on-disk recordings/<name>/ tree is the single source of truth for a cell, so
// a complete recording must carry the daemon events, a manifest, exactly one
// transcript, and — for a jsonl transcript — the replay byte-identity golden.
// An incomplete recording is a hard validation error, never silently tolerated.
func RecordingComplete(recDir string) []string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(recDir, name))
		return err == nil
	}
	var findings []string
	if !exists("events.jsonl") {
		findings = append(findings, "missing events.jsonl")
	}
	if !exists("manifest.json") {
		findings = append(findings, "missing manifest.json")
	}
	hasJSONL := exists("transcript.jsonl")
	hasMD := exists("transcript.md")
	switch {
	case hasJSONL && hasMD:
		findings = append(findings, "ambiguous transcript: both transcript.jsonl and transcript.md present")
	case !hasJSONL && !hasMD:
		findings = append(findings, "missing transcript (need transcript.jsonl or transcript.md)")
	}
	// The replay byte-identity golden is required for jsonl transcripts (the
	// replay test pins them); markdown-transcript adapters (aider) have none.
	if hasJSONL && !exists("transcript.jsonl.replay.json.golden") {
		findings = append(findings, "missing transcript.jsonl.replay.json.golden (required for a jsonl transcript)")
	}
	return findings
}

// ValidateExpected validates the cell's NEWEST recording against the
// spec-grounded expectations in scenarioDir/expected.jsonl. Returns nil + nil
// when there is nothing to validate: either expected.jsonl is missing (no
// expectations declared yet) OR no recording is present (not yet captured —
// typical for applicable:false cells). It returns an ERROR for a half-recorded
// cell — a transcript present in the newest recording but no events.jsonl — so
// a partial recording can't masquerade as a pass (#496 RC6).
//
// Every recording lives under recordings/<name>/; the spec (expected.jsonl)
// stays at the cell root and is validated against the newest recording.
func ValidateExpected(scenarioDir string) (*ExpectedReport, error) {
	expectedPath := filepath.Join(scenarioDir, "expected.jsonl")
	recDir, ok := NewestRecordingDir(scenarioDir)
	if !ok {
		// No recording captured — nothing to validate (same shape as no spec).
		return nil, nil
	}
	eventsPath := filepath.Join(recDir, "events.jsonl")
	// HALF-recorded guard (#496 RC6) — scoped to the CELL path here, NOT to
	// ValidateExpectedAgainst. A cell with expected.jsonl + a transcript but no
	// events.jsonl in its newest recording is a partial recording; returning
	// (nil,nil) made replay-fixtures report a vacuous PASS (opencode/task-list).
	if _, err := os.Stat(expectedPath); err == nil {
		if _, err := os.Stat(eventsPath); err != nil {
			for _, t := range []string{"transcript.jsonl", "transcript.md"} {
				if _, terr := os.Stat(filepath.Join(recDir, t)); terr == nil {
					return nil, fmt.Errorf(
						"incomplete recording: %s present but events.jsonl missing in %s — "+
							"the cell has a spec and a transcript but no captured events; "+
							"re-record it (run-cell.sh) so validation isn't silently skipped",
						t, filepath.Base(recDir))
				}
			}
		}
	}
	return ValidateExpectedAgainst(expectedPath, eventsPath)
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
	if _, err := os.Stat(eventsPath); err != nil {
		// events.jsonl not captured yet — same shape as no expected.jsonl. The
		// half-recorded (transcript-but-no-events) guard lives in ValidateExpected
		// (the cell path); this archive-drift evaluator keeps the silent skip so a
		// missing-events archive isn't reported as a spurious error (#496 RC6).
		return nil, nil
	}
	meta, phases, err := loadExpected(expectedPath)
	if err != nil {
		return nil, fmt.Errorf("load expected.jsonl: %w", err)
	}
	return ValidatePhases(meta, phases, eventsPath)
}

// ValidatePhases validates already-parsed spec phases against the recording at
// eventsPath. It is the shared core of two callers that differ only in WHERE
// the spec comes from: ValidateExpectedAgainst loads it from an on-disk
// expected.jsonl; the shard readers parse it out of a scenario shard via
// ParseShardSpec. Returns (nil, nil) when eventsPath is absent — the recording
// hasn't been captured yet (the half-recorded guard lives in ValidateExpected,
// the cell path).
func ValidatePhases(meta ExpectedMeta, phases []ExpectedPhase, eventsPath string) (*ExpectedReport, error) {
	if _, err := os.Stat(eventsPath); err != nil {
		return nil, nil
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

// ParseShardSpec parses a scenario shard's spec blocks — the per-adapter
// details.expected_meta object and details.expected[] phase array — into the
// same ExpectedMeta + []ExpectedPhase shapes loadExpected produces from an
// on-disk expected.jsonl. The per-phase validation rules are identical to
// loadExpected's (phase name required; exactly one of expected_state/kind;
// same_session_as and new_session mutually exclusive) so a shard spec and an
// expected.jsonl spec are accepted or rejected on the same terms. ok is false
// when there are no phase lines — nothing to validate, not an error.
func ParseShardSpec(metaLine json.RawMessage, phaseLines []json.RawMessage) (ExpectedMeta, []ExpectedPhase, bool, error) {
	var meta ExpectedMeta
	if len(metaLine) > 0 {
		if err := json.Unmarshal(metaLine, &meta); err != nil {
			return meta, nil, false, fmt.Errorf("parse expected_meta: %w", err)
		}
	}
	if len(phaseLines) == 0 {
		return meta, nil, false, nil
	}
	phases := make([]ExpectedPhase, 0, len(phaseLines))
	for i, line := range phaseLines {
		var p ExpectedPhase
		if err := json.Unmarshal(line, &p); err != nil {
			return meta, nil, false, fmt.Errorf("expected phase %d: %w", i, err)
		}
		if p.Phase == "" {
			return meta, nil, false, fmt.Errorf("expected phase %d: phase name is required", i)
		}
		if p.ExpectedState == "" && p.Kind == "" {
			return meta, nil, false, fmt.Errorf("expected phase %d (%q): exactly one of expected_state or kind required", i, p.Phase)
		}
		if p.ExpectedState != "" && p.Kind != "" {
			return meta, nil, false, fmt.Errorf("expected phase %d (%q): expected_state and kind are mutually exclusive", i, p.Phase)
		}
		if p.SameSessionAs != "" && p.NewSession {
			return meta, nil, false, fmt.Errorf("expected phase %d (%q): same_session_as and new_session are mutually exclusive", i, p.Phase)
		}
		phases = append(phases, p)
	}
	return meta, phases, true, nil
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
		if p.SameSessionAs != "" && p.NewSession {
			return meta, nil, fmt.Errorf("line %d (%q): same_session_as and new_session are mutually exclusive", lineNum, p.Phase)
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

	// Resolve session-id constraint up front so candidate filtering is
	// cheap inside the loop. SameSessionAs pins the matched session_id
	// to a specific earlier phase's match; NewSession requires a
	// session_id we haven't seen yet.
	var requireSID string
	if p.SameSessionAs != "" {
		sid, ok := matchedSid[p.SameSessionAs]
		if !ok {
			r.Reason = fmt.Sprintf("same_session_as references unknown phase %q", p.SameSessionAs)
			return r
		}
		requireSID = sid
	}
	var seenSIDs map[string]struct{}
	if p.NewSession {
		seenSIDs = make(map[string]struct{}, len(matchedSid))
		for _, sid := range matchedSid {
			seenSIDs[sid] = struct{}{}
		}
	}

	// 1. Find the first event matching expected_state or kind, at or
	//    after the anchor, satisfying any session-id constraint. Skip
	//    events strictly before anchor — earlier matches belong to an
	//    earlier phase.
	var matched *recordedEvent
	for i := range events {
		ev := &events[i]
		if ev.Ts.Before(anchor) {
			continue
		}
		kindOK := false
		if p.ExpectedState != "" && ev.Kind == "state_transition" && ev.NewState == p.ExpectedState {
			kindOK = true
		} else if p.Kind != "" && ev.Kind == p.Kind {
			kindOK = true
		}
		if !kindOK {
			continue
		}
		if requireSID != "" && ev.SessionID != requireSID {
			continue
		}
		if p.NewSession {
			if _, seen := seenSIDs[ev.SessionID]; seen {
				continue
			}
		}
		matched = ev
		break
	}
	if matched == nil {
		want := p.ExpectedState
		if want == "" {
			want = p.Kind
		}
		switch {
		case requireSID != "":
			r.Reason = fmt.Sprintf("no event matching %q found for session %q at or after anchor %q", want, requireSID, anchorName)
		case p.NewSession:
			r.Reason = fmt.Sprintf("no event matching %q on a NEW session found at or after anchor %q (all candidates were already-seen session ids)", want, anchorName)
		default:
			r.Reason = fmt.Sprintf("no event matching %q found at or after anchor %q", want, anchorName)
		}
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
