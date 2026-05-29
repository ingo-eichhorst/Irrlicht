// Command migrate-shards reads the CURRENT on-disk onboarding layout and emits
// the per-scenario "shard" data model (#510) alongside it — one shard per
// catalog row at replaydata/scenarios/<catalog-id>.json, plus a global
// replaydata/scenarios/_meta.json.
//
// This is a P1 DUAL-WRITE tool: it never deletes or re-points the existing
// storage. Nothing reads the shards yet. The migrator is sourced, idempotent,
// and meant as a DURABLE BLUEPRINT for future bulk restructures — read the
// transform top-to-bottom to understand how the old split layout maps onto the
// unified shard.
//
// What it reads (the current layout):
//   - .claude/skills/ir:onboard-agent/scenarios.json — catalog[] (rows),
//     scenarios[] (recipe variants, each with by_adapter), min_versions.
//   - replaydata/agents/<agent>/capabilities.json — the agent set (any dir
//     with a capabilities.json IS an agent) + optional transcript_extension.
//   - replaydata/agents/<agent>/scenarios/<folder>/{assessment.json,
//     manifest.json, events.jsonl, transcript.*, *.replay.json.golden,
//     recordings/<ts>/} — the per-cell recording + judgement.
//
// What it writes:
//   - replaydata/scenarios/<catalog-id>.json — one shard per catalog row.
//   - replaydata/scenarios/_meta.json — min_versions + transcript_extensions.
//
// CRITICAL: expected.jsonl is intentionally NOT folded into shards. It stays on
// disk next to its recording. The Shard's Details.Expected / ExpectedMeta are
// left empty (the fields exist for a possible future fold-in).
//
// Determinism: every shard is json.MarshalIndent'd (2-space) with a trailing
// newline; struct fields serialize in declaration order and the Agents map in
// sorted-key order. Raw blobs (verify, recipe, assessment) are re-compacted
// with json.Compact so re-runs are byte-identical regardless of source spacing,
// without reordering keys inside the blob.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"irrlicht/tools/agent-onboarding/internal/shard"
)

// ---- input shapes (mirrors internal/matrix/matrix.go's raw* types) ----------

type rawScenarios struct {
	Catalog     []rawCatalog      `json:"catalog"`
	Scenarios   []rawScenario     `json:"scenarios"`
	MinVersions map[string]string `json:"min_versions"`
}

type rawCatalog struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Feature string `json:"feature"`
}

type rawScenario struct {
	Name       string                     `json:"name"`
	CoverageID string                     `json:"coverage_id"`
	Requires   []string                   `json:"requires"`
	Verify     json.RawMessage            `json:"verify"`
	Desc       string                     `json:"description"`
	ByAdapter  map[string]json.RawMessage `json:"by_adapter"`
	IdleOnly   *bool                      `json:"idle_only"`
}

// rawAssessment is the per-cell AssessmentReport; we read a few overview fields
// plus the body for the notes excerpt. The full file is also embedded verbatim
// into Details.Assessment.
type rawAssessment struct {
	AgentSupports    string  `json:"agent_supports"`
	DaemonCapability string  `json:"daemon_capability"`
	DriverCapability string  `json:"driver_capability"`
	Confidence       float64 `json:"confidence"`
	Body             string  `json:"body"`
}

// rawManifest carries the recording's version + pass-rate metadata.
type rawManifest struct {
	AgentCLIVersion  string `json:"agent_cli_version"`
	DaemonVersion    string `json:"daemon_version"`
	ExpectedPassRate string `json:"expected_pass_rate"`
}

// rawCapabilities is the per-agent capabilities.json; only the transcript
// extension matters for _meta.json (default "jsonl" when absent).
type rawCapabilities struct {
	TranscriptExtension string `json:"transcript_extension"`
}

// metaFile is the _meta.json shape: per-agent min versions and transcript
// extensions, keyed by agent (maps serialize sorted).
type metaFile struct {
	MinVersions          map[string]string `json:"min_versions"`
	TranscriptExtensions map[string]string `json:"transcript_extensions"`
}

