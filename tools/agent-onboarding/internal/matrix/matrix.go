package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
// via LoadRepo. AgentsRoot is .../replaydata/agents.
type Config struct {
	ScenariosPath string
	AgentsRoot    string
}

// Matrix is the loaded, normalized model. Construct via Load / LoadRepo.
type Matrix struct {
	catalog   []catalogEntry
	scenarios []scenarioVariant
	agents    []string                        // sorted, agents with a capabilities.json
	caps      map[string]capabilities         // agent → capabilities
	cells     map[string]map[string]CellState // agent → coverage_id → cell (applicable cells only)
	cfg       Config
}

type catalogEntry struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Feature string `json:"feature"`
}

type scenarioVariant struct {
	Name              string   `json:"name"`
	CoverageID        string   `json:"coverage_id"`
	Requires          []string `json:"requires"`
	RequiresTransport []string `json:"requires_transport"`
	// Pointer values so a literal-null entry (`"by_adapter":{"x":null}`)
	// unmarshals to nil and is dropped, matching the bash gates'
	// `.by_adapter[$a] | select(. != null)`.
	ByAdapter map[string]*adapterEntry `json:"by_adapter"`
}

type adapterEntry struct {
	// Applicable is a pointer so we can tell "absent" (nil → recordable) from
	// an explicit false. Mirrors jq's `.applicable == false` test.
	Applicable *bool `json:"applicable"`
}

type capabilities struct {
	Agent     string         `json:"agent"`
	Transport string         `json:"transport"`
	Features  map[string]any `json:"features"`
}

// LoadRepo loads the matrix from a repo root, using the canonical paths:
// scenarios.json under .claude/skills/ir:onboard-agent/ and recordings under
// replaydata/agents/.
func LoadRepo(repoRoot string) (*Matrix, error) {
	return Load(Config{
		ScenariosPath: filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json"),
		AgentsRoot:    filepath.Join(repoRoot, "replaydata", "agents"),
	})
}

