package desktopresults

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

const (
	desktopEntrypoint = "claude-desktop"
	desktopBundleID   = "com.anthropic.claudefordesktop"
	localEnvironment  = "Local"
)

type evidenceFiles struct {
	registry    string
	transcript  string
	hooks       string
	process     string
	irrlicht    string
	environment string
}

type transcriptIdentity struct {
	SessionID  string
	CWD        string
	Entrypoint string
}

type registryIdentity struct {
	SessionID    string          `json:"sessionId"`
	CLISessionID string          `json:"cliSessionId"`
	CWD          string          `json:"cwd"`
	EnvScopeID   json.RawMessage `json:"envScopeId"`
}

type environmentReceipt struct {
	SelectedEnvironment string `json:"selected_environment"`
	RequestedWorkspace  string `json:"requested_workspace"`
}

type irrlichtSession struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	Launcher  struct {
		HostBundleID string `json:"host_bundle_id"`
	} `json:"launcher"`
}

type processEvidence struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

func (v *validation) validateObservedEvidence(rel, cellDir string, result *Result) {
	recordingsRoot := filepath.Join(cellDir, "recordings")
	recordingDir, err := resolveDirectory(recordingsRoot, result.Recording)
	if err != nil {
		v.add(rel, result.ScenarioID, "recording", err.Error())
		return
	}
	files, ok := v.resolveEvidenceFiles(rel, recordingDir, result)
	manifest, manifestOK := v.validateManifest(rel, recordingDir, result.ScenarioID)
	if !ok || !manifestOK {
		return
	}

	transcript, transcriptOK := v.readTranscript(rel, result.ScenarioID, files.transcript)
	registry, registryOK := v.readRegistry(rel, result.ScenarioID, files.registry)
	environment, environmentOK := v.readEnvironment(rel, result.ScenarioID, files.environment)
	irrlicht, irrlichtOK := v.readIrrlichtSession(rel, result.ScenarioID, files.irrlicht)
	hooksOK := v.validateHooks(rel, result.ScenarioID, files.hooks, transcript.SessionID)
	process, processOK := v.readProcess(rel, result.ScenarioID, files.process)

	if transcriptOK {
		v.compareManifestTranscript(rel, result.ScenarioID, manifest, transcript)
	}
	if transcriptOK && registryOK && environmentOK {
		v.compareWorkspaceIdentity(rel, result.ScenarioID, transcript, registry, environment)
	}
	if transcriptOK && registryOK {
		v.compareSessionMapping(rel, result.ScenarioID, transcript, registry)
	}
	if transcriptOK && irrlichtOK {
		v.compareIrrlichtIdentity(rel, result.ScenarioID, transcript, irrlicht)
	}
	if environmentOK && irrlichtOK && !samePath(environment.RequestedWorkspace, irrlicht.CWD) {
		v.add(rel, result.ScenarioID, "irrlicht_session.cwd", "does not match environment.requested_workspace")
	}
	if processOK && irrlichtOK && process.PID != irrlicht.PID {
		v.add(rel, result.ScenarioID, "process.pid", "does not match irrlicht_session.pid")
	}
	_ = hooksOK // validation reports its own finding; no later join depends on it.
}

func (v *validation) resolveEvidenceFiles(rel, recordingDir string, result *Result) (evidenceFiles, bool) {
	evidence := result.Evidence
	refs := []struct {
		field string
		ref   string
		dst   *string
	}{
		{"desktop_registry", evidence.DesktopRegistry, nil},
		{"transcript", evidence.Transcript, nil},
		{"hooks", evidence.Hooks, nil},
		{"process", evidence.Process, nil},
		{"irrlicht_session", evidence.IrrlichtSession, nil},
		{"environment", evidence.Environment, nil},
	}
	var files evidenceFiles
	refs[0].dst = &files.registry
	refs[1].dst = &files.transcript
	refs[2].dst = &files.hooks
	refs[3].dst = &files.process
	refs[4].dst = &files.irrlicht
	refs[5].dst = &files.environment
	ok := true
	for _, item := range refs {
		resolved, err := resolveFile(recordingDir, item.ref)
		if err != nil {
			v.add(rel, result.ScenarioID, "evidence."+item.field, err.Error())
			ok = false
			continue
		}
		*item.dst = resolved
	}
	return files, ok
}