func main() {
	repoRoot := flag.String("repo-root", ".", "repository root")
	check := flag.Bool("check", false, "regenerate in memory and verify on-disk shards match (CI mode); exit 1 on drift")
	flag.Parse()

	files, err := generate(*repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate-shards:", err)
		os.Exit(2)
	}

	if *check {
		os.Exit(runCheck(*repoRoot, files))
	}
	if err := writeAll(*repoRoot, files); err != nil {
		fmt.Fprintln(os.Stderr, "migrate-shards:", err)
		os.Exit(2)
	}
	fmt.Printf("wrote %d shards + _meta.json\n", len(files)-1)
}

// ---- pipeline -------------------------------------------------------------

// generate builds the full set of output files IN MEMORY: every shard keyed by
// "<catalog-id>.json" plus the "_meta.json" entry. Values are the exact bytes
// that would be written to disk (pretty + trailing newline). Returning bytes
// (not paths) is what makes -check a pure in-memory diff.
func generate(repoRoot string) (map[string][]byte, error) {
	rs, err := loadScenarios(repoRoot)
	if err != nil {
		return nil, err
	}
	agents := discoverAgents(repoRoot)
	codes := deriveSectionIndex(rs.Catalog)

	out := make(map[string][]byte)
	for i, cat := range rs.Catalog {
		shard := buildShard(repoRoot, cat, codes[i], rs.Scenarios, agents)
		b, err := marshalShard(shard)
		if err != nil {
			return nil, fmt.Errorf("marshal shard %s: %w", cat.ID, err)
		}
		out[cat.ID+".json"] = b
	}

	meta, err := marshalMeta(buildMeta(repoRoot, rs, agents))
	if err != nil {
		return nil, fmt.Errorf("marshal _meta.json: %w", err)
	}
	out["_meta.json"] = meta
	return out, nil
}

// buildShard maps ONE catalog row onto a Shard. Row metadata comes from the
// catalog entry + a representative recipe variant; each agent column is a
// ShardAgent cell resolved independently for recipe, artifacts, and assessment.
func buildShard(repoRoot string, cat rawCatalog, code string, scenarios []rawScenario, agents []string) shard.Shard {
	rep := representativeVariant(cat.ID, scenarios)

	shard := shard.Shard{
		ID:      code,
		Name:    cat.ID,
		Section: cat.Section,
		Feature: cat.Feature,
		Agents:  map[string]shard.ShardAgent{},
	}
	if rep != nil {
		shard.Description = rep.Desc
		shard.Requires = rep.Requires
		shard.Verify = compactRaw(rep.Verify)
		shard.IdleOnly = rep.IdleOnly
	}

	dirs := candidateDirs(cat.ID, scenarios)
	for _, ag := range agents {
		if cell, ok := buildCell(repoRoot, cat.ID, dirs, scenarios, ag); ok {
			shard.Agents[ag] = cell
		}
	}
	return shard
}

// buildCell resolves a single (scenario, agent) cell. Returns ok=false when the
// agent has NOTHING to say — no recipe, no recording, and no assessment — so we
// omit the column entirely (an "absent" cell).
func buildCell(repoRoot, cid string, dirs []string, scenarios []rawScenario, agent string) (shard.ShardAgent, bool) {
	agentScenDir := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios")

	// ARTIFACTS + RECORDING_DIR: the first candidate folder that actually holds
	// a recording (events.jsonl OR a transcript). May differ from cid — e.g.
	// aider's basic-turn recording lives in the "multi-turn-conversation"
	// variant folder.
	recFolder, artifacts := resolveArtifacts(agentScenDir, agent, dirs)

	// ASSESSMENT: "catalog-id folder wins" — the cell's assessment is the one
	// in the <cid> folder when present (this is the value the viewer displays
	// per-cell), then the recording folder, then any remaining candidate dir.
	assessRaw, parsed := resolveAssessment(agentScenDir, cid, recFolder, dirs)

	// RECIPE: the by_adapter[agent] block of the variant that best fits this
	// cell — preferring the variant whose name IS the recording folder.
	recipe := resolveRecipe(scenarios, cid, agent, recFolder)

	if recFolder == "" && assessRaw == nil && recipe == nil {
		return shard.ShardAgent{}, false
	}

	cell := shard.ShardAgent{
		Artifacts: artifacts,
		Details: shard.ShardDetails{
			Assessment: assessRaw,
			Recipe:     recipe,
			// Expected / ExpectedMeta intentionally left empty — expected.jsonl
			// stays on disk (locked design decision).
		},
	}
	if recFolder != "" {
		cell.RecordingDir = filepath.ToSlash(filepath.Join(agent, "scenarios", recFolder))
	}

	// Overview metadata from the assessment (only the typed fields).
	if parsed != nil {
		cell.Metadata.AgentSupports = parsed.AgentSupports
		cell.Metadata.DaemonCapability = parsed.DaemonCapability
		cell.Metadata.DriverCapability = parsed.DriverCapability
		cell.Metadata.Confidence = parsed.Confidence
		cell.Metadata.Notes = firstParagraph(parsed.Body)
	}
	// Version + pass-rate metadata from the recording's manifest.json.
	if recFolder != "" {
		if m := readManifest(filepath.Join(agentScenDir, recFolder, "manifest.json")); m != nil {
			cell.Metadata.AgentCLIVersion = m.AgentCLIVersion
			cell.Metadata.DaemonVersion = m.DaemonVersion
			cell.Metadata.PassRate = m.ExpectedPassRate
		}
	}
	return cell, true
}

