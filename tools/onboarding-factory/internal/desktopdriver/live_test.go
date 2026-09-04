package desktopdriver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenComposerUsesOfficialRouteWithOnlyExactWorkspace(t *testing.T) {
	workspace := t.TempDir()
	var opened string
	runtime := &LiveRuntime{openDeepLink: func(_ context.Context, target string) error {
		opened = target
		return nil
	}}
	if err := runtime.OpenComposer(context.Background(), workspace); err != nil {
		t.Fatalf("OpenComposer() error = %v", err)
	}
	target, err := url.Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme != "claude" || target.Host != "code" || target.Path != "/new" {
		t.Fatalf("route = %q, want claude://code/new", opened)
	}
	if len(target.Query()) != 1 || target.Query().Get("folder") != workspace {
		t.Fatalf("query = %q, want only exact folder %q", target.RawQuery, workspace)
	}
	if !runtime.deepLinkOpened {
		t.Fatal("successful official route did not arm provisional cleanup")
	}
}

func TestVersionGatePinsTheVerifiedDesktopAndBundledCodePair(t *testing.T) {
	status := helperStatus{
		BundleIdentifier:         desktopBundleID,
		DesktopVersion:           supportedDesktopVersion,
		BundledClaudeCodeVersion: supportedClaudeCodeVersion,
	}
	if _, err := validateVersions(status, "0.7.0+test"); err != nil {
		t.Fatalf("validateVersions() error = %v", err)
	}
	status.BundledClaudeCodeVersion = "2.1.261"
	if _, err := validateVersions(status, "0.7.0+test"); err == nil || !strings.Contains(err.Error(), "not the verified version") {
		t.Fatalf("unverified bundled version error = %v", err)
	}
}

func TestOpenComposerArmsCleanupBeforeTheExternalRouteCanFail(t *testing.T) {
	runtime := &LiveRuntime{openDeepLink: func(_ context.Context, _ string) error {
		return os.ErrPermission
	}}
	if err := runtime.OpenComposer(context.Background(), t.TempDir()); err == nil {
		t.Fatal("OpenComposer() returned nil error")
	}
	if !runtime.deepLinkOpened {
		t.Fatal("failed route did not leave provisional cleanup armed")
	}
}

