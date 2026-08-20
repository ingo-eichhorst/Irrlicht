package geminicli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
)

// --- test doubles, shared by this file and the contract wirings ---

type permCall struct {
	sessionID, transcriptPath, hookEventName string
}

type stopCall struct {
	sessionID, transcriptPath, lastAssistantText string
	waitingCue                                   bool
}

type permPromptCall struct {
	sessionID, transcriptPath, hookName string
}

type mockTarget struct {
	mu          sync.Mutex
	permCalls   []permCall
	stopCalls   []stopCall
	promptCalls []permPromptCall
	// releaseCalls holds one sessionID per ReleasePermissionPromptHold call.
	releaseCalls []string
}

func (m *mockTarget) HandlePermissionHook(sessionID, transcriptPath, hookEventName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permCalls = append(m.permCalls, permCall{sessionID, transcriptPath, hookEventName})
}

func (m *mockTarget) HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls = append(m.stopCalls, stopCall{sessionID, transcriptPath, lastAssistantText, waitingCue})
}

func (m *mockTarget) HandlePermissionPromptHook(sessionID, transcriptPath, hookName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promptCalls = append(m.promptCalls, permPromptCall{sessionID, transcriptPath, hookName})
}

func (m *mockTarget) ReleasePermissionPromptHold(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCalls = append(m.releaseCalls, sessionID)
}

func (m *mockTarget) totalCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.permCalls) + len(m.stopCalls) + len(m.promptCalls) + len(m.releaseCalls)
}

func (m *mockTarget) perms() []permCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]permCall(nil), m.permCalls...)
}

func (m *mockTarget) stops() []stopCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]stopCall(nil), m.stopCalls...)
}

func (m *mockTarget) prompts() []permPromptCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]permPromptCall(nil), m.promptCalls...)
}

func (m *mockTarget) releases() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.releaseCalls...)
}

// keyedGate pins a fixed permission combination for a one-off test. For a
// MUTABLE keyed gate driven through states by the shared contract, use
// contracttesting.ConsentGate instead.
type keyedGate map[string]bool

func (g keyedGate) Granted(_, key string) bool { return g[key] }

type mockLogger struct{}

func (mockLogger) LogInfo(string, string, string)                       {}
func (mockLogger) LogError(string, string, string)                      {}
func (mockLogger) LogProcessingTime(string, string, int64, int, string) {}
func (mockLogger) Close() error                                         { return nil }

// --- helpers ---

// geminiSessionRoot relocates $HOME to a temp dir and returns the declared
// transcript root inside it. Gemini CLI has no adapter-specific home-override
// environment variable (see hookinstaller.go's geminiHomeDirName comment), so
// unlike copilot's COPILOT_HOME-relocating helper, this one moves $HOME
// itself — the same mechanism agentpaths.AbsRoot resolves through
// (os.UserHomeDir, which reads $HOME on this platform).
func geminiSessionRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gemini", "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create sessions dir: %v", err)
	}
	return root
}

// writeSessionTranscript lays down <root>/<project>/chats/<id>.jsonl and
// returns its path — the shape sessionIDFromTranscriptPath expects (filename
// stem, per agent.FilesUnderRoot's default derivation).
func writeSessionTranscript(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "project", "chats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir chats: %v", err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"header"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// writeContractTranscript is the shape the receiving-side contracts want: a
// transcript inside dir whose session id the receiver can resolve.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	return writeSessionTranscript(t, dir, "sess-contract")
}

