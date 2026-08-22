package antigravity

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

func (m *mockTarget) observedPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stopCalls) == 0 {
		return ""
	}
	return m.stopCalls[len(m.stopCalls)-1].transcriptPath
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

// contractConversationID is the conversation id every contract wiring in this
// package posts. It is the real id from the captured Stop payload
// (testdata/hooks/stop-payload.json), so the fixtures and the capture cannot
// key different sessions.
const contractConversationID = "2bb3613b-f2a2-4573-9282-e389ac224bda"

// antigravityBrainRoot relocates $HOME and returns the CLI brain store inside
// the temp home.
//
// Every test in this package must isolate BOTH the brain stores and the
// customization root to a t.TempDir(), never the real ~/.gemini — this
// package's installer WRITES a file the user's own /hooks command also writes.
// There is no env override to clear, and that is a measured fact rather than
// an omission: JETSKI_APP_DATA_DIR does not relocate the customization root
// (see hookinstaller.go), and nothing in this package calls os.Getenv, so
// $HOME is the whole of the isolation. TestNoEnvOverrideEscapesTheTempHome
// asserts it instead of leaving it to inspection.
func antigravityBrainRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, cliBrainDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create brain dir: %v", err)
	}
	return root
}

// writeConversation lays down the full transcript layout for one conversation
// and returns the transcript path the daemon watches.
func writeConversation(t *testing.T, root, conversationID string) string {
	t.Helper()
	dir := filepath.Join(root, conversationID, systemGeneratedDirName, logsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir conversation dir: %v", err)
	}
	path := filepath.Join(dir, transcriptFilename)
	if err := os.WriteFile(path, []byte(`{"source":"MODEL","type":"PLANNER_RESPONSE"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// writeContractTranscript is the shape the receiving-side contracts want.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	return writeConversation(t, dir, contractConversationID)
}

// contractPayload renders the body the beacon forwards for one event name.
//
// hook_event_name is irrlicht's OWN field — antigravity never sends it, and an
// absent value means the sole installed event (see eventOf). The contract
// wirings need to be able to name an event explicitly, which is what keeps
// #1364's unrecognized-event path reachable, so it is rendered here.
func contractPayload(_ string, event string) string {
	body, err := json.Marshal(antigravityHookPayload{
		HookEventName:  event,
		ConversationID: contractConversationID,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// foreignPathPayload splices a caller-supplied transcriptPath into an
// otherwise well-formed body — the key antigravity itself sends and this
// receiver deliberately does not decode. It is what
// AssertHookPathConfined's PathDaemonDerived route posts to prove nothing the
// caller names can move the dispatched string.
func foreignPathPayload(path string) string {
	encoded, err := json.Marshal(path)
	if err != nil {
		panic(err)
	}
	return `{"hook_event_name":"` + HookEventStop +
		`","conversationId":"` + contractConversationID +
		`","transcriptPath":` + string(encoded) + `}`
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
	gate := keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}
	return NewHookHandler(target, gate, mockLogger{}), target
}

// capturedPayload reads one of the two payloads antigravity actually delivered
// during #1723's probes. They are real captures committed verbatim, not
// hand-written fixtures, which is what makes the tests below evidence about
// antigravity rather than about this package's own idea of antigravity.
func capturedPayload(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hooks", name))
	if err != nil {
		t.Fatalf("read captured payload %s: %v", name, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Fatalf("captured payload %s is empty — a fixture that cannot be read must fail, not pass quietly", name)
	}
	return string(data)
}

// --- behaviour ---

// TestStopHook_DispatchesTheRealCapture drives the receiver with the exact
// bytes antigravity wrote to the probe's stdin on the run where Stop was
// measured firing (agy 1.1.18).
//
// Note what the capture does NOT contain: a hook_event_name. Antigravity
// identifies the event by which config key the handler was registered under,
// so this is also the test that the absent-name default is what real traffic
// takes.
func TestStopHook_DispatchesTheRealCapture(t *testing.T) {
	root := antigravityBrainRoot(t)
	want := writeConversation(t, root, contractConversationID)
	h, target := newReceiver(t)

	rec := post(t, h, capturedPayload(t, "stop-payload.json"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook called %d times, want 1", len(stops))
	}
	got := stops[0]
	if got.sessionID != contractConversationID {
		t.Errorf("sessionID = %q, want the payload's conversationId %q", got.sessionID, contractConversationID)
	}
	if got.transcriptPath != want {
		t.Errorf("transcriptPath = %q, want the daemon's own composed path %q", got.transcriptPath, want)
	}
	if got.lastAssistantText != "" {
		t.Errorf("lastAssistantText = %q, want empty — antigravity's Stop carries no message text", got.lastAssistantText)
	}
	if got.waitingCue {
		t.Errorf("waitingCue = true, want false — nothing in the payload can assert one")
	}
}

// TestStopHook_IgnoresTheTranscriptPathTheCaptureCarries is the defect test
// for the trap this adapter's payload sets: the real capture DOES name a
// transcript, and it names transcript_full.jsonl — the sibling
// sessionIDFromPath deliberately refuses, so that a conversation mints one
// session rather than two.
//
// A receiver that forwarded the payload's own path would key the session on a
// file the fswatcher never tails. This asserts the dispatched path is the
// filtered transcript.jsonl AND that it is not the string the capture carries,
// because "it happens to be right" and "the caller's value never travels" are
// different claims and only the second survives a hostile body.
func TestStopHook_IgnoresTheTranscriptPathTheCaptureCarries(t *testing.T) {
	root := antigravityBrainRoot(t)
	writeConversation(t, root, contractConversationID)
	h, target := newReceiver(t)

	body := capturedPayload(t, "stop-payload.json")
	var captured struct {
		TranscriptPath string `json:"transcriptPath"`
	}
	if err := json.Unmarshal([]byte(body), &captured); err != nil {
		t.Fatalf("the committed capture no longer decodes: %v", err)
	}
	if captured.TranscriptPath == "" {
		t.Fatal("the committed capture carries no transcriptPath — this test would be vacuous; " +
			"the capture has been replaced with something that is not what antigravity sent")
	}
	if !strings.HasSuffix(captured.TranscriptPath, "transcript_full.jsonl") {
		t.Fatalf("the committed capture names %q, not the transcript_full.jsonl this test exists for",
			captured.TranscriptPath)
	}

	post(t, h, body)

	got := target.observedPath()
	if got == captured.TranscriptPath {
		t.Fatalf("dispatched the caller's own transcriptPath %q", got)
	}
	if filepath.Base(got) != transcriptFilename {
		t.Errorf("dispatched %q, want a path ending in %q", got, transcriptFilename)
	}
	if id := sessionIDFromPath(got); id != contractConversationID {
		t.Errorf("sessionIDFromPath(dispatched) = %q, want %q — the hook's key and the "+
			"fswatcher's key have drifted into two sessions", id, contractConversationID)
	}
}

// TestStopHook_DispatchesForAnyTerminationReason is a LOCK, and it passes by
// construction: there is no branch on terminationReason to break.
//
// It exists because the shipped doc's value set for that field is WRONG —
// hooks.md §5 lists model_stop / max_steps_exceeded / error, and the only
// value ever observed is NO_TOOL_CALL, uppercase and absent from the list. A
// future edit that "improved" this receiver by matching on the documented set
// would fail closed on the one value real traffic carries, and this is what
// would report it.
func TestStopHook_DispatchesForAnyTerminationReason(t *testing.T) {
	for _, reason := range []string{
		"",                    // absent from the body entirely
		"NO_TOOL_CALL",        // the only value ever observed
		"model_stop",          // what the doc claims
		"max_steps_exceeded",  // what the doc claims
		"error",               // what the doc claims
		"SOMETHING_NEW_IN_2Y", // an upstream addition nobody has seen
	} {
		t.Run("reason="+reason, func(t *testing.T) {
			root := antigravityBrainRoot(t)
			want := writeConversation(t, root, contractConversationID)
			h, target := newReceiver(t)

			body, err := json.Marshal(antigravityHookPayload{
				ConversationID:    contractConversationID,
				TerminationReason: reason,
			})
			if err != nil {
				t.Fatal(err)
			}
			post(t, h, string(body))

			stops := target.stops()
			if len(stops) != 1 {
				t.Fatalf("HandleStopHook called %d times for terminationReason=%q, want 1", len(stops), reason)
			}
			if stops[0].transcriptPath != want {
				t.Errorf("transcriptPath = %q, want %q", stops[0].transcriptPath, want)
			}
		})
	}
}

// TestEventOf_DefaultIsSoundOnlyWhileOneEventIsInstalled is a LOCK on the one
// assumption eventOf rests on.
//
// Antigravity's payload names no event, so an absent hook_event_name is read
// as "the event this adapter installs". That is sound for exactly as long as
// there is one of them. The moment a second event is added, every unnamed body
// would be reported as whichever one sorts first — silently, with no error
// anywhere. This fails instead.
func TestEventOf_DefaultIsSoundOnlyWhileOneEventIsInstalled(t *testing.T) {
	if len(installedHookEvents) != 1 {
		t.Fatalf("installedHookEvents = %v (%d events). eventOf reads an absent hook_event_name as "+
			"installedHookEvents[0], which is only sound while there is exactly one. Adding a second "+
			"event means antigravity's payload can no longer be attributed by absence — either the "+
			"beacon command must carry the event name, or each event needs its own route.",
			installedHookEvents, len(installedHookEvents))
	}
	if got := eventOf(antigravityHookPayload{}); got != HookEventStop {
		t.Errorf("eventOf(empty) = %q, want %q", got, HookEventStop)
	}
	if got := eventOf(antigravityHookPayload{HookEventName: "PostToolUse"}); got != "PostToolUse" {
		t.Errorf("eventOf named = %q, want the name the body carries", got)
	}
}

// TestTranscriptPathForRoundTripsThroughSessionIDFromPath pins the composer
// against the parser.
//
// transcriptPathFor composes DOWN the brain layout from a conversation id;
// sessionIDFromPath walks UP it to derive a session id. If the two ever
// disagreed — a renamed directory, a different transcript filename — the hook
// would mint a second session per conversation, which is the exact failure
// sessionIDFromPath's transcript_full.jsonl exclusion exists to prevent, and
// nothing else in the tree would report it.
func TestTranscriptPathForRoundTripsThroughSessionIDFromPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		makeItLive func(t *testing.T, home string) // create the conversation dir, or not
	}{
		{"conversation directory absent (the fallback path)", func(*testing.T, string) {}},
		{"conversation in the CLI brain store", func(t *testing.T, home string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(home, cliBrainDir, contractConversationID), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"conversation in the IDE brain store", func(t *testing.T, home string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(home, ideBrainDir, contractConversationID), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			tc.makeItLive(t, home)

			path := transcriptPathFor(contractConversationID)
			if path == "" {
				t.Fatal("transcriptPathFor returned empty")
			}
			if !strings.HasPrefix(path, home) {
				t.Fatalf("composed %q, which is outside the temp home %q", path, home)
			}
			if got := sessionIDFromPath(path); got != contractConversationID {
				t.Errorf("sessionIDFromPath(%q) = %q, want %q", path, got, contractConversationID)
			}
		})
	}
}

// TestTranscriptPathFor_PrefersTheStoreThatHoldsTheConversation pins the
// disambiguation between the two surfaces one adapter covers.
func TestTranscriptPathFor_PrefersTheStoreThatHoldsTheConversation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ideBrainDir, contractConversationID), 0o700); err != nil {
		t.Fatal(err)
	}

	got := transcriptPathFor(contractConversationID)
	if want := filepath.Join(home, ideBrainDir, contractConversationID,
		systemGeneratedDirName, logsDirName, transcriptFilename); got != want {
		t.Errorf("transcriptPathFor = %q, want the IDE store's %q", got, want)
	}
}

