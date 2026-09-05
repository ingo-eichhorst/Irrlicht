package desktopdriver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestComposerCatalogPinsRoleLabelAndFullHierarchy(t *testing.T) {
	elements := []helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
		fixtureElement("project", "AXPopUpButton", "workspace", ""),
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("send", "AXButton", "", "Send"),
		fixtureElement("mode", "AXPopUpButton", "Auto", ""),
		fixtureElement("model", "AXPopUpButton", "", "Model: Opus 5"),
	}
	catalog, err := composerCatalog(elements, "/repo/workspace")
	if err != nil {
		t.Fatalf("composerCatalog() error = %v", err)
	}
	for name, selector := range catalog {
		if len(selector.Hierarchy) == 0 || selector.Role == "" {
			t.Fatalf("selector %s is not hierarchy anchored: %+v", name, selector)
		}
	}
}

// Identity addressing trades one failure mode for another: a matcher that
// selects two elements would silently drive whichever came first. It must
// refuse instead. A driver that clicks the wrong control is worse than one
// that stops.
func TestComposerControlsRefuseAnAmbiguousMatch(t *testing.T) {
	elements := []helperElement{
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("prompt-twin", "AXTextArea", "", "Prompt"),
	}
	_, err := composerControls(elements, "/repo/workspace", []string{"prompt"})
	if err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("composerControls() accepted an ambiguous tree: err = %v", err)
	}
}

// A control that is simply not on screen must say so, and must not resolve to
// some other element that happens to be nearby.
func TestComposerControlsRefuseAMissingControl(t *testing.T) {
	_, err := composerControls([]helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
	}, "/repo/workspace", []string{"prompt"})
	if err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("composerControls() error = %v", err)
	}
}

func TestSelectedSessionMenuUsesDynamicOwnedTitle(t *testing.T) {
	element := helperElement{
		Path: selectedSessionMenuPath, Role: "AXPopUpButton",
		Description: "More options for Driver basic turn", Hierarchy: []string{"AXApplication", "AXWindow", "AXGroup"},
	}
	selector, err := selectedSessionMenu([]helperElement{element}, "Driver basic turn")
	if err != nil {
		t.Fatalf("selectedSessionMenu() error = %v", err)
	}
	if selector.Description != element.Description {
		t.Fatalf("selector description = %q", selector.Description)
	}
	if _, err := selectedSessionMenu([]helperElement{element}, "another session"); err == nil {
		t.Fatal("selectedSessionMenu() accepted a different session title")
	}
}

// fixtureElement builds a composer element. Its path is synthetic and
// deliberately arbitrary: the catalog addresses controls by identity, so a
// fixture that derived its paths from the catalog would only prove the catalog
// agrees with itself.
func fixtureElement(name, role, title, description string) helperElement {
	return helperElement{
		Path: []int{len(name), len(role)}, Role: role, Title: title,
		Description: description, Hierarchy: []string{"AXApplication", "AXWindow", "AXGroup", role},
	}
}

// TestComposerCatalogResolvesTheMeasuredDesktopTree is the anti-rot gate for
// the version pin. Every other catalog test builds its elements FROM
// composerPaths, so all of them stayed green while the real composer moved two
// rows under them in 1.46388.2 and `mode` came to point at an "Add" popup.
//
// This one reads a dump measured from the supported build instead. A version
// bump that does not bring a fresh dump, or paths that no longer resolve in the
// one committed, fails here.
func TestComposerCatalogResolvesTheMeasuredDesktopTree(t *testing.T) {
	raw, err := os.ReadFile(composerTreeFixture)
	if err != nil {
		t.Fatalf("no measured Desktop tree for the supported version: %v", err)
	}
	var dump struct {
		MeasuredDesktopVersion    string          `json:"measured_desktop_version"`
		MeasuredClaudeCodeVersion string          `json:"measured_claude_code_version"`
		Elements                  []helperElement `json:"elements"`
	}
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatal(err)
	}
	if dump.MeasuredDesktopVersion != supportedDesktopVersion {
		t.Fatalf("the committed tree was measured on Desktop %q but the catalog supports %q — re-measure before bumping the pin",
			dump.MeasuredDesktopVersion, supportedDesktopVersion)
	}
	if dump.MeasuredClaudeCodeVersion != supportedClaudeCodeVersion {
		t.Fatalf("the committed tree carries Claude Code %q but the catalog supports %q",
			dump.MeasuredClaudeCodeVersion, supportedClaudeCodeVersion)
	}
	if len(dump.Elements) == 0 {
		t.Fatal("the measured tree is empty; this check cannot run, which is a failure")
	}
	catalog, err := composerCatalog(dump.Elements, "/repo/workspace")
	if err != nil {
		t.Fatalf("the catalog does not resolve against the measured Desktop tree: %v", err)
	}
	for _, name := range []string{"environment", "project", "prompt", "send", "model"} {
		if _, ok := catalog[name]; !ok {
			t.Fatalf("catalog has no %s control", name)
		}
	}
}

