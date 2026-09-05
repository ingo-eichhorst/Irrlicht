package desktopdriver

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const desktopBundleID = "com.anthropic.claudefordesktop"

// Versions records the three binaries that produced Desktop evidence.
type Versions struct {
	DesktopApp string `json:"desktop_app"`
	ClaudeCode string `json:"claude_code"`
	Irrlicht   string `json:"irrlicht"`
}

// SessionObservation is the Irrlicht view used for state and identity checks.
type SessionObservation struct {
	SessionID string         `json:"session_id"`
	CWD       string         `json:"cwd"`
	PID       int            `json:"pid"`
	State     string         `json:"state"`
	Launcher  Launcher       `json:"launcher"`
	Raw       map[string]any `json:"-"`
}

type Launcher struct {
	HostBundleID string `json:"host_bundle_id"`
}

// ProcessEvidence is a stable subset of the live process observation.
type ProcessEvidence struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// CapturedEvidence is written before the owned session is archived.
type CapturedEvidence struct {
	Registry        RegistrySession
	TranscriptPath  string
	IrrlichtSession SessionObservation
	Process         ProcessEvidence
	Environment     EnvironmentEvidence
}

// EnvironmentEvidence records only the verified Local/project identity. The
// project title is used for validation and is not written into replaydata.
type EnvironmentEvidence struct {
	SelectedEnvironment string `json:"selected_environment"`
	RequestedWorkspace  string `json:"requested_workspace"`
	Project             string `json:"-"`
}

type Baseline struct {
	SessionIDs map[string]struct{}
	Files      map[string][]byte
	Config     TreeSnapshot
	Processes  map[int]struct{}
}

type RunRequest struct {
	Workspace string
	// Prompt drives the one-turn form. Exactly one of Prompt and Script is set.
	Prompt string
	// Script drives the recipe form. Its grammar is recipe.go's.
	Script         []Step
	EvidenceDir    string
	OverallTimeout time.Duration
	StepTimeout    time.Duration
	CleanupTimeout time.Duration
}

// SessionRecord is one live Desktop session this run created. Every identity a
// multi-session run must keep apart is here, one row per session: the Desktop
// registry ID, the Claude Code transcript ID, the workspace, and the Irrlicht
// session ID the daemon reports.
type SessionRecord struct {
	Slot              int    `json:"slot"`
	DesktopSessionID  string `json:"desktop_session_id"`
	TranscriptID      string `json:"transcript_id"`
	Workspace         string `json:"workspace"`
	IrrlichtSessionID string `json:"irrlicht_session_id"`
}

type RunResult struct {
	// Owned is the FIRST session the run created. It stays the evidence subject
	// so a one-turn run's result is byte-identical to what it was before
	// multi-session recipes existed.
	Owned OwnedSession
	// Sessions carries every session the run created, in creation order.
	Sessions []SessionRecord
	Versions Versions
	Evidence CapturedEvidence
}

// Runtime is the live Desktop boundary. Tests inject it so no test can touch
// Claude Desktop or a user's session registry.
type Runtime interface {
	Preflight(context.Context) (Versions, error)
	CaptureBaseline(context.Context) (Baseline, error)
	OpenComposer(context.Context, string) error
	WaitComposer(context.Context, string) error
	WaitOwnedSession(context.Context, Baseline, string) (OwnedSession, error)
	RecoverOwnedSession(context.Context, Baseline, string) (OwnedSession, error)
	SetPrompt(context.Context, string) error
	Submit(context.Context) error
	// Interrupt stops an in-flight turn through the composer's Stop control.
	Interrupt(context.Context) error
	// PressKey sends one raw keystroke. The key name is one of SupportedKeys().
	PressKey(context.Context, string) error
	// SelectMode and SelectModel drive the composer's two popup menus.
	SelectMode(context.Context, string) error
	SelectModel(context.Context, string) error
	// Sleep is the recipe's `sleep` step. It is on the boundary so a test never
	// waits in real time.
	Sleep(context.Context, time.Duration) error
	WaitIrrlichtState(context.Context, OwnedSession, string) (SessionObservation, error)
	WaitHook(context.Context, OwnedSession) error
	CaptureEvidence(context.Context, OwnedSession, SessionObservation, string) (CapturedEvidence, error)
	ArchiveOwned(context.Context, OwnedSession) error
	WaitProcessExit(context.Context, OwnedSession) error
	WaitIrrlichtRemoved(context.Context, string) error
	// VerifyBaseline proves every post-baseline Desktop session is one of the
	// ones this run created, and that each was archived.
	VerifyBaseline(context.Context, Baseline, []OwnedSession) error
	RecordStep(string)
}

