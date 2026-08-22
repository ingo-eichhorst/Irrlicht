package opencode

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

type promptCall struct {
	sessionID, transcriptPath, hookName string
}

type mockTarget struct {
	mu        sync.Mutex
	stopCalls []stopCall
	prompts   []promptCall
	releases  []string
	// lastPath is the most recent transcript path handed to ANY method that
	// takes one — which is what the #1389 obligation reads. Kept as one field
	// rather than reconstructed from the three slices so a future fourth
	// dispatch cannot be forgotten here while the contract keeps passing.
	lastPath string
}

func (m *mockTarget) HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls = append(m.stopCalls, stopCall{sessionID, transcriptPath, lastAssistantText, waitingCue})
	m.lastPath = transcriptPath
}

func (m *mockTarget) HandlePermissionPromptHook(sessionID, transcriptPath, hookName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, promptCall{sessionID, transcriptPath, hookName})
	m.lastPath = transcriptPath
}

func (m *mockTarget) ReleasePermissionPromptHold(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases = append(m.releases, sessionID)
}

func (m *mockTarget) totalCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.stopCalls) + len(m.prompts) + len(m.releases)
}

func (m *mockTarget) stops() []stopCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]stopCall(nil), m.stopCalls...)
}

func (m *mockTarget) promptCalls() []promptCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]promptCall(nil), m.prompts...)
}

func (m *mockTarget) releaseCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.releases...)
}

func (m *mockTarget) observedPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPath
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

// contractSessionID is the opencode session id every contract wiring in this
// package posts. It carries the "ses" prefix opencode's own SessionID schema
// enforces, because the receiver drops anything else.
const contractSessionID = "ses_19af5168cffem3E4MnRCcfxkNX"

// opencodeStoreRoot relocates $HOME and clears $XDG_CONFIG_HOME, creates the
// directory the OpenCode database would live in, and returns it.
//
// Every test in this package must isolate opencode's config dir to a
// t.TempDir(), never the real ~/.config/opencode: this package's installer
// WRITES a file. Clearing XDG_CONFIG_HOME is not ceremony — opencodeConfigDir
// reads it, so a value leaking in from the developer's own environment would
// point the installer at their real opencode installation.
//
// The directory it RETURNS is the store root rather than the config dir,
// because that is what the PathDaemonDerived route plants its decoys in and
// what StorePath() resolves inside.
func opencodeStoreRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configHomeEnvVar, "")
	root := filepath.Dir(filepath.Join(home, dbRelPath))
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	return root
}

// writeContractTranscript is the shape the receiving-side contracts want: a
// file inside dir that stands in for "the thing a caller might name".
//
// For this adapter it is a DECOY and nothing more. opencode's Source is an
// agent.ProcessOwnedStore — there is no file per session, so no contract here
// resolves a session id from a path, and this file exists only so the shared
// wirings that ask for one get a well-formed path to hand over. Which
// obligations that weakens is stated where each one is wired.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "decoy.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	return path
}

// contractPayload renders the body irrlicht's own opencode plugin writes for
// one event name — hook_event_name plus session_id, nothing else, matching
// plugin.js's own JSON.stringify exactly.
func contractPayload(event string) string {
	body, err := json.Marshal(openCodeHookPayload{
		HookEventName: event,
		SessionID:     contractSessionID,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// foreignPathPayload renders the body a HOSTILE caller would write: the real
// payload plus a transcript_path key this adapter's own payload type does not
// have. It is what AssertHookPathConfined's PathDaemonDerived route posts to
// prove nothing the caller names reaches the dispatch.
//
// Hand-built rather than marshalled from a struct precisely because there is no
// struct with that field — inventing one for the test would be inventing the
// very surface the route asserts does not exist.
func foreignPathPayload(path string) string {
	encoded, err := json.Marshal(path)
	if err != nil {
		panic(err)
	}
	return `{"hook_event_name":"` + HookEventSessionIdle +
		`","session_id":"` + contractSessionID +
		`","transcript_path":` + string(encoded) + `}`
}

// post drives the handler with a raw JSON body and returns the response.
func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, HookEndpointPath, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// newReceiver builds a fully-granted receiver plus the target it dispatches
// into.
func newReceiver(t *testing.T) (http.Handler, *mockTarget) {
	t.Helper()
	target := &mockTarget{}
	gate := keyedGate{PermissionKeyHooks: true, PermissionKeyDatabase: true}
	return NewHookHandler(target, gate, mockLogger{}), target
}

// --- behaviour ---

// TestPermissionAskedHook_AssertsTheBlockedOnUserSignal pins the event that is
// this adapter's whole reason for having a hook channel: opencode holds a
// pending permission request in an in-memory Map and persists only APPROVED
// patterns, so `waiting` is unreachable from the store the watcher reads.
func TestPermissionAskedHook_AssertsTheBlockedOnUserSignal(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(HookEventPermissionAsked))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	prompts := target.promptCalls()
	if len(prompts) != 1 {
		t.Fatalf("HandlePermissionPromptHook called %d times, want 1", len(prompts))
	}
	if prompts[0].sessionID != contractSessionID {
		t.Errorf("sessionID = %q, want %q", prompts[0].sessionID, contractSessionID)
	}
	if want := transcriptPathFor(contractSessionID); prompts[0].transcriptPath != want {
		t.Errorf("transcriptPath = %q, want the daemon's own composed path %q", prompts[0].transcriptPath, want)
	}
	if prompts[0].hookName != HookEventPermissionAsked {
		t.Errorf("hookName = %q, want %q", prompts[0].hookName, HookEventPermissionAsked)
	}
	if n := len(target.stops()); n != 0 {
		t.Errorf("HandleStopHook called %d times for permission.asked, want 0", n)
	}
}

// TestPermissionRepliedHook_ReleasesTheHold pins the release half. It is
// load-bearing because opencode publishes permission.replied for a REJECTION
// too — which is what keeps this adapter out of copilot's position, where a
// denial emits nothing and the hold would sit until the 12-hour ceiling.
func TestPermissionRepliedHook_ReleasesTheHold(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(HookEventPermissionReplied))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := target.releaseCalls(); len(got) != 1 || got[0] != contractSessionID {
		t.Fatalf("ReleasePermissionPromptHold calls = %v, want exactly [%q]", got, contractSessionID)
	}
	if n := len(target.stops()); n != 0 {
		t.Errorf("HandleStopHook called %d times for permission.replied, want 0 — a reply is not a turn end", n)
	}
}

// TestSessionIdleHook_ReleasesAndReportsTurnEnd pins BOTH dispatches
// session.idle makes, and that the release comes with it.
//
// The release is not redundant with permission.replied. A run that is killed or
// cancelled with a request still pending emits no reply at all, and
// session.idle is the only event that still arrives — the same gap gemini-cli's
// AfterAgent handler closes by calling ReleasePermissionPromptHold
// unconditionally.
func TestSessionIdleHook_ReleasesAndReportsTurnEnd(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(HookEventSessionIdle))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := target.releaseCalls(); len(got) != 1 || got[0] != contractSessionID {
		t.Errorf("ReleasePermissionPromptHold calls = %v, want exactly [%q] — a run torn down with a request pending emits no reply, so this is the only release it gets", got, contractSessionID)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook called %d times, want 1", len(stops))
	}
	if stops[0].sessionID != contractSessionID {
		t.Errorf("sessionID = %q, want %q", stops[0].sessionID, contractSessionID)
	}
	if want := transcriptPathFor(contractSessionID); stops[0].transcriptPath != want {
		t.Errorf("transcriptPath = %q, want the daemon's own composed path %q", stops[0].transcriptPath, want)
	}
	if stops[0].lastAssistantText != "" {
		t.Errorf("lastAssistantText = %q, want empty — opencode's session.idle carries no message text", stops[0].lastAssistantText)
	}
	if stops[0].waitingCue {
		t.Error("waitingCue = true, want false — nothing in the payload can assert one")
	}
}

