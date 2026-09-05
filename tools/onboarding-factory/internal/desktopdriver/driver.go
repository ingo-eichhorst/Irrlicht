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
	Workspace      string
	Prompt         string
	EvidenceDir    string
	OverallTimeout time.Duration
	StepTimeout    time.Duration
	CleanupTimeout time.Duration
}

type RunResult struct {
	Owned    OwnedSession
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
	WaitIrrlichtState(context.Context, OwnedSession, string) (SessionObservation, error)
	WaitHook(context.Context, OwnedSession) error
	CaptureEvidence(context.Context, OwnedSession, SessionObservation, string) (CapturedEvidence, error)
	ArchiveOwned(context.Context, OwnedSession) error
	WaitProcessExit(context.Context, OwnedSession) error
	WaitIrrlichtRemoved(context.Context, string) error
	VerifyBaseline(context.Context, Baseline, OwnedSession) error
	RecordStep(string)
}

// Run drives one no-tool Desktop turn. Cleanup is armed before the deep link.
// It runs with an independent deadline after success, failure, timeout, or a
// caller signal.
func Run(ctx context.Context, runtime Runtime, request RunRequest) (result RunResult, err error) {
	if err := validateRunRequest(request); err != nil {
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

	cleanup := cleanupOwner{runtime: runtime, request: request, baseline: baseline}
	runtime.RecordStep("cleanup_armed")
	defer func() {
		cleanup.owned = result.Owned
		err = errors.Join(err, cleanup.run())
	}()

	result.Owned, result.Evidence, err = driveTurn(ctx, runtime, request, baseline)
	return result, err
}

func driveTurn(
	ctx context.Context,
	runtime Runtime,
	request RunRequest,
	baseline Baseline,
) (OwnedSession, CapturedEvidence, error) {
	if err := runtime.OpenComposer(ctx, request.Workspace); err != nil {
		return OwnedSession{}, CapturedEvidence{}, fmt.Errorf("open Desktop composer: %w", err)
	}
	if err := runStep(ctx, request.StepTimeout, "composer controls", func(step context.Context) error {
		return runtime.WaitComposer(step, request.Workspace)
	}); err != nil {
		return OwnedSession{}, CapturedEvidence{}, err
	}

	// The turn is typed and sent BEFORE ownership is bound, because Claude
	// Desktop writes no claude-code-sessions registry row until the first
	// message is sent. Measured live on 1.46388.4 on 2026-09-05: an open,
	// trusted composer showing the right Local project produced no row for 36
	// seconds. The previous order waited here for a row only the later Submit
	// could create, so it could never proceed.
	//
	// What is given up is the pre-turn "ready" observation: there is no session
	// to observe yet. What replaces it as the ownership guarantee is the
	// composer identity WaitComposer just verified — environment "Local" and a
	// project naming this run's own workspace — together with the baseline of
	// pre-existing session IDs. A row that appears after this Submit, for this
	// workspace, and was not in the baseline, is this driver's.
	if err := runtime.SetPrompt(ctx, request.Prompt); err != nil {
		return OwnedSession{}, CapturedEvidence{}, fmt.Errorf("set Desktop prompt: %w", err)
	}
	if err := runtime.Submit(ctx); err != nil {
		return OwnedSession{}, CapturedEvidence{}, fmt.Errorf("submit Desktop prompt: %w", err)
	}

	owned, err := waitForOwned(ctx, runtime, request, baseline)
	if err != nil {
		return OwnedSession{}, CapturedEvidence{}, err
	}
	if err := waitForWorkingAndHook(ctx, runtime, request, owned); err != nil {
		return owned, CapturedEvidence{}, err
	}
	ready, err := waitForCompletion(ctx, runtime, request, owned)
	if err != nil {
		return owned, CapturedEvidence{}, err
	}
	evidence, err := runtime.CaptureEvidence(ctx, owned, ready, request.EvidenceDir)
	if err != nil {
		return owned, CapturedEvidence{}, fmt.Errorf("capture Desktop evidence: %w", err)
	}
	return owned, evidence, nil
}

func waitForOwned(
	ctx context.Context,
	runtime Runtime,
	request RunRequest,
	baseline Baseline,
) (OwnedSession, error) {
	var owned OwnedSession
	err := runStep(ctx, request.StepTimeout, "one owned Desktop session", func(step context.Context) error {
		var err error
		owned, err = runtime.WaitOwnedSession(step, baseline, request.Workspace)
		return err
	})
	return owned, err
}

func waitForWorkingAndHook(
	ctx context.Context,
	runtime Runtime,
	request RunRequest,
	owned OwnedSession,
) error {
	if _, err := waitForState(ctx, runtime, request.StepTimeout, owned, "working"); err != nil {
		return err
	}
	return runStep(ctx, request.StepTimeout, "Claude Code hook", func(step context.Context) error {
		return runtime.WaitHook(step, owned)
	})
}

func waitForCompletion(
	ctx context.Context,
	runtime Runtime,
	request RunRequest,
	owned OwnedSession,
) (SessionObservation, error) {
	return waitForState(ctx, runtime, request.StepTimeout, owned, "ready")
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

type cleanupOwner struct {
	runtime  Runtime
	request  RunRequest
	baseline Baseline
	owned    OwnedSession
}

func (owner cleanupOwner) run() error {
	ctx, cancel := context.WithTimeout(context.Background(), owner.request.CleanupTimeout)
	defer cancel()
	owner.runtime.RecordStep("cleanup_started")

	var cleanupErr error
	if owner.owned.Registry.SessionID == "" {
		recovered, err := owner.runtime.RecoverOwnedSession(ctx, owner.baseline, owner.request.Workspace)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("recover owned session: %w", err))
		} else {
			owner.owned = recovered
		}
	}
	if owner.owned.Registry.SessionID != "" {
		cleanupErr = errors.Join(cleanupErr, owner.archive(ctx))
	}
	cleanupErr = errors.Join(cleanupErr, owner.runtime.VerifyBaseline(ctx, owner.baseline, owner.owned))
	owner.runtime.RecordStep("cleanup_finished")
	if cleanupErr != nil {
		return fmt.Errorf("Desktop cleanup: %w", cleanupErr)
	}
	return nil
}

func (owner cleanupOwner) archive(ctx context.Context) error {
	if err := owner.runtime.ArchiveOwned(ctx, owner.owned); err != nil {
		return fmt.Errorf("archive owned session %q: %w", owner.owned.Registry.SessionID, err)
	}
	if err := owner.runtime.WaitProcessExit(ctx, owner.owned); err != nil {
		return fmt.Errorf("wait for owned process to exit: %w", err)
	}
	sessionID := owner.owned.Transcript.SessionID
	if sessionID == "" {
		sessionID = owner.owned.Registry.CLISessionID
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

func validateRunRequest(request RunRequest) error {
	if request.Workspace == "" || request.Prompt == "" || request.EvidenceDir == "" {
		return errors.New("workspace, prompt, and evidence directory are required")
	}
	if request.OverallTimeout <= 0 || request.StepTimeout <= 0 || request.CleanupTimeout <= 0 {
		return errors.New("all Desktop driver deadlines must be positive")
	}
	return nil
}