// TestHookReceiver_RejectsAnUnsafeConversationID is the defect test for the
// one place a caller-supplied string reaches a filesystem path on this
// receiver.
//
// The payload names no path this receiver reads, so the traversal class moves
// to the id — which transcriptPathFor joins into the brain store. Each of
// these must be dropped BEFORE any path is composed.
func TestHookReceiver_RejectsAnUnsafeConversationID(t *testing.T) {
	for _, id := range []string{
		"",
		".",
		"..",
		"../../etc",
		"..",
		"a/b",
		`a\b`,
		"/absolute",
		".hidden",
		"has space",
		"has\x00nul",
		strings.Repeat("x", 200),
	} {
		t.Run("id="+strings.ReplaceAll(id, "\x00", "<nul>"), func(t *testing.T) {
			antigravityBrainRoot(t)
			h, target := newReceiver(t)

			encoded, err := json.Marshal(id)
			if err != nil {
				t.Fatal(err)
			}
			rec := post(t, h, `{"hook_event_name":"`+HookEventStop+`","conversationId":`+string(encoded)+`}`)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 — a refusal is reported by the drop, not by a status code", rec.Code)
			}
			if n := target.totalCalls(); n != 0 {
				t.Errorf("dispatched %d times for conversationId %q, want 0", n, id)
			}
		})
	}
}

