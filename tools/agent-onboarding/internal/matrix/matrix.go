package matrix

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"irrlicht/tools/agent-onboarding/internal/shard"
)

// AssessmentReport is the persisted artifact of one Stage-1 assessment, one
// file per (agent, scenario) at
// replaydata/agents/<agent>/scenarios/<scenario>/assessment.json. This is the
// canonical definition shared by the matrix model and the viewer (which
// aliases it) so the wire/disk contract lives in one place.
type AssessmentReport struct {
	SchemaVersion    int    `json:"schema_version"`
	ScenarioID       string `json:"scenario_id"`
	Agent            string `json:"agent"`
	AssessedAt       string `json:"assessed_at"`
	AgentSupports    string `json:"agent_supports"`    // yes / partial / no / unknown
	DaemonCapability string `json:"daemon_capability"` // full / bug / incapable / unknown / n/a
	DriverCapability string `json:"driver_capability"` // ready / gap:<primitive>
	// RecordBlocked documents why a cell whose three axes say record-now is
	// nonetheless not recorded — a reason ORTHOGONAL to the axes (infra /
	// unit_test / driver_bug / upstream). The consistency check REQUIRES this
	// whenever a record-now cell is marked applicable:false.
	RecordBlocked string             `json:"record_blocked,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Body          string             `json:"body"`
	Sources       []AssessmentSource `json:"sources,omitempty"`
	Caveats       []string           `json:"caveats,omitempty"`
}

// AssessmentSource is one citation backing an assessment verdict.
type AssessmentSource struct {
	Kind string `json:"kind"` // "url" | "file" | other
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

// CellState is the canonical per-cell value: everything the gates and viewer
// reconstruct independently today, computed once.
type CellState struct {
	Agent      string `json:"agent"`
	CoverageID string `json:"coverage_id"`
	// Applicable is whether the coverage_id is in scope for the agent at all
	// (requires vs capabilities + requires_transport), per cg_applicable_coverage_ids.
	Applicable bool `json:"applicable"`
	// ApplicableState is the by_adapter.<agent>.applicable rollup (absent/true/false).
	ApplicableState ApplicableState   `json:"applicable_state"`
	Recorded        bool              `json:"recorded"`
	Assessment      *AssessmentReport `json:"assessment,omitempty"`
	Route           Route             `json:"route"`
	Disposition     Disposition       `json:"disposition"`
	DisplayState    string            `json:"display_state"`
	BlockedReason   string            `json:"blocked_reason,omitempty"`
}

// Disagreement is one assessment ⟺ scenarios contradiction (the consistency
// gate's finding), carrying the axes and a maintainer-facing message.
type Disagreement struct {
	Agent      string  `json:"agent"`
	CoverageID string  `json:"coverage_id"`
	Verdict    Verdict `json:"verdict"`
	Supports   string  `json:"agent_supports"`
	Daemon     string  `json:"daemon_capability"`
	Driver     string  `json:"driver_capability"`
	Message    string  `json:"message"`
}

// Config locates the inputs. Empty fields fall back to repo-relative defaults
// via LoadRepo. AgentsRoot is .../replaydata/agents. As of P2 the model is
// shard-backed: RepoRoot (or, when empty, the parent of AgentsRoot) is the
// authoritative source — every cell comes from replaydata/scenarios/<name>.json
// and the onboarded-adapter column set from replaydata/scenarios/_meta.json.
// ScenariosPath/AgentsRoot are kept for back-compat (callers still set them) but
// are no longer the data source.
type Config struct {
	ScenariosPath string
	AgentsRoot    string
	RepoRoot      string
}

// Matrix is the loaded, normalized model. Construct via Load / LoadRepo.
type Matrix struct {
	catalog []catalogEntry
	agents  []string                        // sorted onboarded adapters (shard _meta.min_versions keys)
	shards  map[string]shard.Shard          // coverage_id (shard.Name) → shard
	cells   map[string]map[string]CellState // agent → coverage_id → cell (cells present in the shard only)
}

type catalogEntry struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Feature string `json:"feature"`
}

// shardRecipe is the slim view of a cell's Details.Recipe the matrix needs:
// the applicable flag, used to reconstruct ApplicableState per cell.
type shardRecipe struct {
	// Applicable is a pointer so we can tell "absent" (nil → recordable) from
	// an explicit false — mirrors the old per-variant by_adapter rule.
	Applicable *bool `json:"applicable"`
}

// LoadRepo loads the matrix from a repo root. Data comes from the per-scenario
// shards under replaydata/scenarios/ (#510); ScenariosPath/AgentsRoot are still
// populated for back-compat but no longer read.
func LoadRepo(repoRoot string) (*Matrix, error) {
	return Load(Config{
		ScenariosPath: filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json"),
		AgentsRoot:    filepath.Join(repoRoot, "replaydata", "agents"),
		RepoRoot:      repoRoot,
	})
}

// Load assembles the matrix from the per-scenario shards. The column set is
// shard.Agents (the _meta.min_versions keys); each shard is one matrix row; a
// cell exists iff the shard names the agent. Every cell's axes / recording /
// applicable state are reconstructed from the shard's per-agent block.
func Load(cfg Config) (*Matrix, error) {
	repoRoot := cfg.RepoRoot
	if repoRoot == "" {
		// AgentsRoot = …/replaydata/agents → repoRoot = …
		repoRoot = filepath.Dir(filepath.Dir(cfg.AgentsRoot))
	}

	shards := shard.LoadAll(repoRoot)
	if len(shards) == 0 {
		return nil, fmt.Errorf("no shards under %s", shard.Dir(repoRoot))
	}

	m := &Matrix{
		agents: shard.Agents(repoRoot),
		shards: make(map[string]shard.Shard, len(shards)),
		cells:  map[string]map[string]CellState{},
	}
	for _, sh := range shards {
		m.shards[sh.Name] = sh
	}

	// catalog: one row per IN-CATALOG shard, in shard (section.index) order.
	// LoadAll already sorts by stable id. Out-of-catalog shards carry a sentinel
	// section id (>= 99, the migrator's marker for rows absent from the source
	// catalog) and are excluded so the derived rollup matches the committed file
	// row-for-row.
	for _, sh := range shards {
		if !inCatalog(sh.ID) {
			continue
		}
		m.catalog = append(m.catalog, catalogEntry{ID: sh.Name, Section: sh.Section, Feature: sh.Feature})
	}

	for _, agent := range m.agents {
		m.cells[agent] = map[string]CellState{}
		for _, sh := range shards {
			if _, ok := sh.Agents[agent]; !ok {
				continue // no cell for this (agent, scenario)
			}
			m.cells[agent][sh.Name] = m.buildCell(agent, sh.Name)
		}
	}
	return m, nil
}

// inCatalog reports whether a shard id "<section>.<index>" denotes a real
// catalog row. The migrator assigns out-of-catalog shards a sentinel section
// (>= 99); those rows exist as shards but are NOT catalog rows (they never
// appeared in the committed rollup), so the matrix excludes them from m.catalog.
func inCatalog(id string) bool {
	sec, _, ok := splitShardSection(id)
	if !ok {
		return true // malformed → keep (defensive; real ids are well-formed)
	}
	return sec < 99
}

// splitShardSection parses the leading "<section>." of a shard id.
func splitShardSection(id string) (section int, rest string, ok bool) {
	dot := -1
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			dot = i
			break
		}
	}
	if dot <= 0 {
		return 0, "", false
	}
	n := 0
	for i := 0; i < dot; i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return 0, "", false
		}
		n = n*10 + int(c-'0')
	}
	return n, id[dot+1:], true
}

// HasAgent reports whether the agent is an onboarded column (present in the
// shard _meta.min_versions set).
func (m *Matrix) HasAgent(agent string) bool {
	for _, a := range m.agents {
		if a == agent {
			return true
		}
	}
	return false
}

// Agents returns the sorted list of onboarded agents.
func (m *Matrix) Agents() []string { return append([]string(nil), m.agents...) }

// cellRecorded mirrors the old recordedAndAssessment's "recorded" leg
// (events.jsonl + a transcript) using the shard cell's Artifacts refs instead
// of scanning the on-disk candidate dirs.
func cellRecorded(c shard.ShardAgent) bool {
	return c.Artifacts.Events != "" && (c.Artifacts.Transcript != "" || c.Artifacts.TranscriptMD != "")
}

// cellAssessment parses the shard cell's Details.Assessment. hasAssessFile is
// true when the blob is present AND parseable into an AssessmentReport — the
// shard-backed equivalent of finding a parseable assessment.json. rep is nil
// when the blob is empty or malformed.
func cellAssessment(c shard.ShardAgent) (hasAssessFile bool, rep *AssessmentReport) {
	if len(c.Details.Assessment) == 0 {
		return false, nil
	}
	hasAssessFile = true
	var r AssessmentReport
	if json.Unmarshal(c.Details.Assessment, &r) == nil {
		rep = &r
	}
	return hasAssessFile, rep
}

// applicableState reconstructs cs_applicable_state for one (agent, coverage_id)
// from the single chosen cell's recipe. The migrator picked the canonical
// variant per cell, so a per-cell read reproduces the old multi-variant rollup
// (the consistency gate confirms zero disagreements):
//
//	recipe absent                 → AppAbsent
//	recipe.applicable == false    → AppFalse
//	recipe.applicable absent/true → AppTrue
func (m *Matrix) applicableState(agent, cid string) ApplicableState {
	sh, ok := m.shards[cid]
	if !ok {
		return AppAbsent
	}
	c, ok := sh.Agents[agent]
	if !ok {
		return AppAbsent
	}
	if len(c.Details.Recipe) == 0 {
		return AppAbsent
	}
	var r shardRecipe
	if json.Unmarshal(c.Details.Recipe, &r) != nil {
		return AppTrue
	}
	if r.Applicable != nil && !*r.Applicable {
		return AppFalse
	}
	return AppTrue
}

// repAxes returns the three axis strings from a parsed assessment, or all-empty
// when the blob was present-but-malformed (rep nil) — so a keyless assessment
// routes record_now exactly as the bash else-branch did.
func repAxes(rep *AssessmentReport) (supports, daemon, driver string) {
	if rep == nil {
		return "", "", ""
	}
	return rep.AgentSupports, rep.DaemonCapability, rep.DriverCapability
}

// buildCell assembles the full CellState for one (agent, coverage_id) cell that
// the shard names. Axes come from the cell's assessment; recorded / applicable
// are reconstructed from the cell's Artifacts / Recipe.
func (m *Matrix) buildCell(agent, cid string) CellState {
	c := m.shards[cid].Agents[agent]
	recorded := cellRecorded(c)
	hasAssessFile, rep := cellAssessment(c)
	appl := m.applicableState(agent, cid)

	cs := CellState{
		Agent:           agent,
		CoverageID:      cid,
		Applicable:      true,
		ApplicableState: appl,
		Recorded:        recorded,
		Assessment:      rep,
	}

	// Disposition / Route use the parsed-assessment axes exactly as the legacy
	// model did: empty when the assessment is absent or present-but-malformed
	// (rep nil). A keyless/malformed-but-present assessment therefore routes
	// record_now, matching bash's jq-on-keyless else-branch.
	supports, daemon, driver := repAxes(rep)
	if rep != nil {
		cs.BlockedReason = rep.RecordBlocked
	}

	cs.Disposition = m.disposition(agent, cid, recorded, hasAssessFile, supports, daemon, driver)
	if hasAssessFile {
		cs.Route = computeRoute(supports, daemon, driver)
	}

	// DisplayState: axes from the assessment, else the overview Metadata block
	// (the viewer's overview tier) so a cell with no parsed assessment still
	// renders a non-empty verdict rather than collapsing to "unknown".
	dsSupports, dsDaemon, dsDriver := supports, daemon, driver
	if rep == nil {
		dsSupports = c.Metadata.AgentSupports
		dsDaemon = c.Metadata.DaemonCapability
		dsDriver = c.Metadata.DriverCapability
	}
	cs.DisplayState = DeriveDisplayState(dsSupports, dsDaemon, dsDriver, recorded)
	return cs
}

// disposition ports cg_disposition steps 1-8 (steps 1-2 already resolved into
// recorded/hasAssessFile by recordedAndAssessment).
func (m *Matrix) disposition(agent, cid string, recorded, hasAssessFile bool, supports, daemon, driver string) Disposition {
	if recorded {
		return DispRecorded
	}
	if !hasAssessFile {
		return DispUnassessed
	}
	// Malformed/keyless assessment → empty axes → treat as unassessed.
	if supports == "" && daemon == "" && driver == "" {
		return DispUnassessed
	}
	// 3. frozen by capability.
	if supports == "no" || supports == "unknown" {
		return DispApplicableFalse
	}
	if daemon == "incapable" || daemon == "n/a" {
		return DispApplicableFalse
	}
	// 4. degraded out at record time — ALL variants applicable:false.
	if m.applicableState(agent, cid) == AppFalse {
		return DispApplicableFalse
	}
	// 5. driver gap.
	if len(driver) >= 4 && driver[:4] == "gap:" {
		return DispDriverGap
	}
	// 6. assessed recordable, no recording.
	return DispAssessedNotRecord
}

// Cell returns the assembled state for one applicable (agent, coverage_id).
// ok is false when the coverage_id is not applicable to the agent (or the
// agent is unknown) — matching the gates, which never visit inapplicable cells.
func (m *Matrix) Cell(agent, cid string) (CellState, bool) {
	if byCID, ok := m.cells[agent]; ok {
		if cs, ok := byCID[cid]; ok {
			return cs, true
		}
	}
	return CellState{}, false
}

// ApplicableCells returns every applicable cell for an agent, sorted by
// coverage_id.
func (m *Matrix) ApplicableCells(agent string) []CellState {
	byCID, ok := m.cells[agent]
	if !ok {
		return nil
	}
	cids := make([]string, 0, len(byCID))
	for cid := range byCID {
		cids = append(cids, cid)
	}
	sort.Strings(cids)
	out := make([]CellState, 0, len(cids))
	for _, cid := range cids {
		out = append(out, byCID[cid])
	}
	return out
}

// Disagreements ports cs_errors: for every applicable, un-recorded, assessed
// cell across all agents, the assessment verdict and the matrix applicable flag
// must agree. Returns one Disagreement per contradiction, sorted by (agent, cid).
func (m *Matrix) Disagreements() []Disagreement {
	var out []Disagreement
	for _, agent := range m.agents {
		for _, cs := range m.ApplicableCells(agent) {
			if cs.Recorded || cs.Assessment == nil || cs.Route == RouteNone {
				continue
			}
			v := CellVerdict(cs.Route, cs.ApplicableState, false, cs.BlockedReason)
			if v == VerdictOK {
				continue
			}
			s, d, dr := cs.Assessment.AgentSupports, cs.Assessment.DaemonCapability, cs.Assessment.DriverCapability
			dg := Disagreement{
				Agent: agent, CoverageID: cs.CoverageID, Verdict: v,
				Supports: s, Daemon: d, Driver: dr,
			}
			switch v {
			case VerdictContradictRecord:
				dg.Message = fmt.Sprintf("%s/%s: assessment routes RECORD (supports=%s daemon=%s driver=%s) but scenarios.json marks by_adapter.%s applicable:false and no recording exists — reconcile: fix the assessment DOWN (e.g. daemon→incapable/unknown) or flip the matrix UP and record", agent, cs.CoverageID, s, d, dr, agent)
			case VerdictContradictFrozen:
				dg.Message = fmt.Sprintf("%s/%s: scenarios.json marks by_adapter.%s applicable:true but the assessment routes FROZEN (supports=%s daemon=%s) — reconcile: fix the assessment UP or mark the recipe applicable:false", agent, cs.CoverageID, agent, s, d)
			}
			out = append(out, dg)
		}
	}
	return out
}
