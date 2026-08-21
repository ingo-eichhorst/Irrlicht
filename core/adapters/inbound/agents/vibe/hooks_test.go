package vibe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"irrlicht/core/internal/contracttesting"
)

// --- test doubles, shared by this file and the contract wirings ---

type stopCall struct {
	sessionID, transcriptPath, lastAssistantText string
	waitingCue                                   bool
}

type mockTarget struct {
	mu        sync.Mutex
	stopCalls []stopCall
}

func (m *mockTarget) HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls = append(m.stopCalls, stopCall{sessionID, transcriptPath, lastAssistantText, waitingCue})
}

func (m *mockTarget) totalCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.stopCalls)
}

func (m *mockTarget) stops() []stopCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]stopCall(nil), m.stopCalls...)
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

// vibeSessionRoot relocates $HOME and $VIBE_HOME to temp dirs and returns
// the declared transcript root inside it — every test in this package must
// isolate VIBE_HOME to a t.TempDir(), never the real ~/.vibe.
func vibeSessionRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(vibeHomeEnvVar, "")
	root := filepath.Join(home, defaultRootDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create sessions dir: %v", err)
	}
	return root
}

// writeSessionTranscript lays down <root>/<session-id>/messages.jsonl and
// returns its path — the shape sessionIDFromPath expects (parent directory
// name, since the filename itself is the constant messages.jsonl).
func writeSessionTranscript(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, transcriptFilename)
	if err := os.WriteFile(path, []byte(`{"role":"user"}`+"\n"), 0o600); err != nil {
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

// contractPayload renders a post_agent_turn-shaped body for one event name
// — the shape every receiving-side contract wants: transcript_path plus a
// hook_event_name, nothing else, matching Vibe's OWN wire shape for this
// event (live-fired during this issue's audit).
func contractPayload(transcriptPath, event string) string {
	body, err := json.Marshal(vibeHookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  event,
		SessionID:      "fake-session-not-used-for-identity",
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

// TestPostAgentTurnHook_DispatchesToHandleStopHook pins the one event this
// adapter installs: it fires HandleStopHook with an empty text and a false
// waitingCue, since Vibe's post_agent_turn payload carries no message
// content at all (live-fired during this issue's audit).
func TestPostAgentTurnHook_DispatchesToHandleStopHook(t *testing.T) {
	root := vibeSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-turn")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(tp, hookEventPostAgentTurn))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook called %d times, want 1", len(stops))
	}
	got := stops[0]
	if got.sessionID != "sess-turn" {
		t.Errorf("sessionID = %q, want %q", got.sessionID, "sess-turn")
	}
	if got.transcriptPath != tp {
		t.Errorf("transcriptPath = %q, want %q", got.transcriptPath, tp)
	}
	if got.lastAssistantText != "" {
		t.Errorf("lastAssistantText = %q, want empty — Vibe's post_agent_turn carries no message text", got.lastAssistantText)
	}
	if got.waitingCue {
		t.Errorf("waitingCue = true, want false — nothing in the payload can assert one")
	}
}

// TestHookReceiver_PermissionGateContract runs the shared #797 contract once
// per permission this receiver must honour, derived from the receiver's own
// declaration (issue #1488).
func TestHookReceiver_PermissionGateContract(t *testing.T) {
	contracttesting.AssertHookReceiverPermissionGated(t, contracttesting.HookReceiverGate{
		Build: func(t *testing.T, gate *contracttesting.ConsentGate) contracttesting.GatedHookReceiver {
			root := vibeSessionRoot(t)
			body := contractPayload(writeSessionTranscript(t, root, "sess-gate"), hookEventPostAgentTurn)
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
	vibeSessionRoot(t)
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
	vibeSessionRoot(t)
	h, _ := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHookReceiver_UnknownSessionIsQuiet is a LOCK: a transcript_path this
// adapter does not own (wrong filename, not messages.jsonl) is dropped
// rather than guessed at.
func TestHookReceiver_UnknownSessionIsQuiet(t *testing.T) {
	root := vibeSessionRoot(t)
	dir := filepath.Join(root, "sess-other")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(other, hookEventPostAgentTurn))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times for a path this adapter does not own, want 0", n)
	}
}

// TestHookReceiver_DeniedTranscriptsConsentDropsQuietly pins that a
// transcripts-denied session must not dispatch even though the hooks
// consent is granted, and must not log an error while doing it (the
// receiver's own gate answers quietly; only the shared backstop logs).
func TestHookReceiver_DeniedTranscriptsConsentDropsQuietly(t *testing.T) {
	root := vibeSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-denied")
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, contractPayload(tp, hookEventPostAgentTurn))

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
