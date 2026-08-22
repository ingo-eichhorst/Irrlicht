package hermes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

// contractSessionID is the hermes session id every contract wiring in this
// package posts. Its shape is hermes' own —
// `<YYYYMMDD>_<HHMMSS>_<6 lowercase hex>` — and it is copied from a real
// recorded fixture (replaydata/agents/hermes), because the receiver drops
// anything that does not match.
const contractSessionID = "20260802_233614_c97a8d"

// hermesHome relocates $HERMES_HOME to a scratch directory and returns it.
//
// Every test in this package must isolate it: this package's installer WRITES
// two files under that directory, and the user's real ~/.hermes holds their
// live session store and their config. Setting HERMES_HOME rather than HOME is
// what homeDir() reads first, so it wins over anything leaking in from the
// developer's environment.
func hermesHome(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".hermes")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create hermes home: %v", err)
	}
	t.Setenv(homeEnvVar, root)
	return root
}

// writeContractTranscript is the shape the receiving-side contracts want: a
// file inside dir that stands in for "the thing a caller might name".
//
// For this adapter it is a DECOY and nothing more. hermes writes no transcript
// files at all — the "transcript" IS the shared store — so no contract here
// resolves a session id from a path, and this file exists only so the shared
// wirings that ask for one get a well-formed path to hand over. Which
// obligations that weakens is stated where each one is wired.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "decoy.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"assistant"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	return path
}

// contractPayload renders the body hermes pipes to a shell hook for one event,
// in the shape agent/shell_hooks._serialize_payload emits — captured live from
// `hermes hooks test` during this issue's audit.
//
// The session id goes in the field hermes ACTUALLY fills for that event: the
// top-level `session_id` for on_session_end, and `extra.session_key` for the
// two approval events, which pass a `session_key` kwarg and no `session_id` at
// all. A single-field payload builder would test a wire shape hermes never
// sends, and would leave the approval path — this adapter's whole reason for a
// hook channel — graded by nothing.
func contractPayload(event string) string {
	var p hermesHookPayload
	p.HookEventName = event
	if event == HookEventOnSessionEnd {
		p.SessionID = contractSessionID
	} else {
		p.Extra.SessionKey = contractSessionID
	}
	body, err := json.Marshal(p)
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
	return `{"hook_event_name":"` + HookEventOnSessionEnd +
		`","session_id":"` + contractSessionID +
		`","cwd":"/tmp","transcript_path":` + string(encoded) + `}`
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
	gate := keyedGate{PermissionKeyHooks: true, PermissionKeyStore: true}
	return NewHookHandler(target, gate, mockLogger{}), target
}

// --- the safety guard ---

// TestInstalledEventsAreObserverOnly is the mechanism behind hookinstaller.go's
// "we do not install a control-bearing event" claim, and it is the hermes
// spelling of #1719's opencode guard.
//
// hermes reads a shell hook's STDOUT as a decision for exactly three events
// (agent/shell_hooks._parse_response): pre_tool_call takes
// {"action":"block"} / {"decision":"block"} and vetoes the tool call,
// pre_verify takes {"action":"continue"} and keeps the turn running, and every
// other event falls through to a {"context": "..."} branch which pre_llm_call
// prepends to the user's message. A monitoring channel must not be able to do
// any of those, and "the author remembered" is not a mechanism.
//
// Both directions are asserted. The intersection must be empty — that is the
// rule — and the denylist must be non-empty and must name events hermes
// actually has, or an emptied list would satisfy the rule while checking
// nothing. The membership half reads VALID_HOOKS through the same list #1364's
// contract already exercises: these three names are hermes', spelled here
// once.
func TestInstalledEventsAreObserverOnly(t *testing.T) {
	if len(controlBearingHookEvents) == 0 {
		t.Fatal("controlBearingHookEvents is empty, so the intersection below is vacuously " +
			"empty and this guard grades nothing")
	}
	for _, e := range installedHookEvents {
		if slices.Contains(controlBearingHookEvents, e) {
			t.Errorf("installedHookEvents contains %q, whose stdout hermes reads as a decision "+
				"(agent/shell_hooks._parse_response). Installing it would let a monitoring hook "+
				"veto a tool call, keep a turn running, or inject text into the user's message.", e)
		}
	}
	// Vacuity guard on the denylist itself: a name hermes does not have would
	// make the rule above true for a reason unrelated to safety.
	for _, e := range controlBearingHookEvents {
		if !strings.HasPrefix(e, "pre_") {
			t.Errorf("controlBearingHookEvents names %q; every event hermes reads a decision "+
				"from is a pre_* event, so this is probably not one of them", e)
		}
	}
}

