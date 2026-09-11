package desktopdriver

// A FRONT-END verdict is the one Desktop result that can expire while nobody
// touches the repository. It says what the app SHOWS — "Desktop exposes no Stop
// control at any point in a turn" (2-20), "Desktop raises no blocking dialog
// that is not a tool permission prompt" (2-27) — and each was measured once,
// against one build. Every other verdict class is anchored in-tree: an observed
// result to its recording, a census result to this package's own grammar, a
// harness result to the shared runner.
//
// Claude Desktop ships on its own schedule, and this driver refuses any build
// but supportedDesktopVersion. So the moment the pin falls behind the installed
// app, two things are true at once and only one of them is visible: the driver
// cannot run a single Desktop cell, and six verdicts still read as present-tense
// facts about an app nothing has looked at since. That is the shape AGENTS.md
// names — absence of a finding and inability to look producing the same output.
// It happened: Desktop moved to 1.49585.0, every Desktop run began refusing at
// the version gate, `of validate` went on printing OK, and the completeness gate
// went on calling all fifty cells terminal.
//
// This test is the coupling that was missing. Bumping the pin now fails until
// every front-end verdict is re-measured against the new build, and it names
// them. It does not decide whether a verdict is still TRUE — only a measurement
// does that — it refuses to let the question go unasked.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/desktopresults"
	"irrlicht/tools/onboarding-factory/internal/matrix"
)

const claudecodeScenariosDir = "../../../../replaydata/agents/claudecode/scenarios"

// frontendVerdict is one committed front-end claim and the build behind it.
type frontendVerdict struct {
	cell     string
	scenario string
	measured string
}

// frontendVerdictsInCell returns one cell's front-end verdicts.
//
// A cell with no result file yields none and no error — cells predate the
// Desktop contract. Anything else that stops the read IS an error: a file that
// exists and cannot be parsed must never read as a cell with nothing to say.
func frontendVerdictsInCell(cellDir, cell string) ([]frontendVerdict, error) {
	path := filepath.Join(cellDir, desktopresults.FileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	doc, err := desktopresults.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	verdicts := make([]frontendVerdict, 0, len(doc.Results))
	for _, result := range doc.Results {
		if matrix.ExecutionProfile(result.ExecutionProfile) != matrix.ProfileDesktopLocal {
			continue
		}
		if !result.CitesFrontendGaps() {
			continue
		}
		verdicts = append(verdicts, frontendVerdict{
			cell:     cell,
			scenario: result.ScenarioID,
			measured: result.MeasuredDesktopVersion,
		})
	}
	return verdicts, nil
}

// loadFrontendVerdicts reads every committed cell and returns the desktop-local
// results that rest on a front-end measurement.
//
// It returns an error rather than an empty slice for anything it could not
// read. A directory it cannot walk and a campaign with no front-end verdicts
// left must not reach the caller as the same value.
func loadFrontendVerdicts(scenariosDir string) ([]frontendVerdict, error) {
	entries, err := os.ReadDir(scenariosDir)
	if err != nil {
		return nil, fmt.Errorf("read the claudecode scenarios directory: %w", err)
	}
	verdicts := make([]frontendVerdict, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		found, err := frontendVerdictsInCell(filepath.Join(scenariosDir, entry.Name()), entry.Name())
		if err != nil {
			return nil, err
		}
		verdicts = append(verdicts, found...)
	}
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].cell < verdicts[j].cell })
	return verdicts, nil
}

// auditFrontendVerdicts returns one line per verdict measured against a build
// this driver no longer supports.
func auditFrontendVerdicts(supported string, verdicts []frontendVerdict) []string {
	stale := make([]string, 0, len(verdicts))
	for _, verdict := range verdicts {
		if verdict.measured == supported {
			continue
		}
		stale = append(stale, fmt.Sprintf(
			"%s (%s): measured on Desktop %q, but the driver supports %q",
			verdict.cell, verdict.scenario, verdict.measured, supported,
		))
	}
	return stale
}

func TestFrontendVerdictsNameTheSupportedDesktopVersion(t *testing.T) {
	verdicts, err := loadFrontendVerdicts(claudecodeScenariosDir)
	if err != nil {
		t.Fatalf("collect the committed front-end verdicts: %v", err)
	}
	// Finding none means the walk broke, not that the campaign is clean: these
	// verdicts are committed data and cells only ever gain them.
	if len(verdicts) == 0 {
		t.Fatalf("no front-end Desktop verdict found under %s — this check cannot run", claudecodeScenariosDir)
	}
	for _, stale := range auditFrontendVerdicts(supportedDesktopVersion, verdicts) {
		t.Errorf(
			"a front-end Desktop verdict is stale: %s\n"+
				"re-measure it against %s and update its reason, or restore the pin",
			stale, supportedDesktopVersion,
		)
	}
}

// TestFrontendVerdictAuditCatchesAPinBump mutates the thing the audit protects.
// The audit is an ADDED check, so it has no state in which it ever ran red on
// its own: what proves it works is that moving the pin turns every committed
// verdict red, by name.
func TestFrontendVerdictAuditCatchesAPinBump(t *testing.T) {
	verdicts, err := loadFrontendVerdicts(claudecodeScenariosDir)
	if err != nil {
		t.Fatalf("collect the committed front-end verdicts: %v", err)
	}
	if got := auditFrontendVerdicts(supportedDesktopVersion, verdicts); len(got) != 0 {
		t.Fatalf("the committed verdicts must be clean before the mutation: %v", got)
	}

	const bumped = "1.49585.0"
	if bumped == supportedDesktopVersion {
		t.Fatalf("the mutation must differ from the pin %q", supportedDesktopVersion)
	}
	stale := auditFrontendVerdicts(bumped, verdicts)
	if len(stale) != len(verdicts) {
		t.Fatalf("a pin bump must flag every front-end verdict; flagged %d of %d", len(stale), len(verdicts))
	}
}

// TestFrontendVerdictAuditRefusesAnUnreadableTree keeps "cannot look" separate
// from "found nothing".
func TestFrontendVerdictAuditRefusesAnUnreadableTree(t *testing.T) {
	if _, err := loadFrontendVerdicts(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a scenarios directory that cannot be read must be an error, not an empty result")
	}
	if verdicts, err := loadFrontendVerdicts(t.TempDir()); err != nil || len(verdicts) != 0 {
		t.Fatalf("an empty tree must read cleanly and carry no verdict; got %v, %v", verdicts, err)
	}
}
