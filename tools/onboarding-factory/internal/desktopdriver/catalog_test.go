package desktopdriver

import (
	"encoding/json"
	"os"
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

func TestComposerCatalogRejectsGeneratedPathDrift(t *testing.T) {
	element := fixtureElement("prompt", "AXTextArea", "", "Prompt")
	element.Path = append([]int(nil), element.Path...)
	element.Path[len(element.Path)-1]++
	_, err := composerCatalog([]helperElement{element}, "/repo/workspace")
	if err == nil || !strings.Contains(err.Error(), "stable path") {
		t.Fatalf("composerCatalog() error = %v", err)
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

func fixtureElement(name, role, title, description string) helperElement {
	return helperElement{
		Path: append([]int(nil), composerPaths[name]...), Role: role, Title: title,
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
	for _, name := range []string{"environment", "project", "prompt", "send", "mode", "model"} {
		if _, ok := catalog[name]; !ok {
			t.Fatalf("catalog has no %s control", name)
		}
	}
}
