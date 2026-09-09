package desktopdriver

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	// The send slot was believed to be state dependent — the same slot reading
	// "Stop" while a turn runs. On 1.46388.4 it is NOT. Measured on 2026-09-07,
	// ten seconds into a streaming turn, across all 767 controls on screen
	// (the committed copy is redacted down to 517 — see frontend-gaps.md):
	// the composer still showed "Send" and nothing anywhere was described or
	// titled "Stop". The tree is kept at
	// replaydata/agents/claudecode/desktop-evidence/, and it is why cell 2-20's
	// interrupt step has no control to drive.
	//
	// Resolve it when the driver is about to click it all the same: an empty
	// composer is not a reliable place to find it, and a fresh reading costs
	// nothing. See basicTurnControls.
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
	// `mode`'s label IS its value ("Auto" and the other mode names), so no fixed
	// label identifies it, and the obvious widening — "any titled AXPopUpButton"
	// — also matches `environment` and `project`. Its identity instead comes
	// from a measurement of the composer row's OWN shape, not from any title:
	// on the committed 1.46388.4 dump every AXPopUpButton the driver names
	// carries a value in its DESCRIPTION ("Model: Opus 5", "Effort: Extra",
	// "Usage: …") except three — environment, project, and mode — which carry
	// theirs in the TITLE instead, with an empty description. Environment and
	// project are already excluded by their own verified identity (titled
	// "Local", titled the project), so "an AXPopUpButton with no description
	// that is neither of those two" resolves to mode alone against the measured
	// tree, and does so without ever reading — let alone guessing — a mode
	// name. The one way this can misfire is a workspace folder actually named
	// after the mode currently selected (e.g. a folder named "Auto"); that
	// makes `project` itself ambiguous first, so composerControls refuses
	// loudly rather than silently picking either the wrong element or this one.
	"mode": {
		role: "AXPopUpButton",
		wants: func(project string) string {
			return fmt.Sprintf("with no description, titled neither \"Local\" nor %q", project)
		},
		matches: func(element helperElement, project string) bool {
			if element.Role != "AXPopUpButton" || element.Description != "" {
				return false
			}
			return element.Title != "Local" && element.Title != project
		},
	},
}

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
		element, err := matchControl(elements, expectedProject, name)
		if err != nil {
			return nil, err
		}
		if len(element.Hierarchy) == 0 {
			return nil, fmt.Errorf("Desktop %s control has no role hierarchy", name)
		}
		controls[name] = selectorFor(element)
	}
	return controls, nil
}

// matchControl resolves one control's live element by identity, against an
// already-computed expected project title.
func matchControl(elements []helperElement, expectedProject, name string) (helperElement, error) {
	matcher, known := composerMatchers[name]
	if !known {
		return helperElement{}, fmt.Errorf("Desktop control catalog has no %q control", name)
	}
	var found []helperElement
	for _, element := range elements {
		if matcher.matches(element, expectedProject) {
			found = append(found, element)
		}
	}
	if len(found) != 1 {
		return helperElement{}, fmt.Errorf(
			"Desktop %s control requires one %s %s; found %d. Visible %s controls: %s",
			name,
			matcher.role,
			matcher.wants(expectedProject),
			len(found),
			matcher.role,
			describeCandidates(elements, matcher.role),
		)
	}
	return found[0], nil
}

// matchedElement resolves one control's live element by identity, for a
// caller that needs the element itself — its Title and Description — rather
// than a stable selector. SelectMode/SelectModel use it to confirm what a
// popup reports after selecting an entry.
func matchedElement(elements []helperElement, workspace, name string) (helperElement, error) {
	return matchControl(elements, filepath.Base(filepath.Clean(workspace)), name)
}