// ---- artifact / assessment / recipe resolution ----------------------------

// resolveArtifacts finds the first candidate folder holding a recording and
// returns the folder name plus its on-disk artifacts (paths relative to
// replaydata/agents/). Empty folder name means no recording for this agent.
func resolveArtifacts(agentScenDir, agent string, dirs []string) (string, shard.ShardArtifacts) {
	for _, d := range dirs {
		cellDir := filepath.Join(agentScenDir, d)
		// agentScenDir is .../replaydata/agents/<agent>/scenarios — the prefix
		// we strip to make Artifacts paths relative to replaydata/agents/.
		rel := filepath.ToSlash(filepath.Join(agent, "scenarios", d))

		hasEvents := fileExists(filepath.Join(cellDir, "events.jsonl"))
		transcript, transcriptMD := findTranscript(cellDir, rel)
		if !hasEvents && transcript == "" && transcriptMD == "" {
			continue
		}

		art := shard.ShardArtifacts{}
		if hasEvents {
			art.Events = rel + "/events.jsonl"
		}
		art.Transcript = transcript
		art.TranscriptMD = transcriptMD
		if fileExists(filepath.Join(cellDir, "manifest.json")) {
			art.Manifest = rel + "/manifest.json"
		}
		if g := findGolden(cellDir); g != "" {
			art.Golden = rel + "/" + g
		}
		art.Recordings = findRecordings(cellDir, rel)
		return d, art
	}
	return "", shard.ShardArtifacts{}
}

// findTranscript returns the relative transcript paths (jsonl, md) present in a
// cell folder. Either or both may be empty.
func findTranscript(cellDir, rel string) (jsonl, md string) {
	if fileExists(filepath.Join(cellDir, "transcript.jsonl")) {
		jsonl = rel + "/transcript.jsonl"
	}
	if fileExists(filepath.Join(cellDir, "transcript.md")) {
		md = rel + "/transcript.md"
	}
	return jsonl, md
}

// findGolden returns the *.replay.json.golden filename in a cell folder, or "".
// The prefix differs by transcript kind (transcript.jsonl.replay.json.golden
// vs transcript.md.replay.json.golden), so we glob by suffix.
func findGolden(cellDir string) string {
	entries, err := os.ReadDir(cellDir)
	if err != nil {
		return ""
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".replay.json.golden") {
			found = append(found, e.Name())
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		return found[0]
	}
	return ""
}

