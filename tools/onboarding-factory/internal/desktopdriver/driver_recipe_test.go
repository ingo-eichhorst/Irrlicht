package desktopdriver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func recipeRunRequest(script []Step) RunRequest {
	request := validRunRequest()
	request.Prompt = ""
	request.Script = script
	return request
}

// A recipe the Desktop driver cannot drive must cost NOTHING: no preflight, no
// baseline, no deep link, no composer. This is #1888's "a missing primitive
// fails before Claude Desktop is changed", measured as zero Runtime calls.
func TestRunRefusesAnUndrivableRecipeBeforeTouchingDesktop(t *testing.T) {
	runtime := &fakeRuntime{}
	_, err := Run(context.Background(), runtime, recipeRunRequest([]Step{
		{Type: StepSend, Text: "hello"},
		{Type: "reset_session"},
	}))
	var notRunnable *NotRunnableError
	if !errors.As(err, &notRunnable) {
		t.Fatalf("Run() error = %v, want *NotRunnableError", err)
	}
	if !strings.Contains(err.Error(), "session-reset") {
		t.Fatalf("Run() did not name the missing control: %v", err)
	}
	if len(runtime.steps) != 0 {
		t.Fatalf("a refused recipe reached Claude Desktop: %v", runtime.steps)
	}
	if runtime.sessionCreated {
		t.Fatal("a refused recipe created a Desktop session")
	}
}

// The one-turn form is expressed as a recipe internally. Its Runtime call
// sequence must stay byte-identical to what #1887 drove, or every existing
// Desktop fixture changes meaning.
func TestOneTurnPromptDrivesTheSameSequenceAsItsRecipeForm(t *testing.T) {
	prompted := &fakeRuntime{}
	if _, err := Run(context.Background(), prompted, validRunRequest()); err != nil {
		t.Fatalf("Run(prompt) error = %v", err)
	}
	scripted := &fakeRuntime{}
	request := recipeRunRequest([]Step{
		{Type: StepSend, Text: "Reply with exactly the word: ok"},
		{Type: StepWaitTurn},
	})
	if _, err := Run(context.Background(), scripted, request); err != nil {
		t.Fatalf("Run(script) error = %v", err)
	}
	if strings.Join(prompted.steps, "\n") != strings.Join(scripted.steps, "\n") {
		t.Fatalf("prompt form:\n%s\nrecipe form:\n%s",
			strings.Join(prompted.steps, "\n"), strings.Join(scripted.steps, "\n"))
	}
}

func TestRunDrivesEveryElicitedStepThroughItsControl(t *testing.T) {
	runtime := &fakeRuntime{}
	request := recipeRunRequest([]Step{
		{Type: StepSend, Text: "one"},
		{Type: StepWaitTurn},
		{Type: StepSleep, Seconds: 2},
		{Type: StepSend, Text: "two"},
		{Type: StepInterrupt},
		{Type: StepKeys, Keys: "Escape"},
		{Type: StepMode, Value: "Plan"},
		{Type: StepModel, Value: "Opus 5"},
		{Type: StepWaitTurn},
	})
	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Named individually rather than as a contains-set: the ORDER is what the
	// recipe asked for, and a driver that reordered steps would still contain
	// them all.
	want := []string{
		"preflight", "baseline", "cleanup_armed", "open", "composer", "owned",
		"state_ready", "set_prompt", "submit", "state_working",
		"hook", "state_ready",
		"sleep_2s",
		"state_ready", "set_prompt", "submit", "state_working",
		"interrupt", "state_ready",
		"key_Escape",
		"mode_Plan", "model_Opus 5",
		"state_ready",
		"evidence",
		"cleanup_started", "archive_local_new", "process_gone", "irrlicht_removed",
		"verify_baseline", "cleanup_finished",
	}
	if strings.Join(runtime.steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(runtime.steps, "\n"), strings.Join(want, "\n"))
	}
}

// The hook proves Claude Code reached the recording daemon. It is a
// once-per-session fact, so a two-turn recipe must not wait for it twice — and
// the second turn must still wait for the turn to close.
func TestWaitTurnWaitsForTheHookOncePerSession(t *testing.T) {
	runtime := &fakeRuntime{}
	request := recipeRunRequest([]Step{
		{Type: StepSend, Text: "one"}, {Type: StepWaitTurn},
		{Type: StepSend, Text: "two"}, {Type: StepWaitTurn},
	})
	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	hooks := 0
	for _, step := range runtime.steps {
		if step == "hook" {
			hooks++
		}
	}
	if hooks != 1 {
		t.Fatalf("hook waits = %d, want 1: %v", hooks, runtime.steps)
	}
}

