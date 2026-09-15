package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// --- of scenario recipes --name <scenario> ---
//
// #1969: `assess` writes a new cell's recipe by reasoning about the agent
// under test, but timing constants (a daemon cooldown, a debounce window, a
// settle interval) are properties of *irrlicht*, not of the agent — so a
// sibling adapter's already-COMMITTED, already-driven recipe for the same
// scenario is near-authoritative. muse/1-4_session-resume (#1960) shipped
// without the sleep every sibling recipe already carried between its
// `resume` step and the post-resume `send`, and failed 6/8 phases because
// the whole second turn landed inside the daemon's 10s deletedSessions
// cooldown. This command is the "one command instead of a manual glob" the
// issue asks for: list every onboarded adapter's committed recipe for one
// scenario, step by step, side by side, so an omitted sleep is visible
// without reading five metadata.json files by hand.

// recipeStatus classifies what a (adapter, scenario) cell carries, from
// "never assessed" to "has a comparable driven script". Kept as a small
// closed vocabulary (not a bool) because "no recipe" and "blocked" and "no
// cell at all" are different facts a reader needs told apart — collapsing
// them would hide exactly the kind of gap this command exists to surface.
type recipeStatus string

const (
	recipeStatusNoCell    recipeStatus = "no_cell"    // adapter never assessed this scenario
	recipeStatusNoRecipe  recipeStatus = "no_recipe"  // cell exists, details.recipe absent
	recipeStatusBlocked   recipeStatus = "blocked"    // recipe.applicable == false, never driven
	recipeStatusNoScript  recipeStatus = "no_script"  // recipe present but script is empty
	recipeStatusHasScript recipeStatus = "has_script" // comparable: non-empty driven script
)

// recipeDoc is the subset of a details.recipe object this command reads.
// Mirrors the field set recipe.go's recipeWouldDrive/recipeTimeoutFinding
// already read (applicable, script[].type, timeout_seconds) rather than
// inventing a second parse of the same JSON shape.
type recipeDoc struct {
	Applicable     *bool           `json:"applicable,omitempty"`
	TimeoutSeconds float64         `json:"timeout_seconds,omitempty"`
	Script         []recipeStepDoc `json:"script,omitempty"`
}

type recipeStepDoc struct {
	Type    string  `json:"type"`
	Seconds float64 `json:"seconds,omitempty"`
}

// recipeStepView is one script step as reported. Seconds is only meaningful
// (and only set) when Type == "sleep".
type recipeStepView struct {
	Index   int     `json:"index"`
	Type    string  `json:"type"`
	Seconds float64 `json:"seconds,omitempty"`
}

// adapterRecipeView is one adapter's answer for the requested scenario.
type adapterRecipeView struct {
	Adapter        string           `json:"adapter"`
	Folder         string           `json:"folder,omitempty"`
	Status         recipeStatus     `json:"status"`
	Recorded       bool             `json:"recorded"`
	TimeoutSeconds float64          `json:"timeout_seconds,omitempty"`
	Steps          []recipeStepView `json:"steps,omitempty"`
}

// scenarioRecipeReport is the full answer for `of scenario recipes`.
type scenarioRecipeReport struct {
	ScenarioID   string              `json:"scenario_id"`
	ScenarioName string              `json:"scenario_name"`
	Adapters     []adapterRecipeView `json:"adapters"`
}