// contractPayload renders an AfterAgent-shaped body (every gemini event
// carries transcript_path directly, unlike copilot's Notification) for one
// event name.
func contractPayload(transcriptPath, event string) string {
	body, err := json.Marshal(geminiHookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  event,
		SessionID:      "fake-uuid-not-used-for-identity",
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// notificationBody renders a Notification envelope for one notification_type.
func notificationBody(transcriptPath, notificationType string) string {
	body, err := json.Marshal(geminiHookPayload{
		TranscriptPath:   transcriptPath,
		HookEventName:    HookNotification,
		SessionID:        "fake-uuid-not-used-for-identity",
		NotificationType: notificationType,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// afterAgentBody renders an AfterAgent envelope carrying a prompt_response.
func afterAgentBody(transcriptPath, promptResponse string) string {
	body, err := json.Marshal(geminiHookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  HookAfterAgent,
		SessionID:      "fake-uuid-not-used-for-identity",
		PromptResponse: promptResponse,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// post drives the handler with a raw JSON body and returns the response.
func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, HookEndpointPath, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// newReceiver builds a fully-granted receiver over the real production
// confiner, plus the target it dispatches into.
func newReceiver(t *testing.T) (http.Handler, *mockTarget) {
	t.Helper()
	target := &mockTarget{}
	gate := keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}
	return NewHookHandler(target, gate, mockLogger{}), target
}

// --- behaviour ---

// TestAfterToolHook_DispatchesBroadRelease pins that AfterTool goes through
// the generic name-keyed path, and that the name it forwards ("AfterTool")
// is one session.HookSignal resolves to a broad release — the row
// hook_signal.go declares for it.
func TestAfterToolHook_DispatchesBroadRelease(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-aftertool")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(tp, HookAfterTool))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	perms := target.perms()
	if len(perms) != 1 {
		t.Fatalf("got %d AfterTool dispatches, want 1", len(perms))
	}
	if perms[0].sessionID != "sess-aftertool" {
		t.Errorf("sessionID = %q, want %q", perms[0].sessionID, "sess-aftertool")
	}
	if perms[0].hookEventName != HookAfterTool {
		t.Errorf("forwarded hook name = %q, want %q", perms[0].hookEventName, HookAfterTool)
	}
	effect, ok := session.HookSignal(perms[0].hookEventName)
	if !ok {
		t.Fatalf("session.HookSignal(%q) has no row — AfterTool's broad release depends on one",
			perms[0].hookEventName)
	}
	if effect.Signal != session.SignalPermissionPrompt || !effect.Release {
		t.Errorf("HookSignal(%q) = %+v, want a Release of SignalPermissionPrompt",
			perms[0].hookEventName, effect)
	}
}

// TestAfterAgentHook_DispatchesTurnDoneAndReleasesPermissionPrompt pins the
// turn-end half of the channel AND the Step-A backstop in one place: an
// AfterAgent POST must call BOTH HandleStopHook (the authoritative turn-done
// push) and ReleasePermissionPromptHold (see that method's doc comment for
// why — a cancelled tool confirmation never fires AfterTool, so this is the
// only reliable release for that case).
func TestAfterAgentHook_DispatchesTurnDoneAndReleasesPermissionPrompt(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-afteragent")
	h, target := newReceiver(t)

	rec := post(t, h, afterAgentBody(tp, "All done. Want me to continue?"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("got %d Stop dispatches, want 1", len(stops))
	}
	if stops[0].sessionID != "sess-afteragent" {
		t.Errorf("sessionID = %q, want %q", stops[0].sessionID, "sess-afteragent")
	}
	if stops[0].transcriptPath != tp {
		t.Errorf("transcriptPath = %q, want the confined path %q", stops[0].transcriptPath, tp)
	}
	if !strings.Contains(stops[0].lastAssistantText, "All done") {
		t.Errorf("lastAssistantText = %q, want it to carry the prompt_response text", stops[0].lastAssistantText)
	}
	if !stops[0].waitingCue {
		t.Errorf("waitingCue = false for a final message ending in a question; want true")
	}

	releases := target.releases()
	if len(releases) != 1 {
		t.Fatalf("got %d ReleasePermissionPromptHold calls, want 1 — AfterAgent must release "+
			"unconditionally, because a cancelled confirmation never fires AfterTool", len(releases))
	}
	if releases[0] != "sess-afteragent" {
		t.Errorf("release sessionID = %q, want %q", releases[0], "sess-afteragent")
	}
}

// TestNotification_ToolPermissionOpensAHold pins that a genuine
// ToolPermission notification dispatches through the dedicated
// HandlePermissionPromptHook.
func TestNotification_ToolPermissionOpensAHold(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-notif")
	h, target := newReceiver(t)

	rec := post(t, h, notificationBody(tp, notificationToolPermission))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	prompts := target.prompts()
	if len(prompts) != 1 {
		t.Fatalf("got %d permission-prompt dispatches, want 1 (perm=%d, stop=%d)",
			len(prompts), len(target.perms()), len(target.stops()))
	}
	if prompts[0].sessionID != "sess-notif" {
		t.Errorf("sessionID = %q, want %q", prompts[0].sessionID, "sess-notif")
	}
	if prompts[0].transcriptPath != tp {
		t.Errorf("transcriptPath = %q, want %q", prompts[0].transcriptPath, tp)
	}
}

// TestNotification_DispatchesThroughDedicatedMethodNotTheGenericTable is the
// guard on this adapter's central design decision — see hooks.go's package
// comment and HandlePermissionPromptHook's own doc comment
// (session_detector_lifecycle.go).
//
// READ THIS IF YOU ARE EDITING core/domain/session/hook_signal.go, NOT THIS
// FILE. If a future change adds a "Notification" row to hookSignalEffects
// (its own doc comment invites exactly that: "a row here is the right fix,
// not a bespoke branch"), the SECOND assertion below goes red — and that is
// correct, because such a row would ALSO change what copilot's identically-
// named Notification dispatch does: copilot's package comment explains that
// copilot must NOT take a hold there, because it has nothing that reliably
// fires on a denied prompt to release it. If this test breaks from that
// side, the fix is giving copilot an explicit no-hold dispatch, not deleting
// this assertion.
func TestNotification_DispatchesThroughDedicatedMethodNotTheGenericTable(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-notif")
	h, target := newReceiver(t)

	post(t, h, notificationBody(tp, notificationToolPermission))

	prompts := target.prompts()
	if len(prompts) != 1 {
		t.Fatalf("got %d dispatches through HandlePermissionPromptHook, want 1", len(prompts))
	}
	if prompts[0].hookName != HookNotification {
		t.Errorf("forwarded hook name = %q, want %q", prompts[0].hookName, HookNotification)
	}
	if _, holds := session.HookSignal(prompts[0].hookName); holds {
		t.Errorf("hook name %q now has a session.HookSignal row; gemini-cli's Notification "+
			"assert must keep going through HandlePermissionPromptHook, a table row would "+
			"also change claudecode's and copilot's own identically-named dispatch",
			prompts[0].hookName)
	}
	// No call should have gone through the generic table-driven path for this
	// event — that would be the mutation this test exists to catch.
	if n := len(target.perms()); n != 0 {
		t.Errorf("Notification also dispatched %d time(s) through the generic "+
			"HandlePermissionHook path, want 0", n)
	}
}

// TestNotification_NonToolPermissionTypesAreIgnored pins that any other
// notification_type does nothing. It is a KNOWN event, so it must also not be
// counted as unrecognized.
func TestNotification_NonToolPermissionTypesAreIgnored(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-notif")

	for _, nt := range []string{"", "SomeFutureType"} {
		h, target := newReceiver(t)
		rec := post(t, h, notificationBody(tp, nt))
		if rec.Code != http.StatusOK {
			t.Errorf("notification_type=%q: status = %d, want 200", nt, rec.Code)
		}
		if n := target.totalCalls(); n != 0 {
			t.Errorf("notification_type=%q dispatched %d times, want 0", nt, n)
		}
	}
}

// TestHookReceiver_DeniedHooksConsentDropsQuietly is the #570 gate at the
// receiving end: while the hooks permission is not granted, an installed
// hook that is still firing must be dropped without dispatching.
func TestHookReceiver_DeniedHooksConsentDropsQuietly(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-afteragent")
	target := &mockTarget{}
	h := NewHookHandler(target, keyedGate{}, mockLogger{})

	rec := post(t, h, afterAgentBody(tp, "hi"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while hooks consent was denied, want 0", n)
	}
}

// TestHookReceiver_DeniedTranscriptsConsentDropsQuietly pins the second gate:
// dispatching makes the detector read the transcript, so a transcripts-denied
// session must not dispatch even though the hooks consent is granted, and
// must not log an error while doing it (the receiver's own gate answers
// quietly; only the shared backstop logs).
func TestHookReceiver_DeniedTranscriptsConsentDropsQuietly(t *testing.T) {
	root := geminiSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-afteragent")
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, afterAgentBody(tp, "hi"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while transcripts consent was denied, want 0", n)
	}
	if len(log.Errors()) != 0 {
		t.Errorf("a transcripts-denied hook logged %d error(s): %v", len(log.Errors()), log.Errors())
	}
}

// TestHookReceiver_PermissionGateContract runs the shared #797 contract once
// per permission this receiver must honour, derived from the receiver's own
// declaration (issue #1488).
func TestHookReceiver_PermissionGateContract(t *testing.T) {
	contracttesting.AssertHookReceiverPermissionGated(t, contracttesting.HookReceiverGate{
		Build: func(t *testing.T, gate *contracttesting.ConsentGate) contracttesting.GatedHookReceiver {
			root := geminiSessionRoot(t)
			body := afterAgentBody(writeSessionTranscript(t, root, "sess-gate"), "hi")
			target := &mockTarget{}
			h := NewHookHandler(target, gate, mockLogger{})

			before := 0
			return contracttesting.GatedHookReceiver{
				Handler: h,
				Exercise: func() {
					before = target.totalCalls()
					post(t, h, body)
				},
				Observe: func() bool { return target.totalCalls() > before },
			}
		},
	})
}

// TestHookReceiver_DeclaresItsPermissions is the LOCK on the key list the
// wiring above used to spell out (#1450).
func TestHookReceiver_DeclaresItsPermissions(t *testing.T) {
	contracttesting.AssertDeclaredPermissions(t,
		NewHookHandler(&mockTarget{}, nil, mockLogger{}),
		PermissionKeyHooks, PermissionKeyTranscripts)
}

// TestHookReceiver_NonPostRejected is a LOCK on the shared receiver shape.
func TestHookReceiver_NonPostRejected(t *testing.T) {
	geminiSessionRoot(t)
	h, _ := newReceiver(t)
	req := httptest.NewRequest(http.MethodGet, HookEndpointPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

// TestHookReceiver_MalformedJSONIs400 is a LOCK: a body that will not decode
// is the one case that answers non-2xx, because nothing about it is a path.
func TestHookReceiver_MalformedJSONIs400(t *testing.T) {
	geminiSessionRoot(t)
	h, target := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times on malformed JSON, want 0", n)
	}
}

// TestHookReceiver_HostileTranscriptPathIsConfined pins path confinement
// end-to-end through this receiver: a caller-supplied transcript_path
// escaping the declared root must dispatch nothing and still answer 2xx.
func TestHookReceiver_HostileTranscriptPathIsConfined(t *testing.T) {
	geminiSessionRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, afterAgentBody("/etc/passwd", "hi"))

	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx: a confinement refusal is reported by the log and "+
			"counter, not by a status code", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("an out-of-tree path dispatched %d times, want 0", n)
	}
}
