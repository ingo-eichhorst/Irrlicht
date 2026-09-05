package desktopdriver

// What a run records, and the identity join it must satisfy before any of it
// reaches the staging tree.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// A Desktop session is created BY its first turn, so its recorded state
// sequence starts at working. There is no pre-turn ready to observe: the
// registry row and the Claude Code session do not exist until the prompt is
// sent.
//
// Live run 17 (2026-09-05, Desktop 1.46388.4) recorded exactly this for session
// 941db969: state_transition→working "new session created" at 22:17:16.392,
// hook_received at :19.088, state_transition→ready "agent finished turn" at
// :19.092. The driver still demanded a leading ready and timed out after 1m20s
// against a recording that already held the whole turn.
//
// Reading the sequence from the recording also removes a race the live state
// cannot win: that turn was ready 2.7 seconds after it started.
func TestDesktopStateSequenceStartsAtWorking(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		`{"kind":"state_transition","session_id":"941db969","new_state":"working","reason":"new session created"}`,
		`{"kind":"hook_received","session_id":"941db969"}`,
		`{"kind":"state_transition","session_id":"941db969","new_state":"ready","reason":"agent finished turn → ready"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "recording.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &LiveRuntime{options: LiveOptions{RecordingDirectory: dir}}

	// The turn is already finished when the driver first looks. Both waits must
	// still be satisfied, from the recording.
	working, err := runtime.stateObserved("941db969", "ready", "working")
	if err != nil || !working {
		t.Fatalf("working not observed in a finished Desktop turn: %t, %v", working, err)
	}
	ready, err := runtime.stateObserved("941db969", "ready", "ready")
	if err != nil || !ready {
		t.Fatalf("ready not observed in a finished Desktop turn: %t, %v", ready, err)
	}

	// A session that never worked must not read as ready.
	quiet := t.TempDir()
	if err := os.WriteFile(filepath.Join(quiet, "recording.jsonl"),
		[]byte(`{"kind":"state_transition","session_id":"941db969","new_state":"ready"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	idle := &LiveRuntime{options: LiveOptions{RecordingDirectory: quiet}}
	if observed, err := idle.stateObserved("941db969", "ready", "ready"); err != nil || observed {
		t.Fatalf("a session that never worked read as a finished turn: %t, %v", observed, err)
	}
}

// The environment recorded beside a Desktop recording must be the one the turn
// was SENT in, and it can only be read then. After a turn Claude Desktop shows
// the session, not a composer, so a re-read at evidence time finds nothing.
//
// Live run 18 (2026-09-05) drove a complete turn — composer, trust, prompt,
// submit, ownership, working, ready, hook — and then failed at the last step
// with `Desktop environment control requires one AXPopUpButton titled "Local";
// found 0`.
//
// The helper path here does not exist, so any attempt to re-read the live tree
// fails. The verified environment must still come back.
func TestCapturedEnvironmentComesFromTheVerifiedComposer(t *testing.T) {
	runtime := &LiveRuntime{
		helper: helperClient{path: filepath.Join(t.TempDir(), "no-such-helper")},
		environment: EnvironmentEvidence{
			SelectedEnvironment: "Local",
			RequestedWorkspace:  "/repo/workspace",
			Project:             "workspace",
		},
	}
	environment, err := runtime.captureEnvironment(context.Background(), "/repo/workspace")
	if err != nil {
		t.Fatalf("captureEnvironment() on a post-turn tree: %v", err)
	}
	if environment.SelectedEnvironment != "Local" || environment.Project != "workspace" {
		t.Fatalf("captured environment = %+v", environment)
	}

	// A run that never verified a composer must not invent one.
	empty := &LiveRuntime{helper: runtime.helper}
	if _, err := empty.captureEnvironment(context.Background(), "/repo/workspace"); err == nil {
		t.Fatal("captureEnvironment() invented an environment no composer verified")
	}

	// Evidence must belong to the workspace the registry recorded.
	if _, err := runtime.captureEnvironment(context.Background(), "/repo/elsewhere"); err == nil {
		t.Fatal("captureEnvironment() accepted a foreign workspace")
	}
}
