package desktopdriver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const slashPopupTree = "../../../../replaydata/agents/claudecode/desktop-evidence/slash-command-popup-1.46388.4.json"

func loadMeasuredTree(t *testing.T, path string) []helperElement {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("read the measured tree: %v", err)
	}
	var elements []helperElement
	if err := json.Unmarshal(data, &elements); err != nil {
		t.Fatalf("decode the measured tree: %v", err)
	}
	if len(elements) == 0 {
		t.Fatal("the measured tree is empty; this test proved nothing")
	}
	return elements
}

// The popup entries are AXMenuItem elements, and so are Claude Desktop's own
// macOS menus. This runs against the tree the popup was measured on, so a
// selector that stops distinguishing them fails here rather than on a live run.
func TestSlashPopupEntriesComeOnlyFromTheWebArea(t *testing.T) {
	elements := loadMeasuredTree(t, slashPopupTree)
	entries := slashPopupEntries(elements)

	if len(entries) != 24 {
		t.Fatalf("slashPopupEntries() returned %d entries, want the 24 measured on 1.46388.4", len(entries))
	}
	if entries[0].Title != "schedule" {
		t.Errorf("first entry = %q, want the measured %q", entries[0].Title, "schedule")
	}
	for _, entry := range entries {
		for _, ancestor := range entry.Hierarchy {
			if ancestor == "AXMenuBar" {
				t.Fatalf("entry %q came from the macOS menu bar", entry.Title)
			}
		}
	}
	// The committed tree has its macOS menu-bar subtree removed — it carried
	// the operator's Recent Items — so it cannot exercise the exclusion on its
	// own. Splicing one row back in is what exercises it, and the 226 real
	// macOS AXMenuItem rows this stands in for are why the exclusion exists.
	spliced := append(append([]helperElement{}, elements...), helperElement{
		Role:      "AXMenuItem",
		Title:     "compact",
		Hierarchy: []string{"AXApplication", "AXMenuBar", "AXMenuBarItem", "AXMenu"},
	})
	if got := len(slashPopupEntries(spliced)); got != len(entries) {
		t.Fatalf("a macOS menu row leaked into the popup: %d entries, want %d", got, len(entries))
	}
}

func TestRequireSlashPopupOffersNamesWhatItSaw(t *testing.T) {
	entry := func(title string) helperElement {
		return helperElement{Role: "AXMenuItem", Title: title, Hierarchy: []string{"AXWebArea", "AXGroup"}}
	}
	cases := []struct {
		name     string
		elements []helperElement
		command  string
		wantErr  string
	}{
		{
			name:     "the exact command is first",
			elements: []helperElement{entry("compact"), entry("autocompact")},
			command:  "compact",
		},
		{
			// Measured: "/help" returns debug, team-onboarding, heapdump and
			// find-skills. Return would have run "debug" as "/help".
			name:     "a fuzzy match must never be accepted",
			elements: []helperElement{entry("debug"), entry("team-onboarding"), entry("heapdump")},
			command:  "help",
			wantErr:  `Claude Desktop has no "/help" command`,
		},
		{
			name:     "the command exists but is not first",
			elements: []helperElement{entry("autocompact"), entry("compact")},
			command:  "compact",
			wantErr:  `offers "compact" but not first`,
		},
		{
			name:     "the popup never opened",
			elements: []helperElement{{Role: "AXButton", Title: "Send"}},
			command:  "compact",
			wantErr:  "no Claude Desktop command popup is open",
		},
		{
			name:     "a macOS menu row with the right name is not the popup",
			elements: []helperElement{{Role: "AXMenuItem", Title: "compact", Hierarchy: []string{"AXMenuBar", "AXMenu"}}},
			command:  "compact",
			wantErr:  "no Claude Desktop command popup is open",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := requireSlashPopupOffers(testCase.elements, testCase.command)
			switch {
			case testCase.wantErr == "" && err != nil:
				t.Fatalf("requireSlashPopupOffers() error = %v; want nil", err)
			case testCase.wantErr == "":
			case err == nil:
				t.Fatalf("requireSlashPopupOffers() = nil; want an error naming %q", testCase.wantErr)
			case !strings.Contains(err.Error(), testCase.wantErr):
				t.Fatalf("requireSlashPopupOffers() error = %v; want it to name %q", err, testCase.wantErr)
			}
		})
	}
}

