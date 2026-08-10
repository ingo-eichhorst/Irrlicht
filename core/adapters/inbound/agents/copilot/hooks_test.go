package copilot

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
)

// --- test doubles, shared by this file and the contract wirings ---

type permCall struct {
	sessionID, transcriptPath, hookEventName string
}

type stopCall struct {
	sessionID, transcriptPath, lastAssistantText string
	waitingCue                                   bool
}

type mockTarget struct {
	mu        sync.Mutex
	permCalls []permCall
	stopCalls []stopCall
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

func (m *mockTarget) totalCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.permCalls) + len(m.stopCalls)
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

type keyedGate map[string]bool

func (g keyedGate) Granted(_, key string) bool { return g[key] }

type mockLogger struct{}

func (mockLogger) LogInfo(string, string, string)                       {}
func (mockLogger) LogError(string, string, string)                      {}
func (mockLogger) LogProcessingTime(string, string, int64, int, string) {}
func (mockLogger) Close() error                                         { return nil }

// --- helpers ---

// copilotSessionRoot relocates COPILOT_HOME to a temp dir and returns the
// declared transcript root inside it. COPILOT_HOME moves the whole config
// directory, and transcripts live in its session-state/ subdirectory.
func copilotSessionRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(copilotHomeEnvVar, home)
	root := filepath.Join(home, "session-state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create session-state dir: %v", err)
	}
	return root
}

