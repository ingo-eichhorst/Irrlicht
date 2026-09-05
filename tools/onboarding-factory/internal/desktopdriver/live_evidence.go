package desktopdriver

// Evidence: what the run captures, how it is cross-checked, and how it is
// written to the staging tree.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (runtime *LiveRuntime) CaptureEvidence(
	ctx context.Context,
	owned OwnedSession,
	observation SessionObservation,
	evidenceDir string,
) (CapturedEvidence, error) {
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		return CapturedEvidence{}, err
	}
	owned, err := runtime.refreshOwnedRegistry(owned)
	if err != nil {
		return CapturedEvidence{}, err
	}
	environment, err := runtime.captureEnvironment(ctx, owned.Registry.CWD)
	if err != nil {
		return CapturedEvidence{}, err
	}
	transcriptPath, err := runtime.verifiedTranscriptPath(owned.Transcript.SessionID)
	if err != nil {
		return CapturedEvidence{}, err
	}
	process, err := runtime.ownedProcessEvidence(owned.Registry.SessionID)
	if err != nil {
		return CapturedEvidence{}, err
	}
	evidence := CapturedEvidence{
		Registry: owned.Registry, TranscriptPath: transcriptPath,
		IrrlichtSession: observation,
		Process:         process,
		Environment:     environment,
	}
	if err := runtime.writeEvidenceFiles(evidenceDir, owned, evidence); err != nil {
		return CapturedEvidence{}, err
	}
	return evidence, nil
}

// refreshOwnedRegistry re-reads the Desktop registry row for the owned
// session and confirms its identity has not drifted since it was captured,
// returning owned with the freshly-read row in place.
func (runtime *LiveRuntime) refreshOwnedRegistry(owned OwnedSession) (OwnedSession, error) {
	currentRegistry, err := runtime.registrySession(owned.Registry.SessionID)
	if err != nil {
		return OwnedSession{}, err
	}
	if err := validateRegistryIdentity(owned.Registry, currentRegistry); err != nil {
		return OwnedSession{}, err
	}
	owned.Registry = currentRegistry
	return owned, nil
}

// verifiedTranscriptPath resolves the transcript path for a session and
// rejects it outright if it contains a tool call: this driver's turns must
// be tool-free, and evidence built from one that is not would misrepresent
// what ran.
func (runtime *LiveRuntime) verifiedTranscriptPath(sessionID string) (string, error) {
	transcriptPath, err := runtime.transcriptPath(sessionID)
	if err != nil {
		return "", err
	}
	if err := validateNoToolTranscript(transcriptPath); err != nil {
		return "", err
	}
	return transcriptPath, nil
}

// ownedProcessEvidence looks up the process evidence captured for the owned
// session's PID watch. Its absence means the watch never fired, which is an
// error, not an empty result.
func (runtime *LiveRuntime) ownedProcessEvidence(sessionID string) (ProcessEvidence, error) {
	process, ok := runtime.processEvidence[sessionID]
	if !ok {
		return ProcessEvidence{}, errors.New("owned Claude process evidence was not captured")
	}
	return process, nil
}

// captureEnvironment returns what the composer showed when WaitComposer
// verified it, immediately before the prompt was typed.
//
// It used to re-read the live tree here, on the principle that the environment
// recorded beside a recording should be the one on screen when the evidence was
// taken. On Claude Desktop that principle cannot hold: a turn replaces its own
// composer with the session it creates, so at evidence time there is no
// environment control to read. Live run 18 drove a complete turn and then
// failed with `Desktop environment control requires one AXPopUpButton titled
// "Local"; found 0`.
//
// The verified reading is also the more truthful one: it is the environment the
// turn was actually SENT in. What is still checked here is that it belongs to
// the workspace the registry recorded.
func (runtime *LiveRuntime) captureEnvironment(
	_ context.Context,
	workspace string,
) (EnvironmentEvidence, error) {
	environment := runtime.environment
	if environment.SelectedEnvironment == "" || environment.RequestedWorkspace == "" {
		return EnvironmentEvidence{}, errors.New(
			"no Desktop composer environment was verified before the turn")
	}
	if !sameWorkspace(environment.RequestedWorkspace, workspace) {
		return EnvironmentEvidence{}, fmt.Errorf(
			"verified Desktop environment is for workspace %q, but the registry recorded %q",
			environment.RequestedWorkspace, workspace)
	}
	return environment, nil
}

func (runtime *LiveRuntime) writeEvidenceFiles(
	dir string,
	owned OwnedSession,
	evidence CapturedEvidence,
) error {
	if err := validateEvidenceIdentity(owned, evidence); err != nil {
		return err
	}
	registry := map[string]any{
		"sessionId": owned.Registry.SessionID, "cliSessionId": owned.Registry.CLISessionID,
		"cwd": owned.Registry.CWD,
	}
	if owned.Registry.EnvScopePresent {
		registry["envScopeId"] = owned.Registry.EnvScopeID
	}
	irrlicht := struct {
		SessionID string   `json:"session_id"`
		CWD       string   `json:"cwd"`
		PID       int      `json:"pid"`
		Launcher  Launcher `json:"launcher"`
	}{
		evidence.IrrlichtSession.SessionID,
		evidence.IrrlichtSession.CWD,
		evidence.IrrlichtSession.PID,
		evidence.IrrlichtSession.Launcher,
	}
	for name, value := range map[string]any{
		"desktop-registry.json":    registry,
		"desktop-environment.json": evidence.Environment,
		"process.json":             evidence.Process,
		"irrlicht-session.json":    irrlicht,
	} {
		if err := writeJSON(filepath.Join(dir, name), value); err != nil {
			return err
		}
	}
	return copyRegularFile(evidence.TranscriptPath, filepath.Join(dir, "transcript.jsonl"))
}

