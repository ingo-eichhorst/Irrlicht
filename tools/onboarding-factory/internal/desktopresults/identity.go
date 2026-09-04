package desktopresults

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	expectedvalidate "irrlicht/tools/onboarding-factory/internal/validate"
)

const (
	desktopEntrypoint       = "claude-desktop"
	desktopBundleID         = "com.anthropic.claudefordesktop"
	localEnvironment        = "Local"
	fieldIrrlichtCWD        = "irrlicht_session.cwd"
	fieldDesktopRegistryCWD = "desktop_registry.cwd"
	mustNotBeBlank          = "must not be blank"
	mismatchEnvironmentCWD  = "does not match environment.requested_workspace"
)

type evidenceFiles struct {
	registry    string
	transcript  string
	hooks       string
	events      string
	process     string
	irrlicht    string
	environment string
}

type hookReceiptScan struct {
	matchedSession     bool
	consistentSessions bool
	correctKinds       bool
	named              bool
	preserved          bool
}

type hookReceipt struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	HookName  string `json:"hook_name"`
}

type hookValidation struct {
	target     findingTarget
	path       string
	eventsPath string
	sessionID  string
}

type recordingValidationContext struct {
	target       findingTarget
	cellDir      string
	recordingDir string
	result       *Result
}

type identityValidation struct {
	target   findingTarget
	identity observedIdentity
}

type evidenceRead struct {
	target findingTarget
	path   string
}

type transcriptRow struct {
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
}

type hookScanInput struct {
	path       string
	eventsPath string
	sessionID  string
}

type hookReceiptInput struct {
	scan      *hookReceiptScan
	line      string
	sessionID string
	remaining map[string]int
}

type hookSessionCheck struct {
	row       hookReceipt
	sessionID string
}

type lineClaim struct {
	remaining map[string]int
	line      string
}

type jsonRead struct {
	path  string
	value any
}

type identityMerge struct {
	current *string
	next    string
}

