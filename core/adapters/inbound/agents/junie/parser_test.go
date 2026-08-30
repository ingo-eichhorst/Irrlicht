package junie

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// line parses a raw JSONL string into the map shape ParseLine receives. All
// inputs are verbatim captured lines from live Junie CLI sessions
// (~/.junie/sessions/*/events.jsonl, August 2026) unless a test says
// otherwise; the multi-line flows live in testdata/.
func line(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad test line: %v", err)
	}
	return m
}

// fixtureLines reads a testdata JSONL fixture into its individual lines.
func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// writeTranscript writes lines to a session-shaped transcript and returns its
// path: <tmp>/session-<stamp>/events.jsonl, matching Junie's layout.
func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "session-260824-174140-oolz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, transcriptFilename)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tailFixture replays fixture lines through a fresh tailer and returns the
// resulting metrics — the copilot permission_test pattern.
func tailFixture(t *testing.T, lines []string) *tailer.SessionMetrics {
	t.Helper()
	path := writeTranscript(t, lines)
	m, err := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	return m
}

func TestParser_UserPrompt_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"UserPromptEvent","requestId":"prompt-260824-174155-sm5c","prompt":"test","presentablePrompt":"test","requiresConfirmation":true,"timestampMs":1787586115220}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames on a user prompt")
	}
	if ev.UserText != "test" {
		t.Errorf("UserText = %q", ev.UserText)
	}
	// timestampMs is unix millis — the shared ParseTimestamp heuristics do
	// not read the field, so the parser must.
	if want := time.UnixMilli(1787586115220); !ev.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", ev.Timestamp, want)
	}
}

func TestParser_TaskStarted_TurnStart(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"TaskStartedEvent","taskId":"task-260824-174155-3ua6","timestampMs":1787586115228}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_start" {
		t.Errorf("EventType = %q, want turn_start", ev.EventType)
	}
}

func TestParser_TaskStateCompleted_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"TaskState","state":"COMPLETED","timestampMs":1787586131690}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.SessionError != nil {
		t.Errorf("COMPLETED must not carry a SessionError, got %+v", ev.SessionError)
	}
}

func TestParser_CancelAgent_UserInterrupt(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"CancelAgentEvent","timestampMs":1787586326026}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if !ev.IsUserInterrupt {
		t.Error("IsUserInterrupt = false, want true — a cancel is a real user ESC")
	}
	if ev.IsError || ev.SessionError != nil {
		t.Error("a user cancel is not a failure — IsError/SessionError must stay unset")
	}
}

// AgentFailureEvent carries Junie's detailed failure (message + errorCode).
// Skip=true — the failure must not become LastEventType — but the
// SessionError still lands via the tailer's skipped-event metadata path.
func TestParser_AgentFailure_SessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"FAILED","agentEvent":{"kind":"AgentFailureEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"message":"Junie: {\"type\":\"NO_LICENSE\",\"message\":\"No active JetBrains AI subscription found.\",\"additionalInfo\":null}","errorCode":"AuthorizationFailed"}},"taskId":"task-260824-171930-1nfi","timestampMs":1787584773237}`))
	if ev == nil || !ev.Skip {
		t.Fatalf("AgentFailureEvent should be Skip=true, got %+v", ev)
	}
	if ev.SessionError == nil {
		t.Fatal("expected a SessionError")
	}
	if ev.SessionError.Class != "AuthorizationFailed" {
		t.Errorf("Class = %q, want AuthorizationFailed", ev.SessionError.Class)
	}
	if ev.SessionError.Message == "" {
		t.Error("Message empty, want Junie's own failure prose")
	}
	// Phase stays Unknown: the payload says nothing about a retry, and
	// TaskState (→ turn_done) follows immediately — a "retrying" phase would
	// let that boundary clear the error milliseconds after it was raised.
	if ev.SessionError.Phase != tailer.ErrorPhaseUnknown {
		t.Errorf("Phase = %q, want unknown", ev.SessionError.Phase)
	}
	if ev.SessionError.ClearedByTurnBoundary() {
		t.Error("failure must survive the TaskState turn boundary that follows it")
	}
}

// The bare AgentTaskFailedEvent reports a generic SessionError only when no
// detailed AgentFailureEvent was seen — otherwise it would overwrite the
// informative message with a generic one.
func TestParser_AgentTaskFailed_DedupesAgainstDetail(t *testing.T) {
	failedLine := `{"kind":"AgentTaskFailedEvent","timestampMs":1787585385934}`

	// Alone: the generic error is all there is, so it must be reported.
	alone := (&Parser{}).ParseLine(line(t, failedLine))
	if alone.SessionError == nil {
		t.Fatal("bare AgentTaskFailedEvent with no prior detail: want a generic SessionError")
	}

	// After the detailed AgentFailureEvent: suppressed.
	p := &Parser{}
	p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"FAILED","agentEvent":{"kind":"AgentFailureEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"message":"Junie: license check failed","errorCode":"AuthorizationFailed"}},"timestampMs":1787585385900}`))
	ev := p.ParseLine(line(t, failedLine))
	if ev.SessionError != nil {
		t.Errorf("generic error after a detailed one: got %+v, want nil (dedupe)", ev.SessionError)
	}
}