// selectedSessionMenu returns the "More options" menu of the conversation
// currently OPEN, which is the session the driver just drove.
//
// Claude Desktop renders that control twice over: once per row of the sidebar
// session list, and once for the open conversation itself. Measured live on
// 1.46388.4 on 2026-09-06 with ten on screen, the nine sidebar rows carried a
// 21-deep hierarchy and the open conversation's carried a 29-deep one. Three of
// the ten shared a title, because Desktop names a session after its content and
// a scenario that always sends the same prompt always earns the same name.
//
// So depth is the discriminator, and it is read relatively — the deepest match
// wins, and a tie refuses — rather than against a pinned number. Addressing
// this by absolute path, as it was, points at whichever session happens to
// occupy one row of a list the driver does not control.
const sessionMenuPrefix = "More options for "

func selectedSessionMenu(elements []helperElement, expectedTitle string) (helperSelector, error) {
	open, err := openConversationMenu(elements)
	if err != nil {
		return helperSelector{}, err
	}
	if open.Description != sessionMenuPrefix+expectedTitle {
		return helperSelector{}, fmt.Errorf(
			"the open Claude Desktop conversation is %q, but this run owns %q",
			strings.TrimPrefix(open.Description, sessionMenuPrefix), expectedTitle)
	}
	if len(open.Hierarchy) == 0 {
		return helperSelector{}, errors.New("Desktop session menu has no role hierarchy")
	}
	return selectorFor(open), nil
}

// openConversationMenu returns the "More options" menu of the conversation
// currently OPEN, chosen without reference to any title.
//
// Claude Desktop renders that control twice over: once per row of the sidebar
// session list, and once for the open conversation itself. The open one is
// nested deeper, because it lives inside the conversation view rather than in a
// flat list. Measured live on 1.46388.4 on 2026-09-06 with ten on screen: nine
// sidebar rows at hierarchy depth 21, and exactly one at depth 29.
//
// Depth is read RELATIVELY — deepest wins, a tie refuses — never against a
// pinned number. Titles play no part in the choice, which is why this works
// when several sessions share one: Desktop names a session after its content,
// and a scenario that always sends the same prompt always earns the same name.
// Three of those ten were called "Confirmation response".
func openConversationMenu(elements []helperElement) (helperElement, error) {
	var deepest []helperElement
	for _, element := range elements {
		if element.Role != "AXPopUpButton" || !strings.HasPrefix(element.Description, sessionMenuPrefix) {
			continue
		}
		switch {
		case len(deepest) == 0 || len(element.Hierarchy) > len(deepest[0].Hierarchy):
			deepest = []helperElement{element}
		case len(element.Hierarchy) == len(deepest[0].Hierarchy):
			deepest = append(deepest, element)
		}
	}
	if len(deepest) == 0 {
		return helperElement{}, fmt.Errorf(
			"no Claude Desktop conversation menu is on screen. AXPopUpButton controls: %s",
			describeCandidates(elements, "AXPopUpButton"))
	}
	if len(deepest) > 1 {
		return helperElement{}, fmt.Errorf(
			"%d Desktop conversation menus are equally nested; none can be proven to be the open one",
			len(deepest))
	}
	return deepest[0], nil
}

// Claude Desktop's permission dialog, measured on 1.46388.4 on 2026-09-07 from
// the tree cell 2-26 was refused against.
//
// It renders as plain AXButtons — there is no AXSheet, no AXDialog and no
// subrole to key on. Each choice carries its own keyboard number in the title:
//
//	AXButton titled "Allow Claude to write gate.txt?"   (the question)
//	AXButton titled "Deny 1"
//	AXButton titled "Allow once 2"
//
// So the choices are identified by that shape: a title that begins with Allow
// or Deny and ends in its number. The question button is deliberately NOT
// matched — its text is the tool call, which changes with every scenario.
var permissionOptionPattern = regexp.MustCompile(`^(Allow|Deny)\b.*\s\d+$`)

// permissionDialogOptions returns the dialog's choices, or nothing when no
// dialog is on screen.
func permissionDialogOptions(elements []helperElement) []helperElement {
	var options []helperElement
	for _, element := range elements {
		if element.Role == "AXButton" && permissionOptionPattern.MatchString(element.Title) {
			options = append(options, element)
		}
	}
	return options
}

