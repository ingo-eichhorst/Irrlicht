package desktopdriver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const pollInterval = 100 * time.Millisecond

const (
	maxProcessCensusBytes = 4 * 1024 * 1024
	maxProcessCensusRows  = 100_000
)

var errTranscriptPending = errors.New("Claude transcript has not appeared")

type LiveOptions struct {
	Home               string
	HelperPath         string
	DaemonAddress      string
	RecordingDirectory string
	IrrlichtVersion    string
	DesktopSupportRoot string
	ClaudeProjectsRoot string
	ConfigurationRoots []string
}

type LiveRuntime struct {
	options         LiveOptions
	helper          helperClient
	controls        map[string]helperSelector
	processes       map[string]int
	processEvidence map[string]ProcessEvidence
	processBaseline map[int]struct{}
	stepLog         string
	httpClient      *http.Client
	registryByID    map[string]RegistrySession
	workingSeen     map[string]bool
	deepLinkOpened  bool
	openDeepLink    func(context.Context, string) error
	processExists   func(int) (bool, error)
	listProcesses   func(context.Context) (map[int]struct{}, error)
	observeProcess  func(context.Context, int) (string, error)
}

func NewLiveRuntime(options LiveOptions, stepLog string) (*LiveRuntime, error) {
	if options.Home == "" || !filepath.IsAbs(options.Home) {
		return nil, errors.New("an absolute home directory is required")
	}
	if options.HelperPath == "" || !filepath.IsAbs(options.HelperPath) {
		return nil, errors.New("an absolute Desktop helper path is required")
	}
	if options.DaemonAddress == "" || options.RecordingDirectory == "" {
		return nil, errors.New("daemon address and recording directory are required")
	}
	if options.DesktopSupportRoot == "" {
		options.DesktopSupportRoot = filepath.Join(options.Home, "Library", "Application Support", "Claude")
	}
	if options.ClaudeProjectsRoot == "" {
		options.ClaudeProjectsRoot = filepath.Join(options.Home, ".claude", "projects")
	}
	if len(options.ConfigurationRoots) == 0 {
		options.ConfigurationRoots = defaultConfigurationRoots(options.Home, options.DesktopSupportRoot)
	}
	return &LiveRuntime{
		options: options, helper: helperClient{path: options.HelperPath},
		processes: map[string]int{}, processEvidence: map[string]ProcessEvidence{}, stepLog: stepLog,
		httpClient:     &http.Client{Timeout: 2 * time.Second},
		registryByID:   map[string]RegistrySession{},
		workingSeen:    map[string]bool{},
		openDeepLink:   openOfficialDesktopURL,
		processExists:  liveProcessExists,
		listProcesses:  readProcessCensus,
		observeProcess: processCommand,
	}, nil
}

func defaultConfigurationRoots(home, desktopRoot string) []string {
	return []string{
		filepath.Join(desktopRoot, "claude_desktop_config.json"),
		filepath.Join(desktopRoot, "config.json"),
		filepath.Join(desktopRoot, "cowork-enabled-cli-ops.json"),
		filepath.Join(desktopRoot, "extensions-blocklist.json"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".claude", "plugins"),
		filepath.Join(home, ".claude", "skills"),
	}
}

func (runtime *LiveRuntime) Preflight(ctx context.Context) (Versions, error) {
	info, err := os.Stat(runtime.options.HelperPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return Versions{}, fmt.Errorf("Desktop helper is not executable at %q", runtime.options.HelperPath)
	}
	status, err := runtime.helper.preflight(ctx)
	if err != nil {
		return Versions{}, err
	}
	return validateVersions(status, runtime.options.IrrlichtVersion)
}

