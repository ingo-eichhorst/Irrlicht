package matrix

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// This file is the capability model (#1369): the machine-readable answer to
// "why is this cell dead?", replacing hand-written per-cell assessments with
// one-line declarations in a single reviewable file. At #1369 that measured
// 122 assessments becoming 124 declarations, only 2 of which had no cell
// directory at all — a reading of that moment, not a live figure, and both
// halves have moved a long way since. Print the current directory-less set
// (never restate it here) with:
//
//	go test ./tools/onboarding-factory/internal/matrix/ \
//	  -run TestShardCellEquivalence -v -count=1
//
// WHAT IT IS NOT. It is not a predictor at all, and the measurement is worth
// stating plainly because the ticket that asked for this model assumed
// otherwise. EVERY trait below maps to exactly one scenario, so the model is a
// COMPACTION of the axes rather than an inference from them: declaring
// `interrupt: untraced` for kiro-cli says precisely what kiro-cli's
// user-esc-interrupt cell already said, in one line instead of a directory.
// Cross-scenario inference accounts for ZERO of the 122 structural cells.
//
// Two multi-scenario traits were written and both were withdrawn on evidence —
// architect_editor for asserting something false, and a second, since-retired
// pair for making the validator unsatisfiable. architect_editor's entry below
// records its own case; the retired pair's case, and the general rule that
// survives them both, are written up in docs/replay-testing.md under "The
// one-scenario trait rule". That write-up keeps the historical trait names,
// because the rule is only legible through the two cases that produced it.
//
// WHAT IT IS FOR, given that. Two things a per-cell assessment cannot do:
//
//  1. Onboarding cost, for the NEXT adapter. A structurally dead cell no
//     longer needs to exist on disk: `Load` synthesizes it, so a new adapter
//     ships its recorded cells plus `of agent update --capability t=absent`
//     per missing feature, instead of a directory with a metadata.json and a
//     written assessment body per dead cell. Note the tense — most of the
//     declarations re-encode a cell that still exists on disk, and nothing
//     schedules deleting those. So the corpus now carries two statements of
//     the same fact, kept honest by the validator; the saving is realised only
//     by the pairs that already have no directory, and the census command
//     above is what prints which those are.
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
// observable, together with the scenario that needs it.
//
// ONE scenario, not a list. A list was tried twice and withdrawn twice (see
// architect_editor below, and the since-retired pair written up in
// docs/replay-testing.md under "The one-scenario trait rule"), so the type is
// now the lock: widening a trait is a deliberate type change rather than an
// edit to a literal, which is the bar those two cases ask for. It also removes the
// case the validator cannot describe — a trait spanning scenarios whose cells
// disagree has no truthful value, and nothing in this matrix guarantees two
// scenarios move together for every adapter.
type Trait struct {
	ID       string
	Title    string
	Scenario string
}

