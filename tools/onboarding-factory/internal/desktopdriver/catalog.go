package desktopdriver

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// The catalog is pinned to ONE Desktop build (live.go compares for exact
// equality) because these are positional accessibility paths, and Claude
// Desktop moves them between releases. Measured on 1.46388.2, the composer row
// shifted by two against 1.46388.1: `prompt` and `send` left index 7 for 5,
// `mode` left 8 for 6, and `model` left 12 for 10. Index 8 now holds an "Add"
// popup and index 12 the usage popup, so a bare version bump without
// re-deriving the paths would have driven the wrong controls.
//
// A version bump therefore needs a fresh `inspect` dump, not just a new
// constant. testdata/composer-<version>.json holds the measured tree, and
// TestComposerCatalogResolvesTheMeasuredDesktopTree fails when the paths here
// no longer resolve against it.
const supportedDesktopVersion = "1.46388.2"
const supportedClaudeCodeVersion = "2.1.260"

// composerTreeFixture names the measured dump the paths below were derived
// from. It must stay in step with supportedDesktopVersion.
const composerTreeFixture = "testdata/composer-1.46388.2.json"

var composerPaths = map[string][]int{
	"environment": {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 1},
	"project":     {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 2},
	"prompt":      {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 5, 1, 0, 0},
	"send":        {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 5, 1, 1, 0},
	"mode":        {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 6},
	"model":       {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 10},
}

var selectedSessionMenuPath = []int{0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 5, 4, 0, 6, 0, 1, 0}

func composerCatalog(elements []helperElement, workspace string) (map[string]helperSelector, error) {
	expectedProject := filepath.Base(filepath.Clean(workspace))
	controls := make(map[string]helperSelector, len(composerPaths))
	for name, path := range composerPaths {
		element, ok := elementAtPath(elements, path)
		if !ok {
			return nil, fmt.Errorf("Desktop %s control is missing at stable path %v", name, path)
		}
		if err := validateComposerElement(name, element, expectedProject); err != nil {
			return nil, err
		}
		controls[name] = selectorFor(element)
	}
	return controls, nil
}

func validateComposerElement(name string, element helperElement, project string) error {
	valid := false
	switch name {
	case "environment":
		valid = element.Role == "AXPopUpButton" && element.Title == "Local"
	case "project":
		valid = element.Role == "AXPopUpButton" && element.Title == project
	case "prompt":
		valid = element.Role == "AXTextArea" && element.Description == "Prompt"
	case "send":
		valid = element.Role == "AXButton" && element.Description == "Send"
	case "mode":
		valid = element.Role == "AXPopUpButton" && element.Title != ""
	case "model":
		valid = element.Role == "AXPopUpButton" && strings.HasPrefix(element.Description, "Model: ")
	}
	if !valid {
		return fmt.Errorf(
			"Desktop %s control at stable path %v has role=%q title=%q description=%q",
			name,
			element.Path,
			element.Role,
			element.Title,
			element.Description,
		)
	}
	if len(element.Hierarchy) == 0 {
		return fmt.Errorf("Desktop %s control has no role hierarchy", name)
	}
	return nil
}

func selectedSessionMenu(elements []helperElement, expectedTitle string) (helperSelector, error) {
	element, ok := elementAtPath(elements, selectedSessionMenuPath)
	if !ok {
		return helperSelector{}, fmt.Errorf("selected-session menu is missing at stable path %v", selectedSessionMenuPath)
	}
	expectedDescription := "More options for " + expectedTitle
	if element.Role != "AXPopUpButton" || element.Description != expectedDescription {
		return helperSelector{}, fmt.Errorf(
			"selected-session menu does not identify the owned session: role=%q description=%q, want %q",
			element.Role,
			element.Description,
			expectedDescription,
		)
	}
	return selectorFor(element), nil
}

// Claude Desktop refuses to render a composer for a folder it has not seen
// before, and shows a modal trust prompt instead. The driver's staging
// workspace is new on every run, so this prompt stands between every
// desktop-local run and its first turn.
const (
	trustConfirmTitle = "Trust workspace"
	trustCancelTitle  = "Cancel"
)

// trustPromptButton returns the confirm button of a Claude Desktop workspace
// trust prompt, and false when no prompt is on screen.
//
// The prompt is identified by its exact two-button shape, and BOTH buttons must
// be unique. That is deliberately strict: the accessibility tree does not carry
// the folder the prompt is asking about — the helper reads role, title and
// description, and the path lives in an AXStaticText value it does not emit —
// so the driver cannot confirm from the prompt itself WHICH workspace it would
// trust. Shape plus timing is the whole guarantee: this is only consulted while
// waiting for the composer the driver just opened for its own scratch
// workspace. Anything but that exact shape is left alone.
func trustPromptButton(elements []helperElement) (helperSelector, bool) {
	confirm, confirmErr := uniqueElement(elements, func(element helperElement) bool {
		return element.Role == "AXButton" && element.Title == trustConfirmTitle
	}, "Desktop trust prompt confirm")
	if confirmErr != nil {
		return helperSelector{}, false
	}
	if _, cancelErr := uniqueElement(elements, func(element helperElement) bool {
		return element.Role == "AXButton" && element.Title == trustCancelTitle
	}, "Desktop trust prompt cancel"); cancelErr != nil {
		return helperSelector{}, false
	}
	return selectorFor(confirm), true
}

func uniqueElement(elements []helperElement, matches func(helperElement) bool, name string) (helperElement, error) {
	var found []helperElement
	for _, element := range elements {
		if matches(element) {
			found = append(found, element)
		}
	}
	if len(found) != 1 {
		return helperElement{}, fmt.Errorf("%s requires one visible control; found %d", name, len(found))
	}
	return found[0], nil
}

func elementAtPath(elements []helperElement, path []int) (helperElement, bool) {
	for _, element := range elements {
		if slices.Equal(element.Path, path) {
			return element, true
		}
	}
	return helperElement{}, false
}