// TestInstalledCommandSuppressesStdout is the defence-in-depth half of the
// guard above, and it is not hypothetical: hookbeacon.LegacyGuardToken puts
// `--version` FIRST in the command line so an irrlichd predating #1417 exits 0
// instead of starting a daemon — and such a binary prints a version banner on
// STDOUT, which is the channel hermes parses.
func TestInstalledCommandSuppressesStdout(t *testing.T) {
	cmd := hookCommand("'/opt/irrlichd' --version hook-post hermes >/dev/null || true")
	if !strings.HasPrefix(cmd, shellInterpreter+" -c ") {
		t.Fatalf("command = %q, want it wrapped in %s -c so the redirect below is honoured "+
			"— hermes runs the command with shell=False, so a bare argv has no redirection at all",
			cmd, shellInterpreter)
	}
	if !strings.Contains(cmd, ">/dev/null") {
		t.Errorf("command = %q, want it to redirect stdout away from hermes' response parser", cmd)
	}
	if !strings.Contains(cmd, "|| true") {
		t.Errorf("command = %q, want a fail-open suffix so a deleted binary is not a per-turn "+
			"warning in the user's agent.log", cmd)
	}
}

// --- behaviour ---

// TestPreApprovalRequestHook_AssertsTheBlockedOnUserSignal pins the event that
// is this adapter's whole reason for having a hook channel: hermes holds a
// pending approval in process memory (a module-level queue plus a contextvar)
// and state.db has no approval table at all, so `waiting` is unreachable from
// the store the watcher reads at any polling interval.
func TestPreApprovalRequestHook_AssertsTheBlockedOnUserSignal(t *testing.T) {
	hermesHome(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(HookEventPreApprovalRequest))

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
	if prompts[0].hookName != HookEventPreApprovalRequest {
		t.Errorf("hookName = %q, want %q", prompts[0].hookName, HookEventPreApprovalRequest)
	}
	if n := len(target.stops()); n != 0 {
		t.Errorf("HandleStopHook called %d times for pre_approval_request, want 0", n)
	}
}

// TestPostApprovalResponseHook_ReleasesTheHold pins the release half. It is
// load-bearing because hermes fires it for EVERY outcome including `deny` and
// `timeout` — which is what keeps this adapter out of copilot's position, where
// a denial emits nothing and the hold would sit until the 12-hour ceiling.
func TestPostApprovalResponseHook_ReleasesTheHold(t *testing.T) {
	hermesHome(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(HookEventPostApprovalResponse))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := target.releaseCalls(); len(got) != 1 || got[0] != contractSessionID {
		t.Fatalf("ReleasePermissionPromptHold calls = %v, want exactly [%q]", got, contractSessionID)
	}
	if n := len(target.stops()); n != 0 {
		t.Errorf("HandleStopHook called %d times for post_approval_response, want 0 — an answer is not a turn end", n)
	}
}

// TestOnSessionEndHook_ReleasesAndReportsTurnEnd pins BOTH dispatches
// on_session_end makes, and that the release comes with it.
//
// The release is not redundant with post_approval_response. hermes' approval
// wait can be torn down without a response — an interrupted turn, a killed
// process — and on_session_end is the only event that still arrives, the same
// gap gemini-cli's AfterAgent and opencode's session.idle close.
func TestOnSessionEndHook_ReleasesAndReportsTurnEnd(t *testing.T) {
	hermesHome(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(HookEventOnSessionEnd))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := target.releaseCalls(); len(got) != 1 || got[0] != contractSessionID {
		t.Errorf("ReleasePermissionPromptHold calls = %v, want exactly [%q] — a turn torn down with an approval pending gets no response, so this is the only release it gets", got, contractSessionID)
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
		t.Errorf("lastAssistantText = %q, want empty — hermes' envelope carries no message text", stops[0].lastAssistantText)
	}
	if stops[0].waitingCue {
		t.Error("waitingCue = true, want false — nothing in the payload can assert one")
	}
}

