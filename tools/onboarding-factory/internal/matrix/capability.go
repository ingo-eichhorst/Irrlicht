package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// This file is the capability model (#1369): the machine-readable answer to
// "why is this cell dead?", replacing 122 hand-written per-cell assessments
// with 116 one-line declarations in a single reviewable file.
//
// WHAT IT IS NOT. It is barely a predictor, and the measurement is worth
// stating plainly because the ticket that asked for this model assumed
// otherwise. Fitted against the matrix as it stands, 30 of the 31 traits below
// map to exactly one scenario, so for those the model is a COMPACTION of the
// axes rather than an inference from them: declaring `interrupt: untraced` for
// kiro-cli says precisely what kiro-cli's user-esc-interrupt cell already
// said, in one line instead of a directory. Exactly ONE trait spans more than
// one scenario — `backchannel`, whose two scenarios agree for every adapter
// that has both — so cross-scenario inference accounts for 2 of the 122
// structural cells.
//
// A second spanning trait was written and then withdrawn on evidence; the
// architect_editor entry below records why. The general shape of the failure
// is worth knowing before anyone widens a trait: two scenarios whose dead sets
// happen to coincide across the adapters that HAVE both cells will confidently
// synthesize a wrong cell for an adapter that has neither.
//
// WHAT IT IS FOR, given that. Two things a per-cell assessment cannot do:
//
//  1. Onboarding cost. A structurally dead cell no longer needs to exist on
//     disk. `Load` synthesizes it from the model, so a new adapter ships its
//     recorded cells plus one line per missing feature instead of a directory
//     with a metadata.json and a written assessment body per dead cell. On the
//     current corpus that is 122 directories' worth of prose the next adapter
//     does not write.
//  2. Drift. Once the model exists, the stored axes and the declared
//     capability are two statements that can contradict each other, and
//     `of validate` fails when they do. Before this file there was no second
//     statement to disagree with.
//
// THE FOUR DELIBERATE EXCLUSIONS. Four cells derive to n/a WITHOUT being
// structurally dead: aider/token-quota-exhausted (record_blocked: infra),
// codex/oversized-transcript-line (unit_test),
// copilot/provider-failover-midturn (upstream) and
// opencode/checkpoint-rewind (driver_bug). All four are agent_supports:yes,
// daemon_capability:full — the agent HAS the feature and the daemon CAN see
// it; the recording is deferred for a documented reason. Modelling them as
// capabilities would assert something false about the adapter (codex plainly
// has a file transcript), so the model leaves them traced and the validator
// exempts any cell carrying a record_blocked reason. That exemption is the
// only place the two concepts touch.

// Trait is one behavioural feature a scenario needs in order to be
// observable, together with the scenarios that need it.
type Trait struct {
	ID        string
	Title     string
	Scenarios []string
}