// Run drives one Desktop recipe. The recipe is planned BEFORE any Runtime call,
// so a recipe needing a control the driver cannot elicit costs no Desktop
// session. Cleanup is armed before the deep link. It runs with an independent
// deadline after success, failure, timeout, or a caller signal.
func Run(ctx context.Context, runtime Runtime, request RunRequest) (result RunResult, err error) {
	script, err := effectiveScript(request)
	if err != nil {
		return result, err
	}
	// Deliberately the first thing that happens. `not runnable through Desktop`
	// must reach the caller with Claude Desktop untouched — no preflight, no
	// baseline, no deep link.
	if err := Plan(script); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, request.OverallTimeout)
	defer cancel()

	result.Versions, err = runtime.Preflight(ctx)
	if err != nil {
		return result, fmt.Errorf("Desktop preflight: %w", err)
	}
	baseline, err := runtime.CaptureBaseline(ctx)
	if err != nil {
		return result, fmt.Errorf("capture Desktop baseline: %w", err)
	}

	cleanup := &cleanupOwner{runtime: runtime, request: request, baseline: baseline}
	runtime.RecordStep("cleanup_armed")
	defer func() {
		err = errors.Join(err, cleanup.run())
	}()

	runner := &scriptRunner{
		runtime: runtime, request: request, cleanup: cleanup,
		claimed: cloneSessionIDs(baseline.SessionIDs), baseline: baseline,
	}
	err = runner.drive(ctx, script)
	result.Owned = runner.primary()
	result.Sessions = runner.records()
	if err != nil {
		return result, err
	}
	result.Evidence, err = runner.captureEvidence(ctx)
	return result, err
}

// effectiveScript turns the one-turn form into the recipe that expresses it, so
// there is one execution path rather than two. The expansion is exactly the
// sequence #1887 drove: type the prompt, send it, wait for the turn to close.
func effectiveScript(request RunRequest) ([]Step, error) {
	if err := validateRunRequest(request); err != nil {
		return nil, err
	}
	if len(request.Script) > 0 {
		return request.Script, nil
	}
	return []Step{{Type: StepSend, Text: request.Prompt}, {Type: StepWaitTurn}}, nil
}

func validateRunRequest(request RunRequest) error {
	if request.Workspace == "" || request.EvidenceDir == "" {
		return errors.New("workspace and evidence directory are required")
	}
	if (request.Prompt == "") == (len(request.Script) == 0) {
		return errors.New("exactly one of prompt and recipe script is required")
	}
	if request.OverallTimeout <= 0 || request.StepTimeout <= 0 || request.CleanupTimeout <= 0 {
		return errors.New("all Desktop driver deadlines must be positive")
	}
	return nil
}

// ownedSlot is one live session the run created, plus the per-session facts the
// executor needs: which workspace it was opened for, whether its Claude Code
// hook has been seen, and the last Irrlicht observation of it.
type ownedSlot struct {
	owned       OwnedSession
	workspace   string
	hookSeen    bool
	observation SessionObservation
}

type scriptRunner struct {
	runtime  Runtime
	request  RunRequest
	cleanup  *cleanupOwner
	baseline Baseline
	// claimed grows as each session is adopted, so ownership selection sees
	// exactly one post-baseline session per start_session rather than refusing
	// the second as concurrent creation.
	claimed map[string]struct{}
	slots   []*ownedSlot
	active  int
}

