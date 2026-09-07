package desktopdriver

// Answering Claude Desktop's blocking permission dialog.

import (
	"context"
	"strings"
	"testing"
)

// permissionDialogElements is the tree cell 2-26 was refused against, measured
// on 1.46388.4 on 2026-09-07: a turn in flight behind a dialog whose choices
// are plain AXButtons carrying their own keyboard numbers.
func permissionDialogElements() []helperElement {
	return []helperElement{
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("stop", "AXButton", "", "Stop"),
		fixtureElement("question", "AXButton", "Allow Claude to write gate.txt?", ""),
		fixtureElement("deny", "AXButton", "Deny 1", ""),
		fixtureElement("allow", "AXButton", "Allow once 2", ""),
	}
}

// RED-FIRST. Cells 2-26 and 2-27 both failed here on 2026-09-06 and again on
// 2026-09-07:
//
//	press Enter: … Desktop send control requires one AXButton described "Send";
//	found 0
//
// Enter was modelled only as "submit the composer", whose postcondition is Send
// giving way to Stop. A turn in flight behind a dialog shows Stop, so the
// control that model needs is not there — and the composer was never what the
// keystroke was aimed at.
func TestEnterAnswersAPermissionDialogByApproving(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) { return permissionDialogElements(), nil }
	var clicked helperSelector
	var watched helperPostcondition
	click := func(_ context.Context, selector helperSelector, condition helperPostcondition) error {
		clicked = selector
		watched = condition
		return nil
	}
	keyboard := func(context.Context, helperSelector, uint16, []string, helperPostcondition) error {
		t.Fatal("a dialog was on screen; the keystroke must not go to the composer behind it")
		return nil
	}
	if err := pressKey(context.Background(), "Enter", "/repo/workspace", noFront, inspect, keyboard, click); err != nil {
		t.Fatalf("pressKey() error = %v", err)
	}
	if clicked.Title != "Allow once 2" {
		t.Fatalf("answered with %q, want the narrowest Allow choice", clicked.Title)
	}
	if watched.Condition != "absent" || watched.Selector.Title != "Allow once 2" {
		t.Fatalf("postcondition = %+v, want the answered choice to go away", watched)
	}
}

// LOCK (passes by construction): with no dialog on screen, Enter still submits.
func TestEnterStillSubmitsTheComposerWithNoDialogOnScreen(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) {
		return controlsComposerElements("workspace"), nil
	}
	click := func(context.Context, helperSelector, helperPostcondition) error {
		t.Fatal("no dialog is on screen; nothing may be clicked")
		return nil
	}
	pressed := false
	keyboard := func(_ context.Context, target helperSelector, _ uint16, _ []string, after helperPostcondition) error {
		pressed = true
		if target.Description != "Prompt" {
			t.Errorf("keystroke went to %+v, want the prompt area", target)
		}
		if after.Selector.Description != stopButtonDescription {
			t.Errorf("postcondition = %+v, want Stop to appear", after)
		}
		return nil
	}
	if err := pressKey(context.Background(), "Enter", "/repo/workspace", noFront, inspect, keyboard, click); err != nil {
		t.Fatalf("pressKey() error = %v", err)
	}
	if !pressed {
		t.Fatal("no keystroke was sent; this check proved nothing")
	}
}

// A dialog the driver cannot answer safely is a refusal, not a guess. Both
// scenarios that answer one assert the turn RESUMES afterwards, which only
// approval produces — so there is no fallback to Deny.
func TestPermissionApproveRefusesRatherThanGuessing(t *testing.T) {
	tests := map[string]struct {
		elements []helperElement
		wants    string
	}{
		"no dialog at all": {
			elements: controlsComposerElements("workspace"),
			wants:    "no Claude Desktop permission dialog",
		},
		"only a deny choice": {
			elements: []helperElement{fixtureElement("deny", "AXButton", "Deny 1", "")},
			wants:    "offers no Allow choice",
		},
		"two grants and neither is Allow once": {
			elements: []helperElement{
				fixtureElement("deny", "AXButton", "Deny 1", ""),
				fixtureElement("session", "AXButton", "Allow for this session 2", ""),
				fixtureElement("always", "AXButton", "Allow always 3", ""),
			},
			wants: "refusing rather than guessing",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := permissionApproveOption(test.elements)
			if err == nil {
				t.Fatal("permissionApproveOption() returned nil error")
			}
			if !strings.Contains(err.Error(), test.wants) {
				t.Fatalf("error = %v, want it to say %q", err, test.wants)
			}
		})
	}
}

// The question button carries the tool call, which differs in every scenario.
// Matching it as a choice would answer the dialog by clicking its own heading.
func TestTheDialogQuestionIsNotMistakenForAChoice(t *testing.T) {
	options := permissionDialogOptions(permissionDialogElements())
	if len(options) != 2 {
		t.Fatalf("matched %d options, want exactly Deny 1 and Allow once 2", len(options))
	}
	for _, option := range options {
		if strings.HasSuffix(option.Title, "?") {
			t.Errorf("the question %q was matched as a choice", option.Title)
		}
	}
}
