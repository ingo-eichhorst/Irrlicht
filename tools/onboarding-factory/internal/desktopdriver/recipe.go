package desktopdriver

// The recipe grammar the Claude Desktop driver understands, and the refusal it
// produces for a recipe it cannot drive.
//
// WHY THIS FILE IS THE ONE OWNER. Every other adapter declares its grammar in
// its own `driver-*.sh` (DRIVE_ELICITS), which recipe-lint scrapes. The Desktop
// profile's driver is this Go binary, not the shell wrapper, so the grammar has
// to live here or it lives in two places. The shell wrapper still carries the
// scraped declaration recipe-lint needs — and contract_test.go fails when the
// two disagree, so the copy cannot drift silently.
//
// THE REFUSAL IS THE POINT. A Desktop step the driver cannot elicit is not a
// step it may skip: skipping records a fixture whose recipe says one thing and
// whose events say another, and nothing downstream can tell that apart from a
// recipe that ran. So every step type is either elicited (with the controls it
// needs named) or refused by name, before Claude Desktop is touched at all.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Step is one recipe step in the shared onboarding recipe grammar
// (replaydata/agents/<adapter>/scenarios/<cell>/metadata.json →
// .details.recipe.script). The field set is the union the claudecode recipes
// actually use: type, text, keys, seconds, session.
type Step struct {
	Type    string  `json:"type"`
	Text    string  `json:"text,omitempty"`
	Keys    string  `json:"keys,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	// Session is the 1-based slot a step retargets before it runs. Absent (0)
	// means "the active slot". Any explicit value is a session SELECTION, which
	// needs a control the measured Desktop tree does not carry — see
	// desktopMissingControls.
	Session int `json:"session,omitempty"`
	// CWD overrides the workspace a `start_session` step opens. Absent means
	// the run's own workspace, which is what makes two sessions share one cwd.
	CWD string `json:"cwd,omitempty"`
	// Value carries the menu entry a mode/model selection asks for.
	Value string `json:"value,omitempty"`
}

// recipeStepFields is the grammar's field set, checked by name before decoding.
// A field this driver does not read is a field whose intent it would silently
// drop, so it is refused as a Desktop limitation rather than accepted.
var recipeStepFields = map[string]struct{}{
	"type": {}, "text": {}, "keys": {}, "seconds": {}, "session": {}, "cwd": {}, "value": {},
}

// Step types the Desktop driver elicits.
const (
	StepSend         = "send"
	StepWaitTurn     = "wait_turn"
	StepSleep        = "sleep"
	StepInterrupt    = "interrupt"
	StepKeys         = "keys"
	StepMode         = "mode"
	StepModel        = "model"
	StepArchive      = "archive"
	StepStartSession = "start_session"
)

// Control names. Each is either measured in testdata/composer-<version>.json or
// derived from a measured control — never invented. `stop` is the in-flight
// state of the measured Send button and is already what Submit's postcondition
// watches for; `archive-menu-item` and `session-menu` are the two the archive
// path already drives.
const (
	controlComposerDeepLink = "composer-deep-link"
	controlPrompt           = "prompt"
	controlSend             = "send"
	controlStop             = "stop"
	controlMode             = "mode"
	controlModel            = "model"
	controlSessionMenu      = "session-menu"
	controlArchiveMenuItem  = "archive-menu-item"
	controlKeyboard         = "keyboard"
)

// desktopElicits maps each elicited step type to the controls it drives. A step
// type with an empty control list needs no Desktop control at all — it is
// observed through the Irrlicht daemon or is a plain wait.
var desktopElicits = map[string][]string{
	StepSend:         {controlPrompt, controlSend, controlStop},
	StepWaitTurn:     nil,
	StepSleep:        nil,
	StepInterrupt:    {controlStop, controlSend},
	StepKeys:         {controlKeyboard},
	StepMode:         {controlMode},
	StepModel:        {controlModel},
	StepArchive:      {controlSessionMenu, controlArchiveMenuItem},
	StepStartSession: {controlComposerDeepLink, controlPrompt, controlSend},
}

// desktopMissingControls names, for every step type the shared recipe grammar
// has and this driver cannot drive, the Desktop control that is missing.
//
// These are not "unimplemented". Each names a control that has no measured path
// in the committed accessibility dump, so writing one would be inventing it:
//
//   - session-list-row — the sidebar row for a session that is NOT selected.
//     The dump carries only the SELECTED session's "More options for …" popup.
//   - slash-command-entry — nothing measured shows the Desktop composer
//     EXECUTING a slash command rather than storing it as prompt text.
//   - session-restart / session-resume / session-reset / session-exit —
//     Desktop owns the Claude Code process lifetime; no measured control
//     restarts, resumes, rotates or ends a session in place.
//   - agent-process-kill — signalling Desktop's child would leave Desktop's own
//     registry and this driver's ownership bookkeeping disagreeing about what
//     is alive, and no measured control does it through the app.
var desktopMissingControls = map[string]string{
	"session":       "session-list-row",
	"slash":         "slash-command-entry",
	"restart":       "session-restart",
	"resume":        "session-resume",
	"reset_session": "session-reset",
	"sigkill":       "agent-process-kill",
	"exit_clean":    "session-exit",
}

// desktopKey is one raw keyboard action the driver can prove landed.
//
// The helper's `keyboard` command REQUIRES a postcondition and refuses an
// action whose postcondition is already true, so a key with no observable
// effect on the composer cannot be driven at all — it would be a silent no-op,
// which is the one outcome this driver must never produce. Only keys with a
// composer-visible false-to-true transition are supported; every other key is
// refused by name.
type desktopKey struct {
	// code is the macOS virtual key code the helper is handed (0...127).
	code uint16
	// effect names the observable transition the postcondition watches.
	effect string
}

var desktopKeys = map[string]desktopKey{
	// kVK_Escape. Cancels the in-flight turn: Stop goes away, Send returns.
	"Escape": {code: 53, effect: "the composer returns from Stop to Send"},
	// kVK_Return. Submits the composed prompt: Send is replaced by Stop.
	"Enter": {code: 36, effect: "the composer replaces Send with Stop"},
}

// maxRecipeSleepSeconds bounds a recipe `sleep`. A recipe that asks to sit idle
// longer than the whole Desktop run budget is a recipe error, and finding that
// out after the run has started costs a live Desktop session.
const maxRecipeSleepSeconds = 300

// maxDesktopSessions bounds how many live Desktop sessions one run may own.
// Every one of them is a real Claude Code process this run must archive again,
// and ownership selection refuses concurrent creation, so the ceiling is low on
// purpose.
const maxDesktopSessions = 4

// Primitives returns the recipe step types the Desktop driver genuinely
// elicits, sorted. This is what driver-desktop.sh's DRIVE_ELICITS must equal.
func Primitives() []string {
	names := make([]string, 0, len(desktopElicits))
	for name := range desktopElicits {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MissingControls returns the step-type → missing-control pairs, sorted by step
// type. This is what driver-desktop.sh's DRIVE_MISSING_CONTROLS must equal.
func MissingControls() []string {
	pairs := make([]string, 0, len(desktopMissingControls))
	for step, control := range desktopMissingControls {
		pairs = append(pairs, step+":"+control)
	}
	sort.Strings(pairs)
	return pairs
}

// SupportedKeys returns the raw key names `keys` steps may use, sorted.
func SupportedKeys() []string {
	names := make([]string, 0, len(desktopKeys))
	for name := range desktopKeys {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MissingControl is one reason a recipe cannot run through Claude Desktop.
type MissingControl struct {
	// Index is the 1-based position of the offending step, or 0 for a
	// whole-recipe problem.
	Index int
	// Step is the recipe step type that cannot be driven.
	Step string
	// Control names the Desktop control the driver would need.
	Control string
	// Reason says what specifically could not be driven.
	Reason string
}

func (missing MissingControl) String() string {
	position := "recipe"
	if missing.Index > 0 {
		position = fmt.Sprintf("step %d", missing.Index)
	}
	return fmt.Sprintf("%s %q needs control %q: %s", position, missing.Step, missing.Control, missing.Reason)
}

// NotRunnableError is the Desktop-limitation verdict. Its message is the exact
// phrase #1888 asks callers to key on, followed by every missing control.
type NotRunnableError struct {
	Missing []MissingControl
}

func (err *NotRunnableError) Error() string {
	lines := make([]string, 0, len(err.Missing)+1)
	lines = append(lines, "not runnable through Desktop")
	for _, missing := range err.Missing {
		lines = append(lines, "  - "+missing.String())
	}
	return strings.Join(lines, "\n")
}

// ParseRecipe decodes a recipe `script` array.
//
// A step field outside the grammar comes back as a *NotRunnableError naming the
// field, not as a JSON decode error: another adapter's driver may carry step
// fields this one has never seen, and "unknown field \"cwd\"" tells a recording
// operator nothing about which Desktop control is missing.
func ParseRecipe(data []byte) ([]Step, error) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode Desktop recipe script: %w", err)
	}
	var missing []MissingControl
	for index, object := range raw {
		var stepType string
		if encoded, ok := object["type"]; ok {
			_ = json.Unmarshal(encoded, &stepType)
		}
		for field := range object {
			if _, known := recipeStepFields[field]; known {
				continue
			}
			missing = append(missing, MissingControl{
				Index: index + 1, Step: stepType, Control: "recipe-field:" + field,
				Reason: "the Desktop driver does not read this recipe step field, and driving the step without it would change what the recipe asked for",
			})
		}
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(left, right int) bool {
			if missing[left].Index != missing[right].Index {
				return missing[left].Index < missing[right].Index
			}
			return missing[left].Control < missing[right].Control
		})
		return nil, &NotRunnableError{Missing: missing}
	}
	var steps []Step
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("decode Desktop recipe script: %w", err)
	}
	return steps, nil
}

// Plan decides whether the Desktop driver can drive this recipe, and returns a
// *NotRunnableError naming EVERY missing control when it cannot.
//
// It reports all of them rather than the first, because a recording operator
// who fixes one refusal and re-runs pays another Desktop run per hidden gap.
func Plan(steps []Step) error {
	if len(steps) == 0 {
		return &NotRunnableError{Missing: []MissingControl{{
			Step: "recipe", Control: "none", Reason: "the recipe has no steps to drive",
		}}}
	}
	var missing []MissingControl
	sessions := 1
	for index, step := range steps {
		missing = append(missing, planStep(index+1, step, &sessions)...)
	}
	if len(missing) > 0 {
		return &NotRunnableError{Missing: missing}
	}
	return nil
}

func planStep(position int, step Step, sessions *int) []MissingControl {
	var missing []MissingControl
	if step.CWD != "" && step.Type != StepStartSession {
		missing = append(missing, MissingControl{
			Index: position, Step: step.Type, Control: controlComposerDeepLink,
			Reason: "only a start_session step may name its own workspace; the driver cannot move a live session to another folder",
		})
	}
	if step.Session != 0 {
		missing = append(missing, MissingControl{
			Index: position, Step: step.Type, Control: desktopMissingControls["session"],
			Reason: fmt.Sprintf(
				"the step retargets session slot %d, and the measured Desktop tree carries no row for a session that is not already selected",
				step.Session),
		})
	}
	if control, unsupported := desktopMissingControls[step.Type]; unsupported {
		return append(missing, MissingControl{
			Index: position, Step: step.Type, Control: control,
			Reason: "the Desktop driver has no measured control for this step type",
		})
	}
	if _, elicited := desktopElicits[step.Type]; !elicited {
		return append(missing, MissingControl{
			Index: position, Step: step.Type, Control: "unknown",
			Reason: "the Desktop driver does not know this step type",
		})
	}
	return append(missing, planElicitedStep(position, step, sessions)...)
}

func planElicitedStep(position int, step Step, sessions *int) []MissingControl {
	switch step.Type {
	case StepSend, StepStartSession:
		return planTextStep(position, step, sessions)
	case StepSleep:
		return planSleepStep(position, step)
	case StepKeys:
		return planKeysStep(position, step)
	case StepMode, StepModel:
		return planSelectionStep(position, step)
	case StepWaitTurn, StepInterrupt, StepArchive:
		return nil
	default:
		// Unreachable: desktopElicits gates entry, and every entry has an arm
		// above. Stated rather than assumed — a new elicited step type with no
		// arm must refuse here rather than fall through as runnable.
		return []MissingControl{{
			Index: position, Step: step.Type, Control: "unknown",
			Reason: "the Desktop planner has no arm for this elicited step type",
		}}
	}
}

func planTextStep(position int, step Step, sessions *int) []MissingControl {
	var missing []MissingControl
	if step.Type == StepStartSession {
		*sessions++
		if *sessions > maxDesktopSessions {
			missing = append(missing, MissingControl{
				Index: position, Step: step.Type, Control: controlComposerDeepLink,
				Reason: fmt.Sprintf("the recipe asks for %d live Desktop sessions; the driver owns at most %d",
					*sessions, maxDesktopSessions),
			})
		}
		if step.CWD != "" && !filepath.IsAbs(step.CWD) {
			missing = append(missing, MissingControl{
				Index: position, Step: step.Type, Control: controlComposerDeepLink,
				Reason: fmt.Sprintf("the Desktop deep link needs an absolute workspace; the step names %q", step.CWD),
			})
		}
		return missing
	}
	if strings.TrimSpace(step.Text) == "" {
		missing = append(missing, MissingControl{
			Index: position, Step: step.Type, Control: controlPrompt,
			Reason: "the step carries no text to type into the composer",
		})
	}
	if strings.HasPrefix(strings.TrimSpace(step.Text), "/") {
		missing = append(missing, MissingControl{
			Index: position, Step: step.Type, Control: desktopMissingControls["slash"],
			Reason: "the step's text is a slash command, and nothing measured shows the Desktop composer executing one",
		})
	}
	return missing
}

func planSleepStep(position int, step Step) []MissingControl {
	if step.Seconds <= 0 {
		return []MissingControl{{
			Index: position, Step: step.Type, Control: "none",
			Reason: "a sleep step needs a positive `seconds`",
		}}
	}
	if step.Seconds > maxRecipeSleepSeconds {
		return []MissingControl{{
			Index: position, Step: step.Type, Control: "none",
			Reason: fmt.Sprintf("the step sleeps %.0fs; the Desktop driver caps a recipe sleep at %ds",
				step.Seconds, maxRecipeSleepSeconds),
		}}
	}
	return nil
}

func planKeysStep(position int, step Step) []MissingControl {
	if _, ok := desktopKeys[step.Keys]; ok {
		return nil
	}
	// The key name is quoted because a recipe `keys` step carries a tmux
	// send-keys SEQUENCE ("f o l l o w u p Space Enter"), not one key name, and
	// an unquoted control name with spaces in it cannot be read back out of a
	// refusal list.
	return []MissingControl{{
		Index: position, Step: step.Type, Control: controlKeyboard + "-key:" + strconv.Quote(step.Keys),
		Reason: fmt.Sprintf(
			"the Desktop helper refuses a keystroke with no observable postcondition; supported keys are %s",
			strings.Join(SupportedKeys(), ", ")),
	}}
}

func planSelectionStep(position int, step Step) []MissingControl {
	control := controlMode
	if step.Type == StepModel {
		control = controlModel
	}
	if strings.TrimSpace(step.Value) == "" {
		return []MissingControl{{
			Index: position, Step: step.Type, Control: control,
			Reason: "the step names no menu entry to select",
		}}
	}
	return nil
}