// The operator-facing prerequisite names the Desktop build a recording needs,
// and the driver refuses any other one. Those two numbers are the same fact
// typed in two places, so this pins them together: prerequisites.md said
// 1.46388.1 for a whole branch after the catalog moved to 1.46388.2, which is
// the drift AGENTS.md warns a hand-copied figure produces.
func TestPrerequisiteNamesTheSupportedDesktopVersion(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..",
		"replaydata", "agents", "claudecode", "prerequisites.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the operator prerequisites: %v", err)
	}
	line := regexp.MustCompile(`(?m)^.*Claude Desktop ([0-9]+(?:\.[0-9]+)*) is installed.*$`)
	match := line.FindSubmatch(raw)
	if match == nil {
		t.Fatalf("no Desktop version prerequisite found in %s — this check cannot run, "+
			"which is a failure, not a pass", path)
	}
	if got := string(match[1]); got != supportedDesktopVersion {
		t.Fatalf("prerequisites.md asks for Claude Desktop %q but the driver refuses "+
			"anything but %q", got, supportedDesktopVersion)
	}
}

// A basic turn drives four controls. Gating it on the two it never touches
// made a state-dependent control decide whether the turn could start at all:
// measured on 1.46388.4, index 6 is an AXPopUpButton "Auto" on a settled
// composer and a plain AXGroup on one just trusted, while the four below all
// resolve.
func TestBasicTurnDoesNotRequireRecipeOnlyControls(t *testing.T) {
	for _, name := range basicTurnControls() {
		if _, known := composerMatchers[name]; !known {
			t.Fatalf("basic turn requires %q, which the catalog does not carry", name)
		}
	}
	for _, recipeOnly := range []string{"mode", "model"} {
		for _, required := range basicTurnControls() {
			if required == recipeOnly {
				t.Fatalf("%q is a recipe control; a basic turn must not be gated on it", recipeOnly)
			}
		}
	}

	// A composer whose mode row has not settled still starts a basic turn.
	elements := []helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
		fixtureElement("project", "AXPopUpButton", "workspace", ""),
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("send", "AXButton", "", "Send"),
		fixtureElement("mode", "AXGroup", "", ""),
	}
	controls, err := composerControls(elements, "/repo/workspace", basicTurnControls())
	if err != nil {
		t.Fatalf("composerControls() error = %v", err)
	}
	if len(controls) != len(basicTurnControls()) {
		t.Fatalf("resolved %d controls, want %d", len(controls), len(basicTurnControls()))
	}
	// The full catalog still refuses that same tree, so nothing was weakened
	// for the callers that do need every control.
	if _, err := composerCatalog(elements, "/repo/workspace"); err == nil {
		t.Fatal("the full catalog accepted an unsettled mode row")
	}
}

// A name the catalog does not carry must refuse, not resolve fewer controls
// than asked for.
func TestComposerControlsRefusesAnUnknownName(t *testing.T) {
	_, err := composerControls(nil, "/repo/workspace", []string{"nonesuch"})
	if err == nil || !strings.Contains(err.Error(), "no \"nonesuch\" control") {
		t.Fatalf("composerControls() error = %v", err)
	}
}

// TestComposerControlsSurviveASiblingShift is the regression gate for the
// defect that cost issue #1887 fifteen live runs.
//
// The catalog used to address every control by its ABSOLUTE child index. One
// extra or missing element in the window chrome shifts every later sibling by
// one, and the lookup then points at a different control or at nothing. The
// driver reported "prompt control is missing at stable path […]" while the
// composer was open and correct on screen.
//
// Measured on the live 1.46388.4 app on 2026-09-05: the prompt text area sat at
// composer index 2,4,1,0,0 where the committed tree has it at 3,5,1,0,0 — the
// same control, two indices moved. Its identity did not move: it stayed the one
// AXTextArea described "Prompt" among 663 elements.
//
// This test shifts the measured tree the same way. It fails against an
// index-addressed catalog and passes against an identity-addressed one.
func TestComposerControlsSurviveASiblingShift(t *testing.T) {
	elements := shiftComposerRow(t, loadMeasuredComposerTree(t))
	controls, err := composerControls(elements, "/repo/workspace", basicTurnControls())
	if err != nil {
		t.Fatalf("the catalog does not survive a one-sibling shift: %v", err)
	}
	for _, name := range basicTurnControls() {
		if controls[name].Role == "" {
			t.Fatalf("control %s did not resolve after the shift", name)
		}
	}
}

// shiftComposerRow moves the composer one sibling earlier, the way a missing
// piece of window chrome does on the live app.
const composerRowIndex = 26

func shiftComposerRow(t *testing.T, elements []helperElement) []helperElement {
	t.Helper()
	shifted := make([]helperElement, 0, len(elements))
	moved := 0
	for _, element := range elements {
		if len(element.Path) > composerRowIndex {
			path := append([]int(nil), element.Path...)
			path[composerRowIndex]--
			element.Path = path
			moved++
		}
		shifted = append(shifted, element)
	}
	if moved == 0 {
		t.Fatal("no element sits on the composer row; this mutation cannot run, which is a failure")
	}
	return shifted
}

func loadMeasuredComposerTree(t *testing.T) []helperElement {
	t.Helper()
	raw, err := os.ReadFile(composerTreeFixture)
	if err != nil {
		t.Fatalf("no measured Desktop tree: %v", err)
	}
	var dump struct {
		Elements []helperElement `json:"elements"`
	}
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatal(err)
	}
	if len(dump.Elements) == 0 {
		t.Fatal("the measured tree is empty; this check cannot run, which is a failure")
	}
	return dump.Elements
}
