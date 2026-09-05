package desktopdriver

// The static recipe lint #1888 asks for: plan EVERY current claudecode recipe
// against the Desktop driver and pin the verdict.
//
// This is the whole-corpus counterpart to run-cell.sh's per-cell refusal. It
// runs in `go test ./tools/onboarding-factory/...`, so a change to the grammar,
// the control catalog, or any recipe shows up as a diff in the committed census
// rather than as a surprise on a live Desktop run.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

const censusGolden = "testdata/claudecode-desktop-recipe-census.txt"

const censusHeader = `# Every claudecode recipe, planned against the Claude Desktop driver.
#
# Produced by: go test ./tools/onboarding-factory/internal/desktopdriver/ -run TestDesktopRecipeCensus
# Regenerate with: UPDATE_DESKTOP_CENSUS=1 go test ./tools/onboarding-factory/internal/desktopdriver/ -run TestDesktopRecipeCensus -count=1
#
# A "not-runnable" line lists the Desktop controls the recipe would need and the
# driver cannot elicit, deduplicated and sorted. It is a statement about the
# CONTROLS, not about the scenario: the cell stays valid for the cli-local
# profile, which is where it is recorded today.
`

type censusRow struct {
	scenario string
	verdict  string
}

func loadClaudecodeRecipes(t *testing.T) map[string][]byte {
	t.Helper()
	repoRoot := filepath.Join("..", "..", "..", "..")
	cells := shard.LoadAdapterCells(repoRoot, "claudecode")
	if len(cells) == 0 {
		t.Fatalf("no claudecode cells found under %s; this check cannot run, which is a failure, not a pass",
			shard.AgentCellDir(repoRoot, "claudecode", ""))
	}
	scripts := map[string][]byte{}
	for scenario, cell := range cells {
		if cell == nil || len(cell.Details.Recipe) == 0 {
			continue
		}
		var recipe struct {
			Script json.RawMessage `json:"script"`
		}
		if err := json.Unmarshal(cell.Details.Recipe, &recipe); err != nil {
			t.Fatalf("decode %s recipe: %v", scenario, err)
		}
		if len(recipe.Script) == 0 {
			continue
		}
		scripts[scenario] = recipe.Script
	}
	if len(scripts) == 0 {
		t.Fatal("no claudecode recipe carries a script; this check cannot run, which is a failure, not a pass")
	}
	return scripts
}

func planVerdict(script []byte) string {
	steps, err := ParseRecipe(script)
	if err == nil {
		err = Plan(steps)
	}
	if err == nil {
		return "runnable"
	}
	var notRunnable *NotRunnableError
	if !errors.As(err, &notRunnable) {
		return "malformed: " + err.Error()
	}
	seen := map[string]bool{}
	var controls []string
	for _, missing := range notRunnable.Missing {
		if seen[missing.Control] {
			continue
		}
		seen[missing.Control] = true
		controls = append(controls, missing.Control)
	}
	sort.Strings(controls)
	// Comma-separated, not space-separated: a keyboard-key control carries the
	// recipe's quoted tmux key SEQUENCE, which contains spaces.
	return "not-runnable: " + strings.Join(controls, ", ")
}

func TestDesktopRecipeCensusCoversEveryClaudecodeRecipe(t *testing.T) {
	scripts := loadClaudecodeRecipes(t)
	rows := make([]censusRow, 0, len(scripts))
	for scenario, script := range scripts {
		rows = append(rows, censusRow{scenario: scenario, verdict: planVerdict(script)})
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].scenario < rows[right].scenario })

	var builder strings.Builder
	builder.WriteString(censusHeader)
	runnable := 0
	for _, row := range rows {
		if row.verdict == "runnable" {
			runnable++
		}
		fmt.Fprintf(&builder, "%s\t%s\n", row.scenario, row.verdict)
	}
	fmt.Fprintf(&builder, "#\n# %d of %d scripted claudecode recipes are runnable through Claude Desktop.\n",
		runnable, len(rows))
	got := builder.String()

	if os.Getenv("UPDATE_DESKTOP_CENSUS") == "1" {
		if err := os.WriteFile(censusGolden, []byte(got), 0o644); err != nil {
			t.Fatalf("write census golden: %v", err)
		}
		t.Log("census golden regenerated")
		return
	}
	want, err := os.ReadFile(censusGolden)
	if err != nil {
		t.Fatalf("read census golden: %v", err)
	}
	if string(want) != got {
		t.Fatalf("the Desktop recipe census changed.\n--- committed ---\n%s\n--- now ---\n%s\n"+
			"Regenerate with: UPDATE_DESKTOP_CENSUS=1 go test ./tools/onboarding-factory/internal/desktopdriver/ -run TestDesktopRecipeCensus -count=1",
			want, got)
	}
}

// Every verdict in the census must be one this package can act on. A
// "malformed:" row means a recipe the driver cannot even parse, which is a
// refusal with no named control — the one shape that would leave a recording
// operator with nothing to fix.
func TestDesktopRecipeCensusHasNoUnparseableRecipe(t *testing.T) {
	for scenario, script := range loadClaudecodeRecipes(t) {
		if verdict := planVerdict(script); strings.HasPrefix(verdict, "malformed:") {
			t.Errorf("%s: %s", scenario, verdict)
		}
	}
}

// The refusal has to be actionable: every not-runnable recipe names at least one
// control, and every control it names is one the driver documents as missing (or
// a keyboard key / recipe field, which carry their own suffix).
func TestEveryDesktopRefusalNamesAKnownControl(t *testing.T) {
	documented := map[string]bool{}
	for _, pair := range MissingControls() {
		_, control, _ := strings.Cut(pair, ":")
		documented[control] = true
	}
	checked := 0
	for scenario, script := range loadClaudecodeRecipes(t) {
		verdict := planVerdict(script)
		if !strings.HasPrefix(verdict, "not-runnable: ") {
			continue
		}
		controls := strings.Split(strings.TrimPrefix(verdict, "not-runnable: "), ", ")
		if len(controls) == 0 {
			t.Errorf("%s was refused without naming a control", scenario)
			continue
		}
		for _, control := range controls {
			checked++
			if documented[control] ||
				strings.HasPrefix(control, controlKeyboard+"-key:") ||
				strings.HasPrefix(control, "recipe-field:") {
				continue
			}
			t.Errorf("%s names undocumented control %q", scenario, control)
		}
	}
	if checked == 0 {
		t.Fatal("no refusal was examined; this check cannot run, which is a failure, not a pass")
	}
}