func cloneSessionIDs(ids map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(ids))
	for id := range ids {
		clone[id] = struct{}{}
	}
	return clone
}

func (runner *scriptRunner) primary() OwnedSession {
	if len(runner.slots) == 0 {
		return OwnedSession{}
	}
	return runner.slots[0].owned
}

func (runner *scriptRunner) records() []SessionRecord {
	records := make([]SessionRecord, 0, len(runner.slots))
	for index, slot := range runner.slots {
		records = append(records, SessionRecord{
			Slot:              index + 1,
			DesktopSessionID:  slot.owned.Registry.SessionID,
			TranscriptID:      slot.owned.Transcript.SessionID,
			Workspace:         slot.workspace,
			IrrlichtSessionID: slot.observation.SessionID,
		})
	}
	return records
}

func (runner *scriptRunner) current() (*ownedSlot, error) {
	if runner.active >= len(runner.slots) {
		return nil, errors.New("no live Desktop session is selected")
	}
	return runner.slots[runner.active], nil
}

// drive runs every step in order. There is no arm that does nothing: a step
// type without a case here fails the run rather than being skipped.
func (runner *scriptRunner) drive(ctx context.Context, script []Step) error {
	if err := runner.startSession(ctx, runner.request.Workspace); err != nil {
		return err
	}
	for index, step := range script {
		if err := runner.runOne(ctx, step); err != nil {
			return fmt.Errorf("Desktop recipe step %d (%s): %w", index+1, step.Type, err)
		}
	}
	return nil
}

func (runner *scriptRunner) runOne(ctx context.Context, step Step) error {
	switch step.Type {
	case StepSend:
		return runner.send(ctx, step.Text)
	case StepWaitTurn:
		return runner.waitTurn(ctx)
	case StepSleep:
		return runner.runtime.Sleep(ctx, time.Duration(step.Seconds*float64(time.Second)))
	case StepInterrupt:
		return runner.interrupt(ctx)
	case StepKeys:
		return runner.pressKey(ctx, step.Keys)
	case StepMode:
		return runner.runtime.SelectMode(ctx, step.Value)
	case StepModel:
		return runner.runtime.SelectModel(ctx, step.Value)
	case StepArchive:
		return runner.archive(ctx)
	case StepStartSession:
		workspace := step.CWD
		if workspace == "" {
			workspace = runner.request.Workspace
		}
		return runner.startSession(ctx, workspace)
	default:
		// Plan refuses an unknown step type before the run starts, so reaching
		// here means the grammar and the executor disagree. Fail loudly: a
		// silent skip is the exact outcome #1888 forbids.
		return fmt.Errorf("the Desktop executor has no arm for step type %q; it was planned as runnable but cannot be driven", step.Type)
	}
}

func (runner *scriptRunner) startSession(ctx context.Context, workspace string) error {
	if err := runner.runtime.OpenComposer(ctx, workspace); err != nil {
		return fmt.Errorf("open Desktop composer: %w", err)
	}
	if err := runStep(ctx, runner.request.StepTimeout, "composer controls", func(step context.Context) error {
		return runner.runtime.WaitComposer(step, workspace)
	}); err != nil {
		return err
	}
	var owned OwnedSession
	if err := runStep(ctx, runner.request.StepTimeout, "one owned Desktop session", func(step context.Context) error {
		var err error
		owned, err = runner.runtime.WaitOwnedSession(step, runner.claimedBaseline(), workspace)
		return err
	}); err != nil {
		return err
	}
	// No readiness wait here on purpose: `send` opens with one, so a one-turn
	// run makes exactly the same sequence of Runtime calls it made before
	// recipes existed. A recipe that never sends never needed the session live.
	return runner.adopt(owned, workspace)
}

