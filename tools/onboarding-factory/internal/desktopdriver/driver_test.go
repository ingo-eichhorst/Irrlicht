package desktopdriver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRuntime struct {
	failAt         string
	omitArchive    bool
	omitRestore    bool
	provisional    string
	sessionCreated bool
	sessionActive  bool
	configChanged  bool
	removalID      string
	steps          []string
}

func (runtime *fakeRuntime) step(name string) error {
	runtime.steps = append(runtime.steps, name)
	if runtime.failAt == name {
		return errors.New("injected " + name + " failure")
	}
	return nil
}

func (runtime *fakeRuntime) Preflight(context.Context) (Versions, error) {
	return Versions{DesktopApp: "1.2.3", ClaudeCode: "2.3.4", Irrlicht: "0.7.0"}, runtime.step("preflight")
}

func (runtime *fakeRuntime) CaptureBaseline(context.Context) (Baseline, error) {
	return Baseline{SessionIDs: map[string]struct{}{"local_existing": {}}}, runtime.step("baseline")
}

func (runtime *fakeRuntime) OpenComposer(context.Context, string) error {
	runtime.sessionCreated = true
	runtime.sessionActive = true
	runtime.configChanged = true
	return runtime.step("open")
}

func (runtime *fakeRuntime) WaitComposer(context.Context, string) error {
	return runtime.step("composer")
}

func (runtime *fakeRuntime) WaitOwnedSession(context.Context, Baseline, string) (OwnedSession, error) {
	if err := runtime.step("owned"); err != nil {
		return OwnedSession{}, err
	}
	return fakeOwnedSession(), nil
}

func (runtime *fakeRuntime) RecoverOwnedSession(context.Context, Baseline, string) (OwnedSession, error) {
	runtime.steps = append(runtime.steps, "recover_owned")
	if !runtime.sessionCreated {
		return OwnedSession{}, nil
	}
	owned := fakeOwnedSession()
	if runtime.provisional != "" {
		owned.Transcript = TranscriptIdentity{}
	}
	if runtime.provisional == "no-cli" {
		owned.Registry.CLISessionID = ""
	}
	return owned, nil
}

func (runtime *fakeRuntime) SetPrompt(context.Context, string) error {
	return runtime.step("set_prompt")
}

func (runtime *fakeRuntime) Submit(context.Context) error { return runtime.step("submit") }

func (runtime *fakeRuntime) WaitIrrlichtState(_ context.Context, _ OwnedSession, state string) (SessionObservation, error) {
	if err := runtime.step("state_" + state); err != nil {
		return SessionObservation{}, err
	}
	return SessionObservation{
		SessionID: fakeOwnedSession().Transcript.SessionID,
		CWD:       "/repo/workspace",
		PID:       4321,
		State:     state,
		Launcher:  Launcher{HostBundleID: desktopBundleID},
	}, nil
}

func (runtime *fakeRuntime) WaitHook(context.Context, OwnedSession) error {
	return runtime.step("hook")
}

func (runtime *fakeRuntime) CaptureEvidence(_ context.Context, owned OwnedSession, observation SessionObservation, _ string) (CapturedEvidence, error) {
	if err := runtime.step("evidence"); err != nil {
		return CapturedEvidence{}, err
	}
	return CapturedEvidence{Registry: owned.Registry, IrrlichtSession: observation}, nil
}

func (runtime *fakeRuntime) ArchiveOwned(_ context.Context, owned OwnedSession) error {
	if err := runtime.step("archive_" + owned.Registry.SessionID); err != nil {
		return err
	}
	if !runtime.omitArchive {
		runtime.sessionActive = false
	}
	return nil
}

func (runtime *fakeRuntime) WaitProcessExit(context.Context, OwnedSession) error {
	if runtime.sessionActive {
		return errors.New("owned process is still live")
	}
	return runtime.step("process_gone")
}

func (runtime *fakeRuntime) WaitIrrlichtRemoved(_ context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("Irrlicht removal wait received a blank session ID")
	}
	runtime.removalID = sessionID
	if runtime.sessionActive {
		return errors.New("owned Irrlicht session is still present")
	}
	return runtime.step("irrlicht_removed")
}

func (runtime *fakeRuntime) VerifyBaseline(context.Context, Baseline, OwnedSession) error {
	runtime.steps = append(runtime.steps, "verify_baseline")
	if !runtime.omitRestore {
		runtime.configChanged = false
	}
	if runtime.configChanged {
		return errors.New("configuration bytes differ from baseline")
	}
	if runtime.sessionActive {
		return errors.New("owned session was not archived")
	}
	return nil
}

func (runtime *fakeRuntime) RecordStep(step string) { runtime.steps = append(runtime.steps, step) }

func fakeOwnedSession() OwnedSession {
	registry, transcript := validOwnedSession()
	return OwnedSession{Registry: registry, Transcript: transcript}
}

func validRunRequest() RunRequest {
	return RunRequest{
		Workspace:      "/repo/workspace",
		Prompt:         "Reply with exactly the word: ok",
		EvidenceDir:    "/repo/evidence",
		OverallTimeout: time.Second,
		StepTimeout:    100 * time.Millisecond,
		CleanupTimeout: 100 * time.Millisecond,
	}
}

func TestRunDrivesReadyWorkingReadyAndCleansOwnedSession(t *testing.T) {
	runtime := &fakeRuntime{}
	result, err := Run(context.Background(), runtime, validRunRequest())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Owned.Registry.SessionID != "local_new" {
		t.Fatalf("owned session = %+v", result.Owned)
	}
	wantOrder := []string{
		"preflight", "baseline", "cleanup_armed", "open", "composer", "owned",
		"state_ready", "set_prompt", "submit", "state_working", "hook",
		"state_ready", "evidence", "cleanup_started", "archive_local_new",
		"process_gone", "irrlicht_removed", "verify_baseline", "cleanup_finished",
	}
	if strings.Join(runtime.steps, "\n") != strings.Join(wantOrder, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(runtime.steps, "\n"), strings.Join(wantOrder, "\n"))
	}
}

