package desktopdriver

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// The catalog is pinned to ONE Desktop build (live.go compares for exact
// equality). The pin is now a CONSERVATISM, not the mechanism: controls are
// addressed by identity (see composerMatchers), so a release that only moves
// elements around no longer breaks them. The pin still guards the case a
// version comparison cannot: a release that RENAMES a control, or gives two
// controls the same label.
//
// History, because it is the reason the design changed. The paths were
// positional. Measured on 1.46388.2, the composer row had shifted by two
// against 1.46388.1: `prompt` and `send` left index 7 for 5, `mode` left 8 for
// 6, `model` left 12 for 10 — and index 8 held an "Add" popup, so a bare
// version bump would have driven the wrong control. 1.46388.4 then measured
// the same as .2. Then, on 2026-09-05, the SAME build produced a THIRD layout
// on a live window, and the positional catalog failed against a composer that
// was open and correct. Position is a property of everything preceding a
// control, so it was never the control's to pin.
//
// testdata/composer-<version>.json holds the measured tree.
// TestComposerCatalogResolvesTheMeasuredDesktopTree proves the matchers
// resolve against it, and TestComposerControlsSurviveASiblingShift proves they
// survive the drift that broke the positional ones.
const supportedDesktopVersion = "1.46388.4"
const supportedClaudeCodeVersion = "2.1.260"

// composerTreeFixture names the measured dump the matchers are proven against.
// It must stay in step with supportedDesktopVersion.
const composerTreeFixture = "testdata/composer-1.46388.4.json"

// composerMatchers addresses each control by IDENTITY — the role plus the
// label the app gives it — and never by its position in the accessibility
// tree.
//
// Position was the original design and it was wrong. An absolute child index
// is not a property of the control; it is a property of everything that
// happens to precede it. One extra or missing element in the window chrome
// shifts every later sibling by one, and the lookup then lands on a different
// control or on nothing at all. Measured on the live 1.46388.4 app on
// 2026-09-05, the prompt text area sat at composer index 2,4,1,0,0 where the
// committed tree of the SAME build has it at 3,5,1,0,0. Its identity had not
// moved: it was still the only AXTextArea described "Prompt" among 663
// elements. Fifteen live runs of #1887 reported "prompt control is missing"
// against an open, correct composer because of that difference.
//
// Every matcher must select exactly ONE element. Ambiguity is an error, not a
// silent first-match: a driver that clicks the wrong control is worse than one
// that refuses.
type composerMatcher struct {
	role    string
	wants   func(project string) string
	matches func(element helperElement, project string) bool
}

var composerMatchers = map[string]composerMatcher{
	"environment": {
		role:  "AXPopUpButton",
		wants: func(string) string { return `titled "Local"` },
		matches: func(element helperElement, _ string) bool {
			return element.Role == "AXPopUpButton" && element.Title == "Local"
		},
	},
	"project": {
		role:  "AXPopUpButton",
		wants: func(project string) string { return fmt.Sprintf("titled %q", project) },
		matches: func(element helperElement, project string) bool {
			return element.Role == "AXPopUpButton" && element.Title == project
		},
	},
	"prompt": {
		role:  "AXTextArea",
		wants: func(string) string { return `described "Prompt"` },
		matches: func(element helperElement, _ string) bool {
			return element.Role == "AXTextArea" && element.Description == "Prompt"
		},
	},
	// The send slot is state dependent: the SAME slot reads "Stop" while a turn
	// runs (measured live on 1.46388.4). Resolve it when the driver is about to
	// click it, never as a precondition for a composer that has not been typed
	// into yet — see basicTurnControls.
	"send": {
		role:  "AXButton",
		wants: func(string) string { return `described "Send"` },
		matches: func(element helperElement, _ string) bool {
			return element.Role == "AXButton" && element.Description == "Send"
		},
	},
	"model": {
		role:  "AXPopUpButton",
		wants: func(string) string { return `described "Model: …"` },
		matches: func(element helperElement, _ string) bool {
			return element.Role == "AXPopUpButton" && strings.HasPrefix(element.Description, "Model: ")
		},
	},
}