// An AskAsync question under INPUT_REQUIRED opens a transcript-tier
// permission prompt keyed on stepId; its status=COMPLETED re-emission
// resolves it; the end-of-task replay of the long-resolved prompt is skipped.
func TestParser_AskAsync_OpenResolveReplay(t *testing.T) {
	p := &Parser{}
	open := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"INPUT_REQUIRED","agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"461a1305-fc14-4960-bf52-274da6c47edb","title":"Who is the primary audience for this Kotlin Toolchain talk?","request":{"id":"12bf06bb-bbba-404c-b754-b9296ad74914","question":"Who is the primary audience for this Kotlin Toolchain talk?","isRequired":false},"status":"IN_PROGRESS"}},"taskId":"task-260824-174337-1avu","timestampMs":1787586232389}`))
	if open.EventType != "permission_requested" {
		t.Fatalf("EventType = %q, want permission_requested", open.EventType)
	}
	if len(open.PermissionRequestIDs) != 1 || open.PermissionRequestIDs[0] != "461a1305-fc14-4960-bf52-274da6c47edb" {
		t.Errorf("PermissionRequestIDs = %v, want the stepId", open.PermissionRequestIDs)
	}
	if open.AssistantText == "" {
		t.Error("AssistantText empty, want the question title for waiting-state display")
	}

	resolve := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"461a1305-fc14-4960-bf52-274da6c47edb","title":"Who is the primary audience for this Kotlin Toolchain talk?","status":"COMPLETED"}},"taskId":"task-260824-174337-1avu","timestampMs":1787586236325}`))
	if resolve.EventType != "permission_completed" {
		t.Fatalf("EventType = %q, want permission_completed", resolve.EventType)
	}
	if len(resolve.PermissionResolvedIDs) != 1 || resolve.PermissionResolvedIDs[0] != "461a1305-fc14-4960-bf52-274da6c47edb" {
		t.Errorf("PermissionResolvedIDs = %v, want the stepId", resolve.PermissionResolvedIDs)
	}

	// End-of-task replay (state COMPLETED, status COMPLETED, no longer open).
	replay := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"COMPLETED","agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"461a1305-fc14-4960-bf52-274da6c47edb","title":"Who is the primary audience for this Kotlin Toolchain talk?","status":"COMPLETED"}},"timestampMs":1787586402383}`))
	if !replay.Skip {
		t.Errorf("end-of-task replay of a resolved prompt should be skipped, got %+v", replay)
	}
}

// UserAsyncResponseEvent carries no stepId, so it resolves EVERY open prompt.
func TestParser_UserAsyncResponse_DrainsOpenPrompts(t *testing.T) {
	p := &Parser{}
	p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"INPUT_REQUIRED","agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"f6e34e2e-0000-4000-8000-000000000001","title":"What experience level should attendees have?","status":"IN_PROGRESS"}},"timestampMs":1787586300000}`))
	ev := p.ParseLine(line(t, `{"kind":"UserAsyncResponseEvent","entries":[{"question":"What experience level should attendees have?","answer":"Mixed Levels"}],"timestampMs":1787586236324}`))
	if ev.EventType != "permission_completed" {
		t.Errorf("EventType = %q, want permission_completed", ev.EventType)
	}
	if len(ev.PermissionResolvedIDs) != 1 || ev.PermissionResolvedIDs[0] != "f6e34e2e-0000-4000-8000-000000000001" {
		t.Errorf("PermissionResolvedIDs = %v, want the open stepId drained", ev.PermissionResolvedIDs)
	}
}