func (v *validation) validateManifest(rel, recordingDir, scenario string) (matrix.RecordingManifest, bool) {
	manifestPath, err := resolveFile(recordingDir, "manifest.json")
	if err != nil {
		v.add(rel, scenario, "manifest", err.Error())
		return matrix.RecordingManifest{}, false
	}
	manifest, err := matrix.LoadRecordingManifest(manifestPath)
	if err != nil {
		v.add(rel, scenario, "manifest", err.Error())
		return manifest, false
	}
	if manifest.ExecutionProfile != matrix.ProfileDesktopLocal {
		v.add(rel, scenario, "manifest.execution_profile", fmt.Sprintf("got %q; want %q", manifest.ExecutionProfile, matrix.ProfileDesktopLocal))
	}
	if manifest.Entrypoint != desktopEntrypoint {
		v.add(rel, scenario, "manifest.entrypoint", fmt.Sprintf("got %q; want %q", manifest.Entrypoint, desktopEntrypoint))
	}
	for field, value := range map[string]string{
		"manifest.daemon_version":      manifest.DaemonVersion,
		"manifest.agent_cli_version":   manifest.AgentCLIVersion,
		"manifest.desktop_app_version": manifest.DesktopAppVersion,
	} {
		if blankOrUnknown(value) {
			v.add(rel, scenario, field, "must contain the measured version")
		}
	}
	return manifest, true
}

func (v *validation) readTranscript(rel, scenario, path string) (transcriptIdentity, bool) {
	f, err := os.Open(path)
	if err != nil {
		v.add(rel, scenario, "transcript", err.Error())
		return transcriptIdentity{}, false
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	identity := transcriptIdentity{}
	for {
		var row struct {
			SessionID  string `json:"sessionId"`
			CWD        string `json:"cwd"`
			Entrypoint string `json:"entrypoint"`
		}
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			v.add(rel, scenario, "transcript", "invalid JSONL: "+err.Error())
			return identity, false
		}
		if !mergeIdentity(&identity.SessionID, row.SessionID) {
			v.add(rel, scenario, "transcript.sessionId", "contains conflicting values")
		}
		if !mergeIdentity(&identity.CWD, row.CWD) {
			v.add(rel, scenario, "transcript.cwd", "contains conflicting values")
		}
		if !mergeIdentity(&identity.Entrypoint, row.Entrypoint) {
			v.add(rel, scenario, "transcript.entrypoint", "contains conflicting values")
		}
	}
	ok := true
	for field, value := range map[string]string{
		"transcript.sessionId":  identity.SessionID,
		"transcript.cwd":        identity.CWD,
		"transcript.entrypoint": identity.Entrypoint,
	} {
		if strings.TrimSpace(value) == "" {
			v.add(rel, scenario, field, "is absent from the raw transcript")
			ok = false
		}
	}
	if identity.Entrypoint != "" && identity.Entrypoint != desktopEntrypoint {
		v.add(rel, scenario, "transcript.entrypoint", fmt.Sprintf("got %q; want %q", identity.Entrypoint, desktopEntrypoint))
		ok = false
	}
	return identity, ok
}

func (v *validation) readRegistry(rel, scenario, path string) (registryIdentity, bool) {
	var registry registryIdentity
	if err := readJSON(path, &registry); err != nil {
		v.add(rel, scenario, "desktop_registry", err.Error())
		return registry, false
	}
	ok := true
	for field, value := range map[string]string{
		"desktop_registry.sessionId":    registry.SessionID,
		"desktop_registry.cliSessionId": registry.CLISessionID,
		"desktop_registry.cwd":          registry.CWD,
	} {
		if strings.TrimSpace(value) == "" {
			v.add(rel, scenario, field, "must not be blank")
			ok = false
		}
	}
	if registry.SessionID != "" && !strings.HasPrefix(registry.SessionID, "local_") {
		v.add(rel, scenario, "desktop_registry.sessionId", "must start with local_")
		ok = false
	}
	if string(registry.EnvScopeID) != "null" {
		v.add(rel, scenario, "desktop_registry.envScopeId", "must be explicit null for the Local environment")
		ok = false
	}
	return registry, ok
}

func (v *validation) readEnvironment(rel, scenario, path string) (environmentReceipt, bool) {
	var receipt environmentReceipt
	if err := decodeStrict(path, &receipt); err != nil {
		v.add(rel, scenario, "environment", "invalid or unknown field: "+err.Error())
		return receipt, false
	}
	ok := true
	if receipt.SelectedEnvironment != localEnvironment {
		v.add(rel, scenario, "environment.selected_environment", fmt.Sprintf("got %q; want %q", receipt.SelectedEnvironment, localEnvironment))
		ok = false
	}
	if strings.TrimSpace(receipt.RequestedWorkspace) == "" {
		v.add(rel, scenario, "environment.requested_workspace", "must not be blank")
		ok = false
	}
	return receipt, ok
}

