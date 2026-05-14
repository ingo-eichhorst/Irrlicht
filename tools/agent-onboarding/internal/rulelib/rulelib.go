// Package rulelib defines the candidate rule kinds the synthesizer
// composes when deriving a per-agent state classifier from ground-truth
// labels. Each Rule encodes one observable signal pattern that, when
// matched, implies a target state.
//
// Phase 3's greedy compositor picks the smallest priority-ordered list
// of rules that classifies every labeled point correctly. The chosen
// rules are emitted into ruleset.json and consumed at runtime by
// `core/application/services/rule_evaluator.go`.
package rulelib

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Rule is one classification rule. Each rule has a Kind, parameters
// (interpreted by the matching Kind's logic), an implied target state,
// and a stable RuleID for evidence reporting in the Phase 7 viewer.
type Rule struct {
	RuleID      string          `json:"rule_id"`
	Kind        string          `json:"kind"`
	TargetState string          `json:"target_state"` // working | waiting | ready
	Params      json.RawMessage `json:"params"`
	Priority    int             `json:"priority"` // lower wins
}

// 11 candidate kinds per the issue spec.
const (
	KindTranscriptTailRegex      = "transcript_tail_regex"
	KindTranscriptFieldValue     = "transcript_field_value"
	KindTranscriptEventKind      = "transcript_event_kind"
	KindIdleGap                  = "idle_gap"
	KindFileEventBurst           = "file_event_burst"
	KindProcessSpawned           = "process_spawned"
	KindPTYANSISequence          = "pty_ansi_sequence"
	KindPaneSubstringPresent     = "pane_substring_present"
	KindPaneSubstringDisappeared = "pane_substring_disappeared"
	KindNetworkRequestActive     = "network_request_active"
	KindHookFired                = "hook_fired"
	KindInterruptMarker          = "interrupt_marker"
)

// AllKinds returns every supported kind in stable priority order.
// Synthesis tries earlier kinds first when proposing candidates.
func AllKinds() []string {
	return []string{
		KindHookFired,
		KindTranscriptFieldValue,
		KindTranscriptEventKind,
		KindInterruptMarker,
		KindTranscriptTailRegex,
		KindPaneSubstringPresent,
		KindPaneSubstringDisappeared,
		KindPTYANSISequence,
		KindProcessSpawned,
		KindFileEventBurst,
		KindNetworkRequestActive,
		KindIdleGap,
	}
}

// MatchInput is the per-signal shape passed into Match. Sensor + Kind are
// from the Signal; Payload is the raw JSON.
type MatchInput struct {
	Sensor  string          // e.g. "transcript", "pane"
	Kind    string          // e.g. "line", "snapshot"
	Payload json.RawMessage // sensor-specific
}