// Traits is the closed set. It lives in Go rather than in the JSON data file
// on purpose: a trait that could be invented in data is a trait that could be
// invented to silence a finding. The JSON says only which VALUE each adapter
// takes for a trait named here.
//
// Coverage: these traits cover, one-for-one, the scenarios that have at least
// one dead cell today. The remaining scenarios have none and therefore no
// trait — the model deliberately says nothing about a scenario nothing has ever
// failed. The split is deliberately NOT written down here; print it with:
//
//	go test ./tools/onboarding-factory/internal/matrix/ \
//	  -run TestTraitCoverageCensus -v -count=1
//
// The previous spelling of this comment carried the counts ("32 traits ... 32
// scenarios ... other 14") and was wrong the moment a scenario landed. The
// spelling after that named the command AND restated the numbers, which
// reintroduced the drift in the same sentence that described it. A live figure
// belongs in output, not in a comment — see cmd/replay's
// TestNoCommentRestatesALiveCensusFigure for the same rule enforced
// mechanically.
var Traits = []Trait{
	{"cloud_agent", "dispatches a run onto remote infrastructure with no local PID", "cloud-background-agent"},
	{"session_resume", "resumes a prior session under a stable session id", "session-resume"},
	{"session_reset", "mints a NEW session id on reset (not merely clearing context)", "session-reset"},
	{"checkpoint_rewind", "rewinds to an earlier checkpoint", "checkpoint-rewind"},
	{"message_queue", "queues a message typed mid-turn", "mid-turn-message-queued"},
	{"permission_classifier", "auto-classifies a tool call against a permission policy", "auto-classified-permission"},
	{"context_compaction", "compacts its own context window", "context-compaction"},
	{"error_epilogue", "writes a terminal record when a turn dies of an error", "turn-aborted-by-error"},
	// The three #1803 traits below are all error-shaped, and none of them is
	// folded into error_epilogue — deliberately, under this file's own rule
	// that a trait may span scenarios only while those scenarios are
	// guaranteed to move together for EVERY adapter. Measured, they do not:
	//
	//   overload_retry vs error_epilogue — a retry-in-progress record and a
	//   terminal epilogue are different bytes. Exactly ONE adapter writes the
	//   first (claudecode's system/subtype:"api_error", which is the only site
	//   in the tree that can produce ErrorPhaseRetrying — the gate is
	//   claudecode/sessionerror.go's `Attempt != nil || RetryIn != nil`), while
	//   five more write the second. A shared trait would have to be wrong for
	//   one group or the other.
	//
	//   auth_refusal vs overload_* — an overload is retryable and a rejected
	//   credential is not, and the adapters that swallow retryable statuses
	//   into invisibility (gemini-cli's retryWithBackoff on 429/500/503,
	//   opencode's AI-SDK on 429 — both documented in their mocks' headers)
	//   still surface a non-retryable refusal. Same adapter, opposite value.
	{"overload_retry", "records that a failed provider call has another attempt pending", "provider-overloaded-retry"},
	{"overload_terminal", "records a provider overload it never recovered from", "provider-overloaded-terminal"},
	{"auth_refusal", "records a rejected-credentials refusal no retry can clear", "auth-credentials-rejected"},
	{"file_transcript", "writes a line-oriented transcript file (vs a store)", "oversized-transcript-line"},
	{"structured_question", "asks the user a structured, blocking question", "user-blocking-question"},
	{"plan_mode", "holds a plan-approval gate the user must clear", "user-blocking-plan-mode-approval"},
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
	{"architect_editor", "hands one turn between two models and records both", "architect-editor-pair"},
	{"permission_prompt", "blocks on an interactive tool-permission prompt", "tool-gate-permission-prompt"},
	{"interrupt", "records that the user cancelled a turn", "user-esc-interrupt"},
	{"partial_flush", "flushes partial assistant output as it streams", "streaming-partial-writes"},
	{"task_list", "emits a structured task list", "task-list"},
	{"autonomous_loop", "runs a goal-seeking loop without per-turn prompting", "autonomous-loop"},
	{"iteration_limit", "caps that loop at a maximum iteration count", "autonomous-loop-iteration-limit"},
	{"quota_exhaustion", "surfaces a provider quota refusal", "token-quota-exhausted"},
	{"subagent_foreground", "spawns a child agent the parent turn waits on", "foreground-subagent"},
	{"subagent_background", "dispatches a child agent fire-and-forget", "background-subagent"},
	{"background_process", "starts a shell process the turn does not block on", "background-process"},
	{"subagent_orphan", "can leave an orphaned child for the reaper to collect", "subagent-orphan-cleanup"},
	{"workflow_fanout", "fans one prompt out across several child agents", "workflow-fanout"},
	{"session_isolation", "keeps two concurrent sessions in one cwd distinct", "multiple-sessions-same-cwd"},
	{"token_usage", "reports per-turn token counts", "token-accounting"},
	{"model_switch", "switches model mid-session", "model-switch-midsession"},
	{"provider_failover", "fails over to a second provider WITHIN one turn", "provider-failover-midturn"},
	// Split from burndown_progression on evidence: claudecode observes
	// subscription-detection (via the statusLine hook POST) while its
	// quota-burndown is unobservable. One trait covering both would have
	// declared a live cell dead.
	{"subscription_signal", "exposes which billing model the session is on", "subscription-detection"},
	{"burndown_progression", "exposes a rate-limit window that moves across turns", "quota-burndown"},
	// THE GENERAL RULE, for whoever is tempted to widen one: a trait may span
	// several scenarios only while those scenarios are guaranteed to move
	// together for every adapter. Nothing in this matrix guarantees that, and
	// both attempts to assume it were wrong within one ticket — so every trait
	// here covers exactly one scenario, and the cost of that is one JSON line
	// per cell rather than per feature. Both cases are in docs/replay-testing.md
	// under "The one-scenario trait rule"; the second pair was retired with the
	// feature it described, so only the write-up survives it.
}