// A terminal command approval (real captured flow in testdata): the
// INPUT_REQUIRED block with approvalRequest opens the prompt WITHOUT opening
// a ToolUse (the command has not run); the grant arrives as the same stepId
// re-emitted under state IN_PROGRESS — resolving the prompt AND opening the
// tool; the COMPLETED replay closes the tool.
func TestParser_TerminalApproval_OpenGrantComplete(t *testing.T) {
	lines := fixtureLines(t, "real-terminal-approval.jsonl")
	if len(lines) != 3 {
		t.Fatalf("fixture has %d lines, want 3", len(lines))
	}
	p := &Parser{}

	open := p.ParseLine(line(t, lines[0]))
	if open.EventType != "permission_requested" {
		t.Fatalf("open: EventType = %q, want permission_requested", open.EventType)
	}
	if len(open.PermissionRequestIDs) != 1 {
		t.Fatalf("open: PermissionRequestIDs = %v", open.PermissionRequestIDs)
	}
	if len(open.ToolUses) != 0 {
		t.Errorf("open: ToolUses = %v, want none — the command has not been approved yet", open.ToolUses)
	}

	grant := p.ParseLine(line(t, lines[1]))
	if len(grant.PermissionResolvedIDs) != 1 || grant.PermissionResolvedIDs[0] != open.PermissionRequestIDs[0] {
		t.Errorf("grant: PermissionResolvedIDs = %v, want [%s]", grant.PermissionResolvedIDs, open.PermissionRequestIDs[0])
	}
	if len(grant.ToolUses) != 1 || grant.ToolUses[0].ID != open.PermissionRequestIDs[0] {
		t.Errorf("grant: ToolUses = %v, want the approved command opened", grant.ToolUses)
	}

	complete := p.ParseLine(line(t, lines[2]))
	if complete.Skip {
		t.Fatal("complete: block completion must not be skipped")
	}
	if len(complete.ToolResultIDs) != 1 || complete.ToolResultIDs[0] != open.PermissionRequestIDs[0] {
		t.Errorf("complete: ToolResultIDs = %v", complete.ToolResultIDs)
	}
}

func TestParser_ToolBlock_OpenAndComplete(t *testing.T) {
	p := &Parser{}
	open := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ToolBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"083a37ec-a506-40c9-97e3-70b8d02a5811","text":"Open TODOS.md","status":"IN_PROGRESS"}},"timestampMs":1787585999929}`))
	if open.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", open.EventType)
	}
	if len(open.ToolUses) != 1 || open.ToolUses[0].ID != "083a37ec-a506-40c9-97e3-70b8d02a5811" {
		t.Fatalf("ToolUses = %v, want the stepId", open.ToolUses)
	}

	// Completion names the tool (toolType "Search" on this real line).
	done := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ToolBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"590d1724-0d25-4455-b737-ff2af87f627e","text":"Found \"login\" ","status":"COMPLETED","details":"main.js [573—577]","toolType":"Search"}},"timestampMs":1787585997213}`))
	if done.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", done.EventType)
	}
	if len(done.ToolResultIDs) != 1 || done.ToolResultIDs[0] != "590d1724-0d25-4455-b737-ff2af87f627e" {
		t.Errorf("ToolResultIDs = %v", done.ToolResultIDs)
	}
	if done.IsError {
		t.Error("a COMPLETED block is not an error")
	}
}

