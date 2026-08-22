package pi

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

// piSessionRoot relocates $HOME and clears BOTH pi overrides, then returns
// the declared transcript root inside the temp home. Every test in this
// package must isolate pi's agent dir to a t.TempDir(), never the real
// ~/.pi/agent — this package's installer WRITES a file.
//
// Clearing PI_CODING_AGENT_DIR is not ceremony: since #1721 sessionsDir()
// reads it, so a leaked value from the developer's own environment would
// silently point the confiner at a tree the test never wrote into.
func piSessionRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentDirEnvVar, "")
	t.Setenv(sessionDirEnvVar, "")
	root := filepath.Join(home, defaultRootDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create sessions dir: %v", err)
	}
	return root
}

// writeSessionTranscript lays down <root>/<cwd-dir>/<id>.jsonl and returns
// its path — the shape sessionIDFromTranscriptPath expects (the filename
// stem is the id; the per-cwd directory pi nests transcripts in is
// irrelevant to identity but is reproduced so the fixture matches what pi
// actually writes).
func writeSessionTranscript(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "--tmp-project--")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, id+transcriptExt)
	if err := os.WriteFile(path, []byte(`{"type":"message"}`+"\n"), 0o600); err != nil {
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

// contractPayload renders the body irrlicht's own pi extension writes for
// one event name — transcript_path plus hook_event_name, nothing else,
// matching extension.js's own JSON.stringify exactly.
func contractPayload(transcriptPath, event string) string {
	body, err := json.Marshal(piHookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  event,
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

// TestAgentSettledHook_DispatchesToHandleStopHook pins the one event this
// adapter installs: it fires HandleStopHook with an empty text and a false
// waitingCue, since pi's agent_settled event carries no message content and
// extension.js deliberately does not go looking for one (hooks.go's header
// on why the payload-free floor is the trade being made).
func TestAgentSettledHook_DispatchesToHandleStopHook(t *testing.T) {
	root := piSessionRoot(t)
	tp := writeSessionTranscript(t, root, "2026-08-22T03-42-52-286Z_01a02790")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(tp, HookEventAgentSettled))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook called %d times, want 1", len(stops))
	}
	got := stops[0]
	if got.sessionID != "2026-08-22T03-42-52-286Z_01a02790" {
		t.Errorf("sessionID = %q, want the transcript filename stem", got.sessionID)
	}
	if got.transcriptPath != tp {
		t.Errorf("transcriptPath = %q, want %q", got.transcriptPath, tp)
	}
	if got.lastAssistantText != "" {
		t.Errorf("lastAssistantText = %q, want empty — pi's agent_settled carries no message text", got.lastAssistantText)
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
			root := piSessionRoot(t)
			body := contractPayload(writeSessionTranscript(t, root, "sess-gate"), HookEventAgentSettled)
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
	piSessionRoot(t)
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
	piSessionRoot(t)
	h, _ := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHookReceiver_UnknownSessionIsQuiet is a LOCK: a transcript_path inside
// the adapter's own root whose filename does not end in .jsonl is dropped
// rather than guessed at.
func TestHookReceiver_UnknownSessionIsQuiet(t *testing.T) {
	root := piSessionRoot(t)
	dir := filepath.Join(root, "--tmp-project--")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(other, HookEventAgentSettled))

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
// Since #1488's chokepoint the SILENCE is the surviving discriminator —
// deleting the gate below leaves dispatch-shaped assertions green.
func TestHookReceiver_DeniedTranscriptsConsentDropsQuietly(t *testing.T) {
	root := piSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-denied")
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, contractPayload(tp, HookEventAgentSettled))

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
