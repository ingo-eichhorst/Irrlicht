// Package shard is the per-scenario "shard" data model (#510): one unified
// object per matrix row at replaydata/scenarios/<name>.json, plus a global
// replaydata/scenarios/_meta.json. It lives in its own package (rather than
// under internal/viewer) so BOTH the viewer AND the matrix model can import it
// — viewer imports matrix, so the shared shard types can live in neither and
// must sit in a third package both depend on.
package shard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Shard is one scenario's unified object, the single home for a matrix row
// (#510). It lives at replaydata/scenarios/<name>.json and replaces the old
// split across scenarios.json (catalog[]+scenarios[]),
// agent-scenarios-coverage.json, and per-cell assessment.json.
//
// Recording artifacts (events.jsonl, transcript.jsonl, manifest.json,
// recordings/, *.golden) and the spec-grounded expected.jsonl stay on disk
// under replaydata/agents/<adapter>/{scenarios,regression}/<name>/ and are
// referenced from each agent's Artifacts block.
type Shard struct {
	ID          string                `json:"id"`   // stable section.index, e.g. "2.19"
	Name        string                `json:"name"` // row identity / filename / coverage_id
	Section     string                `json:"section"`
	Feature     string                `json:"feature"`
	Description string                `json:"description,omitempty"`
	Requires    []string              `json:"requires,omitempty"` // informational driver hint (no longer gates applicability)
	Verify      json.RawMessage       `json:"verify,omitempty"`
	IdleOnly    *bool                 `json:"idle_only,omitempty"` // observation-only scenario (frontend badge)
	Agents      map[string]ShardAgent `json:"agents"`
}

// ShardAgent is one (scenario, adapter) cell, split into an overview-tier
// Metadata block and a detail-tier Details block, plus explicit Artifacts
// refs to the on-disk recording files.
type ShardAgent struct {
	RecordingDir string         `json:"recording_dir,omitempty"`
	Artifacts    ShardArtifacts `json:"artifacts,omitempty"`
	Metadata     ShardMetadata  `json:"metadata,omitempty"`
	Details      ShardDetails   `json:"details,omitempty"`
}

// ShardArtifacts names the on-disk recording files, as paths relative to
// replaydata/agents/. Missing files are omitted.
type ShardArtifacts struct {
	Events       string   `json:"events,omitempty"`
	Transcript   string   `json:"transcript,omitempty"`
	TranscriptMD string   `json:"transcript_md,omitempty"`
	Manifest     string   `json:"manifest,omitempty"`
	Golden       string   `json:"golden,omitempty"`
	Recordings   []string `json:"recordings,omitempty"`
}

// ShardMetadata is the overview tier — what the matrix needs to render a
// cell's status without opening the detail view.
type ShardMetadata struct {
	AgentSupports    string  `json:"agent_supports,omitempty"`
	DaemonCapability string  `json:"daemon_capability,omitempty"`
	DriverCapability string  `json:"driver_capability,omitempty"`
	PassRate         string  `json:"pass_rate,omitempty"`
	AgentCLIVersion  string  `json:"agent_cli_version,omitempty"`
	DaemonVersion    string  `json:"daemon_version,omitempty"`
	Confidence       float64 `json:"confidence,omitempty"`
	Notes            string  `json:"notes,omitempty"`
}

// ShardDetails is the detail tier — loaded when a cell is opened. Expected /
// ExpectedMeta are reserved for a possible future fold-in of expected.jsonl;
// for now expected.jsonl stays on disk and these stay empty. Recipe is the
// old by_adapter block.
type ShardDetails struct {
	Assessment   json.RawMessage   `json:"assessment,omitempty"`
	Expected     []json.RawMessage `json:"expected,omitempty"`
	ExpectedMeta json.RawMessage   `json:"expected_meta,omitempty"`
	Recipe       json.RawMessage   `json:"recipe,omitempty"`
}

// Meta is the global replaydata/scenarios/_meta.json: the onboarded-adapter
// column set (min_versions) plus each adapter's transcript file extension.
type Meta struct {
	MinVersions          map[string]string `json:"min_versions"`
	TranscriptExtensions map[string]string `json:"transcript_extensions"`
}

// Dir is the directory holding the scenario shards.
func Dir(repoRoot string) string {
	return filepath.Join(repoRoot, "replaydata", "scenarios")
}

// LoadAll reads every replaydata/scenarios/<name>.json shard (skipping the
// global _meta.json), sorted by stable id (section, then index). A malformed
// shard is skipped rather than failing the whole load.
func LoadAll(repoRoot string) []Shard {
	dir := Dir(repoRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Shard
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "_meta.json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s Shard
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return lessShardID(out[i].ID, out[j].ID) })
	return out
}

// Load reads a single shard by name (the filename IS the name) without
// scanning the whole catalog. Returns ok=false when absent or malformed.
func Load(repoRoot, name string) (Shard, bool) {
	var s Shard
	b, err := os.ReadFile(filepath.Join(Dir(repoRoot), name+".json"))
	if err != nil {
		return s, false
	}
	if json.Unmarshal(b, &s) != nil {
		return s, false
	}
	return s, true
}

// LoadMeta reads replaydata/scenarios/_meta.json. Returns an empty Meta on any
// error (missing dir, unreadable file, malformed JSON) — callers tolerate an
// empty column set and fall back to other sources.
func LoadMeta(repoRoot string) Meta {
	var m Meta
	b, err := os.ReadFile(filepath.Join(Dir(repoRoot), "_meta.json"))
	if err != nil {
		return m
	}
	if json.Unmarshal(b, &m) != nil {
		return Meta{}
	}
	return m
}

// Agents returns the SORTED keys of LoadMeta().MinVersions — the onboarded
// adapter column set. Empty when _meta.json is absent or malformed.
func Agents(repoRoot string) []string {
	mv := LoadMeta(repoRoot).MinVersions
	out := make([]string, 0, len(mv))
	for a := range mv {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// lessShardID orders "section.index" ids numerically (so "2.10" sorts after
// "2.9", not before it). Falls back to lexical order when either id isn't a
// well-formed "<int>.<int>".
func lessShardID(a, b string) bool {
	as, ai, aok := splitID(a)
	bs, bi, bok := splitID(b)
	if !aok || !bok {
		return a < b
	}
	if as != bs {
		return as < bs
	}
	return ai < bi
}

// splitID parses a "<section>.<index>" id. ok is false when the shape doesn't
// match (no dot, or non-numeric parts).
func splitID(id string) (section, index int, ok bool) {
	parts := strings.SplitN(id, ".", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var err1, err2 error
	section, err1 = strconv.Atoi(parts[0])
	index, err2 = strconv.Atoi(parts[1])
	return section, index, err1 == nil && err2 == nil
}