// A FAILED terminal block (real capture: docker port already allocated) is
// the agent working normally — IsError, never SessionError.
func TestParser_TerminalBlockFailed_ToolErrorNotSessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"TerminalBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"4c2120e4-215a-4e38-b0bf-5dff7f745fec","status":"FAILED","command":"docker compose up -d","output":"Error response from daemon: Bind for 0.0.0.0:5432 failed: port is already allocated"}},"timestampMs":1787600000000}`))
	if ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if !ev.IsError {
		t.Error("IsError = false, want true for a FAILED block")
	}
	if ev.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil — a failed command must never turn the session red", ev.SessionError)
	}
	if len(ev.ToolResultIDs) != 1 {
		t.Errorf("ToolResultIDs = %v, want the failed block closed", ev.ToolResultIDs)
	}
}

func TestParser_AgentThought_AssistantMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"AgentThoughtBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"0d91a528-25c2-42c5-bb71-e2292ec466b0","text":"Now I'm reviewing the brainstorming document to understand the key ideas."}},"taskId":"task-260824-174337-1avu","timestampMs":1787586223111}`))
	if ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if ev.AssistantText == "" {
		t.Error("AssistantText empty, want the thought text")
	}
}

func TestParser_ResultBlock_FinalAnswer(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"stepId":"7ed437e1-a589-495a-b9de-6b7fb9fa7bce","cancelled":false,"result":"Hello! I am ready to help. Please let me know what task, issue, or question you would like to work on.","title":"Ready To Help","changes":[],"errorCode":"Submit"}},"taskId":"task-260824-172925-rixm","timestampMs":1787585375354}`))
	if ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if !strings.Contains(ev.AssistantText, "ready to help") {
		t.Errorf("AssistantText = %q, want the result text", ev.AssistantText)
	}
}

// Unknown kinds — top-level and nested — are skipped, never fatal: the schema
// is unversioned, and a parser that can't read a line checks MORE, not less.
func TestParser_UnknownKinds_Skip(t *testing.T) {
	cases := map[string]string{
		"no kind":              `{"timestampMs":1787584770854}`,
		"unknown top-level":    `{"kind":"UserMessagesCommittedToHistory","timestampMs":1787585384700}`,
		"system message":       `{"kind":"SystemMessageEvent","text":"Model switched to Gemini 3.7 Flash with effort medium provided via JetBrains AI","details":"The changes will take effect with the next task.","level":"MODEL_SWITCH","timestampMs":1787567861542}`,
		"unknown agentEvent":   `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"AvailablePullRequestsEvent","pullRequests":[]}},"timestampMs":1787585384700}`,
		"tip under waiting":    `{"kind":"SessionA2uxEvent","event":{"state":"INPUT_REQUIRED","agentEvent":{"kind":"TipSuggestionCreatedEvent","tip":"press tab"}},"timestampMs":1787585384700}`,
		"a2ux without event":   `{"kind":"SessionA2uxEvent","timestampMs":1787585384700}`,
		"block without stepId": `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ToolBlockUpdatedEvent","status":"IN_PROGRESS"}},"timestampMs":1787585384700}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			ev := (&Parser{}).ParseLine(line(t, raw))
			if ev == nil || !ev.Skip {
				t.Errorf("want Skip=true, got %+v", ev)
			}
			if ev != nil && (len(ev.PermissionRequestIDs) != 0 || len(ev.ToolUses) != 0) {
				t.Errorf("a skipped line must carry no deltas, got %+v", ev)
			}
		})
	}
}

// Full-session lifecycle over the captured fixture: the session reads
// working (user prompt) → waiting (INPUT_REQUIRED) → working (answer) →
// ready (TaskState). Asserted at the tailer level — TranscriptPermissionPending
// is what lets the classifier reach `waiting`, LastEventType "turn_done" is
// what settles it to `ready`.
func TestLifecycle_WorkingWaitingWorkingReady(t *testing.T) {
	lines := fixtureLines(t, "real-lifecycle-session.jsonl")
	if len(lines) != 10 {
		t.Fatalf("fixture has %d lines, want 10", len(lines))
	}
	askAt, answerAt := 6, 7 // 1-based prefix lengths: through the ask / the answer

	// Through the INPUT_REQUIRED ask: blocked on the user → waiting.
	m := tailFixture(t, lines[:askAt])
	if !m.TranscriptPermissionPending {
		t.Error("after the ask: TranscriptPermissionPending = false, want true (waiting)")
	}

	// Through the user's answer: unblocked → working again.
	m = tailFixture(t, lines[:answerAt])
	if m.TranscriptPermissionPending {
		t.Error("after the answer: TranscriptPermissionPending = true, want false (working)")
	}
	if m.LastEventType == "turn_done" {
		t.Error("the turn must not be done yet after the answer")
	}

	// Full session: settled → ready, no error, nothing pending.
	m = tailFixture(t, lines)
	if m.LastEventType != "turn_done" {
		t.Errorf("full session: LastEventType = %q, want turn_done (ready)", m.LastEventType)
	}
	if m.TranscriptPermissionPending {
		t.Error("full session: TranscriptPermissionPending = true, want false")
	}
	if m.SessionError != nil {
		t.Errorf("full session: SessionError = %+v, want nil", m.SessionError)
	}
}