func TestArchiveStepIsNotRepeatedByTeardown(t *testing.T) {
	runtime := &fakeRuntime{}
	request := recipeRunRequest([]Step{
		{Type: StepSend, Text: "one"}, {Type: StepWaitTurn}, {Type: StepArchive},
	})
	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	archives := 0
	for _, step := range runtime.steps {
		if step == "archive_local_new" {
			archives++
		}
	}
	if archives != 1 {
		t.Fatalf("archive calls = %d, want 1: %v", archives, runtime.steps)
	}
}

// #1888: a multi-session run keeps Desktop session IDs, transcript IDs,
// workspaces and Irrlicht session IDs apart, and archives BOTH sessions.
func TestMultiSessionRunKeepsEveryIdentityApartAndArchivesBoth(t *testing.T) {
	runtime := &fakeRuntime{}
	request := recipeRunRequest([]Step{
		{Type: StepSend, Text: "alpha"},
		{Type: StepWaitTurn},
		{Type: StepStartSession, CWD: "/repo/second-workspace"},
		{Type: StepSend, Text: "bravo"},
		{Type: StepWaitTurn},
	})
	result, err := Run(context.Background(), runtime, request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Sessions) != 2 {
		t.Fatalf("session records = %+v, want 2", result.Sessions)
	}
	first, second := result.Sessions[0], result.Sessions[1]
	pairs := []struct {
		name        string
		left, right string
	}{
		{"desktop session ID", first.DesktopSessionID, second.DesktopSessionID},
		{"transcript ID", first.TranscriptID, second.TranscriptID},
		{"workspace", first.Workspace, second.Workspace},
		{"Irrlicht session ID", first.IrrlichtSessionID, second.IrrlichtSessionID},
	}
	for _, pair := range pairs {
		if pair.left == "" || pair.right == "" {
			t.Fatalf("%s is blank on one side: %q vs %q", pair.name, pair.left, pair.right)
		}
		if pair.left == pair.right {
			t.Fatalf("%s is shared between the two sessions: %q", pair.name, pair.left)
		}
	}
	if first.Workspace != "/repo/workspace" || second.Workspace != "/repo/second-workspace" {
		t.Fatalf("workspaces = %q, %q", first.Workspace, second.Workspace)
	}
	// Evidence stays on the FIRST session, and both are archived newest first.
	if result.Owned.Registry.SessionID != first.DesktopSessionID {
		t.Fatalf("evidence subject = %q, want the first session", result.Owned.Registry.SessionID)
	}
	if !containsStep(runtime.steps, "archive_local_new") ||
		!containsStep(runtime.steps, "archive_local_new2") {
		t.Fatalf("both sessions were not archived: %v", runtime.steps)
	}
	if indexOfStep(runtime.steps, "archive_local_new2") > indexOfStep(runtime.steps, "archive_local_new") {
		t.Fatalf("teardown archived the older session first: %v", runtime.steps)
	}
	if runtime.sessionActive() {
		t.Fatal("teardown left a session live")
	}
}

// A run that fails AFTER starting its second session must still archive both.
// The teardown list grows as sessions are adopted, so this is the case that
// proves it is not pinned to the first.
func TestFailureAfterStartSessionStillArchivesEverySessionThisRunCreated(t *testing.T) {
	runtime := &fakeRuntime{failAt: "mode_Plan"}
	request := recipeRunRequest([]Step{
		{Type: StepStartSession},
		{Type: StepMode, Value: "Plan"},
	})
	_, err := Run(context.Background(), runtime, request)
	if err == nil || !strings.Contains(err.Error(), "injected mode_Plan failure") {
		t.Fatalf("Run() error = %v", err)
	}
	if !containsStep(runtime.steps, "archive_local_new") ||
		!containsStep(runtime.steps, "archive_local_new2") {
		t.Fatalf("a failed multi-session run left a session behind: %v", runtime.steps)
	}
}