// TestHookReceiver_AcceptsAConversationIDThatIsNotAUUID is the vacuity guard
// for the test above: the id check must reject a path segment, not enforce a
// format antigravity is free to change.
func TestHookReceiver_AcceptsAConversationIDThatIsNotAUUID(t *testing.T) {
	root := antigravityBrainRoot(t)
	writeConversation(t, root, "conv_2026-08-22_not-a-uuid.v2")
	h, target := newReceiver(t)

	post(t, h, `{"hook_event_name":"`+HookEventStop+`","conversationId":"conv_2026-08-22_not-a-uuid.v2"}`)

	if n := target.totalCalls(); n != 1 {
		t.Errorf("dispatched %d times for a non-UUID id, want 1 — the id check has become a format gate, "+
			"and an upstream id-format change would silently stop reporting turn ends", n)
	}
}

// TestNoEnvOverrideEscapesTheTempHome asserts what antigravityBrainRoot's doc
// claims: $HOME is the whole of this adapter's isolation, because the package
// reads no environment variable of its own.
//
// A grep that matched nothing would be indistinguishable from a grep that
// could not run, so the walk asserts it actually read the package's sources.
func TestNoEnvOverrideEscapesTheTempHome(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		if strings.Contains(string(src), "os.Getenv") {
			t.Errorf("%s calls os.Getenv. This package's path resolution is $HOME-relative with no "+
				"override (JETSKI_APP_DATA_DIR was measured not to relocate the customization root), "+
				"and righome.Unisolatable states that as the reason antigravity has no rig row. A new "+
				"override means both that entry and every test helper's isolation are now wrong.", name)
		}
	}
	if scanned < 5 {
		t.Fatalf("scanned only %d non-test .go files in this package — the walk found nothing to read, "+
			"which is not the same as finding nothing wrong", scanned)
	}
}