// Match reports whether rule fires on the given signal. Each rule kind
// interprets r.Params according to its own schema (documented inline).
//
// Unknown kinds return false silently — synthesis treats them as
// non-firing candidates and prunes them from the ruleset.
func Match(r Rule, in MatchInput) bool {
	switch r.Kind {
	case KindTranscriptTailRegex:
		// params: {"pattern": "<regex>"}
		if in.Sensor != "transcript" || in.Kind != "line" {
			return false
		}
		var p struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return false
		}
		return re.MatchString(pl.Line)

	case KindTranscriptFieldValue:
		// params: {"field": "stop_reason", "value": "end_turn"}
		// Treats the transcript line as a JSON object and inspects the field.
		if in.Sensor != "transcript" || in.Kind != "line" {
			return false
		}
		var p struct {
			Field string `json:"field"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		var lineObj map[string]any
		if err := json.Unmarshal([]byte(pl.Line), &lineObj); err != nil {
			return false
		}
		return fieldEquals(lineObj, p.Field, p.Value)

	case KindTranscriptEventKind:
		// params: {"event_kind": "transcript_new"}
		if in.Sensor != "transcript" || in.Kind != "line" {
			return false
		}
		var p struct {
			EventKind string `json:"event_kind"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		var lineObj map[string]any
		if err := json.Unmarshal([]byte(pl.Line), &lineObj); err != nil {
			return false
		}
		return fieldEquals(lineObj, "kind", p.EventKind) ||
			fieldEquals(lineObj, "event_type", p.EventKind) ||
			fieldEquals(lineObj, "type", p.EventKind)

	case KindIdleGap:
		// params: {"channel": "transcript", "ms": 5000}
		// Match logic for idle_gap is time-based; the synth treats this as
		// a synthetic "no signals seen recently" rule. Returns false here;
		// the rule evaluator handles timing separately.
		return false

	case KindFileEventBurst:
		// params: {"glob": "*.py", "count": 3}. Fires on a single fs event;
		// the evaluator buckets matches across a time window.
		if in.Sensor != "fs" {
			return false
		}
		var p struct {
			Glob string `json:"glob"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		// Simple suffix match; full glob is the evaluator's job.
		if p.Glob == "" {
			return true
		}
		return strings.HasSuffix(pl.Path, strings.TrimPrefix(p.Glob, "*"))

	case KindProcessSpawned:
		// params: {"name_pattern": "^python"}
		if in.Sensor != "proc" || in.Kind != "spawn" {
			return false
		}
		var p struct {
			NamePattern string `json:"name_pattern"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		if len(pl.Args) == 0 {
			return false
		}
		re, err := regexp.Compile(p.NamePattern)
		if err != nil {
			return false
		}
		return re.MatchString(pl.Args[0])

	case KindPTYANSISequence:
		// params: {"regex": "<binary regex over chunk bytes>"} — base64'd
		// payload is decoded by the evaluator. Returns false in this stub.
		return false

	case KindPaneSubstringPresent:
		// params: {"substring": "Booting MCP"}
		if in.Sensor != "pane" || in.Kind != "snapshot" {
			return false
		}
		var p struct {
			Substring string `json:"substring"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Snapshot string `json:"snapshot"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		return strings.Contains(pl.Snapshot, p.Substring)

	case KindPaneSubstringDisappeared:
		// Mirror of Present; stateful, evaluated by the rule evaluator with
		// prev-snapshot context. False at the per-signal level.
		return false

	case KindNetworkRequestActive:
		// params: {"host_glob": "*.anthropic.com"}
		if in.Sensor != "net" || in.Kind != "open" {
			return false
		}
		var p struct {
			HostGlob string `json:"host_glob"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Host string `json:"host"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		if p.HostGlob == "" {
			return true
		}
		suffix := strings.TrimPrefix(p.HostGlob, "*")
		return strings.HasSuffix(pl.Host, suffix)

	case KindHookFired:
		// params: {"hook_kind": "PreToolUse"} — claudecode hook signals.
		// Transcript-tail variant: looks for an embedded hook marker.
		if in.Sensor != "transcript" || in.Kind != "line" {
			return false
		}
		var p struct {
			HookKind string `json:"hook_kind"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		return strings.Contains(pl.Line, p.HookKind)

	case KindInterruptMarker:
		// params: {"marker_substring": "[Request interrupted by user]"}
		if in.Sensor != "transcript" || in.Kind != "line" {
			return false
		}
		var p struct {
			MarkerSubstring string `json:"marker_substring"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return false
		}
		var pl struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(in.Payload, &pl); err != nil {
			return false
		}
		return strings.Contains(pl.Line, p.MarkerSubstring)
	}
	return false
}

// fieldEquals tolerates nested objects by walking dot-paths ("a.b.c").
func fieldEquals(obj map[string]any, dotPath, want string) bool {
	parts := strings.Split(dotPath, ".")
	var cur any = obj
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[p]
		if !ok {
			return false
		}
	}
	switch v := cur.(type) {
	case string:
		return v == want
	case float64:
		// JSON numbers come back as float64; compare stringified.
		return strings.TrimSuffix(strings.TrimSuffix(jsonNumber(v), "0"), ".") == want ||
			jsonNumber(v) == want
	case bool:
		if want == "true" {
			return v
		}
		if want == "false" {
			return !v
		}
		return false
	}
	return false
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