func TestSplitSlashCommandSeparatesTheNameFromItsArguments(t *testing.T) {
	cases := []struct {
		text, name, args string
		wantErr          bool
	}{
		{text: "/compact", name: "compact"},
		{text: "/goal clear", name: "goal", args: "clear"},
		{text: "/model sonnet", name: "model", args: "sonnet"},
		{text: "/ir:code-review", name: "ir:code-review"},
		{text: "/goal Run them one at a time.", name: "goal", args: "Run them one at a time."},
		{text: "  /compact  ", name: "compact"},
		{text: "compact", wantErr: true},
		{text: "/", wantErr: true},
		{text: "/ compact", wantErr: true},
		{text: "", wantErr: true},
	}
	for _, testCase := range cases {
		name, args, err := splitSlashCommand(testCase.text)
		if testCase.wantErr {
			if err == nil {
				t.Errorf("splitSlashCommand(%q) = (%q, %q, nil); want an error", testCase.text, name, args)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitSlashCommand(%q) error = %v", testCase.text, err)
			continue
		}
		if name != testCase.name || args != testCase.args {
			t.Errorf("splitSlashCommand(%q) = (%q, %q); want (%q, %q)",
				testCase.text, name, args, testCase.name, testCase.args)
		}
	}
}

// LOCK (passes by construction): the two composer shapes are MEASURED, on
// /compact and again on /model. They are pinned here so a future edit that
// "tidies" the newline has to argue with a measurement.
func TestAcceptedComposerValueMatchesTheMeasuredShapes(t *testing.T) {
	if got := acceptedComposerValue("compact", ""); got != "compact " {
		t.Errorf("acceptedComposerValue(compact, none) = %q, want %q", got, "compact ")
	}
	if got := acceptedComposerValue("compact", "hello world"); got != "compact\n hello world" {
		t.Errorf("acceptedComposerValue(compact, args) = %q, want %q", got, "compact\n hello world")
	}
	if got := acceptedComposerValue("model", "sonnet"); got != "model\n sonnet" {
		t.Errorf("acceptedComposerValue(model, args) = %q, want %q", got, "model\n sonnet")
	}
}

type slashRecorder struct {
	calls    []string
	elements []helperElement
}

func (recorder *slashRecorder) typeText(
	_ context.Context, _ helperSelector, text string, post helperPostcondition,
) error {
	recorder.calls = append(recorder.calls, "type("+text+")=>"+post.Value)
	return nil
}

func (recorder *slashRecorder) keyboard(
	_ context.Context, _ helperSelector, code uint16, _ []string, post helperPostcondition,
) error {
	recorder.calls = append(recorder.calls, "key("+itoa(code)+")=>"+post.Value)
	return nil
}

func itoa(value uint16) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func composerWithPopup(workspace string, entries ...string) []helperElement {
	webArea := []string{"AXApplication", "AXWindow", "AXWebArea", "AXGroup"}
	elements := []helperElement{
		{Role: "AXPopUpButton", Title: "Local", Hierarchy: webArea},
		{Role: "AXPopUpButton", Title: filepath.Base(workspace), Hierarchy: webArea},
		{Role: "AXTextArea", Description: "Prompt", Hierarchy: webArea},
	}
	for _, title := range entries {
		elements = append(elements,
			helperElement{Role: "AXMenuItem", Title: title, Hierarchy: webArea})
	}
	return elements
}

func runCompose(t *testing.T, text string, entries ...string) (*slashRecorder, error) {
	t.Helper()
	workspace := t.TempDir()
	recorder := &slashRecorder{elements: composerWithPopup(workspace, entries...)}
	err := composeSlashCommand(
		context.Background(), workspace, OwnedSession{}, text,
		func(context.Context) error { return nil },
		func(context.Context) ([]helperElement, error) { return recorder.elements, nil },
		recorder.typeText, recorder.keyboard,
	)
	return recorder, err
}

// The measured sequence, in order: type the command, accept it, type the
// arguments. Every step carries the composer value it expects, because that
// postcondition is the only thing that catches a keystroke which did not land.
func TestComposeSlashCommandDrivesTheMeasuredSequence(t *testing.T) {
	recorder, err := runCompose(t, "/model sonnet", "model", "advisor")
	if err != nil {
		t.Fatalf("composeSlashCommand() error = %v", err)
	}
	want := []string{
		"type(/model)=>/model",
		"key(36)=>model ",
		"type(sonnet)=>model\n sonnet",
	}
	if len(recorder.calls) != len(want) {
		t.Fatalf("calls = %v; want %v", recorder.calls, want)
	}
	for index, call := range recorder.calls {
		if call != want[index] {
			t.Errorf("call %d = %q; want %q", index, call, want[index])
		}
	}
}

func TestComposeSlashCommandTypesNoArgumentsWhenTheRecipeGivesNone(t *testing.T) {
	recorder, err := runCompose(t, "/compact", "compact", "autocompact")
	if err != nil {
		t.Fatalf("composeSlashCommand() error = %v", err)
	}
	want := []string{"type(/compact)=>/compact", "key(36)=>compact "}
	if len(recorder.calls) != len(want) || recorder.calls[0] != want[0] || recorder.calls[1] != want[1] {
		t.Fatalf("calls = %v; want exactly %v", recorder.calls, want)
	}
}

// RED-FIRST shape: without requireSlashPopupOffers between the typing and the
// Return, this passes and the driver accepts "debug" as "/help" — the exact
// silent-wrong-recording the postconditions cannot see, because "debug" IS a
// real command and every postcondition after it would hold.
func TestComposeSlashCommandRefusesBeforePressingReturnOnAFuzzyMatch(t *testing.T) {
	recorder, err := runCompose(t, "/help", "debug", "team-onboarding", "heapdump")
	if err == nil {
		t.Fatal("composeSlashCommand() = nil; want a refusal: Desktop has no /help command")
	}
	if !strings.Contains(err.Error(), `no "/help" command`) {
		t.Fatalf("error = %v; want it to say Desktop has no /help command", err)
	}
	for _, call := range recorder.calls {
		if strings.HasPrefix(call, "key(") {
			t.Fatalf("Return was pressed on a fuzzy match; calls = %v", recorder.calls)
		}
	}
}

func TestComposeSlashCommandRefusesAMalformedStep(t *testing.T) {
	recorder, err := runCompose(t, "not a command", "compact")
	if err == nil {
		t.Fatal("composeSlashCommand() = nil; want a refusal")
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a malformed step typed something: %v", recorder.calls)
	}
}

// The Desktop composer rewrites a typed backtick into an inline code node, so
// the agent receives text the recipe did not write. Measured 2026-09-07:
// typing "a `code` b" leaves the composer reading "a code​ b", and
// sending it headed the conversation "You said: /compact keep echo hi please".
// Only the backtick does this — *, _, ** and # all survive.
func TestSlashArgumentsThatTheComposerWouldRewriteAreRefused(t *testing.T) {
	rewritten := []string{
		"run `echo iter1` first",
		"`x`",
		"a backtick ` on its own",
	}
	for _, args := range rewritten {
		if err := requireTypableSlashArguments(args); err == nil {
			t.Errorf("requireTypableSlashArguments(%q) = nil; the composer rewrites a typed backtick", args)
		}
	}
	survives := []string{
		"a *star* b", "a _under_ b", "a **bold** b", "a #hash b",
		"plain words only", "", "Keep guessing.",
	}
	for _, args := range survives {
		if err := requireTypableSlashArguments(args); err != nil {
			t.Errorf("requireTypableSlashArguments(%q) error = %v; this text was measured to survive", args, err)
		}
	}
}

// Plan refuses it before a Desktop session is opened, and compose refuses it
// before a keystroke lands. Both, because either one alone leaves a path that
// types text the recipe did not write.
func TestABacktickedSlashArgumentIsRefusedAtBothLayers(t *testing.T) {
	step := Step{Type: StepSlash, Text: "/goal run `echo iter1` first"}
	err := Plan([]Step{step})
	if err == nil || !strings.Contains(err.Error(), "verbatim-typed-text") {
		t.Fatalf("Plan() error = %v; want the verbatim-typed-text limit named", err)
	}
	recorder, err := runCompose(t, step.Text, "goal")
	if err == nil || !strings.Contains(err.Error(), "backtick") {
		t.Fatalf("composeSlashCommand() error = %v; want a refusal naming the backtick", err)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a refused slash step still typed something: %v", recorder.calls)
	}
}
