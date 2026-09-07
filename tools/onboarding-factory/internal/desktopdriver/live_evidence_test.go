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
	working, err := runtime.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "ready", wantedState: "working",
	})
	if err != nil || !working {
		t.Fatalf("working not observed in a finished Desktop turn: %t, %v", working, err)
	}
	ready, err := runtime.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "ready", wantedState: "ready",
	})
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
	if observed, err := idle.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "ready", wantedState: "ready",
	}); err != nil || observed {
		t.Fatalf("a session that never worked read as a finished turn: %t, %v", observed, err)
	}
}

// A multi-turn recipe (#1888) sends more than one turn to the SAME session.
// recordingHasStateSequence rescans the whole recording from the start on
// every call — it has no cursor of its own — so without a per-turn
// expectation, a second turn's wait for "working" would stale-match the
// FIRST turn's own "working" transition and return immediately, before
// Desktop had even started the second turn. The expectation must be
// CUMULATIVE: turn N's wait requires N-1 complete working->ready cycles
// before it, so a later turn can only be satisfied by matching strictly
// further into the recording than any earlier turn's own transitions.
func TestStateObservedRequiresEveryPriorTurnBeforeMatchingALaterOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recording.jsonl")
	working := `{"kind":"state_transition","session_id":"941db969","new_state":"working"}`
	ready := `{"kind":"state_transition","session_id":"941db969","new_state":"ready"}`
	write := func(lines ...string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Turn one's own complete cycle is recorded; turn two has sent nothing yet.
	write(working, ready)
	runtime := &LiveRuntime{options: LiveOptions{RecordingDirectory: dir}, turn: 2}

	if seen, err := runtime.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "working", wantedState: "working",
	}); err != nil || seen {
		t.Fatalf(`turn two's wait for "working" matched turn one's own transition: seen=%t err=%v`, seen, err)
	}

	// Turn two's own working transition now appears.
	write(working, ready, working)
	if seen, err := runtime.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "working", wantedState: "working",
	}); err != nil || !seen {
		t.Fatalf(`stateObserved() did not match turn two's own "working" transition: seen=%t err=%v`, seen, err)
	}

	// Turn two's ready has not appeared yet.
	if seen, err := runtime.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "ready", wantedState: "ready",
	}); err != nil || seen {
		t.Fatalf(`turn two's wait for "ready" matched before its own transition existed: seen=%t err=%v`, seen, err)
	}

	// Turn two completes.
	write(working, ready, working, ready)
	if seen, err := runtime.stateObserved(stateCheck{
		sessionID: "941db969", currentState: "ready", wantedState: "ready",
	}); err != nil || !seen {
		t.Fatalf(`stateObserved() did not match turn two's own "ready" transition: seen=%t err=%v`, seen, err)
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

// The recording is written by the daemon; the live state comes from its HTTP
// API. The file can lag, and when it does the driver waits for a transition
// that has already happened.
//
// Measured 2026-09-06 on cell 1-1: the run failed with `wait for Irrlicht state
// working timed out after 1m30s`, and the recording read afterwards held
// `ready (new session created)`, `working (force ready→working on first
// activity)`, `ready (agent finished turn)` for that very session. Nothing was
// missing; it simply was not on disk yet.
//
// For the FIRST turn the live state settles that: if the daemon says the
// session is working, it is. For a later turn it cannot, because a live
// "working" does not say WHICH turn it belongs to — only the cumulative
// recorded sequence does. So the shortcut is confined to turn one.
func TestWorkingIsAcceptedFromTheLiveStateOnTheFirstTurn(t *testing.T) {
	empty := t.TempDir() // no recording flushed yet
	runtime := &LiveRuntime{options: LiveOptions{RecordingDirectory: empty}}

	observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "working", wantedState: "working",
	})
	if err != nil || !observed {
		t.Fatalf("a live working state was not accepted on turn one: %t, %v", observed, err)
	}
	// A short turn is already ready before any poll can see it working, and the
	// recording that proves it worked has not been flushed. Moving on is right:
	// waitForCompletion still demands the recorded working→ready sequence.
	if observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "ready", wantedState: "working",
	}); err != nil || !observed {
		t.Fatalf("a turn that finished before the first poll blocked the run: %t, %v", observed, err)
	}
	// A session that has not started at all must still wait.
	if observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "error", wantedState: "working",
	}); err != nil || observed {
		t.Fatalf("a session in error read as a turn that ran: %t, %v", observed, err)
	}

	// On a later turn the live state cannot say which turn it belongs to, so
	// only the recorded sequence counts.
	later := &LiveRuntime{options: LiveOptions{RecordingDirectory: empty}, turn: 2}
	if observed, err := later.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "working", wantedState: "working",
	}); err != nil || observed {
		t.Fatalf("turn two accepted a live working state with no recorded history: %t, %v", observed, err)
	}
}

