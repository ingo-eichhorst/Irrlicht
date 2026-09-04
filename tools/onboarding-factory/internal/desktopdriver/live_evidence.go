package desktopdriver

// Evidence: what the run captures, how it is cross-checked, and how it is
// written to the staging tree.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	currentRegistry, err := runtime.registrySession(owned.Registry.SessionID)
	if err != nil {
		return CapturedEvidence{}, err
	}
	if err := validateRegistryIdentity(owned.Registry, currentRegistry); err != nil {
		return CapturedEvidence{}, err
	}
	owned.Registry = currentRegistry
	elements, err := runtime.helper.inspect(ctx)
	if err != nil {
		return CapturedEvidence{}, err
	}
	controls, err := composerCatalog(elements, owned.Registry.CWD)
	if err != nil {
		return CapturedEvidence{}, fmt.Errorf("verify Desktop environment for evidence: %w", err)
	}
	if err := runtime.helper.probe(ctx, controls); err != nil {
		return CapturedEvidence{}, fmt.Errorf("re-probe Desktop environment for evidence: %w", err)
	}
	environment := EnvironmentEvidence{
		SelectedEnvironment: controls["environment"].Title,
		RequestedWorkspace:  owned.Registry.CWD,
		Project:             controls["project"].Title,
	}
	transcriptPath, err := runtime.transcriptPath(owned.Transcript.SessionID)
	if err != nil {
		return CapturedEvidence{}, err
	}
	if err := validateNoToolTranscript(transcriptPath); err != nil {
		return CapturedEvidence{}, err
	}
	process, ok := runtime.processEvidence[owned.Registry.SessionID]
	if !ok {
		return CapturedEvidence{}, errors.New("owned Claude process evidence was not captured")
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

func validateNoToolTranscript(path string) error {
	found, err := jsonlContains(path, containsToolRecord)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("Desktop transcript %q contains a tool call or tool result", path)
	}
	return nil
}

func containsToolRecord(value map[string]any) bool {
	return containsToolValue(value)
}

func containsToolValue(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if containsToolValue(child) {
				return true
			}
		}
	case map[string]any:
		if kind, ok := typed["type"].(string); ok && (kind == "tool_use" || kind == "tool_result") {
			return true
		}
		for _, child := range typed {
			if containsToolValue(child) {
				return true
			}
		}
	}
	return false
}

func jsonlContains(path string, predicate func(map[string]any) bool) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return false, fmt.Errorf("decode recording %q: %w", path, err)
		}
		if predicate(value) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func recordingHasStateSequence(directory, sessionID string, expected []string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(directory, "*.jsonl"))
	if err != nil {
		return false, err
	}
	sort.Strings(files)
	matched := 0
	for _, file := range files {
		_, err := jsonlContains(file, func(value map[string]any) bool {
			if matched < len(expected) && value["kind"] == "state_transition" &&
				value["session_id"] == sessionID && value["new_state"] == expected[matched] {
				matched++
			}
			return matched == len(expected)
		})
		if err != nil {
			return false, err
		}
		if matched == len(expected) {
			return true, nil
		}
	}
	return false, nil
}

func (runtime *LiveRuntime) VerifyBaseline(_ context.Context, baseline Baseline, owned OwnedSession) error {
	if err := VerifyTreeSnapshot(baseline.Config); err != nil {
		return err
	}
	sessions, files, err := runtime.readRegistry()
	if err != nil {
		return err
	}
	for path, expected := range baseline.Files {
		actual, ok := files[path]
		if !ok || !bytes.Equal(expected, actual) {
			return fmt.Errorf("pre-existing Desktop session changed at %q", path)
		}
	}
	for _, session := range sessions {
		if _, existed := baseline.SessionIDs[session.SessionID]; existed {
			continue
		}
		if session.SessionID != owned.Registry.SessionID {
			return fmt.Errorf("unowned post-baseline Desktop session %q exists", session.SessionID)
		}
		if !session.Archived {
			return fmt.Errorf("owned Desktop session %q is not archived", session.SessionID)
		}
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func copyRegularFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("evidence source is not a regular file: %q", source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o600)
}
