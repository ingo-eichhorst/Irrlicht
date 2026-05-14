// Package groundtruth converts driver-emitted `gt:<marker>` sidecar lines
// into a schema-conformant ground_truth.jsonl. The driver script writes
// timing-aligned markers during a recording session; the labeler runs
// post-recording to produce the validator's supervised signal.
//
// The conversion is deterministic: each sidecar line "<unix_ms> gt:<marker> <state> [tolerance_ms] [evidence_kind] [notes...]"
// maps to one Label record. The first line of the output is the optional
// meta header.
package groundtruth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Meta is the first line of ground_truth.jsonl.
type Meta struct {
	SchemaVersion      int       `json:"schema_version"`
	Agent              string    `json:"agent"`
	Scenario           string    `json:"scenario"`
	RecordingStartedAt time.Time `json:"recording_started_at,omitzero"`
	Notes              string    `json:"notes,omitempty"`
}

// Label is one labelled timeline point.
type Label struct {
	TsOffsetMs    int    `json:"ts_offset_ms"`
	Marker        string `json:"marker"`
	ExpectedState string `json:"expected_state"`
	ToleranceMs   int    `json:"tolerance_ms,omitempty"`
	EvidenceKind  string `json:"evidence_kind,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// ValidEvidenceKinds enumerates accepted values for Label.EvidenceKind.
// Matches the schema's enum.
var ValidEvidenceKinds = map[string]struct{}{
	"transcript_line":            {},
	"transcript_field_value":     {},
	"transcript_event_kind":      {},
	"idle_gap":                   {},
	"file_event_burst":           {},
	"process_spawned":            {},
	"pty_ansi_sequence":          {},
	"pane_substring_present":     {},
	"pane_substring_disappeared": {},
	"network_request_active":     {},
	"hook_fired":                 {},
	"interrupt_marker":           {},
	"driver_emitted":             {},
}

// FromSidecar reads `gt:` lines from sidecar and returns Meta + labels
// sorted by ts_offset_ms. Sidecar format per line (whitespace-separated):
//
//	<unix_ms> gt:<marker> <expected_state> [tolerance_ms] [evidence_kind] [notes...]
//
// recordingStart is the wall-clock time the recording began (from
// recording-meta.json `started_at`); the labeler subtracts it from each
// `unix_ms` to compute `ts_offset_ms`.
//
// Lines not starting with `gt:` (or, more generously, not containing the
// marker prefix) are skipped silently — the sidecar may carry other
// driver chatter.
func FromSidecar(r io.Reader, agent, scenario string, recordingStart time.Time) (Meta, []Label, error) {
	meta := Meta{
		SchemaVersion:      1,
		Agent:              agent,
		Scenario:           scenario,
		RecordingStartedAt: recordingStart.UTC(),
	}
	var labels []Label
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// First field: unix_ms.
		unixMs, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// Second field: gt:<marker>.
		if !strings.HasPrefix(fields[1], "gt:") {
			continue
		}
		marker := strings.TrimPrefix(fields[1], "gt:")
		if marker == "" {
			continue
		}
		// Third field: expected_state.
		expected := fields[2]
		if expected != "working" && expected != "waiting" && expected != "ready" {
			return meta, nil, fmt.Errorf("line %d: unknown state %q (want working|waiting|ready)", lineNum, expected)
		}
		l := Label{
			TsOffsetMs:    int(unixMs - recordingStart.UnixMilli()),
			Marker:        marker,
			ExpectedState: expected,
			EvidenceKind:  "driver_emitted",
		}
		if l.TsOffsetMs < 0 {
			l.TsOffsetMs = 0 // tolerate small clock skew at start
		}
		// Optional: tolerance_ms.
		fi := 3
		if fi < len(fields) {
			if n, err := strconv.Atoi(fields[fi]); err == nil {
				l.ToleranceMs = n
				fi++
			}
		}
		// Optional: evidence_kind.
		if fi < len(fields) {
			if _, ok := ValidEvidenceKinds[fields[fi]]; ok {
				l.EvidenceKind = fields[fi]
				fi++
			}
		}
		// Remainder: notes.
		if fi < len(fields) {
			l.Notes = strings.Join(fields[fi:], " ")
		}
		labels = append(labels, l)
	}
	if err := scanner.Err(); err != nil {
		return meta, nil, err
	}
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].TsOffsetMs < labels[j].TsOffsetMs })
	return meta, labels, nil
}

// Write emits ground_truth.jsonl: one meta header line, then one label per
// line. Uses json.Encoder, which appends \n after each Encode call.
func Write(w io.Writer, meta Meta, labels []Label) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("encode meta: %w", err)
	}
	for i, l := range labels {
		if l.ExpectedState != "working" && l.ExpectedState != "waiting" && l.ExpectedState != "ready" {
			return fmt.Errorf("label %d: bad expected_state %q", i, l.ExpectedState)
		}
		if err := enc.Encode(l); err != nil {
			return fmt.Errorf("encode label %d: %w", i, err)
		}
	}
	return nil
}

// Read parses a ground_truth.jsonl. The first non-empty line MAY be the
// meta header (detected by presence of `schema_version`); subsequent lines
// are labels.
func Read(r io.Reader) (Meta, []Label, error) {
	var meta Meta
	var labels []Label
	dec := json.NewDecoder(r)
	first := true
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return meta, nil, err
		}
		if first {
			if _, hasMeta := raw["schema_version"]; hasMeta {
				b, _ := json.Marshal(raw)
				if err := json.Unmarshal(b, &meta); err != nil {
					return meta, nil, fmt.Errorf("decode meta: %w", err)
				}
				first = false
				continue
			}
			first = false
		}
		var l Label
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &l); err != nil {
			return meta, nil, fmt.Errorf("decode label: %w", err)
		}
		labels = append(labels, l)
	}
	// Phase 3's greedyCompose assigns priority by iteration order, and
	// the validator iterates in label order looking for the closest
	// emitted transition. A hand-edited ground_truth.jsonl with rows
	// out of chronological order would silently produce a ruleset whose
	// priorities don't match temporal order — sort defensively so the
	// invariant holds regardless of authoring discipline.
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].TsOffsetMs < labels[j].TsOffsetMs })
	return meta, labels, nil
}

// Process runs the end-to-end flow used by `agent-onboard label`:
//
//	read sidecar → build labels → write ground_truth.jsonl into outDir.
//
// outDir is typically the scenario's directory in replaydata/agents/.
// recordingStart is read from outDir/recording-meta.json's `started_at`
// if zero; callers can also pass it explicitly.
func Process(ctx context.Context, sidecarPath, outDir, agent, scenario string, recordingStart time.Time) (string, error) {
	if recordingStart.IsZero() {
		// Try to read from recording-meta.json.
		metaPath := filepath.Join(outDir, "recording-meta.json")
		if b, err := os.ReadFile(metaPath); err == nil {
			var m struct {
				StartedAt time.Time `json:"started_at"`
			}
			if err := json.Unmarshal(b, &m); err == nil {
				recordingStart = m.StartedAt
			}
		}
	}
	if recordingStart.IsZero() {
		return "", errors.New("recordingStart is zero and no recording-meta.json found")
	}

	f, err := os.Open(sidecarPath)
	if err != nil {
		return "", fmt.Errorf("open sidecar: %w", err)
	}
	defer f.Close()
	meta, labels, err := FromSidecar(f, agent, scenario, recordingStart)
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(outDir, "ground_truth.jsonl")
	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if err := Write(out, meta, labels); err != nil {
		return "", err
	}
	return outPath, nil
}