// The no-tool rule is the safety boundary of the ONE-TURN form: a bare prompt
// the driver sends must not have caused tool execution in the user's workspace.
//
// A recipe is different. Its steps are declared, reviewed and refused up front
// when they need a control the driver lacks, and many catalog cells exist
// precisely to exercise tool use — task lists, subagents, background processes.
// Applying the one-turn rule to them made every such cell impossible: cell 3-3
// drove its whole recipe and then failed with `Desktop transcript … contains a
// tool call or tool result`.
func TestToolsAreRefusedForABarePromptAndAllowedForARecipe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	withTool := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(withTool), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateTranscriptToolUse(path, false); err == nil {
		t.Fatal("a bare prompt was allowed to have caused tool execution")
	}
	if err := validateTranscriptToolUse(path, true); err != nil {
		t.Fatalf("a recipe was refused for using tools it declares: %v", err)
	}

	// An unreadable transcript fails either way: a check that cannot look must
	// never report what a check that looked and found nothing reports.
	missing := filepath.Join(dir, "absent.jsonl")
	if err := validateTranscriptToolUse(missing, true); err == nil {
		t.Fatal("a transcript that could not be read was accepted")
	}
}

// The recorded `ready` is the LAST event a turn writes, so it is the one most
// likely not to be on disk when the driver looks. Cells 2-3, 3-1 and 2-16 all
// timed out at `wait for Irrlicht state ready` on turns that had finished.
//
// What the gate needs to establish is that this turn ran and is over. The
// recorded `working` for this turn establishes the first; the live state
// establishes the second. Requiring the recorded `ready` as well adds nothing
// the other two do not already say, and costs runs.
//
// The recording is still validated in full at promotion time, by
// expected-validate against expected.jsonl. This gate is not that check.
func TestReadyIsSatisfiedByALiveIdleSessionThatHasWorked(t *testing.T) {
	dir := t.TempDir()
	worked := `{"kind":"state_transition","session_id":"cli-1","new_state":"working"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "recording.jsonl"), []byte(worked), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &LiveRuntime{options: LiveOptions{RecordingDirectory: dir}}
	observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "ready", wantedState: "ready",
	})
	if err != nil || !observed {
		t.Fatalf("a finished turn whose ready is not flushed yet blocked the run: %t, %v", observed, err)
	}

	// Still working: not finished, must wait.
	if observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "working", wantedState: "ready",
	}); err != nil || observed {
		t.Fatalf("a turn still in flight read as finished: %t, %v", observed, err)
	}

	// Never worked: there is no turn to be finished with.
	empty := &LiveRuntime{options: LiveOptions{RecordingDirectory: t.TempDir()}}
	if observed, err := empty.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "ready", wantedState: "ready",
	}); err != nil || observed {
		t.Fatalf("a session that never worked read as a completed turn: %t, %v", observed, err)
	}
}

// A blocking user question ends the agent turn in waiting, not ready. The
// driver must require the recorded working->waiting transition. A live waiting
// state with only working recorded is not enough, because the recording is the
// evidence that the waiting transition belongs to this turn.
func TestWaitingRequiresItsOwnRecordedTransition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recording.jsonl")
	working := `{"kind":"state_transition","session_id":"cli-1","new_state":"working"}`
	waiting := `{"kind":"state_transition","session_id":"cli-1","new_state":"waiting"}`
	if err := os.WriteFile(path, []byte(working+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &LiveRuntime{options: LiveOptions{RecordingDirectory: dir}}
	if observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "waiting", wantedState: "waiting",
	}); err != nil || observed {
		t.Fatalf("waiting was accepted before its recorded transition: observed=%t err=%v", observed, err)
	}
	if err := os.WriteFile(path, []byte(strings.Join([]string{working, waiting}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if observed, err := runtime.stateObserved(stateCheck{
		sessionID: "cli-1", currentState: "waiting", wantedState: "waiting",
	}); err != nil || !observed {
		t.Fatalf("recorded waiting was not accepted: observed=%t err=%v", observed, err)
	}
}