func validateRegistryIdentity(expected, current RegistrySession) error {
	if current.SessionID != expected.SessionID ||
		current.CLISessionID != expected.CLISessionID ||
		!sameWorkspace(current.CWD, expected.CWD) ||
		current.EnvScopeID != nil {
		return fmt.Errorf("owned Desktop registry identity changed before evidence capture")
	}
	return nil
}

// validateEvidenceIdentity is the join every Desktop recording rests on: the
// registry row, the Claude Code transcript, what Irrlicht observed, the live
// process, and the environment Desktop was asked for must all name the SAME
// session and the same workspace. Each link is checked on its own so a failure
// says which one parted.
func validateEvidenceIdentity(owned OwnedSession, evidence CapturedEvidence) error {
	if err := validateRegistryTranscriptLink(owned); err != nil {
		return err
	}
	if err := validateIrrlichtLink(owned, evidence.IrrlichtSession); err != nil {
		return err
	}
	if evidence.Process.PID != evidence.IrrlichtSession.PID ||
		strings.TrimSpace(evidence.Process.Command) == "" {
		return errors.New("process evidence does not match the owned Irrlicht session")
	}
	return validateEnvironmentLink(owned, evidence.Environment)
}

// validateRegistryTranscriptLink binds the Desktop registry row to the Claude
// Code transcript it claims. EnvScopeID must be absent: a scoped row is not a
// Local session.
func validateRegistryTranscriptLink(owned OwnedSession) error {
	if owned.Registry.SessionID == "" || owned.Registry.CLISessionID == "" ||
		owned.Registry.CLISessionID != owned.Transcript.SessionID ||
		!sameWorkspace(owned.Registry.CWD, owned.Transcript.CWD) ||
		owned.Transcript.Entrypoint != "claude-desktop" || owned.Registry.EnvScopeID != nil {
		return errors.New("Desktop registry and transcript evidence identity is invalid")
	}
	return nil
}

// validateIrrlichtLink binds what the daemon observed to the owned session,
// including the host bundle that proves the process came from Claude Desktop.
func validateIrrlichtLink(owned OwnedSession, observation SessionObservation) error {
	if observation.SessionID != owned.Transcript.SessionID ||
		!sameWorkspace(observation.CWD, owned.Registry.CWD) ||
		observation.PID <= 0 || observation.Launcher.HostBundleID != desktopBundleID {
		return errors.New("Irrlicht evidence identity does not match the owned Desktop session")
	}
	return nil
}

// validateEnvironmentLink binds the environment Desktop was asked for to the
// workspace the registry recorded.
func validateEnvironmentLink(owned OwnedSession, environment EnvironmentEvidence) error {
	if environment.SelectedEnvironment != "Local" ||
		!sameWorkspace(environment.RequestedWorkspace, owned.Registry.CWD) ||
		environment.Project != filepath.Base(filepath.Clean(owned.Registry.CWD)) {
		return errors.New("Desktop environment evidence does not match the verified Local project")
	}
	return nil
}

func (runtime *LiveRuntime) VerifyBaseline(_ context.Context, baseline Baseline, owned OwnedSession) error {
	if err := VerifyTreeSnapshot(baseline.Config); err != nil {
		return err
	}
	sessions, files, err := runtime.readRegistry()
	if err != nil {
		return err
	}
	if err := verifyBaselineFiles(baseline.Files, files); err != nil {
		return err
	}
	return verifyPostBaselineSessions(sessions, baseline.SessionIDs, owned.Registry.SessionID)
}

// verifyBaselineFiles proves the run left every registry file it did not own
// byte-for-byte as it found it.
func verifyBaselineFiles(expected, actual map[string][]byte) error {
	for path, want := range expected {
		got, ok := actual[path]
		if !ok || !bytes.Equal(want, got) {
			return fmt.Errorf("pre-existing Desktop session changed at %q", path)
		}
	}
	return nil
}

// verifyPostBaselineSessions proves the only session that appeared during the
// run is the one this driver created, and that it was archived.
func verifyPostBaselineSessions(
	sessions []RegistrySession,
	baselineIDs map[string]struct{},
	ownedID string,
) error {
	for _, session := range sessions {
		if _, existed := baselineIDs[session.SessionID]; existed {
			continue
		}
		if session.SessionID != ownedID {
			return fmt.Errorf("unowned post-baseline Desktop session %q exists", session.SessionID)
		}
		if !session.Archived {
			return fmt.Errorf("owned Desktop session %q is not archived", session.SessionID)
		}
	}
	return nil
}