// TestApprovalHooks_ReadTheSessionKeyFromExtra is the defect test for the one
// thing about this adapter's wire shape that is NOT the documented one.
//
// hermes' envelope has a top-level `session_id`, and its own docs describe it
// as the session identifier. The approval fire sites in tools/approval.py pass
// `session_key=` and no `session_id`, and _serialize_payload only fills the
// top-level field from a `session_id` kwarg — so both approval events arrive
// with `"session_id": ""` and the id inside `extra`. A receiver reading only
// the documented field would answer 200 to every approval and dispatch none of
// them, which is indistinguishable from an agent that never prompts.
func TestApprovalHooks_ReadTheSessionKeyFromExtra(t *testing.T) {
	hermesHome(t)
	for _, event := range []string{HookEventPreApprovalRequest, HookEventPostApprovalResponse} {
		t.Run(event, func(t *testing.T) {
			h, target := newReceiver(t)
			body := `{"hook_event_name":"` + event +
				`","session_id":"","cwd":"/tmp","extra":{"session_key":"` + contractSessionID +
				`","surface":"cli"}}`

			if rec := post(t, h, body); rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if n := target.totalCalls(); n != 1 {
				t.Fatalf("dispatched %d times, want 1 — the id is in extra.session_key for this event, not in session_id", n)
			}
		})
	}
}

// TestHookReceiver_DispatchedPathMatchesTheWatchers is the property that makes
// a hook-delivered observation land on the SAME session the store watcher is
// tracking: both key on transcriptPathIn, so a session cannot end up as two.
//
// It is a LOCK on a shared derivation rather than a new claim, which is why it
// asserts against a watcher built through the production constructor rather
// than against a second copy of the format string.
func TestHookReceiver_DispatchedPathMatchesTheWatchers(t *testing.T) {
	hermesHome(t)
	h, target := newReceiver(t)

	post(t, h, contractPayload(HookEventOnSessionEnd))

	want := transcriptPathIn(New(0).dbPath, contractSessionID)
	if got := target.observedPath(); got != want {
		t.Errorf("receiver dispatched %q but the watcher emits %q for the same session — two spellings of one session are two sessions", got, want)
	}
}

// TestHookReceiver_ForeignSessionIDIsQuiet is a LOCK: an id hermes could not
// have minted is dropped rather than turned into a phantom session. The
// endpoint is local and unauthenticated, so "our own entry wrote it" buys
// nothing.
//
// The three rows are not decoration. "default" is the literal hermes' own
// contextvar falls back to when a turn runs without a session
// (`set_current_session_key(self.session_id or "default")`), and a gateway
// routing key is what the same field holds for a WhatsApp or Slack
// conversation — a surface this adapter filters out of the store entirely, so
// admitting one through the hook path would surface a chat as a coding
// session.
func TestHookReceiver_ForeignSessionIDIsQuiet(t *testing.T) {
	for _, id := range []string{"not-a-session", "default", "whatsapp:4915112345678"} {
		t.Run(id, func(t *testing.T) {
			hermesHome(t)
			h, target := newReceiver(t)
			encoded, err := json.Marshal(id)
			if err != nil {
				t.Fatal(err)
			}
			rec := post(t, h, `{"hook_event_name":"`+HookEventOnSessionEnd+
				`","session_id":`+string(encoded)+`,"extra":{"session_key":`+string(encoded)+`}}`)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if n := target.totalCalls(); n != 0 {
				t.Errorf("dispatched %d times for an id hermes could not have minted, want 0", n)
			}
		})
	}
}

