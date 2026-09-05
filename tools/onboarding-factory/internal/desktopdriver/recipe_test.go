package desktopdriver

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPlanAcceptsEveryElicitedStepType(t *testing.T) {
	steps := []Step{
		{Type: StepSend, Text: "Reply with exactly the word: ok"},
		{Type: StepWaitTurn},
		{Type: StepSleep, Seconds: 3},
		{Type: StepKeys, Keys: "Escape"},
		{Type: StepKeys, Keys: "Enter"},
		{Type: StepInterrupt},
		{Type: StepMode, Value: "Plan"},
		{Type: StepModel, Value: "Opus 5"},
		{Type: StepStartSession},
		{Type: StepArchive},
	}
	if err := Plan(steps); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	// Every elicited primitive must appear above, or this test proves nothing
	// about the ones it skipped.
	seen := map[string]bool{}
	for _, step := range steps {
		seen[step.Type] = true
	}
	for _, primitive := range Primitives() {
		if !seen[primitive] {
			t.Fatalf("primitive %q is elicited but this test never plans it", primitive)
		}
	}
}

func TestPlanNamesTheMissingControlForEveryUnsupportedStepType(t *testing.T) {
	for _, pair := range MissingControls() {
		stepType, control, _ := strings.Cut(pair, ":")
		t.Run(stepType, func(t *testing.T) {
			err := Plan([]Step{{Type: StepSend, Text: "hello"}, {Type: stepType}})
			var notRunnable *NotRunnableError
			if !errors.As(err, &notRunnable) {
				t.Fatalf("Plan() error = %v, want *NotRunnableError", err)
			}
			if !strings.Contains(err.Error(), "not runnable through Desktop") {
				t.Fatalf("error does not carry the Desktop verdict: %v", err)
			}
			if !strings.Contains(err.Error(), control) {
				t.Fatalf("error does not name the missing control %q: %v", control, err)
			}
			if notRunnable.Missing[0].Index != 2 {
				t.Fatalf("missing control points at step %d, want 2", notRunnable.Missing[0].Index)
			}
		})
	}
}

func TestPlanReportsEveryMissingControlNotJustTheFirst(t *testing.T) {
	err := Plan([]Step{
		{Type: "slash", Text: "/model sonnet"},
		{Type: StepKeys, Keys: "1"},
		{Type: "reset_session"},
	})
	var notRunnable *NotRunnableError
	if !errors.As(err, &notRunnable) {
		t.Fatalf("Plan() error = %v, want *NotRunnableError", err)
	}
	if len(notRunnable.Missing) != 3 {
		t.Fatalf("Plan() reported %d missing controls, want 3: %v", len(notRunnable.Missing), err)
	}
}

func TestPlanRefusesAKeyWithNoObservablePostcondition(t *testing.T) {
	err := Plan([]Step{{Type: StepKeys, Keys: "1"}})
	if err == nil || !strings.Contains(err.Error(), `keyboard-key:\"1\"`) {
		t.Fatalf("Plan() error = %v; want the unsupported key named", err)
	}
	for _, key := range SupportedKeys() {
		if err := Plan([]Step{{Type: StepKeys, Keys: key}}); err != nil {
			t.Fatalf("Plan() refused supported key %q: %v", key, err)
		}
	}
}

func TestPlanRefusesASessionRetarget(t *testing.T) {
	err := Plan([]Step{{Type: StepSend, Text: "hello", Session: 1}})
	if err == nil || !strings.Contains(err.Error(), "session-list-row") {
		t.Fatalf("Plan() error = %v; want the session-list-row control named", err)
	}
}

func TestPlanRefusesASlashCommandTypedAsSendText(t *testing.T) {
	err := Plan([]Step{{Type: StepSend, Text: "/model sonnet"}})
	if err == nil || !strings.Contains(err.Error(), "slash-command-entry") {
		t.Fatalf("Plan() error = %v; want the slash control named", err)
	}
}