// Traits is the closed set. It lives in Go rather than in the JSON data file
// on purpose: a trait that could be invented in data is a trait that could be
// invented to silence a finding. The JSON says only which VALUE each adapter
// takes for a trait named here.
//
// Coverage: these 31 traits span the 32 scenarios that have at least one dead
// cell today. The other 14 scenarios have none and therefore no trait — the
// model deliberately says nothing about a scenario nothing has ever failed.
var Traits = []Trait{
	{"cloud_agent", "dispatches a run onto remote infrastructure with no local PID", []string{"cloud-background-agent"}},
	{"session_resume", "resumes a prior session under a stable session id", []string{"session-resume"}},
	{"session_reset", "mints a NEW session id on reset (not merely clearing context)", []string{"session-reset"}},
	{"checkpoint_rewind", "rewinds to an earlier checkpoint", []string{"checkpoint-rewind"}},
	{"message_queue", "queues a message typed mid-turn", []string{"mid-turn-message-queued"}},
	{"permission_classifier", "auto-classifies a tool call against a permission policy", []string{"auto-classified-permission"}},
	{"context_compaction", "compacts its own context window", []string{"context-compaction"}},
	{"error_epilogue", "writes a terminal record when a turn dies of an error", []string{"turn-aborted-by-error"}},
	{"file_transcript", "writes a line-oriented transcript file (vs a store)", []string{"oversized-transcript-line"}},
	{"structured_question", "asks the user a structured, blocking question", []string{"user-blocking-question"}},
	{"plan_mode", "holds a plan-approval gate the user must clear", []string{"user-blocking-plan-mode-approval"}},
	// architect-editor-pair is deliberately NOT folded into plan_mode, even
	// though five adapters' 5.4 assessments say in as many words that it is
	// "the SAME architectural blocker" as their 2.18. It has two
	// instantiations, and only one of them goes through a plan gate: (b) is
	// the plan→implement mode pair, but (a) is a genuine two-model handoff,
	// and the acceptance criteria are written for (a) — two model
	// contributions in one turn, each with its own ModelName.
	//
	// The bundled version of this trait was written, and then measured: it
	// synthesized an `unobservable` architect-editor-pair cell for aider, the
	// one adapter whose signature feature IS architect/editor mode, from a
	// plan_mode value that says only that aider's /ask gate is not persisted.
	// A false claim, in a cell nobody had assessed. Splitting the trait costs
	// the model its second predicting trait and is worth it.
	{"architect_editor", "hands one turn between two models and records both", []string{"architect-editor-pair"}},
	{"permission_prompt", "blocks on an interactive tool-permission prompt", []string{"tool-gate-permission-prompt"}},
	{"interrupt", "records that the user cancelled a turn", []string{"user-esc-interrupt"}},
	{"partial_flush", "flushes partial assistant output as it streams", []string{"streaming-partial-writes"}},
	{"task_list", "emits a structured task list", []string{"task-list"}},
	{"autonomous_loop", "runs a goal-seeking loop without per-turn prompting", []string{"autonomous-loop"}},
	{"iteration_limit", "caps that loop at a maximum iteration count", []string{"autonomous-loop-iteration-limit"}},
	{"quota_exhaustion", "surfaces a provider quota refusal", []string{"token-quota-exhausted"}},
	{"subagent_foreground", "spawns a child agent the parent turn waits on", []string{"foreground-subagent"}},
	{"subagent_background", "dispatches a child agent fire-and-forget", []string{"background-subagent"}},
	{"background_process", "starts a shell process the turn does not block on", []string{"background-process"}},
	{"subagent_orphan", "can leave an orphaned child for the reaper to collect", []string{"subagent-orphan-cleanup"}},
	{"workflow_fanout", "fans one prompt out across several child agents", []string{"workflow-fanout"}},
	{"session_isolation", "keeps two concurrent sessions in one cwd distinct", []string{"multiple-sessions-same-cwd"}},
	{"token_usage", "reports per-turn token counts", []string{"token-accounting"}},
	{"model_switch", "switches model mid-session", []string{"model-switch-midsession"}},
	{"provider_failover", "fails over to a second provider WITHIN one turn", []string{"provider-failover-midturn"}},
	// Split from burndown_progression on evidence: claudecode observes
	// subscription-detection (via the statusLine hook POST) while its
	// quota-burndown is unobservable. One trait covering both would have
	// declared a live cell dead.
	{"subscription_signal", "exposes which billing model the session is on", []string{"subscription-detection"}},
	{"burndown_progression", "exposes a rate-limit window that moves across turns", []string{"quota-burndown"}},
	// The second predicting trait: hermes is untraced for both halves.
	{"backchannel", "can be driven or observed through a controlling terminal", []string{"backchannel-control", "backchannel-observe"}},
}

// TraitForScenario returns the trait a scenario needs, if any. Every scenario
// has at most one — TestEachScenarioHasAtMostOneTrait pins that, because two
// traits gating one scenario would make the derived state depend on map order.
func TraitForScenario(scenario string) (Trait, bool) {
	for _, t := range Traits {
		if slices.Contains(t.Scenarios, scenario) {
			return t, true
		}
	}
	return Trait{}, false
}

// TraitByID looks up a trait by id.
func TraitByID(id string) (Trait, bool) {
	for _, t := range Traits {
		if t.ID == id {
			return t, true
		}
	}
	return Trait{}, false
}

// AdapterModel is one adapter's row in the capability model.
type AdapterModel struct {
	// Maturity is the tier the adapter CLAIMS. `of validate` fails when the
	// claim outruns the evidence; it never rewrites the claim.
	Maturity string `json:"maturity"`
	// Capabilities holds only the non-default values. An omitted trait is
	// CapabilityTraced, so a fully-capable adapter's entry is one line.
	Capabilities map[string]string `json:"capabilities,omitempty"`
	// Notes is free text for a human reader; nothing reads it.
	Notes string `json:"notes,omitempty"`
}

// CapabilityModel is replaydata/agents/adapters.json — the COLUMN file,
// symmetric to scenarios.json's rows.
type CapabilityModel struct {
	SchemaVersion int                     `json:"schema_version"`
	Adapters      map[string]AdapterModel `json:"adapters"`

	// loaded records whether the file was actually read. A missing file is not
	// an error (every consumer degrades to "no model"), but the validator needs
	// to tell "absent" from "empty".
	loaded bool
}

// CapabilityFile returns the model's path under a repo root.
func CapabilityFile(repoRoot string) string {
	return filepath.Join(repoRoot, "replaydata", "agents", "adapters.json")
}