// TestSessionIDShape_RejectsGatewayMintedIDs pins the ACTUAL mechanism that
// keeps hermes' messaging-gateway sessions out of this adapter: sessionIDShape's
// hex length. hookinstaller.go used to say `source` filtering did this; the
// shell-hook envelope carries no `source` field at all (#1773).
//
// Upstream mint sites (all origin/main @ 261a4efb, v0.20.5 — unchanged since
// v0.19.0):
//   - gateway sessions: `uuid.uuid4().hex[:8]` — gateway/session.py:2831, :3354
//   - CLI/TUI sessions: `uuid.uuid4().hex[:6]` — agent/agent_init.py:1647,
//     cli.py:5437, tui_gateway/server.py:7359
//
// Table-driven so the failure names which rule rejects each row: the gateway
// mint is rejected specifically on hex length, while "default" (the
// contextvar fallback, `set_current_session_key(self.session_id or
// "default")`) and a platform routing key are rejected because they are not
// date/time-shaped at all.
//
// This is a LOCK — it pins behaviour that must not change, not a defect fix —
// so it has no red-before-green phase. hooks_mutations_test.go's mutation
// check is what earns it: widening the hex run to {6,8} flips this exact
// table's gateway-mint row.
func TestSessionIDShape_RejectsGatewayMintedIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"cli/tui 6-hex mint is accepted", "20260802_233614_c97a8d", true},
		{"gateway 8-hex mint is rejected on hex length", "20260802_233614_c97a8d12", false},
		{"contextvar default fallback is rejected (not date-shaped)", "default", false},
		{"gateway routing key is rejected (not date-shaped)", "whatsapp:4915112345678", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionIDShape.MatchString(tt.id); got != tt.want {
				t.Errorf("sessionIDShape.MatchString(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestHookReceiver_GatewaySurfaceApprovalIsQuiet is a LOCK on the same
// mechanism as the table above, exercised through the real receiver rather
// than the regex alone: a 0.20.5-shaped gateway approval — a WELL-FORMED
// 8-hex top-level session_id (tools/approval.py:126-133 now forwards one for
// gateway turns too, unlike v0.19.0's empty field) plus a routing key in
// extra.session_key — must dispatch nothing.
//
// This is the row that would have caught the drift: it fails the moment
// either the gateway mint shape or the receiver's first-field preference
// (payload.sessionID(), which reads top-level session_id before
// extra.session_key) moves. TestHookReceiver_ForeignSessionIDIsQuiet does not
// cover this — its rows share the same malformed string in both fields, never
// a WELL-FORMED-but-wrong-length top-level id the way 0.20.5 actually sends
// one.
func TestHookReceiver_GatewaySurfaceApprovalIsQuiet(t *testing.T) {
	hermesHome(t)
	h, target := newReceiver(t)

	const gatewayMintedSessionID = "20260802_233614_c97a8d12" // 8 hex — gateway mint shape
	body := `{"hook_event_name":"` + HookEventPreApprovalRequest +
		`","session_id":"` + gatewayMintedSessionID +
		`","extra":{"session_key":"whatsapp:4915112345678","surface":"gateway"}}`

	rec := post(t, h, body)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times for a gateway-surface approval, want 0 — hex length is "+
			"the only guard left standing since hermes 0.20.5 forwards a well-formed session_id "+
			"for gateway turns too", n)
	}
}

// TestHookReceiver_PermissionGateContract runs the shared #797 contract once
// per permission this receiver must honour, derived from the receiver's own
// declaration (issue #1488).
func TestHookReceiver_PermissionGateContract(t *testing.T) {
	contracttesting.AssertHookReceiverPermissionGated(t, contracttesting.HookReceiverGate{
		Build: func(t *testing.T, gate *contracttesting.ConsentGate) contracttesting.GatedHookReceiver {
			hermesHome(t)
			body := contractPayload(HookEventOnSessionEnd)
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
		PermissionKeyHooks, PermissionKeyStore)
}

// TestHookReceiver_NonPostRejected is a LOCK on the shared receiver shape.
func TestHookReceiver_NonPostRejected(t *testing.T) {
	hermesHome(t)
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
	hermesHome(t)
	h, _ := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestHookReceiver_DeniedStoreConsentDropsQuietly pins that a store-denied
// session must not dispatch even though the hooks consent is granted, and must
// not log an error while doing it (the receiver's own gate answers quietly;
// only the shared backstop logs).
//
// Since #1488's chokepoint the SILENCE is the surviving discriminator —
// deleting the gate below leaves every dispatch-shaped assertion green, because
// DecodeSealed re-checks the whole declared set and drops the payload itself.
// What it does NOT reproduce is the quiet: reaching the backstop logs an error,
// and an ordinary denied session must not collect one per event.
func TestHookReceiver_DeniedStoreConsentDropsQuietly(t *testing.T) {
	hermesHome(t)
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, contractPayload(HookEventOnSessionEnd))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while store consent was denied, want 0", n)
	}
	if len(log.Errors()) != 0 {
		t.Errorf("a store-denied hook logged %d error(s): %v", len(log.Errors()), log.Errors())
	}
}