// Load reads scenarios.json plus every agent's capabilities.json under
// AgentsRoot and assembles the per-cell state for all applicable cells.
func Load(cfg Config) (*Matrix, error) {
	b, err := os.ReadFile(cfg.ScenariosPath)
	if err != nil {
		return nil, fmt.Errorf("read scenarios.json: %w", err)
	}
	var doc struct {
		Catalog   []catalogEntry    `json:"catalog"`
		Scenarios []scenarioVariant `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse scenarios.json: %w", err)
	}

	m := &Matrix{
		catalog:   doc.Catalog,
		scenarios: doc.Scenarios,
		caps:      map[string]capabilities{},
		cells:     map[string]map[string]CellState{},
		cfg:       cfg,
	}

	entries, err := os.ReadDir(cfg.AgentsRoot)
	if err != nil {
		return nil, fmt.Errorf("read agents root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agent := e.Name()
		capsPath := filepath.Join(cfg.AgentsRoot, agent, "capabilities.json")
		cb, err := os.ReadFile(capsPath)
		if err != nil {
			continue // no capabilities.json → not an onboarded agent column
		}
		var c capabilities
		if json.Unmarshal(cb, &c) != nil {
			continue
		}
		m.caps[agent] = c
		m.agents = append(m.agents, agent)
	}
	sort.Strings(m.agents)

	for _, agent := range m.agents {
		m.cells[agent] = map[string]CellState{}
		for _, cid := range m.applicableCoverageIDs(agent) {
			m.cells[agent][cid] = m.buildCell(agent, cid)
		}
	}
	return m, nil
}

// HasAgent reports whether the agent has a capabilities.json under AgentsRoot.
func (m *Matrix) HasAgent(agent string) bool {
	_, ok := m.caps[agent]
	return ok
}

// Agents returns the sorted list of onboarded agents.
func (m *Matrix) Agents() []string { return append([]string(nil), m.agents...) }

// variantApplicable mirrors cg_applicable_coverage_ids' per-variant rule: every
// `requires` feature must be boolean true in the agent's capabilities (false /
// "unknown" both block), and when requires_transport is set the agent's
// transport must be listed.
func variantApplicable(v scenarioVariant, c capabilities) bool {
	for _, k := range v.Requires {
		if b, ok := c.Features[k].(bool); !ok || !b {
			return false
		}
	}
	if len(v.RequiresTransport) > 0 && !slices.Contains(v.RequiresTransport, c.Transport) {
		return false
	}
	return true
}

// applicableCoverageIDs ports cg_applicable_coverage_ids: a coverage_id is
// applicable iff ANY of its recipe variants is applicable. Sorted-unique,
// empty coverage_ids dropped.
func (m *Matrix) applicableCoverageIDs(agent string) []string {
	c, ok := m.caps[agent]
	if !ok {
		return nil
	}
	applic := map[string]bool{}
	for _, v := range m.scenarios {
		if v.CoverageID == "" {
			continue
		}
		if variantApplicable(v, c) {
			applic[v.CoverageID] = true
		} else if _, seen := applic[v.CoverageID]; !seen {
			applic[v.CoverageID] = false
		}
	}
	var out []string
	for cid, ok := range applic {
		if ok {
			out = append(out, cid)
		}
	}
	sort.Strings(out)
	return out
}

// candidateDirs ports cg_candidate_dirs: the coverage_id itself plus every
// recipe variant `name` mapping to it. Sorted-unique.
func (m *Matrix) candidateDirs(cid string) []string {
	set := map[string]bool{cid: true}
	for _, v := range m.scenarios {
		if v.CoverageID == cid && v.Name != "" {
			set[v.Name] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// applicableState ports cs_applicable_state for one (agent, coverage_id).
func (m *Matrix) applicableState(agent, cid string) ApplicableState {
	var vals []*bool
	for _, v := range m.scenarios {
		if v.CoverageID != cid {
			continue
		}
		// A nil entry is a literal JSON null — dropped, like jq's select(. != null).
		if e, ok := v.ByAdapter[agent]; ok && e != nil {
			vals = append(vals, e.Applicable)
		}
	}
	if len(vals) == 0 {
		return AppAbsent
	}
	allFalse := true
	for _, a := range vals {
		// nil (absent) or true both count as recordable → not "all false".
		if !(a != nil && !*a) {
			allFalse = false
			break
		}
	}
	if allFalse {
		return AppFalse
	}
	return AppTrue
}

// cellDir is the on-disk path for one (agent, recording folder).
func (m *Matrix) cellDir(agent, dir string) string {
	return filepath.Join(m.cfg.AgentsRoot, agent, "scenarios", dir)
}

// recordedAndAssessment scans the candidate dirs once: recorded iff any dir has
// (transcript.jsonl|transcript.md) + events.jsonl; assessment is the first dir
// with a parseable assessment.json. Mirrors cg_disposition steps 1-2 / cs_errors.
func (m *Matrix) recordedAndAssessment(agent, cid string) (recorded bool, hasAssessFile bool, rep *AssessmentReport) {
	for _, d := range m.candidateDirs(cid) {
		if d == "" {
			continue
		}
		dir := m.cellDir(agent, d)
		hasEvents := fileExists(filepath.Join(dir, "events.jsonl"))
		hasTranscript := fileExists(filepath.Join(dir, "transcript.jsonl")) || fileExists(filepath.Join(dir, "transcript.md"))
		if hasEvents && hasTranscript {
			recorded = true
		}
		if !hasAssessFile {
			if ab, err := os.ReadFile(filepath.Join(dir, "assessment.json")); err == nil {
				hasAssessFile = true
				var r AssessmentReport
				if json.Unmarshal(ab, &r) == nil {
					rep = &r
				}
			}
		}
	}
	return recorded, hasAssessFile, rep
}

// buildCell assembles the full CellState for an applicable (agent, coverage_id).
func (m *Matrix) buildCell(agent, cid string) CellState {
	recorded, hasAssessFile, rep := m.recordedAndAssessment(agent, cid)
	appl := m.applicableState(agent, cid)

	cs := CellState{
		Agent:           agent,
		CoverageID:      cid,
		Applicable:      true,
		ApplicableState: appl,
		Recorded:        recorded,
		Assessment:      rep,
	}

	var supports, daemon, driver string
	if rep != nil {
		supports, daemon, driver = rep.AgentSupports, rep.DaemonCapability, rep.DriverCapability
		cs.BlockedReason = rep.RecordBlocked
	}

	cs.Disposition = m.disposition(agent, cid, recorded, hasAssessFile, supports, daemon, driver)
	// Route mirrors cs_route: only meaningful when an assessment file is
	// present (cs_errors skips cells with no assessment, and a malformed file
	// parses to a nil report → empty axes → still routes record_now, matching
	// bash's jq-on-keyless behaviour).
	if hasAssessFile {
		cs.Route = computeRoute(supports, daemon, driver)
	}
	cs.DisplayState = DeriveDisplayState(supports, daemon, driver, recorded)
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
