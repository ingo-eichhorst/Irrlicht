package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
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

// isTimingLandmark reports whether a step type is one this command checks
// for an adjoining settle sleep: resume, exit_clean, and sigkill — the only
// three step types that can produce the daemon's process_exited event for a
// session id the daemon may RE-ADMIT. resume explicitly relaunches the SAME
// session id (the re-admission event itself); exit_clean and sigkill are the
// two ways the prior process can end before it. That pairing is precisely
// the #1960 mechanism (the 10s deletedSessions cooldown) this command exists
// to surface — and the only one its "MISSING" marker claims to detect.
//
// Deliberately excludes:
//   - restart / start_session: both mint a BRAND NEW session id, never
//     re-admitted, so the deletedSessions cooldown does not apply to them at
//     all — and empirically every restart/start_session occurrence in the
//     corpus carries no adjoining sleep on either side, so there is no
//     sibling precedent to enforce even if it did apply.
//   - reset_session / interrupt: neither ends the underlying process — no
//     process_exited, no re-admission window.
//   - send / wait_turn / sleep / keys / slash / seed_instruction / fork /
//     rewind_fork / mid_turn_send / session / live: not process-lifecycle
//     events at all.
//
// A first cut treating every non-send/wait_turn/sleep step as a landmark
// flagged 109 of 280 occurrences (39%) corpus-wide, including 100% of keys,
// slash, start_session, seed_instruction and restart — none of which relate
// to the #1960 cooldown. This narrower list, combined with
// landmarkSleepPair.flagged's any-side/has-next gate, brings the SAME scan
// to 0 of 79 — see TestScenarioRecipesCorpusFlagRateStaysLow, which measures
// this on every run rather than asserting the number by hand.
func isTimingLandmark(stepType string) bool {
	switch stepType {
	case "resume", "exit_clean", "sigkill":
		return true
	default:
		return false
	}
}

// landmarkSleepPair is the sleep (seconds) immediately before and
// immediately after one landmark step occurrence. Either is nil when that
// occurrence has no sleep step adjoining it on that side.
//
// Both sides matter, not just "after": running this against the real
// replaydata corpus (see #1969's report) showed most siblings for
// 1-4_session-resume split `resume` into two script steps — `exit_clean`,
// then a sleep, then `resume` — and put their settle sleep BEFORE resume,
// not after. Reporting "after" alone flagged those as looking identical to
// muse's actual pre-fix bug (a genuinely absent sleep on EITHER side of an
// atomic resume). They are not the same shape, and only showing "before" and
// "after" side by side — rather than picking one or silently merging them —
// lets a reader tell the two apart instead of this command asserting which
// one is correct.
type landmarkSleepPair struct {
	Before *float64
	After  *float64
	// HasNext is true when a step actually follows this landmark in the
	// script. A landmark with no settle sleep on either side but ALSO
	// nothing after it (the recipe simply ends there) has nothing to race,
	// so it is never flagged regardless of Before/After.
	HasNext bool
}

// flagged reports whether this occurrence should render "<-- MISSING": no
// settle sleep on EITHER side, AND a step actually follows to race it.
//
// Only "no sleep at all nearby" is flagged — not "no sleep on this
// particular side" — because the corpus's dominant real shape for `resume`
// is `exit_clean -> sleep -> resume -> send` (the sleep sits BEFORE resume,
// clearing the daemon's deletedSessions cooldown ahead of the relaunch), and
// that is not a bug: six committed sibling adapters agree on it. Flagging on
// "after" alone made those six indistinguishable from muse's actual #1960
// bug. See TestScenarioRecipesCorpusFlagRateStaysLow for the measured
// before/after corpus-wide rate this predicate change produced.
func (p landmarkSleepPair) flagged() bool {
	return p.Before == nil && p.After == nil && p.HasNext
}

// landmarkSleeps maps each landmark step type occurring in steps to one
// landmarkSleepPair per occurrence, in script order.
func landmarkSleeps(steps []recipeStepView) map[string][]landmarkSleepPair {
	out := map[string][]landmarkSleepPair{}
	for i, s := range steps {
		if !isTimingLandmark(s.Type) {
			continue
		}
		var pair landmarkSleepPair
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
// shown unflagged as plain facts. "<-- MISSING" is reserved (via
// landmarkSleepPair.flagged, rendered on the after row) for an occurrence
// with NO settle sleep on EITHER side and a step following it to race — see
// that method's doc comment for why "after empty" alone is not enough:
// the corpus's dominant real shape for `resume` puts the settle sleep
// BEFORE it (`exit_clean -> sleep -> resume -> send`), which six committed
// sibling adapters agree on and is not the #1960 bug. Both rows are printed
// regardless of whether the flag fires, so a reader can always see WHERE a
// sibling's settle time actually sits, not just whether the marker lit up.
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

	fmt.Fprintln(stdout, "sleep adjoining each landmark step (before -> after; MISSING = no settle sleep on EITHER side, with a step following to race it — the #1960 failure mode):")
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