// adopt records a newly created session and refuses to record the same Desktop
// identity twice — two slots naming one registry row would make the teardown
// archive it twice and the identity table lie about how many sessions ran.
func (runner *scriptRunner) adopt(owned OwnedSession, workspace string) error {
	for index, slot := range runner.slots {
		if slot.owned.Registry.SessionID == owned.Registry.SessionID {
			return fmt.Errorf(
				"Desktop session %q was already adopted as slot %d; the driver cannot own one session twice",
				owned.Registry.SessionID, index+1)
		}
		if owned.Transcript.SessionID != "" && slot.owned.Transcript.SessionID == owned.Transcript.SessionID {
			return fmt.Errorf(
				"Claude transcript %q was already adopted as slot %d",
				owned.Transcript.SessionID, index+1)
		}
	}
	runner.slots = append(runner.slots, &ownedSlot{owned: owned, workspace: workspace})
	runner.active = len(runner.slots) - 1
	runner.claimed[owned.Registry.SessionID] = struct{}{}
	runner.cleanup.adopt(owned)
	return nil
}

func (runner *scriptRunner) claimedBaseline() Baseline {
	baseline := runner.baseline
	baseline.SessionIDs = runner.claimed
	return baseline
}

func (runner *scriptRunner) send(ctx context.Context, text string) error {
	if _, err := runner.waitState(ctx, "ready"); err != nil {
		return err
	}
	if err := runner.runtime.SetPrompt(ctx, text); err != nil {
		return fmt.Errorf("set Desktop prompt: %w", err)
	}
	if err := runner.runtime.Submit(ctx); err != nil {
		return fmt.Errorf("submit Desktop prompt: %w", err)
	}
	_, err := runner.waitState(ctx, "working")
	return err
}

func (runner *scriptRunner) waitTurn(ctx context.Context) error {
	slot, err := runner.current()
	if err != nil {
		return err
	}
	// The hook proves Claude Code reached this daemon at all. It is a
	// once-per-session fact, so a later turn does not re-wait for it.
	if !slot.hookSeen {
		if err := runStep(ctx, runner.request.StepTimeout, "Claude Code hook", func(step context.Context) error {
			return runner.runtime.WaitHook(step, slot.owned)
		}); err != nil {
			return err
		}
		slot.hookSeen = true
	}
	_, err = runner.waitState(ctx, "ready")
	return err
}

func (runner *scriptRunner) interrupt(ctx context.Context) error {
	if err := runner.runtime.Interrupt(ctx); err != nil {
		return fmt.Errorf("interrupt the in-flight Desktop turn: %w", err)
	}
	_, err := runner.waitState(ctx, "ready")
	return err
}

func (runner *scriptRunner) pressKey(ctx context.Context, key string) error {
	if _, supported := desktopKeys[key]; !supported {
		return fmt.Errorf("key %q has no observable Desktop postcondition", key)
	}
	return runner.runtime.PressKey(ctx, key)
}

func (runner *scriptRunner) archive(ctx context.Context) error {
	slot, err := runner.current()
	if err != nil {
		return err
	}
	if err := runner.runtime.ArchiveOwned(ctx, slot.owned); err != nil {
		return fmt.Errorf("archive owned session %q: %w", slot.owned.Registry.SessionID, err)
	}
	runner.cleanup.markArchived(slot.owned.Registry.SessionID)
	return nil
}

func (runner *scriptRunner) waitState(ctx context.Context, state string) (SessionObservation, error) {
	slot, err := runner.current()
	if err != nil {
		return SessionObservation{}, err
	}
	observation, err := waitForState(ctx, runner.runtime, runner.request.StepTimeout, slot.owned, state)
	if err == nil {
		slot.observation = observation
	}
	return observation, err
}

func (runner *scriptRunner) captureEvidence(ctx context.Context) (CapturedEvidence, error) {
	if len(runner.slots) == 0 {
		return CapturedEvidence{}, errors.New("the Desktop run owns no session to capture evidence from")
	}
	primary := runner.slots[0]
	if primary.observation.SessionID == "" {
		return CapturedEvidence{}, errors.New("the Desktop run never observed its first session through Irrlicht")
	}
	evidence, err := runner.runtime.CaptureEvidence(
		ctx, primary.owned, primary.observation, runner.request.EvidenceDir)
	if err != nil {
		return CapturedEvidence{}, fmt.Errorf("capture Desktop evidence: %w", err)
	}
	return evidence, nil
}