func (scan hookReceiptScan) valid() bool {
	return scan.matchedSession && scan.consistentSessions && scan.correctKinds && scan.named && scan.preserved
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

type observedIdentity struct {
	manifest      matrix.RecordingManifest
	transcript    transcriptIdentity
	registry      registryIdentity
	environment   environmentReceipt
	irrlicht      irrlichtSession
	process       processEvidence
	transcriptOK  bool
	registryOK    bool
	environmentOK bool
	irrlichtOK    bool
	processOK     bool
}

func (v *validation) validateObservedEvidence(context resultValidationContext) {
	recordingsRoot := filepath.Join(context.cellDir, "recordings")
	recordingDir, err := resolveDirectory(pathReference{root: recordingsRoot, reference: context.result.Recording})
	if err != nil {
		v.add(context.target, "recording", err.Error())
		return
	}
	recording := recordingValidationContext{
		target:       context.target,
		cellDir:      context.cellDir,
		recordingDir: recordingDir,
		result:       context.result,
	}
	v.validateRecordingCompleteness(recording)
	v.validateObservedOutcome(recording)
	files, ok := v.resolveEvidenceFiles(recording)
	manifest, manifestOK := v.validateManifest(recording)
	if !ok || !manifestOK {
		return
	}
	identity := v.readObservedIdentity(context.target, files, manifest)
	v.compareObservedIdentity(identityValidation{target: context.target, identity: identity})
}

func (v *validation) validateRecordingCompleteness(context recordingValidationContext) {
	for _, finding := range expectedvalidate.RecordingComplete(context.recordingDir) {
		v.add(context.target, "recording completeness", finding)
	}
}

func (v *validation) validateObservedOutcome(context recordingValidationContext) {
	report, err := expectedvalidate.ValidateExpectedAgainst(
		filepath.Join(context.cellDir, "expected.jsonl"),
		filepath.Join(context.recordingDir, "events.jsonl"),
	)
	if err != nil {
		v.add(context.target, "outcome", "cannot validate the exact recording: "+err.Error())
		return
	}
	if report == nil {
		v.add(context.target, "outcome", "exact recording has no expected-result report")
		return
	}
	wantPass := context.result.Outcome == OutcomeObservedPassing
	if report.Pass != wantPass {
		v.add(context.target, "outcome", fmt.Sprintf("got %q for an expected-result pass value of %t", context.result.Outcome, report.Pass))
	}
}

func (v *validation) readObservedIdentity(target findingTarget, files evidenceFiles, manifest matrix.RecordingManifest) observedIdentity {
	identity := observedIdentity{manifest: manifest}
	identity.transcript, identity.transcriptOK = v.readTranscript(evidenceRead{target: target, path: files.transcript})
	identity.registry, identity.registryOK = v.readRegistry(evidenceRead{target: target, path: files.registry})
	identity.environment, identity.environmentOK = v.readEnvironment(evidenceRead{target: target, path: files.environment})
	identity.irrlicht, identity.irrlichtOK = v.readIrrlichtSession(evidenceRead{target: target, path: files.irrlicht})
	identity.process, identity.processOK = v.readProcess(evidenceRead{target: target, path: files.process})
	v.validateHooks(hookValidation{
		target:     target,
		path:       files.hooks,
		eventsPath: files.events,
		sessionID:  identity.transcript.SessionID,
	})
	return identity
}

func (v *validation) compareObservedIdentity(context identityValidation) {
	v.compareTranscriptIdentity(context)
	v.compareEnvironmentIdentity(context)
	v.compareProcessIdentity(context)
}

func (v *validation) compareTranscriptIdentity(context identityValidation) {
	identity := context.identity
	if !identity.transcriptOK {
		return
	}
	v.compareManifestTranscript(context)
	if identity.registryOK {
		v.compareSessionMapping(context)
	}
	if identity.registryOK && identity.environmentOK {
		v.compareWorkspaceIdentity(context)
	}
	if identity.irrlichtOK {
		v.compareIrrlichtIdentity(context)
	}
}

func (v *validation) compareEnvironmentIdentity(context identityValidation) {
	identity := context.identity
	if !identity.environmentOK || !identity.irrlichtOK {
		return
	}
	if !samePath(identity.environment.RequestedWorkspace, identity.irrlicht.CWD) {
		v.add(context.target, fieldIrrlichtCWD, mismatchEnvironmentCWD)
	}
}

func (v *validation) compareProcessIdentity(context identityValidation) {
	identity := context.identity
	if !identity.processOK || !identity.irrlichtOK {
		return
	}
	if identity.process.PID != identity.irrlicht.PID {
		v.add(context.target, "process.pid", "does not match irrlicht_session.pid")
	}
}

func (v *validation) resolveEvidenceFiles(context recordingValidationContext) (evidenceFiles, bool) {
	evidence := context.result.Evidence
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
		resolved, err := resolveFile(pathReference{root: context.recordingDir, reference: item.ref})
		if err != nil {
			v.add(context.target, "evidence."+item.field, err.Error())
			ok = false
			continue
		}
		*item.dst = resolved
	}
	eventsPath, eventsErr := resolveFile(pathReference{root: context.recordingDir, reference: "events.jsonl"})
	if eventsErr != nil {
		v.add(context.target, "events", eventsErr.Error())
		ok = false
	} else {
		files.events = eventsPath
	}
	return files, ok
}