func validateVersions(status helperStatus, irrlichtVersion string) (Versions, error) {
	if status.BundleIdentifier != desktopBundleID {
		return Versions{}, fmt.Errorf("Desktop bundle ID is %q, want %q", status.BundleIdentifier, desktopBundleID)
	}
	if status.DesktopVersion != supportedDesktopVersion {
		return Versions{}, fmt.Errorf(
			"Desktop version %q has no verified control catalog; supported version is %q",
			status.DesktopVersion,
			supportedDesktopVersion,
		)
	}
	if status.BundledClaudeCodeVersion != supportedClaudeCodeVersion {
		return Versions{}, fmt.Errorf(
			"bundled Claude Code version %q is not the verified version %q",
			status.BundledClaudeCodeVersion,
			supportedClaudeCodeVersion,
		)
	}
	if irrlichtVersion == "" {
		return Versions{}, errors.New("Irrlicht version must be available")
	}
	return Versions{
		DesktopApp: status.DesktopVersion,
		ClaudeCode: status.BundledClaudeCodeVersion,
		Irrlicht:   irrlichtVersion,
	}, nil
}

func (runtime *LiveRuntime) CaptureBaseline(ctx context.Context) (Baseline, error) {
	sessions, files, err := runtime.readRegistry()
	if err != nil {
		return Baseline{}, err
	}
	config, err := CaptureTreeSnapshot(runtime.options.ConfigurationRoots)
	if err != nil {
		return Baseline{}, err
	}
	processes, err := runtime.listProcesses(ctx)
	if err != nil {
		return Baseline{}, fmt.Errorf("capture process baseline: %w", err)
	}
	runtime.processBaseline = processes
	ids := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		ids[session.SessionID] = struct{}{}
	}
	return Baseline{SessionIDs: ids, Files: files, Config: config, Processes: processes}, nil
}

func (runtime *LiveRuntime) OpenComposer(ctx context.Context, workspace string) error {
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("requested Desktop workspace is not a directory: %q", workspace)
	}
	values := url.Values{}
	values.Set("folder", workspace)
	deepLink := "claude://code/new?" + values.Encode()
	// The route can reach Desktop before /usr/bin/open reports an error. Mark
	// the ownership window first so deferred cleanup still searches for it.
	runtime.deepLinkOpened = true
	if err := runtime.openDeepLink(ctx, deepLink); err != nil {
		return err
	}
	return nil
}