func TestPlanBoundsSleepAndSessionCount(t *testing.T) {
	tests := []struct {
		name  string
		steps []Step
		want  string
	}{
		{"missing seconds", []Step{{Type: StepSleep}}, "positive `seconds`"},
		{"oversized sleep", []Step{{Type: StepSleep, Seconds: 3600}}, "caps a recipe sleep"},
		{"too many sessions", []Step{
			{Type: StepStartSession}, {Type: StepStartSession},
			{Type: StepStartSession}, {Type: StepStartSession},
		}, "owns at most"},
		{"empty recipe", nil, "no steps to drive"},
		{"empty send", []Step{{Type: StepSend}}, "no text to type"},
		{"mode with no entry", []Step{{Type: StepMode}}, "names no menu entry"},
		{"relative start workspace", []Step{{Type: StepStartSession, CWD: "cwd"}}, "absolute workspace"},
		{"cwd on a send", []Step{{Type: StepSend, Text: "x", CWD: "/tmp"}}, "only a start_session step"},
		{"unknown step", []Step{{Type: "teleport"}}, "does not know this step type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Plan(test.steps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Plan() error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestParseRecipeNamesAnUnknownStepFieldAsADesktopLimitation(t *testing.T) {
	_, err := ParseRecipe([]byte(`[{"type":"send","text":"hi","tmux_pane":"2"}]`))
	var notRunnable *NotRunnableError
	if !errors.As(err, &notRunnable) {
		t.Fatalf("ParseRecipe() error = %v, want *NotRunnableError", err)
	}
	if !strings.Contains(err.Error(), "recipe-field:tmux_pane") {
		t.Fatalf("error does not name the unread field: %v", err)
	}
}

func TestParseRecipeReadsTheClaudecodeGrammar(t *testing.T) {
	steps, err := ParseRecipe([]byte(
		`[{"type":"send","text":"hi"},{"type":"wait_turn","session":1},{"type":"sleep","seconds":3},` +
			`{"type":"keys","keys":"Enter"},{"type":"start_session","cwd":"/tmp/x"}]`))
	if err != nil {
		t.Fatalf("ParseRecipe() error = %v", err)
	}
	want := []Step{
		{Type: StepSend, Text: "hi"},
		{Type: StepWaitTurn, Session: 1},
		{Type: StepSleep, Seconds: 3},
		{Type: StepKeys, Keys: "Enter"},
		{Type: StepStartSession, CWD: "/tmp/x"},
	}
	if !slices.Equal(steps, want) {
		t.Fatalf("ParseRecipe() = %+v, want %+v", steps, want)
	}
}

// Every elicited primitive needs a control list, and every control it names has
// to be one the driver knows. A primitive added with a typo'd control name would
// otherwise plan as runnable and fail live.
func TestEveryElicitedPrimitiveNamesKnownControls(t *testing.T) {
	known := map[string]bool{
		controlComposerDeepLink: true, controlPrompt: true, controlSend: true,
		controlStop: true, controlMode: true, controlModel: true,
		controlSessionMenu: true, controlArchiveMenuItem: true, controlKeyboard: true,
	}
	for primitive, controls := range desktopElicits {
		for _, control := range controls {
			if !known[control] {
				t.Fatalf("primitive %q names unknown control %q", primitive, control)
			}
		}
	}
	if len(desktopElicits) == 0 {
		t.Fatal("the elicited set is empty; this check cannot run, which is a failure")
	}
}

// A step type cannot be both elicited and refused. The two tables are read by
// different callers (the executor and the lint), so a name in both would make
// them disagree about the same recipe.
func TestElicitedAndMissingControlTablesAreDisjoint(t *testing.T) {
	for stepType := range desktopMissingControls {
		if _, elicited := desktopElicits[stepType]; elicited {
			t.Fatalf("step type %q is both elicited and refused", stepType)
		}
	}
}