func (v *validation) validateManifest(context recordingValidationContext) (matrix.RecordingManifest, bool) {
	manifestPath, err := resolveFile(pathReference{root: context.recordingDir, reference: "manifest.json"})
	if err != nil {
		v.add(context.target, "manifest", err.Error())
		return matrix.RecordingManifest{}, false
	}
	manifest, err := matrix.LoadRecordingManifest(manifestPath)
	if err != nil {
		v.add(context.target, "manifest", err.Error())
		return manifest, false
	}
	if manifest.ExecutionProfile != matrix.ProfileDesktopLocal {
		v.add(context.target, "manifest.execution_profile", fmt.Sprintf("got %q; want %q", manifest.ExecutionProfile, matrix.ProfileDesktopLocal))
	}
	if manifest.Entrypoint != desktopEntrypoint {
		v.add(context.target, "manifest.entrypoint", fmt.Sprintf("got %q; want %q", manifest.Entrypoint, desktopEntrypoint))
	}
	for field, value := range map[string]string{
		"manifest.daemon_version":      manifest.DaemonVersion,
		"manifest.agent_cli_version":   manifest.AgentCLIVersion,
		"manifest.desktop_app_version": manifest.DesktopAppVersion,
	} {
		if blankOrUnknown(value) {
			v.add(context.target, field, "must contain the measured version")
		}
	}
	return manifest, true
}

func (v *validation) readTranscript(input evidenceRead) (transcriptIdentity, bool) {
	identity, conflicts, err := scanTranscript(input.path)
	if err != nil {
		v.add(input.target, "transcript", err.Error())
		return identity, false
	}
	for _, field := range conflicts {
		v.add(input.target, "transcript."+field, "contains conflicting values")
	}
	ok := v.validateTranscriptFields(identityValidation{target: input.target, identity: observedIdentity{transcript: identity}})
	return identity, ok && len(conflicts) == 0
}

func scanTranscript(path string) (transcriptIdentity, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcriptIdentity{}, nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	identity := transcriptIdentity{}
	var conflicts []string
	for {
		var row transcriptRow
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return identity, conflicts, fmt.Errorf("invalid JSONL: %w", err)
		}
		conflicts = append(conflicts, mergeTranscriptRow(&identity, row)...)
	}
	return identity, conflicts, nil
}

func mergeTranscriptRow(identity *transcriptIdentity, row transcriptRow) []string {
	var conflicts []string
	if !mergeIdentity(identityMerge{current: &identity.SessionID, next: row.SessionID}) {
		conflicts = append(conflicts, "sessionId")
	}
	if !mergeIdentity(identityMerge{current: &identity.CWD, next: row.CWD}) {
		conflicts = append(conflicts, "cwd")
	}
	if !mergeIdentity(identityMerge{current: &identity.Entrypoint, next: row.Entrypoint}) {
		conflicts = append(conflicts, "entrypoint")
	}
	return conflicts
}

func (v *validation) validateTranscriptFields(context identityValidation) bool {
	identity := context.identity.transcript
	ok := true
	for field, value := range map[string]string{
		"sessionId":  identity.SessionID,
		"cwd":        identity.CWD,
		"entrypoint": identity.Entrypoint,
	} {
		if strings.TrimSpace(value) == "" {
			v.add(context.target, "transcript."+field, "is absent from the raw transcript")
			ok = false
		}
	}
	if identity.Entrypoint != "" && identity.Entrypoint != desktopEntrypoint {
		v.add(context.target, "transcript.entrypoint", fmt.Sprintf("got %q; want %q", identity.Entrypoint, desktopEntrypoint))
		ok = false
	}
	return ok
}

func (v *validation) readRegistry(input evidenceRead) (registryIdentity, bool) {
	var registry registryIdentity
	if err := readJSON(jsonRead{path: input.path, value: &registry}); err != nil {
		v.add(input.target, "desktop_registry", err.Error())
		return registry, false
	}
	ok := true
	for field, value := range map[string]string{
		"desktop_registry.sessionId":    registry.SessionID,
		"desktop_registry.cliSessionId": registry.CLISessionID,
		fieldDesktopRegistryCWD:         registry.CWD,
	} {
		if strings.TrimSpace(value) == "" {
			v.add(input.target, field, mustNotBeBlank)
			ok = false
		}
	}
	if registry.SessionID != "" && !strings.HasPrefix(registry.SessionID, "local_") {
		v.add(input.target, "desktop_registry.sessionId", "must start with local_")
		ok = false
	}
	if len(registry.EnvScopeID) > 0 && string(registry.EnvScopeID) != "null" {
		v.add(input.target, "desktop_registry.envScopeId", "must be absent or null for the project-local environment")
		ok = false
	}
	return registry, ok
}