func openOfficialDesktopURL(ctx context.Context, deepLink string) error {
	command := exec.CommandContext(ctx, "/usr/bin/open", deepLink)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("open official Desktop deep link: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (runtime *LiveRuntime) WaitComposer(ctx context.Context, workspace string) error {
	controls, err := waitForComposerControls(ctx, workspace, runtime.helper.inspect, runtime.helper.probe)
	if err == nil {
		runtime.controls = controls
	}
	return err
}

func waitForComposerControls(
	ctx context.Context,
	workspace string,
	inspect func(context.Context) ([]helperElement, error),
	probe func(context.Context, map[string]helperSelector) error,
) (map[string]helperSelector, error) {
	var controls map[string]helperSelector
	var lastMismatch error
	err := poll(ctx, "verified Desktop composer controls", func() (bool, error) {
		elements, err := inspect(ctx)
		if err != nil {
			if fatal := transientHelperError(err); fatal != nil {
				return false, fatal
			}
			lastMismatch = err
			return false, nil
		}
		controls, err = composerCatalog(elements, workspace)
		if err != nil {
			lastMismatch = err
			return false, nil
		}
		if err := probe(ctx, controls); err != nil {
			if fatal := transientHelperError(err); fatal != nil {
				return false, fatal
			}
			lastMismatch = err
			return false, nil
		}
		return true, nil
	})
	if err != nil && lastMismatch != nil {
		return controls, fmt.Errorf(
			"%w; last Desktop composer observation for workspace %q: %v",
			err,
			workspace,
			lastMismatch,
		)
	}
	return controls, err
}

func (runtime *LiveRuntime) WaitOwnedSession(
	ctx context.Context,
	baseline Baseline,
	workspace string,
) (OwnedSession, error) {
	if !runtime.deepLinkOpened {
		return OwnedSession{}, nil
	}
	var owned OwnedSession
	err := poll(ctx, "registry and transcript identity", func() (bool, error) {
		sessions, _, err := runtime.readRegistry()
		if err != nil {
			return false, err
		}
		transcripts, err := runtime.readTranscriptIdentities(sessions)
		if err != nil {
			return false, err
		}
		owned, err = SelectOwnedSession(baseline.SessionIDs, sessions, transcripts, workspace)
		if err != nil {
			if identityMayStillAppear(err) {
				return false, nil
			}
			return false, err
		}
		runtime.registryByID[owned.Registry.SessionID] = owned.Registry
		return true, nil
	})
	return owned, err
}

func (runtime *LiveRuntime) RecoverOwnedSession(
	ctx context.Context,
	baseline Baseline,
	workspace string,
) (OwnedSession, error) {
	if !runtime.deepLinkOpened {
		return OwnedSession{}, nil
	}
	var owned OwnedSession
	err := poll(ctx, "provisional Desktop ownership", func() (bool, error) {
		sessions, _, err := runtime.readRegistry()
		if err != nil {
			return false, err
		}
		owned, err = SelectProvisionalSession(baseline.SessionIDs, sessions, workspace)
		if err != nil {
			if identityMayStillAppear(err) {
				return false, nil
			}
			return false, err
		}
		runtime.registryByID[owned.Registry.SessionID] = owned.Registry
		return true, nil
	})
	return owned, err
}

func identityMayStillAppear(err error) bool {
	message := err.Error()
	return strings.Contains(message, "found 0 matches") ||
		strings.Contains(message, "transcript ID") ||
		strings.Contains(message, "is missing")
}

func (runtime *LiveRuntime) SetPrompt(ctx context.Context, prompt string) error {
	selector, ok := runtime.controls["prompt"]
	if !ok {
		return errors.New("prompt selector was not verified")
	}
	return runtime.helper.setValue(ctx, selector, prompt)
}

func (runtime *LiveRuntime) Submit(ctx context.Context) error {
	send, ok := runtime.controls["send"]
	if !ok {
		return errors.New("Send selector was not verified")
	}
	stop := helperSelector{Role: "AXButton", Description: "Stop", Hierarchy: send.Hierarchy}
	return runtime.helper.click(ctx, send, helperPostcondition{
		Selector: stop, Condition: "exists", TimeoutMilliseconds: 10_000,
	})
}

func (runtime *LiveRuntime) WaitIrrlichtState(
	ctx context.Context,
	owned OwnedSession,
	state string,
) (SessionObservation, error) {
	var observation SessionObservation
	err := poll(ctx, "Irrlicht session state "+state, func() (bool, error) {
		sessions, err := runtime.fetchIrrlichtSessions(ctx)
		if err != nil {
			return false, err
		}
		candidate, found, err := selectIrrlichtSession(sessions, owned.Transcript.SessionID)
		if err != nil || !found {
			return false, err
		}
		if !sameWorkspace(candidate.CWD, owned.Registry.CWD) {
			return false, fmt.Errorf("Irrlicht workspace mismatch: registry %q, Irrlicht %q", owned.Registry.CWD, candidate.CWD)
		}
		if candidate.Launcher.HostBundleID != desktopBundleID {
			return false, fmt.Errorf(
				"Irrlicht host bundle ID is %q, want %q",
				candidate.Launcher.HostBundleID,
				desktopBundleID,
			)
		}
		if err := validateOwnedProcessBaseline(runtime.processBaseline, candidate); err != nil {
			return false, err
		}
		command, err := runtime.observeProcess(ctx, candidate.PID)
		if err != nil {
			return false, err
		}
		process := ProcessEvidence{PID: candidate.PID, Command: command}
		if previous, ok := runtime.processEvidence[owned.Registry.SessionID]; ok && previous != process {
			// Name both halves. The comparison is over the whole evidence
			// struct, so a drifting command line with a stable PID is a real
			// mismatch — and reporting only the PIDs printed the same number
			// twice and sent the operator looking at the wrong field.
			return false, fmt.Errorf(
				"owned Claude process identity changed from PID %d (%s) to PID %d (%s)",
				previous.PID, previous.Command, process.PID, process.Command)
		}
		runtime.processes[owned.Registry.SessionID] = candidate.PID
		runtime.processEvidence[owned.Registry.SessionID] = process
		stateSeen, err := runtime.stateObserved(owned.Transcript.SessionID, candidate.State, state)
		if err != nil {
			return false, err
		}
		if !stateSeen {
			return false, nil
		}
		if state == "working" {
			runtime.workingSeen[owned.Transcript.SessionID] = true
		}
		observation = candidate
		return true, nil
	})
	return observation, err
}

func (runtime *LiveRuntime) stateObserved(sessionID, currentState, wantedState string) (bool, error) {
	if wantedState == "ready" && !runtime.workingSeen[sessionID] {
		return currentState == "ready", nil
	}
	expected := []string{"ready", "working"}
	if wantedState == "ready" {
		expected = append(expected, "ready")
	}
	recorded, err := recordingHasStateSequence(runtime.options.RecordingDirectory, sessionID, expected)
	if wantedState == "ready" {
		return currentState == "ready" && recorded, err
	}
	return recorded, err
}

func selectIrrlichtSession(sessions []SessionObservation, sessionID string) (SessionObservation, bool, error) {
	var matches []SessionObservation
	for _, session := range sessions {
		if session.SessionID == sessionID {
			matches = append(matches, session)
		}
	}
	if len(matches) > 1 {
		return SessionObservation{}, false, fmt.Errorf("Irrlicht session %q is ambiguous: found %d rows", sessionID, len(matches))
	}
	if len(matches) == 0 {
		return SessionObservation{}, false, nil
	}
	return matches[0], true, nil
}

// validateOwnedProcessBaseline is the process-identity guard used before an
// Irrlicht observation can be attributed to the newly created Desktop session.
func validateOwnedProcessBaseline(baseline map[int]struct{}, candidate SessionObservation) error {
	if candidate.PID <= 0 {
		return errors.New("Irrlicht session has no live PID")
	}
	if _, existed := baseline[candidate.PID]; existed {
		return fmt.Errorf("Irrlicht session reused baseline process PID %d", candidate.PID)
	}
	return nil
}

func (runtime *LiveRuntime) WaitHook(ctx context.Context, owned OwnedSession) error {
	return poll(ctx, "hook_received event", func() (bool, error) {
		files, err := filepath.Glob(filepath.Join(runtime.options.RecordingDirectory, "*.jsonl"))
		if err != nil {
			return false, err
		}
		for _, file := range files {
			found, err := jsonlContains(file, func(value map[string]any) bool {
				return value["kind"] == "hook_received" && value["session_id"] == owned.Transcript.SessionID
			})
			if err != nil {
				return false, err
			}
			if found {
				return true, nil
			}
		}
		return false, nil
	})
}

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

func (runtime *LiveRuntime) ArchiveOwned(ctx context.Context, owned OwnedSession) error {
	sessions, _, err := runtime.readRegistry()
	if err != nil {
		return err
	}
	elements, err := runtime.helper.inspect(ctx)
	if err != nil {
		return err
	}
	target, err := validateArchiveTarget(owned, sessions, elements)
	if err != nil {
		return err
	}
	if target.registry.Archived {
		return nil
	}
	if err := runtime.helper.probeProject(ctx, target.project); err != nil {
		return fmt.Errorf("re-probe owned Desktop project before archive: %w", err)
	}
	menuRole := helperSelector{Role: "AXMenu"}
	if err := runtime.helper.click(ctx, target.menu, helperPostcondition{
		Selector: menuRole, Condition: "exists", TimeoutMilliseconds: 2_000,
	}); err != nil {
		return fmt.Errorf("open owned-session menu: %w", err)
	}
	elements, err = runtime.helper.inspect(ctx)
	if err != nil {
		return err
	}
	archive, err := uniqueElement(elements, func(element helperElement) bool {
		return element.Role == "AXMenuItem" && element.Title == "Archive"
	}, "Archive menu item")
	if err != nil {
		return err
	}
	if err := runtime.helper.click(ctx, selectorFor(archive), helperPostcondition{
		Selector: menuRole, Condition: "absent", TimeoutMilliseconds: 10_000,
	}); err != nil {
		return fmt.Errorf("archive owned session: %w", err)
	}
	return poll(ctx, "owned registry archive flag", func() (bool, error) {
		current, err := runtime.registrySession(owned.Registry.SessionID)
		return err == nil && current.Archived, err
	})
}

// validateArchiveTarget is the final pure ownership guard before any archive
// click. LiveRuntime supplies a fresh registry and accessibility-tree reading.
type archiveTarget struct {
	registry RegistrySession
	menu     helperSelector
	project  helperSelector
}

func validateArchiveTarget(
	owned OwnedSession,
	sessions []RegistrySession,
	elements []helperElement,
) (archiveTarget, error) {
	var matches []RegistrySession
	for _, session := range sessions {
		if session.SessionID == owned.Registry.SessionID {
			matches = append(matches, session)
		}
	}
	if len(matches) != 1 {
		return archiveTarget{}, fmt.Errorf(
			"Desktop registry session %q requires one row; found %d",
			owned.Registry.SessionID,
			len(matches),
		)
	}
	registry := matches[0]
	if err := validateRegistryIdentity(owned.Registry, registry); err != nil {
		return archiveTarget{}, err
	}
	if registry.Archived {
		return archiveTarget{registry: registry}, nil
	}
	if registry.Title == "" {
		return archiveTarget{}, fmt.Errorf("owned session %q has no title for the selected-session guard", registry.SessionID)
	}
	titleMatches := 0
	for _, session := range sessions {
		if !session.Archived && session.Title == registry.Title {
			titleMatches++
		}
	}
	if titleMatches != 1 {
		return archiveTarget{}, fmt.Errorf("owned active session title %q is not unique; found %d rows", registry.Title, titleMatches)
	}
	controls, err := composerCatalog(elements, registry.CWD)
	if err != nil {
		return archiveTarget{}, fmt.Errorf("validate owned Desktop composer before archive: %w", err)
	}
	menu, err := selectedSessionMenu(elements, registry.Title)
	if err != nil {
		return archiveTarget{}, err
	}
	return archiveTarget{registry: registry, menu: menu, project: controls["project"]}, nil
}

func (runtime *LiveRuntime) WaitProcessExit(ctx context.Context, owned OwnedSession) error {
	pid := runtime.processes[owned.Registry.SessionID]
	sessionID := owned.Transcript.SessionID
	if sessionID == "" {
		sessionID = owned.Registry.CLISessionID
	}
	if pid == 0 {
		if sessionID == "" {
			return nil
		}
		return fmt.Errorf("owned Claude process for session %q was never observed", sessionID)
	}
	return poll(ctx, "owned Claude process exit", func() (bool, error) {
		exists, err := runtime.processExists(pid)
		return !exists, err
	})
}

func liveProcessExists(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func (runtime *LiveRuntime) WaitIrrlichtRemoved(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return poll(ctx, "Irrlicht session removal", func() (bool, error) {
		sessions, err := runtime.fetchIrrlichtSessions(ctx)
		if err != nil {
			return false, err
		}
		for _, session := range sessions {
			if session.SessionID == sessionID {
				return false, nil
			}
		}
		return true, nil
	})
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

func (runtime *LiveRuntime) RecordStep(step string) {
	if runtime.stepLog == "" {
		return
	}
	file, err := os.OpenFile(runtime.stepLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "%s\t%s\n", time.Now().UTC().Format(time.RFC3339Nano), step)
}

func (runtime *LiveRuntime) registryRoot() string {
	return filepath.Join(runtime.options.DesktopSupportRoot, "claude-code-sessions")
}

func (runtime *LiveRuntime) readRegistry() ([]RegistrySession, map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(runtime.registryRoot(), "*", "*", "local_*.json"))
	if err != nil {
		return nil, nil, err
	}
	if len(paths) > 10_000 {
		return nil, nil, fmt.Errorf("Desktop registry exceeds 10000 session files")
	}
	sort.Strings(paths)
	sessions := make([]RegistrySession, 0, len(paths))
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("Desktop registry entry is not a regular file: %q", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		var session RegistrySession
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, nil, fmt.Errorf("decode Desktop registry entry %q: %w", path, err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, nil, fmt.Errorf("decode Desktop registry shape %q: %w", path, err)
		}
		_, session.EnvScopePresent = raw["envScopeId"]
		session.Raw = make(map[string]any, len(raw))
		if err := json.Unmarshal(data, &session.Raw); err != nil {
			return nil, nil, fmt.Errorf("decode Desktop registry raw fields %q: %w", path, err)
		}
		if session.SessionID == "" {
			return nil, nil, fmt.Errorf("Desktop registry entry %q has no sessionId", path)
		}
		session.Path = path
		sessions = append(sessions, session)
		files[path] = data
	}
	return sessions, files, nil
}

func (runtime *LiveRuntime) registrySession(sessionID string) (RegistrySession, error) {
	sessions, _, err := runtime.readRegistry()
	if err != nil {
		return RegistrySession{}, err
	}
	for _, session := range sessions {
		if session.SessionID == sessionID {
			return session, nil
		}
	}
	return RegistrySession{}, fmt.Errorf("Desktop registry session %q is missing", sessionID)
}

func (runtime *LiveRuntime) readTranscriptIdentities(sessions []RegistrySession) (map[string]TranscriptIdentity, error) {
	identities := map[string]TranscriptIdentity{}
	for _, session := range sessions {
		if session.CLISessionID == "" {
			continue
		}
		path, err := runtime.transcriptPath(session.CLISessionID)
		if err != nil {
			if errors.Is(err, errTranscriptPending) {
				continue
			}
			return nil, err
		}
		identity, err := readTranscriptIdentity(path)
		if err != nil {
			return nil, err
		}
		identities[session.CLISessionID] = identity
	}
	return identities, nil
}

func (runtime *LiveRuntime) transcriptPath(sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\\`) {
		return "", fmt.Errorf("invalid transcript session ID %q", sessionID)
	}
	paths, err := filepath.Glob(filepath.Join(runtime.options.ClaudeProjectsRoot, "*", sessionID+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("%w for session %q", errTranscriptPending, sessionID)
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("Claude transcript %q requires one regular file; found %d", sessionID, len(paths))
	}
	info, err := os.Lstat(paths[0])
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Claude transcript is not a regular file: %q", paths[0])
	}
	return paths[0], nil
}

func readTranscriptIdentity(path string) (TranscriptIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return TranscriptIdentity{}, err
	}
	defer file.Close()
	var identity TranscriptIdentity
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var candidate TranscriptIdentity
		if err := json.Unmarshal(scanner.Bytes(), &candidate); err != nil {
			return TranscriptIdentity{}, fmt.Errorf("decode transcript %q: %w", path, err)
		}
		if err := mergeTranscriptIdentity(&identity, candidate); err != nil {
			return TranscriptIdentity{}, fmt.Errorf("transcript %q: %w", path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return TranscriptIdentity{}, err
	}
	if identity.SessionID == "" || identity.CWD == "" || identity.Entrypoint == "" {
		return TranscriptIdentity{}, fmt.Errorf("transcript %q lacks sessionId, cwd, or entrypoint", path)
	}
	return identity, nil
}

func mergeTranscriptIdentity(identity *TranscriptIdentity, candidate TranscriptIdentity) error {
	fields := []struct {
		name      string
		current   *string
		candidate string
	}{
		{"sessionId", &identity.SessionID, candidate.SessionID},
		{"cwd", &identity.CWD, candidate.CWD},
		{"entrypoint", &identity.Entrypoint, candidate.Entrypoint},
	}
	for _, field := range fields {
		if field.candidate == "" {
			continue
		}
		if *field.current != "" && *field.current != field.candidate {
			return fmt.Errorf("inconsistent %s values %q and %q", field.name, *field.current, field.candidate)
		}
		*field.current = field.candidate
	}
	return nil
}

func (runtime *LiveRuntime) fetchIrrlichtSessions(ctx context.Context) ([]SessionObservation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+runtime.options.DaemonAddress+"/api/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	response, err := runtime.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Irrlicht sessions endpoint returned %s", response.Status)
	}
	var root any
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024*1024))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	var sessions []SessionObservation
	if err := collectSessionObjects(root, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func collectSessionObjects(value any, sessions *[]SessionObservation) error {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if err := collectSessionObjects(child, sessions); err != nil {
				return err
			}
		}
	case map[string]any:
		if id, ok := typed["session_id"].(string); ok && id != "" {
			data, err := json.Marshal(typed)
			if err != nil {
				return fmt.Errorf("encode Irrlicht session %q: %w", id, err)
			}
			var session SessionObservation
			if err := json.Unmarshal(data, &session); err != nil {
				return fmt.Errorf("decode Irrlicht session %q: %w", id, err)
			}
			session.Raw = typed
			*sessions = append(*sessions, session)
		}
		for _, child := range typed {
			if err := collectSessionObjects(child, sessions); err != nil {
				return err
			}
		}
	}
	return nil
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

func processCommand(ctx context.Context, pid int) (string, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", fmt.Errorf("read command for PID %d: %w", pid, err)
	}
	command := strings.TrimSpace(string(output))
	if command == "" {
		return "", fmt.Errorf("PID %d has an empty command", pid)
	}
	return command, nil
}

func readProcessCensus(ctx context.Context) (map[int]struct{}, error) {
	command := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxProcessCensusBytes+1))
	if readErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("read process census: %w", readErr)
	}
	if len(data) > maxProcessCensusBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("process census exceeded %d bytes", maxProcessCensusBytes)
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("run process census: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	processes := map[int]struct{}{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if len(processes) >= maxProcessCensusRows {
			return nil, fmt.Errorf("process census exceeded %d rows", maxProcessCensusRows)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("process census contains invalid PID %q", scanner.Text())
		}
		processes[pid] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return processes, nil
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

func validateEvidenceIdentity(owned OwnedSession, evidence CapturedEvidence) error {
	if owned.Registry.SessionID == "" || owned.Registry.CLISessionID == "" ||
		owned.Registry.CLISessionID != owned.Transcript.SessionID ||
		!sameWorkspace(owned.Registry.CWD, owned.Transcript.CWD) ||
		owned.Transcript.Entrypoint != "claude-desktop" || owned.Registry.EnvScopeID != nil {
		return errors.New("Desktop registry and transcript evidence identity is invalid")
	}
	observation := evidence.IrrlichtSession
	if observation.SessionID != owned.Transcript.SessionID ||
		!sameWorkspace(observation.CWD, owned.Registry.CWD) ||
		observation.PID <= 0 || observation.Launcher.HostBundleID != desktopBundleID {
		return errors.New("Irrlicht evidence identity does not match the owned Desktop session")
	}
	if evidence.Process.PID != observation.PID || strings.TrimSpace(evidence.Process.Command) == "" {
		return errors.New("process evidence does not match the owned Irrlicht session")
	}
	environment := evidence.Environment
	if environment.SelectedEnvironment != "Local" ||
		!sameWorkspace(environment.RequestedWorkspace, owned.Registry.CWD) ||
		environment.Project != filepath.Base(filepath.Clean(owned.Registry.CWD)) {
		return errors.New("Desktop environment evidence does not match the verified Local project")
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

func poll(ctx context.Context, name string, observe func() (bool, error)) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		ready, err := observe()
		if err != nil {
			return fmt.Errorf("observe %s: %w", name, err)
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s was not observed before its deadline: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func transientHelperError(err error) error {
	message := err.Error()
	if strings.Contains(message, "control_missing") || strings.Contains(message, "app_not_running") {
		return nil
	}
	return err
}