func waitForState(
	ctx context.Context,
	runtime Runtime,
	timeout time.Duration,
	owned OwnedSession,
	state string,
) (SessionObservation, error) {
	var observation SessionObservation
	err := runStep(ctx, timeout, "Irrlicht state "+state, func(step context.Context) error {
		var err error
		observation, err = runtime.WaitIrrlichtState(step, owned, state)
		return err
	})
	return observation, err
}

// cleanupOwner archives every session THIS RUN created, and nothing else. Its
// owned list grows as the executor adopts sessions, so a run that fails after
// starting a second session still tears down both.
type cleanupOwner struct {
	runtime  Runtime
	request  RunRequest
	baseline Baseline
	owned    []OwnedSession
	archived map[string]struct{}
}

func (owner *cleanupOwner) adopt(owned OwnedSession) {
	owner.owned = append(owner.owned, owned)
}

func (owner *cleanupOwner) markArchived(sessionID string) {
	if owner.archived == nil {
		owner.archived = map[string]struct{}{}
	}
	owner.archived[sessionID] = struct{}{}
}

func (owner *cleanupOwner) run() error {
	ctx, cancel := context.WithTimeout(context.Background(), owner.request.CleanupTimeout)
	defer cancel()
	owner.runtime.RecordStep("cleanup_started")

	var cleanupErr error
	if len(owner.owned) == 0 {
		recovered, err := owner.runtime.RecoverOwnedSession(ctx, owner.baseline, owner.request.Workspace)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("recover owned session: %w", err))
		} else if recovered.Registry.SessionID != "" {
			owner.owned = append(owner.owned, recovered)
		}
	}
	// Newest first: a later session is the one most likely still live.
	for index := len(owner.owned) - 1; index >= 0; index-- {
		cleanupErr = errors.Join(cleanupErr, owner.archive(ctx, owner.owned[index]))
	}
	cleanupErr = errors.Join(cleanupErr, owner.runtime.VerifyBaseline(ctx, owner.baseline, owner.owned))
	owner.runtime.RecordStep("cleanup_finished")
	if cleanupErr != nil {
		return fmt.Errorf("Desktop cleanup: %w", cleanupErr)
	}
	return nil
}

func (owner *cleanupOwner) archive(ctx context.Context, owned OwnedSession) error {
	if _, done := owner.archived[owned.Registry.SessionID]; !done {
		if err := owner.runtime.ArchiveOwned(ctx, owned); err != nil {
			return fmt.Errorf("archive owned session %q: %w", owned.Registry.SessionID, err)
		}
	}
	if err := owner.runtime.WaitProcessExit(ctx, owned); err != nil {
		return fmt.Errorf("wait for owned process to exit: %w", err)
	}
	sessionID := owned.Transcript.SessionID
	if sessionID == "" {
		sessionID = owned.Registry.CLISessionID
	}
	if sessionID == "" {
		return nil
	}
	if err := owner.runtime.WaitIrrlichtRemoved(ctx, sessionID); err != nil {
		return fmt.Errorf("wait for Irrlicht to remove %q: %w", sessionID, err)
	}
	return nil
}

func runStep(
	ctx context.Context,
	timeout time.Duration,
	name string,
	operation func(context.Context) error,
) error {
	step, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := operation(step); err != nil {
		if errors.Is(step.Err(), context.DeadlineExceeded) {
			// Carry the operation's own error. It holds the last thing the step
			// actually observed, and reporting the deadline alone threw that
			// away — leaving "timed out" with no way to tell a composer that
			// never opened from one whose project chip named another folder.
			return fmt.Errorf("wait for %s timed out after %s: %w; last error: %v",
				name, timeout, step.Err(), err)
		}
		return fmt.Errorf("wait for %s: %w", name, err)
	}
	return nil
}