// permissionApproveOption returns the choice that lets the tool call proceed.
//
// Both cells that answer a dialog assert the turn RESUMES afterwards —
// 2-26's `mutating_gate_answered: working`, 2-27's `dialog_answered: working`.
// Denying ends the turn instead, so approval is the only answer that produces
// what they assert. "Allow once" is preferred over any broader grant: it is the
// narrowest choice that satisfies the scenario, and a recording rig must not
// widen a permission on the operator's machine beyond the one call it needs.
func permissionApproveOption(elements []helperElement) (helperElement, error) {
	options := permissionDialogOptions(elements)
	if len(options) == 0 {
		return helperElement{}, errors.New("no Claude Desktop permission dialog is on screen")
	}
	var allow []helperElement
	for _, option := range options {
		if strings.HasPrefix(option.Title, "Allow") {
			allow = append(allow, option)
		}
	}
	if len(allow) == 0 {
		return helperElement{}, fmt.Errorf(
			"the Desktop permission dialog offers no Allow choice; options are %s", optionTitles(options))
	}
	for _, option := range allow {
		if strings.HasPrefix(option.Title, "Allow once") {
			return option, nil
		}
	}
	if len(allow) > 1 {
		return helperElement{}, fmt.Errorf(
			"the Desktop permission dialog offers %d Allow choices and none is \"Allow once\"; "+
				"refusing rather than guessing which grant to make: %s", len(allow), optionTitles(allow))
	}
	return allow[0], nil
}

func optionTitles(options []helperElement) string {
	titles := make([]string, 0, len(options))
	for _, option := range options {
		titles = append(titles, strconv.Quote(option.Title))
	}
	return strings.Join(titles, ", ")
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

// --- the slash-command popup -------------------------------------------------
//
// Typing "/" into the composer opens a filtering list of commands. Measured
// 2026-09-07 on 1.46388.4 and committed at
// replaydata/agents/claudecode/desktop-evidence/slash-command-popup-1.46388.4.json:
// the entries are AXMenuItem elements INSIDE the web area, and that "inside the
// web area" is the whole selector. Claude Desktop's own macOS menus are also
// AXMenuItem, and there are 226 of them on the same tree — matching by role and
// title alone would have picked a menu-bar row with the same name.
//
// The macOS rows also carry a null description where every popup entry carries
// an empty one, which is a second discriminator if the hierarchy ever moves.

// slashPopupEntries returns the command list in the order the app renders it,
// or nothing when the popup is closed.
func slashPopupEntries(elements []helperElement) []helperElement {
	var entries []helperElement
	for _, element := range elements {
		if element.Role != "AXMenuItem" || element.Title == "" {
			continue
		}
		if slices.Contains(element.Hierarchy, "AXMenuBar") {
			continue
		}
		entries = append(entries, element)
	}
	return entries
}

// requireSlashPopupOffers proves the popup is open AND that its first entry is
// exactly the command the recipe named.
//
// Pressing Return accepts the HIGHLIGHTED entry, which is the first one. A
// filter is fuzzy: "/help" returns "debug", "team-onboarding", "heapdump" and
// "find-skills" and no help command at all, so a driver that pressed Return on
// whatever came back would silently run "debug" and record it as "/help". The
// exact-first check is the difference between running the recipe and running
// something else under its name.
func requireSlashPopupOffers(elements []helperElement, command string) error {
	entries := slashPopupEntries(elements)
	if len(entries) == 0 {
		return fmt.Errorf(
			"no Claude Desktop command popup is open after typing %q; the composer holds it as plain text",
			"/"+command)
	}
	if entries[0].Title == command {
		return nil
	}
	for _, entry := range entries {
		if entry.Title == command {
			return fmt.Errorf(
				"the Desktop command popup offers %q but not first, so Return would accept %q instead; entries are %s",
				command, entries[0].Title, slashEntryTitles(entries))
		}
	}
	return fmt.Errorf(
		"Claude Desktop has no %q command; the popup offers %s",
		"/"+command, slashEntryTitles(entries))
}

func slashEntryTitles(entries []helperElement) string {
	titles := make([]string, 0, len(entries))
	for _, entry := range entries {
		titles = append(titles, strconv.Quote(entry.Title))
	}
	return strings.Join(titles, ", ")
}
