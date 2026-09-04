package desktopdriver

import (
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