// TestHookReceiver_PermissionGateContract runs the shared #797 contract once
// per permission this receiver must honour, derived from the receiver's own
// declaration (issue #1488).
func TestHookReceiver_PermissionGateContract(t *testing.T) {
	contracttesting.AssertHookReceiverPermissionGated(t, contracttesting.HookReceiverGate{
		Build: func(t *testing.T, gate *contracttesting.ConsentGate) contracttesting.GatedHookReceiver {
			root := antigravityBrainRoot(t)
			writeConversation(t, root, contractConversationID)
			body := contractPayload("", HookEventStop)
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
	antigravityBrainRoot(t)
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
	antigravityBrainRoot(t)
	h, _ := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHookReceiver_DeniedTranscriptsConsentDropsQuietly pins that a
// transcripts-denied session must not dispatch even though the hooks consent
// is granted, and must not log an error while doing it (the receiver's own
// gate answers quietly; only the shared #1488 backstop logs). Since that
// chokepoint the SILENCE is the surviving discriminator — deleting the gate
// below leaves every dispatch-shaped assertion green.
func TestHookReceiver_DeniedTranscriptsConsentDropsQuietly(t *testing.T) {
	root := antigravityBrainRoot(t)
	writeConversation(t, root, contractConversationID)
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, contractPayload("", HookEventStop))

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

// TestPostToolUseCaptureIsNotDispatchedAsATurnEnd guards the OTHER committed
// capture, and it guards a real hazard rather than a hypothetical one.
//
// #1723's first probe registered PostToolUse, so a payload of exactly this
// shape is what a user would produce by pointing a PostToolUse handler at
// irrlicht's beacon by hand. It carries no hook_event_name either, so the
// absent-name default would read it as a turn end. That is accepted and
// documented — the default is what makes real Stop traffic attributable at all
// — but it must stay a DELIBERATE property: this test pins that the capture
// reaches HandleStopHook and NOT some silently different path, so anyone
// changing eventOf sees this row rather than discovering it in production.
func TestPostToolUseCaptureIsNotDispatchedAsATurnEnd(t *testing.T) {
	root := antigravityBrainRoot(t)
	// The PostToolUse capture is a DIFFERENT conversation, which is itself the
	// point: nothing but conversationId decides attribution.
	const postToolUseConversation = "81b73d0d-4732-4648-be5f-458c4dc05d5e"
	want := writeConversation(t, root, postToolUseConversation)
	h, target := newReceiver(t)

	post(t, h, capturedPayload(t, "posttooluse-payload.json"))

	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook called %d times, want 1 — an unnamed body is read as the sole "+
			"installed event, which is the documented behaviour of eventOf", len(stops))
	}
	if stops[0].sessionID != postToolUseConversation {
		t.Errorf("sessionID = %q, want %q", stops[0].sessionID, postToolUseConversation)
	}
	if stops[0].transcriptPath != want {
		t.Errorf("transcriptPath = %q, want %q", stops[0].transcriptPath, want)
	}
}