// findRecordings returns each recordings/<ts> archive subdir as a path relative
// to replaydata/agents/, sorted.
func findRecordings(cellDir, rel string) []string {
	recDir := filepath.Join(cellDir, "recordings")
	entries, err := os.ReadDir(recDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, rel+"/recordings/"+e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// resolveAssessment reads the cell's assessment.json under a "catalog-id folder
// wins" rule: the <cid> folder first (the value the viewer displays per cell),
// then the recording folder, then any remaining candidate dir. Returns the
// compacted raw JSON (for Details.Assessment) and the parsed overview fields
// (for Metadata). Both nil when no assessment exists. An empty-after-compact
// blob (a zero-byte assessment.json — several exist on disk) counts as no
// assessment, matching the matrix model's intent.
func resolveAssessment(agentScenDir, cid, recFolder string, dirs []string) (json.RawMessage, *rawAssessment) {
	try := func(folder string) (json.RawMessage, *rawAssessment, bool) {
		if folder == "" {
			return nil, nil, false
		}
		b, err := os.ReadFile(filepath.Join(agentScenDir, folder, "assessment.json"))
		if err != nil {
			return nil, nil, false
		}
		raw := compactRaw(b)
		if raw == nil {
			return nil, nil, false
		}
		var a rawAssessment
		if json.Unmarshal(b, &a) != nil {
			return nil, nil, false
		}
		return raw, &a, true
	}

	// Preference order: cid folder, then recording folder, then the rest —
	// de-duplicated so each folder is tried at most once.
	seen := map[string]bool{}
	for _, folder := range append([]string{cid, recFolder}, dirs...) {
		if folder == "" || seen[folder] {
			continue
		}
		seen[folder] = true
		if raw, parsed, ok := try(folder); ok {
			return raw, parsed
		}
	}
	return nil, nil
}

// resolveRecipe picks the by_adapter[agent] block for this cell. Among variants
// that contribute to cid (the variant named cid plus any with
// coverage_id==cid), prefer the one whose name IS the recording folder; else
// the variant named cid; else the first that has a block for this agent.
func resolveRecipe(scenarios []rawScenario, cid, agent, recFolder string) json.RawMessage {
	var (
		byRecFolder json.RawMessage
		byCID       json.RawMessage
		first       json.RawMessage
	)
	for i := range scenarios {
		s := &scenarios[i]
		if s.Name != cid && s.CoverageID != cid {
			continue
		}
		blob, ok := s.ByAdapter[agent]
		if !ok || len(blob) == 0 {
			continue
		}
		if recFolder != "" && s.Name == recFolder {
			byRecFolder = blob
		}
		if s.Name == cid {
			byCID = blob
		}
		if first == nil {
			first = blob
		}
	}
	switch {
	case byRecFolder != nil:
		return compactRaw(byRecFolder)
	case byCID != nil:
		return compactRaw(byCID)
	case first != nil:
		return compactRaw(first)
	default:
		return nil
	}
}

// ---- catalog / variant derivation -----------------------------------------

// deriveSectionIndex assigns each catalog row a stable "<section>.<index>" code,
// EXACTLY like internal/viewer/catalog.go's annotateCatalogCodes: section
// numbers are assigned in first-appearance order starting at 1; the index
// increments within a section starting at 1. The returned slice is parallel to
// cat.
func deriveSectionIndex(cat []rawCatalog) []string {
	sectionNum := map[string]int{}
	nextSection := 1
	idxInSection := map[string]int{}
	codes := make([]string, len(cat))
	for i := range cat {
		sec := cat[i].Section
		if _, ok := sectionNum[sec]; !ok {
			sectionNum[sec] = nextSection
			nextSection++
		}
		idxInSection[sec]++
		codes[i] = strconv.Itoa(sectionNum[sec]) + "." + strconv.Itoa(idxInSection[sec])
	}
	return codes
}

// candidateDirs is the set of recording-folder names that may hold cid's data:
// cid itself plus every variant whose coverage_id maps to it (mirrors
// matrix.go's candidateDirs).
func candidateDirs(coverageID string, scenarios []rawScenario) []string {
	dirs := []string{coverageID}
	seen := map[string]bool{coverageID: true}
	for _, s := range scenarios {
		if s.CoverageID == coverageID && !seen[s.Name] {
			dirs = append(dirs, s.Name)
			seen[s.Name] = true
		}
	}
	return dirs
}

// representativeVariant picks the scenario variant that supplies the shard's
// row-level metadata (description, requires, verify, idle_only): the variant
// named cid if present, else the first variant with coverage_id==cid.
func representativeVariant(cid string, scenarios []rawScenario) *rawScenario {
	var fallback *rawScenario
	for i := range scenarios {
		s := &scenarios[i]
		if s.Name == cid {
			return s
		}
		if fallback == nil && s.CoverageID == cid {
			fallback = s
		}
	}
	return fallback
}

// ---- _meta.json -----------------------------------------------------------

// buildMeta assembles _meta.json from scenarios.json min_versions and each
// agent's capabilities.json transcript_extension (default "jsonl").
func buildMeta(repoRoot string, rs *rawScenarios, agents []string) metaFile {
	mv := map[string]string{}
	ext := map[string]string{}
	for _, ag := range agents {
		if v, ok := rs.MinVersions[ag]; ok {
			mv[ag] = v
		}
		ext[ag] = transcriptExtension(repoRoot, ag)
	}
	return metaFile{MinVersions: mv, TranscriptExtensions: ext}
}

// transcriptExtension reads the agent's capabilities.json; defaults to "jsonl"
// when the key is absent or the file is unreadable.
func transcriptExtension(repoRoot, agent string) string {
	b, err := os.ReadFile(filepath.Join(repoRoot, "replaydata", "agents", agent, "capabilities.json"))
	if err != nil {
		return "jsonl"
	}
	var c rawCapabilities
	if json.Unmarshal(b, &c) != nil || c.TranscriptExtension == "" {
		return "jsonl"
	}
	return c.TranscriptExtension
}

// ---- agent discovery ------------------------------------------------------

// discoverAgents enumerates the adapter columns: every dir under
// replaydata/agents/ that has a capabilities.json (mirrors matrix.go).
func discoverAgents(repoRoot string) []string {
	dir := filepath.Join(repoRoot, "replaydata", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var agents []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !fileExists(filepath.Join(dir, e.Name(), "capabilities.json")) {
			continue
		}
		agents = append(agents, e.Name())
	}
	sort.Strings(agents)
	return agents
}

// ---- input loading --------------------------------------------------------

func loadScenarios(repoRoot string) (*rawScenarios, error) {
	path := filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs rawScenarios
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("parse scenarios.json: %w", err)
	}
	return &rs, nil
}

func readManifest(path string) *rawManifest {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m rawManifest
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

// ---- serialization --------------------------------------------------------

// marshalShard renders a shard deterministically: 2-space indent + trailing
// newline. Field order is the struct declaration order; the Agents map is
// emitted sorted by key (Go's encoding/json sorts map keys).
func marshalShard(s shard.Shard) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func marshalMeta(m metaFile) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// compactRaw normalizes a raw JSON blob's whitespace so re-runs are
// byte-identical regardless of source spacing, WITHOUT reordering keys (same as
// P0's recipeHashOf fix). Returns nil for empty/invalid input.
func compactRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return nil
	}
	return json.RawMessage(buf.Bytes())
}