func runScenarioRecipes(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of scenario recipes")
	var (
		name     = fs.String("name", "", "scenario name or id")
		asJSON   = fs.Bool("json", false, "emit JSON")
		repoRoot = fs.String(repoRootFlagName, ".", repoRootFlagUsage)
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	*repoRoot = absRoot(*repoRoot)
	if *name == "" {
		fmt.Fprintln(stderr, "of scenario recipes: --name is required")
		return exitUsage
	}

	// "cannot look at all" — the catalog itself is unreadable. Checked
	// before the id lookup so that failure isn't reported as "not a
	// scenario", which would send a reader chasing a typo that isn't there.
	if err := checkCatalogReadable(*repoRoot); err != nil {
		fmt.Fprintf(stderr, "of scenario recipes: %v\n", err)
		return exitFail
	}

	sh, ok := resolveScenario(*repoRoot, *name)
	if !ok {
		// "cannot look" — the scenario id itself doesn't exist. This MUST
		// read differently from the "nothing found" case below: different
		// stream framing (an error, not a report), different exit code, and
		// different wording, so a caller can't mistake a typo'd id for a
		// scenario nobody has recorded a sibling recipe for yet.
		fmt.Fprintf(stderr, "of scenario recipes: %q is not a scenario (by name or id)\n", *name)
		return exitFail
	}

	agents := shard.Agents(*repoRoot)
	if len(agents) == 0 {
		fmt.Fprintf(stderr, "of scenario recipes: no onboarded adapters in %s\n", shard.File(*repoRoot))
		return exitFail
	}

	report, err := buildScenarioRecipeReport(*repoRoot, sh, agents)
	if err != nil {
		// A metadata.json this command specifically needed could not be read
		// or parsed. Fail loudly rather than silently rendering that
		// adapter as "no recipe" — an unreadable file and a genuinely
		// absent recipe are different facts, and conflating them is exactly
		// the failure mode AGENTS.md's testing philosophy forbids: absence
		// of a finding and inability to look must never look the same.
		fmt.Fprintf(stderr, "of scenario recipes: %v\n", err)
		return exitFail
	}

	if *asJSON {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "of scenario recipes: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	printScenarioRecipeReport(stdout, report)
	return exitOK
}

// checkCatalogReadable reports whether replaydata/agents/scenarios.json can
// be read and parsed at all, independent of any particular scenario id. A
// missing or malformed catalog makes EVERY id lookup fail the same way
// (shard.LoadAll returns nil), which would otherwise be indistinguishable
// from "that id genuinely isn't in an intact catalog".
//
// #1969 QA finding A: this MUST probe with the same typed shape
// shard.LoadAll's internal loadCatalog unmarshals into (Scenarios []Shard —
// Shard and Meta are both exported, so the shape is reproducible here), not
// a looser one. A probe that accepts any per-element shape (e.g. Scenarios
// []json.RawMessage, which never fails on a nested type mismatch) passes on
// a file where ONE scenario entry has a type-mismatched field — a numeric
// "id" where shard.Shard expects a string, say — while shard.LoadAll's own
// typed json.Unmarshal call returns a non-nil error for that SAME call and
// so discards the WHOLE catalog, not just the bad entry. QA reproduced this
// live: such a file made a lookup of an unrelated, well-formed
// "session-resume" entry fail with "is not a scenario", which is the exact
// misdiagnosis this function exists to prevent. Confirmed with
// TestCheckCatalogReadableRejectsATypeMismatchLoadAllAlsoRejects and
// TestScenarioRecipesReportsCatalogParseFailureNotUnknownScenario, both red
// against the looser probe.
func checkCatalogReadable(repoRoot string) error {
	path := shard.File(repoRoot)
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read catalog %s: %w", path, err)
	}
	var probe struct {
		Meta      shard.Meta    `json:"meta"`
		Scenarios []shard.Shard `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return fmt.Errorf("catalog %s does not parse into the shape shard.LoadAll expects: %w", path, err)
	}
	return nil
}

// resolveScenario looks up one scenario by name OR id — the same dual lookup
// `of status --scenario` uses (buildStatusView in main.go).
func resolveScenario(repoRoot, query string) (shard.Shard, bool) {
	for _, sh := range shard.LoadAll(repoRoot) {
		if sh.Name == query || sh.ID == query {
			return sh, true
		}
	}
	return shard.Shard{}, false
}

// buildScenarioRecipeReport loads one (adapter, scenario) recipe view per
// onboarded adapter. Returns an error the moment any adapter's cell for THIS
// scenario cannot be read or parsed — see loadAdapterRecipeView.
func buildScenarioRecipeReport(repoRoot string, sh shard.Shard, agents []string) (scenarioRecipeReport, error) {
	report := scenarioRecipeReport{ScenarioID: sh.ID, ScenarioName: sh.Name}
	for _, adapter := range agents {
		view, err := loadAdapterRecipeView(repoRoot, adapter, sh)
		if err != nil {
			return scenarioRecipeReport{}, fmt.Errorf("%s: %w", adapter, err)
		}
		report.Adapters = append(report.Adapters, view)
	}
	return report, nil
}

// loadAdapterRecipeView resolves one adapter's cell for sh via the sanctioned
// loaders (shard.AgentFolderForScenario + shard.LoadAgentCell — never a
// hand-rolled directory walk), then classifies and parses its recipe.
//
// Known boundary: AgentFolderForScenario's own variant-folder scan silently
// skips a metadata.json it cannot parse while searching (the same behavior
// shard.LoadAdapterCells has elsewhere in this codebase) — so a corrupt cell
// living in a NON-canonical (variant) folder is invisible to the read-error
// check below and would be misreported as "no cell". The canonical folder
// (<dashed-id>_<name>, where every freshly-assessed cell lives) is checked
// directly and unconditionally: if its metadata.json exists on disk but
// can't be read as a cell, that's a read error, not an absent recipe.
// `of validate` is the tool that owns catalog-wide metadata.json integrity;
// this command only needs to not be silently WRONG about the scenario asked
// for.
func loadAdapterRecipeView(repoRoot, adapter string, sh shard.Shard) (adapterRecipeView, error) {
	view := adapterRecipeView{Adapter: adapter}

	folder := shard.AgentFolderForScenario(repoRoot, adapter, sh.Name)
	metaPath := filepath.Join(shard.AgentCellDir(repoRoot, adapter, folder), "metadata.json")

	cell, ok := shard.LoadAgentCell(repoRoot, adapter, folder)
	if !ok {
		if _, statErr := os.Stat(metaPath); statErr == nil {
			return view, fmt.Errorf("%s exists but could not be read as a cell (malformed JSON?)", metaPath)
		}
		view.Status = recipeStatusNoCell
		return view, nil
	}

	view.Folder = cell.Folder
	if _, recorded := validate.NewestRecordingDir(shard.AgentCellDir(repoRoot, adapter, cell.Folder)); recorded {
		view.Recorded = true
	}

	if len(cell.Details.Recipe) == 0 {
		view.Status = recipeStatusNoRecipe
		return view, nil
	}

	var doc recipeDoc
	if err := json.Unmarshal(cell.Details.Recipe, &doc); err != nil {
		return view, fmt.Errorf("%s: details.recipe is not valid JSON: %w", metaPath, err)
	}
	view.TimeoutSeconds = doc.TimeoutSeconds

	if doc.Applicable != nil && !*doc.Applicable {
		view.Status = recipeStatusBlocked
		return view, nil
	}
	if len(doc.Script) == 0 {
		view.Status = recipeStatusNoScript
		return view, nil
	}

	view.Status = recipeStatusHasScript
	view.Steps = make([]recipeStepView, len(doc.Script))
	for i, step := range doc.Script {
		view.Steps[i] = recipeStepView{Index: i + 1, Type: step.Type, Seconds: step.Seconds}
	}
	return view, nil
}

// --- text rendering ---

// landmarkStrictness is how strictly one landmark type's flagged() check
// reads. strictAfterOnly is the zero value — an unmapped type falls back to
// it — so a landmark type that slips past the completeness guard below
// OVER-reports rather than under-reports (house rule: check MORE when
// uncertain), matching how #1969 QA's second round found the dead-detector
// default should run.
type landmarkStrictness int

const (
	// strictAfterOnly requires a sleep specifically AFTER the landmark — a
	// sleep on the before side does not clear the flag.
	strictAfterOnly landmarkStrictness = iota
	// lenientEitherSide accepts a sleep on EITHER side.
	lenientEitherSide
)

// landmarkTypes is the single declaration of every step type this command
// checks for an adjoining settle sleep: resume, exit_clean, and sigkill —
// the only three step types that can produce the daemon's process_exited
// event for a session id the daemon may RE-ADMIT. resume explicitly
// relaunches the SAME session id (the re-admission event itself);
// exit_clean and sigkill are the two ways the prior process can end before
// it. That pairing is precisely the #1960 mechanism (the 10s
// deletedSessions cooldown) this command exists to surface — and the only
// one its "MISSING" marker claims to detect.
//
// Deliberately excludes — confirmed against the driver source, not assumed:
//   - restart / start_session: step_restart mints a BRAND NEW session id
//     (step_resume is the one that reuses it), never re-admitted, so the
//     deletedSessions cooldown does not apply to them at all — and
//     empirically every restart/start_session occurrence in the corpus
//     carries no adjoining sleep on either side, so there is no sibling
//     precedent to enforce even if it did apply.
//   - reset_session / interrupt: step_interrupt and step_reset_session
//     never kill the process — no process_exited, no re-admission window.
//   - send / wait_turn / sleep / keys / slash / seed_instruction / fork /
//     rewind_fork / mid_turn_send / session / live: not process-lifecycle
//     events at all.
//
// A first cut treating every non-send/wait_turn/sleep step as a landmark
// flagged 109 of 280 occurrences (39%) corpus-wide, including 100% of keys,
// slash, start_session, seed_instruction and restart — none of which relate
// to the #1960 cooldown. This narrower list brings the same scan to 79
// occurrences worth checking at all — see landmarkTypeStrictness for how
// many of those are actually flagged and why the answer differs by type,
// and TestScenarioRecipesCorpusFlagRateStaysLow, which measures the number
// on every run rather than asserting it by hand.
var landmarkTypes = []string{"resume", "exit_clean", "sigkill"}

// landmarkTypeStrictness gives each type in landmarkTypes an EXPLICIT
// strictness, including the lenient ones — isTimingLandmark and
// requiresAfterSleep both read from it rather than each maintaining their
// own switch.
//
// #1969 QA caught a real regression from two independently maintained
// switches: an earlier "either side counts" rule applied to EVERY landmark
// type made a reconstruction of muse's actual pre-fix recipe
// (`exit_clean -> SLEEP 12s -> resume -> send`, no sleep after resume — the
// exact shape #1969 exists to catch) render unflagged, because the 12s
// sleep sits before resume. That is wrong specifically for resume: the
// daemon's #1960 mechanism has TWO distinct windows, and a sleep before
// resume only covers the first —
//
//   - before exit_clean/resume (or between them): lets the deletedSessions
//     cooldown clear before the SAME session id is re-admitted. A sleep
//     anywhere in this window works; QA confirmed corpus-wide (69
//     occurrences) that the step immediately after exit_clean/sigkill is
//     ALWAYS a sleep (45) or end-of-script (24) — never send, keys, slash
//     or wait_turn, zero exceptions — so nothing observable ever races
//     them directly and EITHER side is fine for those two types.
//   - after resume, before the next send: resume IS the re-admission event
//     — the new process just bound to the session id. This is the window
//     the daemon needs to observe an intermediate working state before the
//     whole turn completes and lands on disk, and it is what muse's actual
//     fix added. A sleep sitting BEFORE resume, however large, has already
//     elapsed by the time this window opens and does not cover it — the
//     two are not interchangeable even though they occupy the same "sleep
//     adjoining a landmark" shape in the raw step list. QA independently
//     measured claudecode's committed 1-4_session-resume recording's
//     post-resume `working` dwell at 309 microseconds ("force ready→working
//     on first activity") — passing, but by a margin this thin the six
//     flagged siblings' lack of an after-sleep is a live, not theoretical,
//     risk.
//
// A separate declaration (rather than folding strictness into landmarkTypes
// itself, e.g. as map values there) is deliberate: it is what makes "a
// landmark type added without deciding its strictness" an expressible,
// testable gap — TestLandmarkTypeStrictnessCoversEveryLandmarkType — instead
// of a compile error that can never be demonstrated as a red test. Confirmed
// against a fixture reconstructing muse's pre-fix recipe
// (TestScenarioRecipesFlagsTheReconstructedMusePreFixRecipe) and against the
// real corpus (TestScenarioRecipesCorpusFlagRateStaysLow).
var landmarkTypeStrictness = map[string]landmarkStrictness{
	"resume":     strictAfterOnly,
	"exit_clean": lenientEitherSide,
	"sigkill":    lenientEitherSide,
}

func isTimingLandmark(stepType string) bool {
	return slices.Contains(landmarkTypes, stepType)
}

// requiresAfterSleep reports whether flagging this landmark type considers
// ONLY the after side (ignoring before), vs either side. See
// landmarkTypeStrictness for the reasoning behind each type's entry.
func requiresAfterSleep(landmarkType string) bool {
	return landmarkTypeStrictness[landmarkType] == strictAfterOnly
}

// landmarkTypesMissingStrictness returns, in types order, every entry
// present in types but absent from strictness — the gap
// TestLandmarkTypeStrictnessCoversEveryLandmarkType exists to catch. A
// separate function (rather than the assertion living inline in the test)
// so the same logic backs both the real guard (landmarkTypes vs
// landmarkTypeStrictness) and a permanent synthetic fixture proving the
// logic itself can detect a gap
// (TestLandmarkTypesMissingStrictnessNamesTheGap).
func landmarkTypesMissingStrictness(types []string, strictness map[string]landmarkStrictness) []string {
	var missing []string
	for _, t := range types {
		if _, ok := strictness[t]; !ok {
			missing = append(missing, t)
		}
	}
	return missing
}

// landmarkSleepPair is the sleep (seconds) immediately before and
// immediately after one landmark step occurrence, plus the step type itself
// (flagged's rule differs by type — see requiresAfterSleep). Before/After
// are nil when that occurrence has no sleep step adjoining it on that side.
type landmarkSleepPair struct {
	Type   string
	Before *float64
	After  *float64
	// HasNext is true when a step actually follows this landmark in the
	// script. A landmark with no settle sleep on either side but ALSO
	// nothing after it (the recipe simply ends there) has nothing to race,
	// so it is never flagged regardless of Before/After.
	HasNext bool
}

// flagged reports whether this occurrence should render "<-- MISSING". See
// requiresAfterSleep for why the rule differs between resume and
// exit_clean/sigkill. A landmark with nothing following it is never
// flagged — there is nothing to race.
func (p landmarkSleepPair) flagged() bool {
	if !p.HasNext {
		return false
	}
	if requiresAfterSleep(p.Type) {
		return p.After == nil
	}
	return p.Before == nil && p.After == nil
}

// landmarkSleeps maps each landmark step type occurring in steps to one
// landmarkSleepPair per occurrence, in script order.
func landmarkSleeps(steps []recipeStepView) map[string][]landmarkSleepPair {
	out := map[string][]landmarkSleepPair{}
	for i, s := range steps {
		if !isTimingLandmark(s.Type) {
			continue
		}
		pair := landmarkSleepPair{Type: s.Type}
		if i > 0 && steps[i-1].Type == "sleep" {
			v := steps[i-1].Seconds
			pair.Before = &v
		}
		pair.HasNext = i+1 < len(steps)
		if pair.HasNext && steps[i+1].Type == "sleep" {
			v := steps[i+1].Seconds
			pair.After = &v
		}
		out[s.Type] = append(out[s.Type], pair)
	}
	return out
}

func formatSeconds(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64) + "s"
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + "s"
}

func printScenarioRecipeReport(stdout io.Writer, report scenarioRecipeReport) {
	withScript := 0
	for _, a := range report.Adapters {
		if a.Status == recipeStatusHasScript {
			withScript++
		}
	}
	fmt.Fprintf(stdout, "sibling recipes for %s (%s) -- %d/%d onboarded adapters carry a committed, driven recipe\n\n",
		report.ScenarioName, report.ScenarioID, withScript, len(report.Adapters))

	if withScript == 0 {
		// Distinct from the unknown-scenario error above: this scenario IS
		// real, it simply has nothing committed to compare yet. Printed to
		// stdout with exit 0 — an empty finding, not a failure.
		fmt.Fprintln(stdout, "no adapter has a committed, driven recipe for this scenario yet -- nothing to compare.")
		printOtherAdapterLines(stdout, report.Adapters)
		return
	}

	for _, a := range report.Adapters {
		if a.Status == recipeStatusHasScript {
			printAdapterStepLine(stdout, a)
		}
	}
	fmt.Fprintln(stdout)
	printLandmarkSleeps(stdout, report.Adapters)
	printOtherAdapterLines(stdout, report.Adapters)
}

func printAdapterStepLine(stdout io.Writer, a adapterRecipeView) {
	recorded := "not recorded"
	if a.Recorded {
		recorded = "recorded"
	}
	parts := make([]string, len(a.Steps))
	for i, s := range a.Steps {
		if s.Type == "sleep" {
			parts[i] = "SLEEP " + formatSeconds(s.Seconds)
		} else {
			parts[i] = s.Type
		}
	}
	fmt.Fprintf(stdout, "%-12s [%-12s] %7s timeout  %s\n",
		a.Adapter, recorded, formatSeconds(a.TimeoutSeconds), strings.Join(parts, " -> "))
}

// printLandmarkSleeps is the distilled comparison the issue asks for: for
// every landmark step type any has_script adapter carries, two lines — the
// sleep immediately BEFORE and immediately AFTER it — per adapter, both
// shown as plain facts regardless of whether the "<-- MISSING" marker fires.
// The marker's rule (landmarkSleepPair.flagged / requiresAfterSleep) is NOT
// uniform across types: resume is flagged on an absent AFTER sleep alone
// (before doesn't cover the window that matters — see requiresAfterSleep),
// while exit_clean/sigkill accept either side. Both rows are still printed
// for every type either way, so a reader can always see WHERE a sibling's
// settle time actually sits, not just whether the marker lit up.
func printLandmarkSleeps(stdout io.Writer, adapters []adapterRecipeView) {
	perAdapter := map[string]map[string][]landmarkSleepPair{}
	typesSeen := map[string]bool{}
	var order []string
	for _, a := range adapters {
		if a.Status != recipeStatusHasScript {
			continue
		}
		sa := landmarkSleeps(a.Steps)
		perAdapter[a.Adapter] = sa
		order = append(order, a.Adapter)
		for t := range sa {
			typesSeen[t] = true
		}
	}
	if len(typesSeen) == 0 {
		return
	}
	types := make([]string, 0, len(typesSeen))
	for t := range typesSeen {
		types = append(types, t)
	}
	sort.Strings(types)

	fmt.Fprintln(stdout, "sleep adjoining each landmark step (before -> after; MISSING = resume with no sleep AFTER it, or exit_clean/sigkill with no sleep on either side — always with a step following to race it):")
	for _, t := range types {
		fmt.Fprintf(stdout, "  %-14s before:", t)
		for _, adapter := range order {
			fmt.Fprintf(stdout, "  %s=%s", adapter, formatLandmarkSide(perAdapter[adapter][t], false))
		}
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "  %-14s after: ", t)
		for _, adapter := range order {
			fmt.Fprintf(stdout, "  %s=%s", adapter, formatLandmarkSide(perAdapter[adapter][t], true))
		}
		fmt.Fprintln(stdout)
	}
}

// formatLandmarkSide renders one side (before/after) of every occurrence of
// one landmark type for one adapter. flagMissing is true only for the
// "after" side — see printLandmarkSleeps.
func formatLandmarkSide(pairs []landmarkSleepPair, isAfterRow bool) string {
	if len(pairs) == 0 {
		return "n/a"
	}
	parts := make([]string, len(pairs))
	anyFlagged := false
	for i, p := range pairs {
		v := p.Before
		if isAfterRow {
			v = p.After
		}
		if v == nil {
			parts[i] = "NONE"
		} else {
			parts[i] = formatSeconds(*v)
		}
		// The marker is rendered on the after row only (that's where a
		// reader's eye lands to ask "is there settle time here"), but the
		// underlying flagged() verdict considers BOTH sides — see its doc
		// comment for why "after empty" alone is not enough to flag.
		if isAfterRow && p.flagged() {
			anyFlagged = true
		}
	}
	joined := strings.Join(parts, "+")
	if anyFlagged {
		return joined + " <-- MISSING"
	}
	return joined
}

func printOtherAdapterLines(stdout io.Writer, adapters []adapterRecipeView) {
	var any bool
	for _, a := range adapters {
		if a.Status == recipeStatusHasScript {
			continue
		}
		if !any {
			fmt.Fprintln(stdout, "\nother onboarded adapters (nothing to compare):")
			any = true
		}
		fmt.Fprintf(stdout, "  %-12s %s\n", a.Adapter, recipeStatusNote(a.Status))
	}
}

func recipeStatusNote(status recipeStatus) string {
	switch status {
	case recipeStatusNoCell:
		return "no committed cell for this scenario"
	case recipeStatusNoRecipe:
		return "cell exists, no recipe authored yet (details.recipe absent)"
	case recipeStatusBlocked:
		return "blocked -- recipe.applicable=false, never driven"
	case recipeStatusNoScript:
		return "recipe present but script is empty"
	default:
		return string(status)
	}
}
