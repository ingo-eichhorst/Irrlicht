package desktopdriver

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

const supportedDesktopVersion = "1.46388.1"
const supportedClaudeCodeVersion = "2.1.260"

var composerPaths = map[string][]int{
	"environment": {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 1},
	"project":     {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 2},
	"prompt":      {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 7, 1, 0, 0},
	"send":        {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 7, 1, 1, 0},
	"mode":        {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 8},
	"model":       {0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 12},
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