// ---- output: write + check ------------------------------------------------

// writeAll writes every generated file into replaydata/scenarios/, creating the
// dir if needed. It does NOT prune extra files on disk (dual-write phase).
func writeAll(repoRoot string, files map[string][]byte) error {
	dir := filepath.Join(repoRoot, "replaydata", "scenarios")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runCheck compares the regenerated files against what's on disk and reports
// drift: differing, missing, or extra files. Returns 0 when clean, 1 on drift.
func runCheck(repoRoot string, files map[string][]byte) int {
	dir := filepath.Join(repoRoot, "replaydata", "scenarios")
	drift := 0

	// differing / missing
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := files[name]
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			fmt.Printf("MISSING %s\n", name)
			drift++
			continue
		}
		if !bytes.Equal(want, got) {
			fmt.Printf("DIFFERS %s\n", name)
			drift++
		}
	}

	// extra files on disk that we didn't generate (any .json beside _meta.json)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			if _, ok := files[e.Name()]; !ok {
				fmt.Printf("EXTRA   %s\n", e.Name())
				drift++
			}
		}
	}

	if drift > 0 {
		fmt.Printf("drift: %d file(s) differ\n", drift)
		return 1
	}
	fmt.Printf("ok: %d shards + _meta.json match\n", len(files)-1)
	return 0
}

// ---- tiny local helpers (duplicated from viewer to keep migrator standalone) -

// firstParagraph returns the first non-empty, non-heading paragraph of a
// markdown body (mirrors internal/viewer/catalog.go's firstParagraph).
func firstParagraph(s string) string {
	for _, p := range strings.Split(s, "\n\n") {
		t := strings.TrimSpace(p)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return t
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