// TestHookReceiver_DispatchedPathMatchesTheWatchers is the property that makes
// a hook-delivered observation land on the SAME session the store watcher is
// tracking: both key on transcriptPathFor, so a session cannot end up as two.
//
// It is a LOCK on a shared derivation rather than a new claim, which is why it
// asserts against a watcher built through the production constructor rather
// than against a second copy of the format string.
func TestHookReceiver_DispatchedPathMatchesTheWatchers(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	post(t, h, contractPayload(HookEventSessionIdle))

	want := New(0).transcriptPathFor(contractSessionID)
	if got := target.observedPath(); got != want {
		t.Errorf("receiver dispatched %q but the watcher emits %q for the same session — two spellings of one session are two sessions", got, want)
	}
}

// TestHookReceiver_ForeignSessionIDIsQuiet is a LOCK: an id that is not one
// opencode could have issued is dropped rather than turned into a phantom
// session. The endpoint is local and unauthenticated, so "our own plugin wrote
// it" buys nothing.
func TestHookReceiver_ForeignSessionIDIsQuiet(t *testing.T) {
	opencodeStoreRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, `{"hook_event_name":"`+HookEventSessionIdle+`","session_id":"not-a-session"}`)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times for an id this adapter could not have issued, want 0", n)
	}
}

// TestHookReceiver_PermissionGateContract runs the shared #797 contract once
// per permission this receiver must honour, derived from the receiver's own
// declaration (issue #1488).
func TestHookReceiver_PermissionGateContract(t *testing.T) {
	contracttesting.AssertHookReceiverPermissionGated(t, contracttesting.HookReceiverGate{
		Build: func(t *testing.T, gate *contracttesting.ConsentGate) contracttesting.GatedHookReceiver {
			opencodeStoreRoot(t)
			body := contractPayload(HookEventSessionIdle)
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
		PermissionKeyHooks, PermissionKeyDatabase)
}

// TestHookReceiver_NonPostRejected is a LOCK on the shared receiver shape.
func TestHookReceiver_NonPostRejected(t *testing.T) {
	opencodeStoreRoot(t)
	h, _ := newReceiver(t)
	req := httptest.NewRequest(http.MethodGet, HookEndpointPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

// TestHookReceiver_MalformedJSONIs400 is a LOCK: a body that will not decode is
// the one case that answers non-2xx.
func TestHookReceiver_MalformedJSONIs400(t *testing.T) {
	opencodeStoreRoot(t)
	h, _ := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHookReceiver_DeniedDatabaseConsentDropsQuietly pins that a
// database-denied session must not dispatch even though the hooks consent is
// granted, and must not log an error while doing it (the receiver's own gate
// answers quietly; only the shared backstop logs).
//
// Since #1488's chokepoint the SILENCE is the surviving discriminator —
// deleting the gate below leaves every dispatch-shaped assertion green, because
// DecodeSealed re-checks the whole declared set and drops the payload itself.
// What it does NOT reproduce is the quiet: reaching the backstop logs an error,
// and an ordinary denied session must not collect one per event.
func TestHookReceiver_DeniedDatabaseConsentDropsQuietly(t *testing.T) {
	opencodeStoreRoot(t)
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, contractPayload(HookEventSessionIdle))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while database consent was denied, want 0", n)
	}
	if len(log.Errors()) != 0 {
		t.Errorf("a database-denied hook logged %d error(s): %v", len(log.Errors()), log.Errors())
	}
}