// LoadCapabilities reads the capability model. A missing file yields an empty,
// not-loaded model and no error: the matrix predates the model and must keep
// loading without it, which is also what makes this safe to land before every
// consumer knows about it.
func LoadCapabilities(repoRoot string) (*CapabilityModel, error) {
	b, err := os.ReadFile(CapabilityFile(repoRoot))
	if os.IsNotExist(err) {
		return &CapabilityModel{Adapters: map[string]AdapterModel{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var m CapabilityModel
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", CapabilityFile(repoRoot), err)
	}
	if m.Adapters == nil {
		m.Adapters = map[string]AdapterModel{}
	}
	m.loaded = true
	return &m, nil
}

// Loaded reports whether the model file exists on disk.
func (m *CapabilityModel) Loaded() bool { return m != nil && m.loaded }

// AdapterNames returns the adapters the model declares, sorted.
func (m *CapabilityModel) AdapterNames() []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m.Adapters))
	for a := range m.Adapters {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Maturity returns an adapter's declared tier, or "" when it is not declared.
func (m *CapabilityModel) Maturity(adapter string) string {
	if m == nil {
		return ""
	}
	return m.Adapters[adapter].Maturity
}

// CapabilityState returns an adapter's state for one trait id, defaulting to
// CapabilityTraced for anything undeclared — so the model expresses only what
// is missing.
func (m *CapabilityModel) CapabilityState(adapter, traitID string) string {
	if m == nil {
		return CapabilityTraced
	}
	if v, ok := m.Adapters[adapter].Capabilities[traitID]; ok && v != "" {
		return v
	}
	return CapabilityTraced
}

// StructuralState is THE derivation: the display state the capability model
// says an (adapter, scenario) cell must have, and whether it says anything at
// all. ok=false means "not structurally dead" — either the scenario has no
// trait, or the adapter's state for it is traced. It never returns a live
// state; the model's whole vocabulary of outcomes is {n/a, unobservable}.
func (m *CapabilityModel) StructuralState(adapter, scenario string) (string, bool) {
	t, ok := TraitForScenario(scenario)
	if !ok {
		return "", false
	}
	return StructuralStateFor(m.CapabilityState(adapter, t.ID))
}

// CoreStatus is one core scenario's standing for one adapter.
type CoreStatus struct {
	Scenario string
	// State is the cell's display state, or "absent" when the matrix has no
	// cell for the pair at all.
	State string
	// Settled is whether the scenario counts as done for a maturity floor:
	// observed, or structurally dead per the capability model.
	Settled bool
	// Structural records that Settled was earned by the derivation rather
	// than by a recording.
	Structural bool
}

// CellAbsent is the CoreStatus.State value for a (adapter, scenario) pair with
// no cell in the matrix. It is not a display state and never appears on disk.
const CellAbsent = "absent"

// CoreStanding evaluates every core scenario for one adapter, in core order.
//
// "Settled" is deliberately narrow. A cell that is merely dead ON DISK does
// NOT settle a core scenario — only a recording, or a dead state the
// capability model DERIVES, does. That is the pressure the whole ticket is
// for: a structural claim standing between an adapter and a tier has to be
// declared in the model, where one reviewer can see all 116 of them at once,
// instead of being typed into a cell directory nobody re-reads.
func (m *Matrix) CoreStanding(adapter string) []CoreStatus {
	out := make([]CoreStatus, 0, len(CoreScenarios()))
	for _, name := range CoreScenarios() {
		st := CoreStatus{Scenario: name, State: CellAbsent}
		if c, ok := m.cells[adapter][name]; ok {
			st.State = c.DisplayState
		}
		if derived, ok := m.Capabilities().StructuralState(adapter, name); ok {
			st.Structural = true
			st.Settled = st.State == derived
		} else {
			st.Settled = st.State == StateObserved
		}
		out = append(out, st)
	}
	return out
}

// EarnedMaturity is the highest tier whose floor the adapter's core standing
// satisfies. It is evidence, never a claim: nothing writes it back to disk.
func (m *Matrix) EarnedMaturity(adapter string) string {
	settled := map[string]bool{}
	for _, s := range m.CoreStanding(adapter) {
		settled[s.Scenario] = s.Settled
	}
	earned := MaturityPlanned
	for _, tier := range Maturities {
		ok := true
		for _, name := range MaturityFloor(tier) {
			if !settled[name] {
				ok = false
				break
			}
		}
		if !ok {
			break // floors are cumulative, so the first miss is the ceiling
		}
		earned = tier
	}
	return earned
}

// UnsettledCoreFor lists the core scenarios blocking a given tier, with the
// state each is actually in — the message a maturity finding needs.
func (m *Matrix) UnsettledCoreFor(adapter, tier string) []CoreStatus {
	standing := map[string]CoreStatus{}
	for _, s := range m.CoreStanding(adapter) {
		standing[s.Scenario] = s
	}
	var out []CoreStatus
	for _, name := range MaturityFloor(tier) {
		if s := standing[name]; !s.Settled {
			out = append(out, s)
		}
	}
	return out
}

// derivedCell builds the CellState for an (adapter, scenario) pair that has no
// directory on disk and is structurally dead per the capability model.
//
// It is terminal by construction — RouteFrozen / DispApplicableFalse — which
// is the same standing a hand-written applicable:false cell has, so the
// completeness gate treats a modelled cell and a written one identically. It
// carries no assessment: there is no assessment to carry, and inventing an
// empty one would make a synthesized cell indistinguishable from an
// unassessed real one.
func (m *Matrix) derivedCell(agent, scenario, displayState string) CellState {
	return CellState{
		Agent:           agent,
		CoverageID:      scenario,
		Applicable:      false,
		ApplicableState: AppFalse,
		Recorded:        false,
		Route:           RouteFrozen,
		Disposition:     DispApplicableFalse,
		DisplayState:    displayState,
		Derived:         true,
	}
}
