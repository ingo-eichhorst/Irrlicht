// Package shard is the onboarding-matrix data model. The scenario catalog is a
// single file replaydata/scenarios.json = {"meta": {...}, "scenarios": [...]};
// each (scenario, adapter) cell is a metadata.json at
// replaydata/agents/<adapter>/scenarios/<id>_<name>/metadata.json (folders are
// prefixed by the scenario's dashed id). It lives in its own package (rather
// than under internal/viewer) so BOTH the viewer AND the matrix model can
// import it — viewer imports matrix, so the shared types sit in a third package
// both depend on.
package shard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Shard is one scenario's unified object: the scenario-global spec for one
// matrix row. It lives at replaydata/scenarios/<name>.json. Agent-specific
// cell data (assessment, recipe, artifacts, verdict) moved to
// replaydata/agents/<adapter>/scenarios/<folder>/metadata.json (#split-shards).
//
// Recording artifacts stay on disk under
// replaydata/agents/<adapter>/{scenarios,regression}/<name>/.
type Shard struct {
	ID          string          `json:"id"`   // stable section.index, e.g. "2.19"
	Name        string          `json:"name"` // row identity / filename / coverage_id
	Section     string          `json:"section"`
	Feature     string          `json:"feature"`
	Description string          `json:"description,omitempty"`
	Requires    []string        `json:"requires,omitempty"` // informational driver hint (no longer gates applicability)
	Verify      json.RawMessage `json:"verify,omitempty"`
	IdleOnly    *bool           `json:"idle_only,omitempty"` // observation-only scenario (frontend badge)
	// CrossAdapter, when set, is the list of adapters a cross-adapter cell drives
	// concurrently in one shared workspace (read by run-cell-multi.sh). Only the
	// multiple-agents-same-workspace cell uses it.
	CrossAdapter []string `json:"cross_adapter,omitempty"`
}

// ShardAgent is one (scenario, adapter) cell. It lives at
// replaydata/agents/<adapter>/scenarios/<folder>/metadata.json. ScenarioID
// ties variant-folder cells (where folder ≠ scenario name) back to their
// parent scenario shard.
type ShardAgent struct {
	ScenarioID   string         `json:"scenario_id,omitempty"` // coverage_id; set when folder ≠ scenario name
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
	// CapabilityVocab is the closed agent-capability vocabulary (the former
	// replaydata/agents/features.json), folded in here in #511 so the
	// discovery preamble has a single home. Kept as a raw blob — the matrix
	// never reads it (applicability is shard-driven); only discover-agent.sh
	// formats it.
	CapabilityVocab json.RawMessage `json:"capability_vocab,omitempty"`
}

// AgentCellDir returns the directory that holds one (adapter, scenario) cell:
// replaydata/agents/<adapter>/scenarios/<folder>. Folder is the on-disk
// recording folder — the dashed-id-prefixed scenario name for standard cells
// (e.g. 5-4_architect-editor-pair), or a prefixed variant name otherwise.
func AgentCellDir(repoRoot, adapter, folder string) string {
	return filepath.Join(repoRoot, "replaydata", "agents", adapter, "scenarios", folder)
}

// LoadAgentCell reads replaydata/agents/<adapter>/scenarios/<folder>/metadata.json
// by its on-disk folder name. ok is false when the file is absent or malformed.
// Use this when you already know the folder (e.g. the viewer detail endpoint,
// keyed by the on-disk folder).
func LoadAgentCell(repoRoot, adapter, folder string) (*ShardAgent, bool) {
	b, err := os.ReadFile(filepath.Join(AgentCellDir(repoRoot, adapter, folder), "metadata.json"))
	if err != nil {
		return nil, false
	}
	var cell ShardAgent
	if json.Unmarshal(b, &cell) != nil {
		return nil, false
	}
	return &cell, true
}