func TestRunRestoresAndArchivesAfterEveryExitClass(t *testing.T) {
	for _, failAt := range []string{"composer", "set_prompt", "submit", "state_working", "hook", "evidence"} {
		t.Run(failAt, func(t *testing.T) {
			runtime := &fakeRuntime{failAt: failAt}
			_, err := Run(context.Background(), runtime, validRunRequest())
			if err == nil || !strings.Contains(err.Error(), "injected "+failAt+" failure") {
				t.Fatalf("Run() error = %v", err)
			}
			if runtime.sessionActive || runtime.configChanged {
				t.Fatalf("cleanup state: active=%t configChanged=%t", runtime.sessionActive, runtime.configChanged)
			}
			if !containsStep(runtime.steps, "archive_local_new") || !containsStep(runtime.steps, "verify_baseline") {
				t.Fatalf("cleanup steps missing: %v", runtime.steps)
			}
		})
	}
}

func TestRunRestoresAfterTimeoutAndInterruption(t *testing.T) {
	tests := []struct {
		name    string
		runtime Runtime
		state   *fakeRuntime
	}{
		{"timeout", &blockingRuntime{fakeRuntime: fakeRuntime{}}, nil},
		{"interrupt", &interruptedRuntime{fakeRuntime: fakeRuntime{}}, nil},
	}
	for index := range tests {
		switch runtime := tests[index].runtime.(type) {
		case *blockingRuntime:
			tests[index].state = &runtime.fakeRuntime
		case *interruptedRuntime:
			tests[index].state = &runtime.fakeRuntime
		}
		t.Run(tests[index].name, func(t *testing.T) {
			request := validRunRequest()
			request.StepTimeout = time.Millisecond
			_, err := Run(context.Background(), tests[index].runtime, request)
			if err == nil {
				t.Fatal("Run() returned nil error")
			}
			if tests[index].state.sessionActive || tests[index].state.configChanged {
				t.Fatalf(
					"cleanup state: active=%t configChanged=%t",
					tests[index].state.sessionActive,
					tests[index].state.configChanged,
				)
			}
		})
	}
}

type blockingRuntime struct{ fakeRuntime }

func (runtime *blockingRuntime) WaitComposer(ctx context.Context, _ string) error {
	runtime.steps = append(runtime.steps, "composer")
	<-ctx.Done()
	return ctx.Err()
}

type interruptedRuntime struct{ fakeRuntime }

func (runtime *interruptedRuntime) WaitComposer(context.Context, string) error {
	runtime.steps = append(runtime.steps, "composer")
	return context.Canceled
}

func TestCleanupMutationFixturesFailLoudly(t *testing.T) {
	tests := []struct {
		name    string
		runtime *fakeRuntime
		want    string
	}{
		{"archive omitted", &fakeRuntime{omitArchive: true}, "owned process is still live"},
		{"restoration omitted", &fakeRuntime{omitRestore: true}, "configuration bytes differ from baseline"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Run(context.Background(), test.runtime, validRunRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v; want mutation detection %q", err, test.want)
			}
		})
	}
}

func TestCleanupUsesRegistryCLIIdentityBeforeTranscriptAppears(t *testing.T) {
	runtime := &fakeRuntime{failAt: "composer", provisional: "with-cli"}
	_, err := Run(context.Background(), runtime, validRunRequest())
	if err == nil {
		t.Fatal("Run() returned nil error")
	}
	if runtime.removalID != "cli-new" {
		t.Fatalf("Irrlicht removal ID = %q, want registry cliSessionId", runtime.removalID)
	}
}

func TestCleanupDoesNotWaitOnBlankProvisionalCLIIdentity(t *testing.T) {
	runtime := &fakeRuntime{failAt: "composer", provisional: "no-cli"}
	_, err := Run(context.Background(), runtime, validRunRequest())
	if err == nil {
		t.Fatal("Run() returned nil error")
	}
	if runtime.removalID != "" || containsStep(runtime.steps, "irrlicht_removed") {
		t.Fatalf("blank identity reached removal wait: id=%q steps=%v", runtime.removalID, runtime.steps)
	}
	if !containsStep(runtime.steps, "archive_local_new") {
		t.Fatalf("provisional Desktop ID was not archived: %v", runtime.steps)
	}
}

func TestRunStepNamesItsDeadline(t *testing.T) {
	err := runStep(context.Background(), time.Millisecond, "registry identity", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "wait for registry identity timed out after 1ms") {
		t.Fatalf("runStep() error = %v", err)
	}
}

func containsStep(steps []string, want string) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}

// A step that times out must still say what it last saw. Reporting the deadline
// alone discarded the operation's own error, which is the only thing that
// distinguishes a composer that never opened from one that opened wrong.
func TestRunStepKeepsTheOperationErrorOnTimeout(t *testing.T) {
	err := runStep(context.Background(), time.Millisecond, "composer controls",
		func(step context.Context) error {
			<-step.Done()
			return errors.New("last Desktop composer observation: project chip said No folder")
		})
	if err == nil {
		t.Fatal("runStep() returned nil for a timed-out step")
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("error does not name the deadline: %v", err)
	}
	if !strings.Contains(err.Error(), "No folder") {
		t.Fatalf("error discarded the last observation: %v", err)
	}
}