func (v *validation) readEnvironment(input evidenceRead) (environmentReceipt, bool) {
	var receipt environmentReceipt
	if err := decodeStrict(input.path, &receipt); err != nil {
		v.add(input.target, "environment", "invalid or unknown field: "+err.Error())
		return receipt, false
	}
	ok := true
	if receipt.SelectedEnvironment != localEnvironment {
		v.add(input.target, "environment.selected_environment", fmt.Sprintf("got %q; want %q", receipt.SelectedEnvironment, localEnvironment))
		ok = false
	}
	if strings.TrimSpace(receipt.RequestedWorkspace) == "" {
		v.add(input.target, "environment.requested_workspace", mustNotBeBlank)
		ok = false
	}
	return receipt, ok
}

func (v *validation) readIrrlichtSession(input evidenceRead) (irrlichtSession, bool) {
	var state irrlichtSession
	if err := readJSON(jsonRead{path: input.path, value: &state}); err != nil {
		v.add(input.target, "irrlicht_session", err.Error())
		return state, false
	}
	ok := true
	if strings.TrimSpace(state.SessionID) == "" {
		v.add(input.target, "irrlicht_session.session_id", mustNotBeBlank)
		ok = false
	}
	if strings.TrimSpace(state.CWD) == "" {
		v.add(input.target, fieldIrrlichtCWD, mustNotBeBlank)
		ok = false
	}
	if state.PID <= 0 {
		v.add(input.target, "irrlicht_session.pid", "must be positive")
		ok = false
	}
	if state.Launcher.HostBundleID != desktopBundleID {
		v.add(input.target, "irrlicht_session.launcher.host_bundle_id", fmt.Sprintf("got %q; want %q", state.Launcher.HostBundleID, desktopBundleID))
		ok = false
	}
	return state, ok
}

func (v *validation) validateHooks(input hookValidation) bool {
	scan, err := scanHookReceipts(hookScanInput{path: input.path, eventsPath: input.eventsPath, sessionID: input.sessionID})
	if err != nil {
		v.add(input.target, "hooks", err.Error())
		return false
	}
	v.reportHookReceiptFindings(input.target, scan)
	return scan.valid()
}

func (v *validation) reportHookReceiptFindings(target findingTarget, scan hookReceiptScan) {
	if !scan.matchedSession {
		v.add(target, "hooks.session_id", "contains no hook receipt for transcript.sessionId")
	}
	if !scan.consistentSessions {
		v.add(target, "hooks.session_id", "contains a hook receipt from another session")
	}
	if !scan.correctKinds {
		v.add(target, "hooks.kind", `must be "hook_received" on every row`)
	}
	if !scan.named {
		v.add(target, "hooks.hook_name", "must be nonblank on every hook receipt")
	}
	if !scan.preserved {
		v.add(target, "hooks.events_jsonl", "each row must occur byte-for-byte in this recording's events.jsonl")
	}
}

// scanHookReceipts validates unmodified hook_received rows extracted from the
// recording's events.jsonl. These rows are lifecycle receipts. They are not
// the raw inbound Claude hook payload.
func scanHookReceipts(input hookScanInput) (hookReceiptScan, error) {
	eventLines, err := readJSONLLines(input.eventsPath)
	if err != nil {
		return hookReceiptScan{}, fmt.Errorf("read events.jsonl: %w", err)
	}
	hookLines, err := readJSONLLines(input.path)
	if err != nil {
		return hookReceiptScan{}, err
	}
	remaining := countLines(eventLines)
	scan := hookReceiptScan{consistentSessions: true, correctKinds: true, named: true, preserved: true}
	for _, line := range hookLines {
		if err := applyHookReceipt(hookReceiptInput{scan: &scan, line: line, sessionID: input.sessionID, remaining: remaining}); err != nil {
			return hookReceiptScan{}, fmt.Errorf("invalid JSONL: %w", err)
		}
	}
	return scan, nil
}