func (v *validation) readIrrlichtSession(rel, scenario, path string) (irrlichtSession, bool) {
	var state irrlichtSession
	if err := readJSON(path, &state); err != nil {
		v.add(rel, scenario, "irrlicht_session", err.Error())
		return state, false
	}
	ok := true
	if strings.TrimSpace(state.SessionID) == "" {
		v.add(rel, scenario, "irrlicht_session.session_id", "must not be blank")
		ok = false
	}
	if strings.TrimSpace(state.CWD) == "" {
		v.add(rel, scenario, "irrlicht_session.cwd", "must not be blank")
		ok = false
	}
	if state.PID <= 0 {
		v.add(rel, scenario, "irrlicht_session.pid", "must be positive")
		ok = false
	}
	if state.Launcher.HostBundleID != desktopBundleID {
		v.add(rel, scenario, "irrlicht_session.launcher.host_bundle_id", fmt.Sprintf("got %q; want %q", state.Launcher.HostBundleID, desktopBundleID))
		ok = false
	}
	return state, ok
}

func (v *validation) validateHooks(rel, scenario, path, sessionID string) bool {
	f, err := os.Open(path)
	if err != nil {
		v.add(rel, scenario, "hooks", err.Error())
		return false
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	matched := false
	consistent := true
	for {
		var row struct {
			SessionID string `json:"session_id"`
		}
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			v.add(rel, scenario, "hooks", "invalid JSONL: "+err.Error())
			return false
		}
		if sessionID != "" && row.SessionID == sessionID {
			matched = true
		} else {
			consistent = false
		}
	}
	if !matched {
		v.add(rel, scenario, "hooks.session_id", "contains no raw hook for transcript.sessionId")
	}
	if !consistent {
		v.add(rel, scenario, "hooks.session_id", "contains a hook from another session")
	}
	return matched && consistent
}

func (v *validation) readProcess(rel, scenario, path string) (processEvidence, bool) {
	var process processEvidence
	if err := readJSON(path, &process); err != nil {
		v.add(rel, scenario, "process", err.Error())
		return process, false
	}
	ok := true
	if process.PID <= 0 {
		v.add(rel, scenario, "process.pid", "must be positive")
		ok = false
	}
	if strings.TrimSpace(process.Command) == "" {
		v.add(rel, scenario, "process.command", "must not be blank")
		ok = false
	}
	return process, ok
}

func (v *validation) compareManifestTranscript(rel, scenario string, manifest matrix.RecordingManifest, transcript transcriptIdentity) {
	if manifest.Entrypoint != transcript.Entrypoint {
		v.add(rel, scenario, "manifest.entrypoint", "does not match transcript.entrypoint")
	}
}

func (v *validation) compareWorkspaceIdentity(rel, scenario string, transcript transcriptIdentity, registry registryIdentity, environment environmentReceipt) {
	if !samePath(registry.CWD, transcript.CWD) {
		v.add(rel, scenario, "desktop_registry.cwd", "does not match transcript.cwd")
	}
	if !samePath(registry.CWD, environment.RequestedWorkspace) {
		v.add(rel, scenario, "desktop_registry.cwd", "does not match environment.requested_workspace")
	}
	if !samePath(transcript.CWD, environment.RequestedWorkspace) {
		v.add(rel, scenario, "transcript.cwd", "does not match environment.requested_workspace")
	}
}

func (v *validation) compareSessionMapping(rel, scenario string, transcript transcriptIdentity, registry registryIdentity) {
	if registry.CLISessionID != transcript.SessionID {
		v.add(rel, scenario, "desktop_registry.cliSessionId", "does not match transcript.sessionId")
	}
}

func (v *validation) compareIrrlichtIdentity(rel, scenario string, transcript transcriptIdentity, state irrlichtSession) {
	if state.SessionID != transcript.SessionID {
		v.add(rel, scenario, "irrlicht_session.session_id", "does not match transcript.sessionId")
	}
	if !samePath(state.CWD, transcript.CWD) {
		v.add(rel, scenario, "irrlicht_session.cwd", "does not match transcript.cwd")
	}
}

func readJSON(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func mergeIdentity(current *string, next string) bool {
	if next == "" {
		return true
	}
	if *current == "" {
		*current = next
		return true
	}
	return *current == next
}

func samePath(left, right string) bool {
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func blankOrUnknown(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "unknown"
}