// Adopting one Desktop identity twice would make teardown archive it twice and
// the identity table claim two sessions ran. A second composer that resolves to
// the SAME registry row is a refusal, not a second slot.
func TestAdoptingTheSameDesktopSessionTwiceIsRefused(t *testing.T) {
	runtime := &duplicateSessionRuntime{}
	_, err := Run(context.Background(), runtime, recipeRunRequest([]Step{{Type: StepStartSession}}))
	if err == nil || !strings.Contains(err.Error(), "cannot own one session twice") {
		t.Fatalf("Run() error = %v; want a duplicate-adoption refusal", err)
	}
}

type duplicateSessionRuntime struct{ fakeRuntime }

func (runtime *duplicateSessionRuntime) WaitOwnedSession(
	context.Context, Baseline, string,
) (OwnedSession, error) {
	if err := runtime.step("owned"); err != nil {
		return OwnedSession{}, err
	}
	owned := fakeOwnedSession()
	if runtime.transcriptOwner == nil {
		runtime.transcriptOwner = map[string]string{}
	}
	runtime.transcriptOwner[owned.Transcript.SessionID] = owned.Registry.SessionID
	return owned, nil
}

// Ownership selection refuses more than one post-baseline session, so a second
// start_session only works if the first one's ID has been folded into the
// baseline the selector is handed. Mutating that fold out is what this pins.
func TestStartSessionHandsOwnershipSelectionTheGrownBaseline(t *testing.T) {
	runtime := &baselineRecordingRuntime{}
	request := recipeRunRequest([]Step{
		{Type: StepSend, Text: "alpha"}, {Type: StepWaitTurn}, {Type: StepStartSession},
	})
	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runtime.seen) != 2 {
		t.Fatalf("ownership selection ran %d times, want 2", len(runtime.seen))
	}
	if _, claimed := runtime.seen[0]["local_new"]; claimed {
		t.Fatal("the first session's ID was in the baseline before it existed")
	}
	if _, claimed := runtime.seen[1]["local_new"]; !claimed {
		t.Fatalf("the second selection did not see the first session as claimed: %v", runtime.seen[1])
	}
}

type baselineRecordingRuntime struct {
	fakeRuntime
	seen []map[string]struct{}
}

func (runtime *baselineRecordingRuntime) WaitOwnedSession(
	ctx context.Context, baseline Baseline, workspace string,
) (OwnedSession, error) {
	runtime.seen = append(runtime.seen, cloneSessionIDs(baseline.SessionIDs))
	return runtime.fakeRuntime.WaitOwnedSession(ctx, baseline, workspace)
}

// AGENTS.md: a verification mechanism must fail loudly when it cannot run. An
// elicited step type with no executor arm is planned as runnable, so the
// executor is the last place that can catch it — and it must stop the run, not
// skip the step.
func TestExecutorRefusesAPlannedStepItCannotDrive(t *testing.T) {
	runner := &scriptRunner{
		runtime: &fakeRuntime{},
		request: recipeRunRequest([]Step{{Type: StepSend, Text: "x"}}),
		cleanup: &cleanupOwner{},
	}
	err := runner.runOne(context.Background(), Step{Type: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "has no arm for step type") {
		t.Fatalf("runOne() error = %v; want a loud refusal", err)
	}
}

func TestRunValidatesTheRequestShape(t *testing.T) {
	tests := []struct {
		name    string
		request RunRequest
		want    string
	}{
		{"both forms", RunRequest{
			Workspace: "/w", Prompt: "hi", Script: []Step{{Type: StepWaitTurn}},
			EvidenceDir: "/e", OverallTimeout: time.Second,
			StepTimeout: time.Second, CleanupTimeout: time.Second,
		}, "exactly one of prompt and recipe script"},
		{"neither form", RunRequest{
			Workspace: "/w", EvidenceDir: "/e", OverallTimeout: time.Second,
			StepTimeout: time.Second, CleanupTimeout: time.Second,
		}, "exactly one of prompt and recipe script"},
		{"no workspace", RunRequest{
			Prompt: "hi", EvidenceDir: "/e", OverallTimeout: time.Second,
			StepTimeout: time.Second, CleanupTimeout: time.Second,
		}, "workspace and evidence directory are required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &fakeRuntime{}
			_, err := Run(context.Background(), runtime, test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v; want %q", err, test.want)
			}
			if len(runtime.steps) != 0 {
				t.Fatalf("a malformed request reached Claude Desktop: %v", runtime.steps)
			}
		})
	}
}

func indexOfStep(steps []string, want string) int {
	for index, step := range steps {
		if step == want {
			return index
		}
	}
	return -1
}