func countLines(lines []string) map[string]int {
	counts := make(map[string]int, len(lines))
	for _, line := range lines {
		counts[line]++
	}
	return counts
}

func applyHookReceipt(input hookReceiptInput) error {
	var row hookReceipt
	if err := json.Unmarshal([]byte(input.line), &row); err != nil {
		return err
	}
	correctKind := row.Kind == "hook_received"
	sessionMatches := hookSessionMatches(hookSessionCheck{row: row, sessionID: input.sessionID})
	input.scan.correctKinds = input.scan.correctKinds && correctKind
	input.scan.matchedSession = input.scan.matchedSession || sessionMatches
	input.scan.consistentSessions = input.scan.consistentSessions && sessionMatches
	input.scan.named = input.scan.named && strings.TrimSpace(row.HookName) != ""
	input.scan.preserved = input.scan.preserved && claimLine(lineClaim{remaining: input.remaining, line: input.line})
	return nil
}

func hookSessionMatches(input hookSessionCheck) bool {
	return input.row.Kind == "hook_received" && input.sessionID != "" && input.row.SessionID == input.sessionID
}

func claimLine(input lineClaim) bool {
	if input.remaining[input.line] == 0 {
		return false
	}
	input.remaining[input.line]--
	return true
}

func readJSONLLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			return nil, fmt.Errorf("invalid JSONL: blank row")
		}
	}
	return lines, nil
}

func (v *validation) readProcess(input evidenceRead) (processEvidence, bool) {
	var process processEvidence
	if err := readJSON(jsonRead{path: input.path, value: &process}); err != nil {
		v.add(input.target, "process", err.Error())
		return process, false
	}
	ok := true
	if process.PID <= 0 {
		v.add(input.target, "process.pid", "must be positive")
		ok = false
	}
	if strings.TrimSpace(process.Command) == "" {
		v.add(input.target, "process.command", mustNotBeBlank)
		ok = false
	}
	return process, ok
}

func (v *validation) compareManifestTranscript(context identityValidation) {
	if context.identity.manifest.Entrypoint != context.identity.transcript.Entrypoint {
		v.add(context.target, "manifest.entrypoint", "does not match transcript.entrypoint")
	}
}

func (v *validation) compareWorkspaceIdentity(context identityValidation) {
	identity := context.identity
	if !samePath(identity.registry.CWD, identity.transcript.CWD) {
		v.add(context.target, fieldDesktopRegistryCWD, "does not match transcript.cwd")
	}
	if !samePath(identity.registry.CWD, identity.environment.RequestedWorkspace) {
		v.add(context.target, fieldDesktopRegistryCWD, mismatchEnvironmentCWD)
	}
	if !samePath(identity.transcript.CWD, identity.environment.RequestedWorkspace) {
		v.add(context.target, "transcript.cwd", mismatchEnvironmentCWD)
	}
}

func (v *validation) compareSessionMapping(context identityValidation) {
	if context.identity.registry.CLISessionID != context.identity.transcript.SessionID {
		v.add(context.target, "desktop_registry.cliSessionId", "does not match transcript.sessionId")
	}
}

func (v *validation) compareIrrlichtIdentity(context identityValidation) {
	if context.identity.irrlicht.SessionID != context.identity.transcript.SessionID {
		v.add(context.target, "irrlicht_session.session_id", "does not match transcript.sessionId")
	}
	if !samePath(context.identity.irrlicht.CWD, context.identity.transcript.CWD) {
		v.add(context.target, fieldIrrlichtCWD, "does not match transcript.cwd")
	}
}

func readJSON(input jsonRead) error {
	b, err := os.ReadFile(input.path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, input.value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func mergeIdentity(input identityMerge) bool {
	if input.next == "" {
		return true
	}
	if *input.current == "" {
		*input.current = input.next
		return true
	}
	return *input.current == input.next
}

func samePath(left, right string) bool {
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func blankOrUnknown(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "unknown"
}