// LoadAdapterCells scans one adapter's scenarios/ tree once and returns its
// cells keyed by ScenarioID (coverage_id). Every metadata.json carries a
// scenario_id, so folder names (now id-prefixed) don't need to be guessed.
// Empty map on any error; never returns an error.
func LoadAdapterCells(repoRoot, adapter string) map[string]*ShardAgent {
	out := map[string]*ShardAgent{}
	scenDir := filepath.Join(repoRoot, "replaydata", "agents", adapter, "scenarios")
	entries, err := os.ReadDir(scenDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(scenDir, e.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var cell ShardAgent
		if json.Unmarshal(b, &cell) != nil {
			continue
		}
		key := cell.ScenarioID
		if key == "" {
			key = e.Name() // defensive: a cell without scenario_id keys on its folder
		}
		out[key] = &cell
	}
	return out
}

// LoadAllCells loads every onboarded adapter's cell for the given scenario
// (coverage_id), keyed by adapter. Convenience wrapper over LoadAdapterCells.
func LoadAllCells(repoRoot, scenarioName string) map[string]*ShardAgent {
	out := map[string]*ShardAgent{}
	for _, adapter := range Agents(repoRoot) {
		if cell, ok := LoadAdapterCells(repoRoot, adapter)[scenarioName]; ok {
			out[adapter] = cell
		}
	}
	return out
}

// File is the path to the consolidated scenario catalog.
func File(repoRoot string) string {
	return filepath.Join(repoRoot, "replaydata", "scenarios.json")
}

// catalog is the on-disk shape of replaydata/scenarios.json.
type catalog struct {
	Meta      Meta    `json:"meta"`
	Scenarios []Shard `json:"scenarios"`
}

// loadCatalog reads + parses replaydata/scenarios.json. ok is false on any
// error (missing file, malformed JSON).
func loadCatalog(repoRoot string) (catalog, bool) {
	var c catalog
	b, err := os.ReadFile(File(repoRoot))
	if err != nil {
		return c, false
	}
	if json.Unmarshal(b, &c) != nil {
		return c, false
	}
	return c, true
}

// LoadAll reads every scenario from replaydata/scenarios.json, sorted by stable
// id (section, then index). Returns nil on any error.
func LoadAll(repoRoot string) []Shard {
	c, ok := loadCatalog(repoRoot)
	if !ok {
		return nil
	}
	out := c.Scenarios
	sort.SliceStable(out, func(i, j int) bool { return lessShardID(out[i].ID, out[j].ID) })
	return out
}

// Load reads a single scenario by name. Returns ok=false when absent.
func Load(repoRoot, name string) (Shard, bool) {
	c, ok := loadCatalog(repoRoot)
	if !ok {
		return Shard{}, false
	}
	for _, s := range c.Scenarios {
		if s.Name == name {
			return s, true
		}
	}
	return Shard{}, false
}

// FolderForScenario returns the on-disk recording folder for a standard cell:
// the scenario's dashed id, an underscore, then the scenario name
// (e.g. "5-4_architect-editor-pair"). Empty when the scenario is unknown.
// Variant-folder cells don't follow this rule — resolve those from a loaded
// cell's RecordingDir instead.
func FolderForScenario(repoRoot, name string) string {
	s, ok := Load(repoRoot, name)
	if !ok {
		return ""
	}
	return strings.ReplaceAll(s.ID, ".", "-") + "_" + name
}

// LoadMeta reads the `meta` block of replaydata/scenarios.json. Returns an
// empty Meta on any error — callers tolerate an empty column set.
func LoadMeta(repoRoot string) Meta {
	c, ok := loadCatalog(repoRoot)
	if !ok {
		return Meta{}
	}
	return c.Meta
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
	as, ai, aok := SplitID(a)
	bs, bi, bok := SplitID(b)
	if !aok || !bok {
		return a < b
	}
	if as != bs {
		return as < bs
	}
	return ai < bi
}

// SplitID parses a "<section>.<index>" id. ok is false when the shape doesn't
// match (no dot, or non-numeric parts). Exported so the matrix model reuses
// this one parser instead of duplicating it (#511).
func SplitID(id string) (section, index int, ok bool) {
	parts := strings.SplitN(id, ".", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var err1, err2 error
	section, err1 = strconv.Atoi(parts[0])
	index, err2 = strconv.Atoi(parts[1])
	return section, index, err1 == nil && err2 == nil
}