// Captured CurrentDirectoryUpdatedEvent lines (paths redacted). Junie emits
// the event once per directory change; the FIRST one of a task routinely
// carries an empty currentDirectory before the real path arrives.
const (
	cwdUpdateLine      = `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"CurrentDirectoryUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"currentDirectory":"/Users/dev/Workspace/demo"}},"taskId":"task-260826-145537-blgp","timestampMs":1787748947703}`
	cwdUpdateEmptyLine = `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"CurrentDirectoryUpdatedEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"currentDirectory":""}},"taskId":"task-260826-145537-blgp","timestampMs":1787748937966}`
)

// The transcript is the only place Junie names the session's working
// directory (the events.jsonl lines carry no top-level cwd field the generic
// extractor reads), so the parser must surface CurrentDirectoryUpdatedEvent —
// or every Junie session lands in the dashboard's "unknown" project group.
// Skip=true because the event is pure bookkeeping (it must not become
// LastEventType); the tailer folds CWD from skipped events (#1798).
func TestParser_CurrentDirectoryUpdated_CWD(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, cwdUpdateLine))
	if ev == nil {
		t.Fatal("expected an event")
	}
	if !ev.Skip {
		t.Error("directory bookkeeping must be Skip=true (must not become LastEventType)")
	}
	if ev.CWD != "/Users/dev/Workspace/demo" {
		t.Errorf("CWD = %q, want /Users/dev/Workspace/demo", ev.CWD)
	}
}

// The empty-path variant must surface no CWD at all: through the tailer an
// empty parsed.CWD leaves lastCWD untouched, so a real path seen earlier
// survives the empty re-emit. A user prompt opens the fixture because
// directory events only ever follow a task in live captures — and the
// tailer surfaces LastCWD only on a pass with message history.
func TestTailer_CurrentDirectoryUpdated_LastCWD(t *testing.T) {
	prompt := `{"kind":"UserPromptEvent","requestId":"prompt-260826-145537-iod9","prompt":"test","presentablePrompt":"test","requiresConfirmation":true,"timestampMs":1787748937960}`
	m := tailFixture(t, []string{prompt, cwdUpdateEmptyLine, cwdUpdateLine, cwdUpdateEmptyLine})
	if m.LastCWD != "/Users/dev/Workspace/demo" {
		t.Errorf("LastCWD = %q, want /Users/dev/Workspace/demo", m.LastCWD)
	}
}

// A failed session (captured NO_LICENSE run): the detailed failure survives
// both the generic AgentTaskFailedEvent that follows it and the TaskState
// turn boundary, so the classifier routes the session to `error`.
func TestLifecycle_FailedSession_Error(t *testing.T) {
	lines := fixtureLines(t, "real-failed-session.jsonl")
	if len(lines) != 5 {
		t.Fatalf("fixture has %d lines, want 5", len(lines))
	}
	m := tailFixture(t, lines)
	if m.SessionError == nil {
		t.Fatal("SessionError = nil, want the captured AuthorizationFailed failure")
	}
	if m.SessionError.Class != "AuthorizationFailed" {
		t.Errorf("Class = %q, want AuthorizationFailed (the detailed event, not the generic marker)", m.SessionError.Class)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done — Junie finalizes failed tasks too", m.LastEventType)
	}
}