// `mode` is deliberately absent. Its label IS its value ("Auto" and the other
// mode names), so no fixed label identifies it, and the obvious widening —
// "any titled AXPopUpButton" — also matches `environment` and `project`. A
// matcher that can select the wrong control does not belong here. Nothing in a
// basic turn drives mode; the recipe work (#1888) needs it and must derive a
// real identity for it, from a measurement of the mode menu's own contents.

var selectedSessionMenuPath = []int{0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 5, 4, 0, 6, 0, 1, 0}

// basicTurnControls are the controls that must exist BEFORE the driver types.
//
// `send` is not among them, and that is the point. The send button shares its
// slot with a "Stop" button and, measured live on 1.46388.4, an empty composer
// carries neither label the driver can rely on. A basic turn therefore waits
// for the controls it needs to type, types, and resolves `send` at click time
// from a fresh reading — see LiveRuntime.Submit.
//
// `mode` and `model` are not here either. Nothing in a basic turn touches them.
// Gating a turn on a control it never uses turned into "the composer never
// appeared" once already.
func basicTurnControls() []string {
	return []string{"environment", "project", "prompt"}
}

// composerCatalog resolves every identity-addressable control. Callers that
// need a subset pass it to composerControls; a caller that asks for more than
// it drives couples itself to controls it does not use.
func composerCatalog(elements []helperElement, workspace string) (map[string]helperSelector, error) {
	names := make([]string, 0, len(composerMatchers))
	for name := range composerMatchers {
		names = append(names, name)
	}
	return composerControls(elements, workspace, names)
}

// composerControls resolves exactly the named controls, and refuses a name the
// catalog does not carry rather than silently resolving fewer than asked.
func composerControls(
	elements []helperElement,
	workspace string,
	names []string,
) (map[string]helperSelector, error) {
	expectedProject := filepath.Base(filepath.Clean(workspace))
	controls := make(map[string]helperSelector, len(names))
	for _, name := range names {
		matcher, known := composerMatchers[name]
		if !known {
			return nil, fmt.Errorf("Desktop control catalog has no %q control", name)
		}
		var found []helperElement
		for _, element := range elements {
			if matcher.matches(element, expectedProject) {
				found = append(found, element)
			}
		}
		if len(found) != 1 {
			return nil, fmt.Errorf(
				"Desktop %s control requires one %s %s; found %d. Visible %s controls: %s",
				name,
				matcher.role,
				matcher.wants(expectedProject),
				len(found),
				matcher.role,
				describeCandidates(elements, matcher.role),
			)
		}
		element := found[0]
		if len(element.Hierarchy) == 0 {
			return nil, fmt.Errorf("Desktop %s control has no role hierarchy", name)
		}
		controls[name] = selectorFor(element)
	}
	return controls, nil
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

// describeCandidates lists what the tree DOES carry for a role, so a failed
// lookup says what was on screen instead of only what was not. The composer
// deadline message is the only thing an operator sees after a failed run; when
// it named a path and nothing else, fifteen runs of #1887 read as "the composer
// never opened" while the composer was open and correct.
const maxDescribedCandidates = 8

func describeCandidates(elements []helperElement, role string) string {
	var labels []string
	for _, element := range elements {
		if element.Role != role {
			continue
		}
		if len(labels) == maxDescribedCandidates {
			labels = append(labels, "…")
			break
		}
		labels = append(labels, describeLabel(element))
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

func describeLabel(element helperElement) string {
	switch {
	case element.Title != "":
		return fmt.Sprintf("titled %q", element.Title)
	case element.Description != "":
		return fmt.Sprintf("described %q", element.Description)
	default:
		return "unlabelled"
	}
}
