package desktopdriver

// Interrupt, PressKey, SelectMode and SelectModel all resolve their controls
// FRESH on every call — WaitComposer only ever caches basicTurnControls
// (environment, project, prompt; see catalog.go) — so these exercise that
// resolution against a fake accessibility tree, the same seam
// waitForComposerControls already uses for the composer wait.

import (
	"context"
	"strings"
	"testing"
)

// controlsComposerElements builds a settled composer: environment, project,
// prompt, send, mode ("Auto"), and model ("Model: Opus 5").
func controlsComposerElements(project string) []helperElement {
	return []helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
		fixtureElement("project", "AXPopUpButton", project, ""),
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("send", "AXButton", "", "Send"),
		fixtureElement("mode", "AXPopUpButton", "Auto", ""),
		fixtureElement("model", "AXPopUpButton", "", "Model: Opus 5"),
	}
}

func TestInterruptClicksStopAndWaitsForSendToExist(t *testing.T) {
	elements := controlsComposerElements("workspace")
	inspect := func(context.Context) ([]helperElement, error) { return elements, nil }
	var clicked helperSelector
	var postcondition helperPostcondition
	click := func(_ context.Context, selector helperSelector, condition helperPostcondition) error {
		clicked = selector
		postcondition = condition
		return nil
	}
	if err := interruptTurn(context.Background(), "/repo/workspace", inspect, click); err != nil {
		t.Fatalf("interruptTurn() error = %v", err)
	}
	if clicked.Role != "AXButton" || clicked.Description != "Stop" {
		t.Fatalf("clicked = %+v, want the Stop button", clicked)
	}
	if postcondition.Selector.Description != "Send" || postcondition.Condition != "exists" {
		t.Fatalf("postcondition = %+v, want Send to exist", postcondition)
	}
}

// A composer nothing has been typed into carries no resolvable `send` slot
// (see catalog.go's basicTurnControls comment) — Interrupt must refuse rather
// than click something else.
func TestInterruptFailsLoudlyWhenSendCannotBeResolved(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) { return nil, nil }
	click := func(context.Context, helperSelector, helperPostcondition) error {
		t.Fatal("no send control resolved; the wait must not click anything")
		return nil
	}
	err := interruptTurn(context.Background(), "/repo/workspace", inspect, click)
	if err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("interruptTurn() error = %v", err)
	}
}

// Escape cancels an in-flight turn (Stop gives way to Send); Enter submits
// (Send gives way to Stop). Both press the SAME prompt control.
func TestPressKeyEscapeWaitsForSendAndEnterWaitsForStop(t *testing.T) {
	elements := controlsComposerElements("workspace")
	inspect := func(context.Context) ([]helperElement, error) { return elements, nil }
	tests := []struct {
		key       string
		wantAfter string
	}{
		{"Escape", "Send"},
		{"Enter", "Stop"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			var pressed helperSelector
			var keyCode uint16
			var postcondition helperPostcondition
			keyboard := func(_ context.Context, selector helperSelector, code uint16, _ []string, condition helperPostcondition) error {
				pressed = selector
				keyCode = code
				postcondition = condition
				return nil
			}
			if err := pressKey(context.Background(), test.key, "/repo/workspace", inspect, keyboard); err != nil {
				t.Fatalf("pressKey(%q) error = %v", test.key, err)
			}
			if pressed.Description != "Prompt" {
				t.Fatalf("pressed selector = %+v, want the prompt control", pressed)
			}
			if keyCode != desktopKeys[test.key].code {
				t.Fatalf("key code = %d, want %d", keyCode, desktopKeys[test.key].code)
			}
			if postcondition.Selector.Description != test.wantAfter {
				t.Fatalf("postcondition targets %+v, want %s", postcondition.Selector, test.wantAfter)
			}
		})
	}
}

