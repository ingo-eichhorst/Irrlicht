package desktopdriver

// Submit is the one retry loop in the driver whose every attempt posts a real
// mouse click, so its bugs are not slow runs — they are prompts sent twice, and
// runs failed after the prompt was sent once.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func noFront(context.Context) error { return nil }

// inFlightComposerElements is the composer during a turn: the send slot reads
// "Stop", so there is no "Send" to resolve. Measured live on 1.46388.4 on
// 2026-09-06 — and an idle window carries no Stop button anywhere, including
// with another session shown as "Running" in the sidebar.
func inFlightComposerElements(project string) []helperElement {
	return []helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
		fixtureElement("project", "AXPopUpButton", project, ""),
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("stop", "AXButton", "", "Stop"),
		fixtureElement("mode", "AXPopUpButton", "Auto", ""),
		fixtureElement("model", "AXPopUpButton", "", "Model: Opus 5"),
	}
}

// RED-FIRST. This is cells 2-19 and 2-26 on 2026-09-06, reproduced.
//
// Both failed with `resolve the Desktop send button after the prompt was typed:
// Desktop send control requires one AXButton described "Send"; found 0` after
// forty attempts — and the cleanup of both then named the Claude session ID the
// run had supposedly failed to create. The prompt HAD been sent. The click
// landed, Claude Desktop replaced Send with Stop, and the driver spent every
// remaining attempt hunting the button its own click had removed.
//
// What made it retry at all was the error the landed click returned: a tree
// read that failed while the renderer swapped the composer out. The helper no
// longer lets that escape as a bare accessibility error (see
// awaitPostcondition), and this is the second guard — the one that still holds
// with a helper binary built before that fix, and for any future error that
// looks pre-click but is not.
func TestSubmitStopsClickingOnceItsOwnClickStartedTheTurn(t *testing.T) {
	inFlight := false
	inspect := func(context.Context) ([]helperElement, error) {
		if inFlight {
			return inFlightComposerElements("workspace"), nil
		}
		return controlsComposerElements("workspace"), nil
	}
	clicks := 0
	click := func(context.Context, helperSelector, helperPostcondition) error {
		clicks++
		inFlight = true
		return errors.New("helper action_failed: AX error -25202")
	}
	if err := submitPrompt(context.Background(), "/repo/workspace", noFront, inspect, click); err != nil {
		t.Fatalf("submitPrompt() error = %v; the click landed and the turn is running", err)
	}
	if clicks != 1 {
		t.Fatalf("Send was clicked %d times; every click past the first sends the prompt again", clicks)
	}
}

// The other half of the same rule. A Stop button that was already there before
// this function clicked anything belongs to something else, and reading it as
// success would report a prompt as sent that was never typed.
//
// RED-FIRST against a guard that skipped the `clicked` condition: this returned
// nil with nothing clicked.
func TestSubmitNeverTreatsAPreExistingStopAsItsOwnClick(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) {
		return inFlightComposerElements("workspace"), nil
	}
	click := func(context.Context, helperSelector, helperPostcondition) error {
		t.Fatal("no Send control resolved; nothing may be clicked")
		return nil
	}
	// Bounded: with nothing to click this exhausts all forty attempts, and the
	// deadline carries the last resolution failure into the error either way.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := submitPrompt(ctx, "/repo/workspace", noFront, inspect, click)
	if err == nil {
		t.Fatal("submitPrompt() returned nil; it clicked nothing, so it sent nothing")
	}
	if !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("submitPrompt() error = %v, want the unresolved Send button", err)
	}
}

// A settled composer submits on the first attempt and clicks once.
func TestSubmitClicksSendExactlyOnceOnASettledComposer(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) {
		return controlsComposerElements("workspace"), nil
	}
	clicks := 0
	var watched helperPostcondition
	click := func(_ context.Context, selector helperSelector, condition helperPostcondition) error {
		clicks++
		watched = condition
		if selector.Description != "Send" {
			t.Fatalf("clicked %+v, want the Send button", selector)
		}
		return nil
	}
	if err := submitPrompt(context.Background(), "/repo/workspace", noFront, inspect, click); err != nil {
		t.Fatalf("submitPrompt() error = %v", err)
	}
	if clicks != 1 {
		t.Fatalf("Send was clicked %d times, want exactly 1", clicks)
	}
	if watched.Selector.Description != stopButtonDescription || watched.Condition != "exists" {
		t.Fatalf("postcondition = %+v, want Stop to exist", watched)
	}
}

// A click the helper refused before posting anything leaves no turn running, so
// the loop must keep trying rather than reporting a prompt it never sent.
func TestSubmitRetriesAClickTheHelperRefusedBeforePosting(t *testing.T) {
	inspect := func(context.Context) ([]helperElement, error) {
		return controlsComposerElements("workspace"), nil
	}
	clicks := 0
	click := func(context.Context, helperSelector, helperPostcondition) error {
		clicks++
		if clicks == 1 {
			return errors.New("helper stale_control: The current click point does not hit the selected control.")
		}
		return nil
	}
	if err := submitPrompt(context.Background(), "/repo/workspace", noFront, inspect, click); err != nil {
		t.Fatalf("submitPrompt() error = %v", err)
	}
	if clicks != 2 {
		t.Fatalf("Send was clicked %d times; the first was refused before it landed, so it owed a retry", clicks)
	}
}
