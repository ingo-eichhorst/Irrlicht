package desktopdriver

import (
	"context"
	"encoding/json"
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