// A key Plan already refuses (see desktopKeys) must be refused here too,
// before either closure runs: this is the last place that can still catch it.
func TestPressKeyRefusesAnUnsupportedKeyWithoutTouchingDesktop(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) {
		t.Fatal("an unsupported key must be refused before any control is resolved")
		return nil, nil
	}
	keyboard := func(context.Context, helperSelector, uint16, []string, helperPostcondition) error {
		t.Fatal("an unsupported key must never reach the keyboard helper")
		return nil
	}
	err := pressKey(context.Background(), "F13", "/repo/workspace", inspect, keyboard)
	if err == nil || !strings.Contains(err.Error(), "no observable Desktop postcondition") {
		t.Fatalf("pressKey() error = %v", err)
	}
}

// SelectMode opens the popup, resolves the ONE matching menu item, clicks it,
// and then re-reads the popup to confirm it now reports the new entry — mode
// announces its entry in the popup's TITLE (see modeReportsEntry).
func TestSelectModeOpensTheMenuSelectsTheEntryAndConfirmsTheReport(t *testing.T) {
	workspace := "/repo/workspace"
	closed := controlsComposerElements("workspace")
	menu := []helperElement{
		{Role: "AXMenuItem", Title: "Auto", Hierarchy: []string{"AXApplication", "AXWindow", "AXMenu", "AXMenuItem"}},
		{Role: "AXMenuItem", Title: "Plan", Hierarchy: []string{"AXApplication", "AXWindow", "AXMenu", "AXMenuItem"}},
	}
	updated := make([]helperElement, len(closed))
	copy(updated, closed)
	for index, element := range updated {
		if element.Role == "AXPopUpButton" && element.Description == "" &&
			element.Title != "Local" && element.Title != "workspace" {
			updated[index].Title = "Plan"
		}
	}
	inspectCalls := 0
	inspect := func(context.Context) ([]helperElement, error) {
		inspectCalls++
		switch inspectCalls {
		case 1:
			return closed, nil
		case 2:
			return menu, nil
		default:
			return updated, nil
		}
	}
	var clicks []helperSelector
	click := func(_ context.Context, selector helperSelector, _ helperPostcondition) error {
		clicks = append(clicks, selector)
		return nil
	}
	if err := selectFromPopup(context.Background(), workspace, controlMode, "Plan", modeReportsEntry, inspect, click); err != nil {
		t.Fatalf("selectFromPopup() error = %v", err)
	}
	if len(clicks) != 2 {
		t.Fatalf("clicks = %+v, want exactly 2 (open the popup, then the entry)", clicks)
	}
	if clicks[0].Role != "AXPopUpButton" || clicks[0].Title != "Auto" {
		t.Fatalf("first click = %+v, want the mode popup", clicks[0])
	}
	if clicks[1].Role != "AXMenuItem" || clicks[1].Title != "Plan" {
		t.Fatalf("second click = %+v, want the Plan menu entry", clicks[1])
	}
}

// SelectModel's popup announces its entry in the DESCRIPTION ("Model: …"),
// not the title — modelReportsEntry, not modeReportsEntry — so this proves
// the two are not accidentally interchangeable.
func TestSelectModelConfirmsFromDescriptionNotTitle(t *testing.T) {
	workspace := "/repo/workspace"
	closed := controlsComposerElements("workspace")
	menu := []helperElement{
		{Role: "AXMenuItem", Title: "Opus 5", Hierarchy: []string{"AXApplication", "AXWindow", "AXMenu", "AXMenuItem"}},
		{Role: "AXMenuItem", Title: "Sonnet 5", Hierarchy: []string{"AXApplication", "AXWindow", "AXMenu", "AXMenuItem"}},
	}
	updated := make([]helperElement, len(closed))
	copy(updated, closed)
	for index, element := range updated {
		if element.Role == "AXPopUpButton" && strings.HasPrefix(element.Description, "Model: ") {
			updated[index].Description = "Model: Sonnet 5"
		}
	}
	inspectCalls := 0
	inspect := func(context.Context) ([]helperElement, error) {
		inspectCalls++
		switch inspectCalls {
		case 1:
			return closed, nil
		case 2:
			return menu, nil
		default:
			return updated, nil
		}
	}
	click := func(context.Context, helperSelector, helperPostcondition) error { return nil }
	if err := selectFromPopup(context.Background(), workspace, controlModel, "Sonnet 5", modelReportsEntry, inspect, click); err != nil {
		t.Fatalf("selectFromPopup() error = %v", err)
	}
}