// TraitForScenario returns the trait a scenario needs, if any. At most one
// trait may name a given scenario — TestEachScenarioHasAtMostOneTrait pins
// that, because two traits gating one scenario would make the derived state
// depend on the order of the Traits slice.
func TraitForScenario(scenario string) (Trait, bool) {
	for _, t := range Traits {
		if t.Scenario == scenario {
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
	return slices.Sorted(maps.Keys(m.Adapters))
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

// StructuralState is the display state the capability model says an
// (adapter, scenario) cell must have, and whether it says anything at all.
// ok=false means "not structurally dead" — either the scenario has no trait,
// or the adapter's state for it is traced. It never returns a live state; the
// model's whole vocabulary of outcomes is {n/a, unobservable}.
//
// It goes through StructuralAxes and the SAME DeriveDisplayState every other
// cell uses, rather than switching on the capability state a second time. An
// independent switch here would be a second axes→state table that could drift
// from router.go's, and the only thing holding the two together would be a
// test written to police the duplication.
func (m *CapabilityModel) StructuralState(adapter, scenario string) (string, bool) {
	supports, daemon, driver, ok := m.StructuralAxes(adapter, scenario)
	if !ok {
		return "", false
	}
	// hasRecording=false, applicable=false: a structurally dead pair has
	// neither, by definition.
	return DeriveDisplayState(supports, daemon, driver, false, false), true
}

// SyntheticCell builds the on-disk-shaped cell a structurally dead pair would
// have had, or nil when the model declares nothing for the pair.
//
// It returns a *shard.ShardAgent — the RAW layer — rather than an assembled
// CellState, and that is the whole design. Every field a caller wants
// (Route, Disposition, ApplicableState, DisplayState) is something buildCell
// already computes from exactly these inputs, so handing the raw shape to the
// existing pipeline means a modelled cell and a written one are assembled by
// one code path and cannot disagree. The earlier version of this returned a
// hand-built CellState and had to set four derived fields literally; it got
// one of them wrong (Applicable), needed a duplicate fallback in rollupAxes
// because that reads the raw layer, and needed a test to pin its private
// axes→state derivation against router.go's.
//
// It is deliberately NOT pushed into shard.LoadAdapterCells. Four other
// callers read that (of record, hookcov, the viewer's recipe endpoint twice)
// and would be handed directory-less cells, where AgentCellDir resolves to the
// adapter root. Synthesis stays opt-in per consumer; this helper is what keeps
// it single-sourced anyway.
func (m *CapabilityModel) SyntheticCell(adapter, scenario string) *shard.ShardAgent {
	supports, daemon, driver, ok := m.StructuralAxes(adapter, scenario)
	if !ok {
		return nil
	}
	assessment, err := json.Marshal(AssessmentReport{
		Agent:            adapter,
		ScenarioID:       scenario,
		AgentSupports:    supports,
		DaemonCapability: daemon,
		DriverCapability: driver,
		Body:             DerivedCellNote,
	})
	if err != nil { // unreachable: the struct is all plain fields
		return nil
	}
	return &shard.ShardAgent{
		ScenarioID: scenario,
		Metadata: shard.ShardMetadata{
			AgentSupports:    supports,
			DaemonCapability: daemon,
			DriverCapability: driver,
			Notes:            DerivedCellNote,
		},
		Details: shard.ShardDetails{
			Assessment: assessment,
			// applicable:false is what makes the cell terminal for the
			// completeness gate, exactly as a hand-written deferral is.
			Recipe: json.RawMessage(`{"applicable": false}`),
		},
		// Folder stays empty: there is no directory, and cellRecorded reads
		// Folder to decide whether to look for recordings.
	}
}

// DerivedCellNote is the single spelling of "this cell came from the model",
// shared by every surface that renders one. Two surfaces had already forked
// their own wording before this const existed.
const DerivedCellNote = "derived from replaydata/agents/adapters.json (#1369); no cell directory on disk"

// StructuralAxes returns the three assessment axes a structurally dead pair
// implies. It is the same derivation as StructuralState, expressed in the
// vocabulary the OTHER consumers speak: the rollup and the viewer's catalog
// endpoint both read raw axes off the shard cell rather than a CellState, and
// a pair with no directory would otherwise default to "unknown" — the one
// state the maturity model reads as "not assessed", i.e. exactly the wrong
// answer for a cell we have deliberately declared dead.
//
// The two representations are pinned to each other by
// TestStructuralAxesDeriveTheStructuralState: whatever these axes are, feeding
// them to DeriveDisplayState must reproduce StructuralState. Without that a
// synthesized cell could read n/a in one surface and unobservable in another.
func (m *CapabilityModel) StructuralAxes(adapter, scenario string) (supports, daemon, driver string, ok bool) {
	t, found := TraitForScenario(scenario)
	if !found {
		return "", "", "", false
	}
	switch m.CapabilityState(adapter, t.ID) {
	case CapabilityAbsent:
		return SupportsNo, DaemonNotApplicable, "ready", true
	case CapabilityUntraced:
		return SupportsYes, DaemonIncapable, "ready", true
	default:
		return "", "", "", false
	}
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
	core := CoreScenarios()
	out := make([]CoreStatus, 0, len(core))
	for _, name := range core {
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
	earned := MaturityPlanned
	for _, tier := range Maturities {
		if len(m.UnsettledCoreFor(adapter, tier)) > 0 {
			// Floors are cumulative (TestCoreSetShape locks alpha ⊂ beta ⊂
			// stable), so the first miss is the ceiling.
			break
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