func TestComposerDeadlineNamesObservedNoFolderWorkspaceMismatch(t *testing.T) {
	elements := archiveFixtureElements("No folder", "Unused")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := waitForComposerControls(
		ctx,
		"/repo/exact-workspace",
		func(context.Context) ([]helperElement, error) { return elements, nil },
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error {
			t.Fatal("no trust prompt is on screen; the wait must not click anything")
			return nil
		},
		func(string) {},
	)
	if err == nil || !strings.Contains(err.Error(), "No folder") ||
		!strings.Contains(err.Error(), "project") || !strings.Contains(err.Error(), "/repo/exact-workspace") {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
}

func TestWaitProcessExitObservesTheExactCapturedPID(t *testing.T) {
	observations := 0
	runtime := &LiveRuntime{
		processes: map[string]int{"local_1": 42},
		processExists: func(pid int) (bool, error) {
			if pid != 42 {
				t.Fatalf("process check PID = %d, want 42", pid)
			}
			observations++
			return observations == 1, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_1"}, Transcript: TranscriptIdentity{SessionID: "cli-1"}}
	if err := runtime.WaitProcessExit(ctx, owned); err != nil {
		t.Fatalf("WaitProcessExit() error = %v", err)
	}
	if observations != 2 {
		t.Fatalf("process observations = %d, want 2", observations)
	}
}

func TestWaitProcessExitRefusesAnUnobservedJoinedProcess(t *testing.T) {
	runtime := &LiveRuntime{processes: map[string]int{}, processExists: liveProcessExists}
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_1"}, Transcript: TranscriptIdentity{SessionID: "cli-1"}}
	err := runtime.WaitProcessExit(context.Background(), owned)
	if err == nil || !strings.Contains(err.Error(), "was never observed") {
		t.Fatalf("WaitProcessExit() error = %v", err)
	}
}

func TestReadTranscriptIdentityRejectsInconsistentJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := strings.Join([]string{
		`{"sessionId":"cli-1","cwd":"/exact","entrypoint":"claude-desktop"}`,
		`{"sessionId":"cli-1","cwd":"/other","entrypoint":"claude-desktop"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTranscriptIdentity(path); err == nil || !strings.Contains(err.Error(), "inconsistent cwd") {
		t.Fatalf("readTranscriptIdentity() error = %v", err)
	}
}

func TestIrrlichtSessionSelectionRejectsDuplicateIdentity(t *testing.T) {
	sessions := []SessionObservation{{SessionID: "cli-1"}, {SessionID: "cli-1"}}
	if _, _, err := selectIrrlichtSession(sessions, "cli-1"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("selectIrrlichtSession() error = %v", err)
	}
}

func TestOwnedProcessCannotReuseBaselinePID(t *testing.T) {
	baseline := map[int]struct{}{42: {}}
	candidate := SessionObservation{PID: 42}
	if err := validateOwnedProcessBaseline(baseline, candidate); err == nil {
		t.Fatal("validateOwnedProcessBaseline() accepted a baseline PID")
	}
}

func TestWaitIrrlichtStateInvokesBaselinePIDGuard(t *testing.T) {
	candidate := SessionObservation{
		SessionID: "cli-1", CWD: "/repo/workspace", PID: 42, State: "ready",
		Launcher: Launcher{HostBundleID: desktopBundleID},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(writer).Encode([]SessionObservation{candidate}); err != nil {
			t.Errorf("encode session response: %v", err)
		}
	}))
	defer server.Close()
	runtime := &LiveRuntime{
		options:         LiveOptions{DaemonAddress: strings.TrimPrefix(server.URL, "http://")},
		httpClient:      server.Client(),
		processBaseline: map[int]struct{}{42: {}},
		processes:       map[string]int{},
		processEvidence: map[string]ProcessEvidence{},
		workingSeen:     map[string]bool{},
		observeProcess: func(context.Context, int) (string, error) {
			return "/Applications/Claude.app/claude", nil
		},
	}
	owned := OwnedSession{
		Registry:   RegistrySession{SessionID: "local_1", CWD: "/repo/workspace"},
		Transcript: TranscriptIdentity{SessionID: "cli-1", CWD: "/repo/workspace"},
	}
	_, err := runtime.WaitIrrlichtState(context.Background(), owned, "ready")
	if err == nil || !strings.Contains(err.Error(), "reused baseline process PID 42") {
		t.Fatalf("WaitIrrlichtState() baseline PID error = %v", err)
	}
}

func TestArchiveTargetRejectsDuplicateActiveTitle(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{
		{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Same title"},
		{SessionID: "local_user", CWD: "/repo/other", Title: "Same title"},
	}
	elements := archiveFixtureElements("workspace", "Same title")
	if _, err := validateArchiveTarget(owned, sessions, elements); err == nil {
		t.Fatal("validateArchiveTarget() accepted a duplicate active title")
	}
}

func TestArchiveTargetRejectsSelectedProjectDrift(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Owned title"}}
	elements := archiveFixtureElements("other-project", "Owned title")
	if _, err := validateArchiveTarget(owned, sessions, elements); err == nil {
		t.Fatal("validateArchiveTarget() accepted selected-project drift")
	}
}

func TestArchiveOwnedInvokesDuplicateTitleGuard(t *testing.T) {
	root := t.TempDir()
	workspace := "/repo/workspace"
	registryRoot := filepath.Join(root, "claude-code-sessions", "account", "profile")
	if err := os.MkdirAll(registryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, session := range []RegistrySession{
		{SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace, Title: "Same title"},
		{SessionID: "local_user", CLISessionID: "cli-user", CWD: "/repo/other", Title: "Same title"},
	} {
		data, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(registryRoot, session.SessionID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	response, err := json.Marshal(helperResponse{OK: true, Elements: archiveFixtureElements("workspace", "Same title")})
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "helper")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" + string(response) + "'\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLiveRuntime(LiveOptions{
		Home: root, HelperPath: helper, DaemonAddress: "127.0.0.1:1",
		RecordingDirectory: filepath.Join(root, "recordings"), DesktopSupportRoot: root,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	owned := OwnedSession{Registry: RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace,
	}}
	err = runtime.ArchiveOwned(context.Background(), owned)
	if err == nil || !strings.Contains(err.Error(), "active session title") || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("ArchiveOwned() duplicate-title error = %v", err)
	}
}

func archiveFixtureElements(project, title string) []helperElement {
	elements := []helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
		fixtureElement("project", "AXPopUpButton", project, ""),
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("send", "AXButton", "", "Send"),
		fixtureElement("mode", "AXPopUpButton", "Auto", ""),
		fixtureElement("model", "AXPopUpButton", "", "Model: Opus 5"),
	}
	return append(elements, helperElement{
		Path: selectedSessionMenuPath, Role: "AXPopUpButton",
		Description: "More options for " + title,
		Hierarchy:   []string{"AXApplication", "AXWindow", "AXGroup", "AXPopUpButton"},
	})
}

func TestIrrlichtDecoderRejectsMalformedSessionFields(t *testing.T) {
	var sessions []SessionObservation
	value := map[string]any{"session_id": "cli-1", "pid": "not-a-number"}
	if err := collectSessionObjects(value, &sessions); err == nil || !strings.Contains(err.Error(), "decode Irrlicht session") {
		t.Fatalf("collectSessionObjects() error = %v", err)
	}
}

func TestNoToolTranscriptRejectsNestedToolUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNoToolTranscript(path); err == nil || !strings.Contains(err.Error(), "contains a tool call") {
		t.Fatalf("validateNoToolTranscript() error = %v", err)
	}
}

func TestRecordedStateSequenceRequiresReadyWorkingReadyInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recording.jsonl")
	content := strings.Join([]string{
		`{"kind":"state_transition","session_id":"other","new_state":"ready"}`,
		`{"kind":"state_transition","session_id":"cli-1","new_state":"ready"}`,
		`{"kind":"state_transition","session_id":"cli-1","new_state":"working"}`,
		`{"kind":"state_transition","session_id":"cli-1","new_state":"ready"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := recordingHasStateSequence(dir, "cli-1", []string{"ready", "working", "ready"})
	if err != nil || !found {
		t.Fatalf("recordingHasStateSequence() = %t, %v", found, err)
	}
	mutated, err := recordingHasStateSequence(dir, "cli-1", []string{"ready", "ready", "working"})
	if err != nil || mutated {
		t.Fatalf("out-of-order mutation = %t, %v", mutated, err)
	}
}

func TestEvidencePreservesOmittedLocalScopeAndExactJoin(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"sessionId":"cli-1","cwd":"/exact","entrypoint":"claude-desktop"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owned := OwnedSession{
		Registry: RegistrySession{
			SessionID: "local_1", CLISessionID: "cli-1", CWD: "/exact",
			Raw: map[string]any{"secret-setting": "must-not-leak"},
		},
		Transcript: TranscriptIdentity{SessionID: "cli-1", CWD: "/exact", Entrypoint: "claude-desktop"},
	}
	evidence := CapturedEvidence{
		TranscriptPath: transcript,
		IrrlichtSession: SessionObservation{
			SessionID: "cli-1", CWD: "/exact", PID: 42,
			Launcher: Launcher{HostBundleID: desktopBundleID},
			Raw:      map[string]any{"private-field": "must-not-leak"},
		},
		Process: ProcessEvidence{PID: 42, Command: "/Applications/Claude.app/claude"},
		Environment: EnvironmentEvidence{
			SelectedEnvironment: "Local", RequestedWorkspace: "/exact", Project: "exact",
		},
	}
	out := filepath.Join(dir, "evidence")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := &LiveRuntime{}
	if err := runtime.writeEvidenceFiles(out, owned, evidence); err != nil {
		t.Fatalf("writeEvidenceFiles() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "desktop-registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]json.RawMessage
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	if _, present := registry["envScopeId"]; present {
		t.Fatalf("omitted Local envScopeId was invented: %s", data)
	}
	if string(registry["sessionId"]) != `"local_1"` || string(registry["cliSessionId"]) != `"cli-1"` {
		t.Fatalf("registry evidence did not preserve identity: %s", data)
	}
	if _, present := registry["secret-setting"]; present {
		t.Fatalf("registry evidence leaked an unrelated field: %s", data)
	}
	irrlichtData, err := os.ReadFile(filepath.Join(out, "irrlicht-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var irrlicht map[string]json.RawMessage
	if err := json.Unmarshal(irrlichtData, &irrlicht); err != nil {
		t.Fatal(err)
	}
	if _, present := irrlicht["private-field"]; present {
		t.Fatalf("Irrlicht evidence leaked an unrelated field: %s", irrlichtData)
	}
}

func TestEvidenceRejectsIdentityChangesBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CapturedEvidence)
	}{
		{"session ID", func(e *CapturedEvidence) { e.IrrlichtSession.SessionID = "cli-other" }},
		{"workspace", func(e *CapturedEvidence) { e.IrrlichtSession.CWD = "/other" }},
		{"PID", func(e *CapturedEvidence) { e.Process.PID = 99 }},
		{"bundle", func(e *CapturedEvidence) { e.IrrlichtSession.Launcher.HostBundleID = "other.bundle" }},
		{"environment", func(e *CapturedEvidence) { e.Environment.SelectedEnvironment = "Remote" }},
		{"project", func(e *CapturedEvidence) { e.Environment.Project = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			transcript := filepath.Join(dir, "source.jsonl")
			if err := os.WriteFile(transcript, []byte(`{"sessionId":"cli-1","cwd":"/exact","entrypoint":"claude-desktop"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			owned := OwnedSession{
				Registry:   RegistrySession{SessionID: "local_1", CLISessionID: "cli-1", CWD: "/exact"},
				Transcript: TranscriptIdentity{SessionID: "cli-1", CWD: "/exact", Entrypoint: "claude-desktop"},
			}
			evidence := CapturedEvidence{
				TranscriptPath: transcript,
				IrrlichtSession: SessionObservation{
					SessionID: "cli-1", CWD: "/exact", PID: 42,
					Launcher: Launcher{HostBundleID: desktopBundleID},
				},
				Process: ProcessEvidence{PID: 42, Command: "/Applications/Claude.app/claude"},
				Environment: EnvironmentEvidence{
					SelectedEnvironment: "Local", RequestedWorkspace: "/exact", Project: "exact",
				},
			}
			test.mutate(&evidence)
			if err := (&LiveRuntime{}).writeEvidenceFiles(dir, owned, evidence); err == nil {
				t.Fatal("writeEvidenceFiles() accepted changed identity")
			}
		})
	}
}

func TestRegistryIdentityMustStillMatchAtEvidenceCapture(t *testing.T) {
	expected := RegistrySession{
		SessionID: "local_1", CLISessionID: "cli-1", CWD: "/exact",
	}
	tests := []struct {
		name   string
		mutate func(*RegistrySession)
	}{
		{"session ID", func(r *RegistrySession) { r.SessionID = "local_other" }},
		{"CLI ID", func(r *RegistrySession) { r.CLISessionID = "cli-other" }},
		{"workspace", func(r *RegistrySession) { r.CWD = "/other" }},
		{"scoped environment", func(r *RegistrySession) { value := "builtin_local"; r.EnvScopeID = &value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := expected
			test.mutate(&current)
			if err := validateRegistryIdentity(expected, current); err == nil {
				t.Fatal("validateRegistryIdentity() accepted changed identity")
			}
		})
	}
	if err := validateRegistryIdentity(expected, expected); err != nil {
		t.Fatalf("validateRegistryIdentity() exact match error = %v", err)
	}
}

// trustPromptElements builds the exact two-button shape Claude Desktop shows
// for an untrusted workspace, optionally alongside a working composer.
func trustPromptElements(confirmTitle, cancelTitle string, withComposer bool) []helperElement {
	elements := []helperElement{
		{Path: []int{9, 0}, Role: "AXButton", Title: confirmTitle, Hierarchy: []string{"AXApplication", "AXWindow", "AXSheet"}},
		{Path: []int{9, 1}, Role: "AXButton", Title: cancelTitle, Hierarchy: []string{"AXApplication", "AXWindow", "AXSheet"}},
	}
	if withComposer {
		elements = append(elements, archiveFixtureElements("workspace", "Owned")...)
	}
	return elements
}

// The trust prompt stands in front of the composer on every desktop-local run,
// because the staging workspace is new each time. The wait must answer it and
// then go on to verify the composer.
func TestComposerWaitAnswersTheWorkspaceTrustPrompt(t *testing.T) {
	answered := 0
	inspections := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var steps []string
	controls, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			inspections++
			if answered == 0 {
				return trustPromptElements(trustConfirmTitle, trustCancelTitle, false), nil
			}
			return archiveFixtureElements("workspace", "Owned"), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(_ context.Context, selector helperSelector, post helperPostcondition) error {
			if selector.Title != trustConfirmTitle {
				t.Fatalf("clicked %q, want %q", selector.Title, trustConfirmTitle)
			}
			if post.Condition != "absent" {
				t.Fatalf("postcondition = %q, want the prompt to go away", post.Condition)
			}
			answered++
			return nil
		},
		func(step string) { steps = append(steps, step) },
	)
	if err != nil {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
	if answered != 1 {
		t.Fatalf("trust prompt answered %d times, want exactly 1", answered)
	}
	if len(controls) == 0 {
		t.Fatal("the composer was never verified after the trust prompt")
	}
	if len(steps) != 1 || steps[0] != "trust-workspace-prompt-answered" {
		t.Fatalf("steps = %v; the trust grant must be recorded in the run log", steps)
	}
}

// A prompt that keeps coming back is not this run's own scratch folder being
// trusted, and the tree does not carry the folder being asked about — so the
// second one is a stop, never another click.
func TestComposerWaitRefusesASecondTrustPrompt(t *testing.T) {
	clicks := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := waitForComposerControls(
		ctx,
		"/repo/workspace",
		func(context.Context) ([]helperElement, error) {
			return trustPromptElements(trustConfirmTitle, trustCancelTitle, false), nil
		},
		func(context.Context, map[string]helperSelector) error { return nil },
		func(context.Context, helperSelector, helperPostcondition) error { clicks++; return nil },
		func(string) {},
	)
	if err == nil || !strings.Contains(err.Error(), "second workspace trust prompt") {
		t.Fatalf("waitForComposerControls() error = %v", err)
	}
	if clicks != 1 {
		t.Fatalf("clicked %d times, want exactly 1", clicks)
	}
}

// Only the exact two-button shape is a trust prompt. A lone confirm-looking
// button must never be clicked.
func TestTrustPromptRequiresBothButtons(t *testing.T) {
	for _, shape := range []struct {
		name    string
		confirm string
		cancel  string
	}{
		{"no cancel button", trustConfirmTitle, "Something else"},
		{"no confirm button", "Allow", trustCancelTitle},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if _, prompted := trustPromptButton(
				trustPromptElements(shape.confirm, shape.cancel, false)); prompted {
				t.Fatal("a non-matching shape was read as a trust prompt")
			}
		})
	}
	if _, prompted := trustPromptButton(trustPromptElements(trustConfirmTitle, trustCancelTitle, false)); !prompted {
		t.Fatal("the exact trust-prompt shape was not recognised")
	}
}