// writeSessionTranscript lays down <dir>/<id>/events.jsonl and returns its path.
func writeSessionTranscript(t *testing.T, dir, id string) string {
	t.Helper()
	sessionDir := filepath.Join(dir, id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	path := filepath.Join(sessionDir, transcriptFilename)
	line := `{"type":"session.start","data":{"copilotVersion":"1.0.78"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// writeContractTranscript is the shape the receiving-side contracts want: a
// transcript inside dir whose session id the receiver can resolve. The decoy
// the contract plants outside the tree goes through the same writer, so the
// only thing keeping it from dispatching is confinement, not an unreadable
// file.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	return writeSessionTranscript(t, dir, "sess-contract")
}

// contractPayload renders a Stop-shaped body (the envelope that carries
// transcript_path) for one event name.
func contractPayload(transcriptPath, event string) string {
	body, err := json.Marshal(copilotHookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  event,
		SessionID:      filepath.Base(filepath.Dir(transcriptPath)),
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
	return NewHookHandlerWithConfiner(target, gate, mockLogger{}, TranscriptConfiner()), target
}

// --- behaviour ---

// TestStopHook_DispatchesTurnDone pins the turn-end half of the channel: the
// Stop envelope carries transcript_path directly, and the receiver forwards a
// turn-done signal for the session that path names.
//
// The empty last-assistant text is asserted rather than ignored because it is a
// deliberate choice, not an oversight: Copilot's Stop body carries no such
// field (verified live on 1.0.78), so inventing one would be fabrication.
func TestStopHook_DispatchesTurnDone(t *testing.T) {
	root := copilotSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-stop")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(tp, HookStop))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("got %d Stop dispatches, want 1 (perm=%d)", len(stops), len(target.perms()))
	}
	if stops[0].sessionID != "sess-stop" {
		t.Errorf("sessionID = %q, want %q", stops[0].sessionID, "sess-stop")
	}
	if stops[0].transcriptPath != tp {
		t.Errorf("transcriptPath = %q, want the confined path %q", stops[0].transcriptPath, tp)
	}
	if stops[0].lastAssistantText != "" || stops[0].waitingCue {
		t.Errorf("Stop forwarded text=%q cue=%v; Copilot's Stop body carries neither, so both must be zero",
			stops[0].lastAssistantText, stops[0].waitingCue)
	}
}

// notificationBody renders the Notification envelope exactly as Copilot 1.0.78
// delivers it: camelCase sessionId, hook_event_name, notification_type, and
// NO transcript path of any spelling.
func notificationBody(sessionID, notificationType string) string {
	return `{"sessionId":"` + sessionID + `",` +
		`"timestamp":1786316307940,` +
		`"cwd":"/tmp/wd",` +
		`"message":"Fetch URL: https://example.com",` +
		`"title":"Permission needed",` +
		`"hook_event_name":"Notification",` +
		`"notification_type":"` + notificationType + `"}`
}

// TestNotification_ReconstructsTranscriptPathFromSessionID is the load-bearing
// one for this adapter. Copilot's Notification envelope carries no transcript
// path at all — only a camelCase sessionId — so the receiver has to rebuild the
// path against its own declared root before anything downstream can use it.
//
// A receiver that only read transcript_path would dispatch nothing here, and
// the permission prompt would get no acceleration whatsoever.
func TestNotification_ReconstructsTranscriptPathFromSessionID(t *testing.T) {
	root := copilotSessionRoot(t)
	want := writeSessionTranscript(t, root, "sess-notif")
	h, target := newReceiver(t)

	rec := post(t, h, notificationBody("sess-notif", "permission_prompt"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	perms := target.perms()
	if len(perms) != 1 {
		t.Fatalf("got %d permission dispatches, want 1 — the envelope has no transcript_path, "+
			"so the receiver must rebuild it from sessionId", len(perms))
	}
	if perms[0].sessionID != "sess-notif" {
		t.Errorf("sessionID = %q, want %q", perms[0].sessionID, "sess-notif")
	}
	if perms[0].transcriptPath != want {
		t.Errorf("transcriptPath = %q, want %q", perms[0].transcriptPath, want)
	}
}

// TestNotification_DispatchesUnderAHookNameThatHoldsNoSignal is the guard on
// the whole design decision in hooks.go's package comment.
//
// The name forwarded to HandlePermissionHook decides whether a persistent
// TierHook hold is taken, because the shared handler looks it up in
// session.HookSignal. Notification has no row there, so forwarding it means
// dispatch-only. Forwarding PermissionRequest instead — a one-word change that
// looks like a harmless normalization — would take a hold that Copilot can
// never release on a denied prompt, pinning the session waiting until the
// 12-hour ceiling.
//
// READ THIS IF YOU ARE EDITING core/domain/session/hook_signal.go, NOT THIS
// FILE. The second half of the assertion below fails on a change made in that
// package rather than in this one. hookSignalEffects has no HookNotification
// row today, and its own doc comment invites adding one ("a row here is the
// right fix, not a bespoke branch") — which is sound for claudecode, whose
// Notification means idle_prompt. For copilot the same row would silently
// convert this deliberate no-hold into the 12-hour pin described above,
// because copilot is the one adapter whose CLI emits nothing at all when the
// user denies. If this went red from that side: copilot needs a dispatch that
// takes no hold, so give it one explicitly rather than deleting this check.
func TestNotification_DispatchesUnderAHookNameThatHoldsNoSignal(t *testing.T) {
	root := copilotSessionRoot(t)
	writeSessionTranscript(t, root, "sess-notif")
	h, target := newReceiver(t)

	post(t, h, notificationBody("sess-notif", "permission_prompt"))

	perms := target.perms()
	if len(perms) != 1 {
		t.Fatalf("got %d permission dispatches, want 1", len(perms))
	}
	if perms[0].hookEventName != HookNotification {
		t.Fatalf("forwarded hook name = %q, want %q — any name with a session.HookSignal row "+
			"takes a persistent hold, and Copilot emits nothing on a denied prompt to release it",
			perms[0].hookEventName, HookNotification)
	}
	if _, holds := session.HookSignal(perms[0].hookEventName); holds {
		t.Errorf("hook name %q maps to a signal hold; Copilot must dispatch without holding "+
			"because a denied prompt produces no release event", perms[0].hookEventName)
	}
}

// TestNotification_NonBlockingTypesAreIgnored pins that Copilot's other
// notification types do nothing. They are KNOWN events, so they must also not
// be counted as unrecognized — that counter exists to surface an upstream
// rename, and burying it under routine idle notifications would defeat it.
func TestNotification_NonBlockingTypesAreIgnored(t *testing.T) {
	root := copilotSessionRoot(t)
	writeSessionTranscript(t, root, "sess-notif")

	for _, nt := range []string{"idle_prompt", "background_task", ""} {
		h, target := newReceiver(t)
		rec := post(t, h, notificationBody("sess-notif", nt))
		if rec.Code != http.StatusOK {
			t.Errorf("notification_type=%q: status = %d, want 200", nt, rec.Code)
		}
		if n := target.totalCalls(); n != 0 {
			t.Errorf("notification_type=%q dispatched %d times, want 0", nt, n)
		}
	}
}

// TestNotification_ElicitationDialogAlsoOpens pins the second blocked-on-user
// type alongside permission_prompt.
func TestNotification_ElicitationDialogAlsoOpens(t *testing.T) {
	root := copilotSessionRoot(t)
	writeSessionTranscript(t, root, "sess-notif")
	h, target := newReceiver(t)

	post(t, h, notificationBody("sess-notif", "elicitation_dialog"))

	if n := len(target.perms()); n != 1 {
		t.Fatalf("got %d dispatches for elicitation_dialog, want 1", n)
	}
}

// TestNotification_HostileSessionIDIsConfined is the security consequence of
// rebuilding a path from a caller-supplied id: the id is untrusted, so the
// reconstructed path must still go through the confiner. A traversal id must
// dispatch nothing and still answer 2xx (the shared rule — a refusal is
// reported by the log and counter, never by a status on the user's path).
func TestNotification_HostileSessionIDIsConfined(t *testing.T) {
	root := copilotSessionRoot(t)
	writeSessionTranscript(t, root, "sess-notif")
	h, target := newReceiver(t)

	rec := post(t, h, notificationBody("../../../../etc", "permission_prompt"))

	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx: a confinement refusal is reported by the log and "+
			"counter, not by a status code", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("a traversal sessionId dispatched %d times, want 0", n)
	}
}

// TestHookReceiver_DeniedHooksConsentDropsQuietly is the #570 gate at the
// receiving end: while the hooks permission is not granted, an installed hook
// that is still firing must be dropped without dispatching.
func TestHookReceiver_DeniedHooksConsentDropsQuietly(t *testing.T) {
	root := copilotSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-stop")
	target := &mockTarget{}
	h := NewHookHandlerWithConfiner(target, keyedGate{}, mockLogger{}, TranscriptConfiner())

	rec := post(t, h, contractPayload(tp, HookStop))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while hooks consent was denied, want 0", n)
	}
}

// TestHookReceiver_DeniedTranscriptsConsentDropsQuietly pins the second gate:
// dispatching makes the detector read the transcript, so a transcripts-denied
// session must not dispatch even though the hooks consent is granted.
func TestHookReceiver_DeniedTranscriptsConsentDropsQuietly(t *testing.T) {
	root := copilotSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-stop")
	target := &mockTarget{}
	h := NewHookHandlerWithConfiner(target, keyedGate{PermissionKeyHooks: true}, mockLogger{}, TranscriptConfiner())

	rec := post(t, h, contractPayload(tp, HookStop))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while transcripts consent was denied, want 0", n)
	}
}

// TestHookReceiver_NonPostRejected is a LOCK on the shared receiver shape.
func TestHookReceiver_NonPostRejected(t *testing.T) {
	copilotSessionRoot(t)
	h, _ := newReceiver(t)
	req := httptest.NewRequest(http.MethodGet, HookEndpointPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

// TestHookReceiver_MalformedJSONIs400 is a LOCK: a body that will not decode is
// the one case that answers non-2xx, because nothing about it is a path.
func TestHookReceiver_MalformedJSONIs400(t *testing.T) {
	copilotSessionRoot(t)
	h, target := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times on malformed JSON, want 0", n)
	}
}
