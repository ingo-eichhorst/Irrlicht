package kirocli

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

// kiroSessionRoot relocates KIRO_HOME to a temp dir and returns the declared
// transcript root (sessions/cli) inside it. Does NOT install a fake kiro-cli
// binary — the receiver-only tests in this file, and hookpath_test.go /
// hookreceipt_test.go / hookunknown_test.go, never call EnsureHooksInstalled
// or UninstallHooks, only the installer wirings in hookport_test.go,
// hookversion_test.go and hookinstaller_test.go do; see kiroInstallerHome.
func kiroSessionRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(kiroHomeEnvVar, home)
	root := filepath.Join(home, "sessions", "cli")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create sessions dir: %v", err)
	}
	return root
}

// writeSessionTranscript lays down <dir>/<id>.jsonl and returns its path —
// kiro-cli's transcripts are flat, unlike copilot's <session-id>/events.jsonl
// nesting, so no subdirectory is created.
func writeSessionTranscript(t *testing.T, dir, id string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+transcriptExt)
	line := `{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":"ok"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
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

// contractPayload renders a hook body carrying the given session id — the
// ONLY channel kiro-cli's payload ever carries (see hooks.go's payload doc:
// no event, on any spelling, carries transcript_path directly).
func contractPayload(sessionID, event string) string {
	body, err := json.Marshal(kiroHookPayload{HookEventName: event, SessionID: sessionID})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// sessionIDForPath returns a session id that resolveTranscriptPath's
// filepath.Join(root, id+".jsonl") reconstructs into target once Join's own
// lexical cleaning runs. This is how the shared path-confinement contract's
// escape attempts — expressed as a target FILE PATH — are driven through the
// only channel this receiver accepts: a caller-supplied session id. Every
// other wired adapter carries transcript_path directly on the wire and hands
// the contract's target straight through; kiro-cli never does (see hooks.go),
// so the escape has to travel through the id exactly as a hostile local
// process actually would have to.
//
// climb is generous rather than exact: filepath.Join collapses a leading run
// of "../" against an absolute base down to "/" and then walks the remainder,
// so any climb at least as deep as the root lands on the same result — the
// arithmetic Go's own path.Clean documents ("Eliminate .. elements that begin
// a rooted path"). It also correctly collapses a TARGET that already embeds
// its own ".." (the parent-traversal fixture's own shape), since Join cleans
// the whole concatenated string in one pass rather than component by
// component.
func sessionIDForPath(target string) string {
	target = strings.TrimSuffix(target, transcriptExt)
	const climb = 64
	return strings.Repeat("../", climb) + strings.TrimPrefix(target, string(filepath.Separator))
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

// --- fake kiro-cli, for the installer-facing tests only ---

// fakeKiroCLIScript stands in for the real kiro-cli binary in every test that
// exercises EnsureHooksInstalled/UninstallHooks. It implements exactly the
// three invocations this package's installer makes — `--version`,
// `agent create <name> --from <src>`, `agent set-default <name>` — with the
// same externally-observable behaviour measured live against kiro-cli 2.6.0
// in an isolated KIRO_HOME during this issue's audit: `agent create` errors
// (exit 1) when the target file already exists rather than overwriting it,
// and `agent set-default` splices chat.defaultAgent into settings/cli.json
// IN PLACE, preserving whatever other keys are already there (measured live:
// kiro-cli preserved a pre-existing chat.enableTodoList across a set-default
// call).
//
// It does NOT attempt to reproduce kiro-cli's own agent-config SCHEMA
// (deny_unknown_fields, the exact field set `agent create --from` populates)
// — this package's own JSON merge logic (ensureFlatHooksInstalled and
// friends) operates on a generic map via hookjson.ReadSettings and does not
// care what the file otherwise contains, so a minimal stand-in document
// exercises exactly what this package's Go code needs to get right. The
// settings-splice IS reproduced with more care, because
// TestUninstall_RestoresThePriorDefaultAgent asserts a property of the REAL
// kiro-cli's own behaviour (preserving unrelated keys) that this package's Go
// code does not implement itself — it only ever READS that file, never
// writes it directly — so the fixture has to be faithful for that assertion
// to mean anything.
const fakeKiroCLIScript = `#!/bin/sh
set -e
if [ "$1" = "--version" ]; then
  echo "kiro-cli 2.6.0"
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "create" ]; then
  name="$3"
  target="$KIRO_HOME/agents/$name.json"
  if [ -e "$target" ]; then
    echo "error: Agent with name $name already exists. Aborting" >&2
    exit 1
  fi
  mkdir -p "$KIRO_HOME/agents"
  cat > "$target" <<JSON
{"name":"$name","description":"Default agent","prompt":"stub","tools":["*"],"resources":[],"hooks":{},"model":null}
JSON
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "set-default" ]; then
  name="$3"
  mkdir -p "$KIRO_HOME/settings"
  f="$KIRO_HOME/settings/cli.json"
  if [ -f "$f" ] && grep -q '"chat.defaultAgent"' "$f"; then
    sed -E 's/"chat\.defaultAgent"[[:space:]]*:[[:space:]]*"[^"]*"/"chat.defaultAgent":"'"$name"'"/' "$f" > "$f.tmp"
    mv "$f.tmp" "$f"
  elif [ -f "$f" ]; then
    sed -E 's/^\{/{"chat.defaultAgent":"'"$name"'",/' "$f" > "$f.tmp"
    mv "$f.tmp" "$f"
  else
    printf '{"chat.defaultAgent":"%s"}' "$name" > "$f"
  fi
  exit 0
fi
echo "fake-kiro-cli: unhandled args: $*" >&2
exit 1
`

// installFakeKiroCLI substitutes fakeKiroCLIScript for the real kiro-cli
// binary by overriding the kiroCLIPath package var (kiroctl.go) — NOT by
// prepending a temp dir to $PATH, which does not reach pathutil's
// trusted-directory resolution; see kiroCLIPath's own doc comment for why
// that seam exists.
func installFakeKiroCLI(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kiro-cli")
	if err := os.WriteFile(path, []byte(fakeKiroCLIScript), 0o755); err != nil {
		t.Fatalf("write fake kiro-cli: %v", err)
	}
	original := kiroCLIPath
	kiroCLIPath = func() string { return path }
	t.Cleanup(func() { kiroCLIPath = original })
}

// kiroInstallerHome relocates KIRO_HOME to a fresh temp dir AND installs the
// fake kiro-cli, for every test that calls EnsureHooksInstalled or
// UninstallHooks (which may shell out to materialize the irrlicht agent or to
// flip chat.defaultAgent — see hookinstaller.go's package comment).
func kiroInstallerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(kiroHomeEnvVar, home)
	installFakeKiroCLI(t)
	return home
}

// --- behaviour ---

// TestStopHook_DispatchesTurnDone pins the turn-end half of the channel: a
// stop payload's session_id resolves to the transcript the fswatcher already
// tails, and the receiver forwards a turn-done signal for it.
func TestStopHook_DispatchesTurnDone(t *testing.T) {
	root := kiroSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-stop")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload("sess-stop", HookStop))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("got %d stop dispatches, want 1 (perm=%d)", len(stops), len(target.perms()))
	}
	if stops[0].sessionID != "sess-stop" {
		t.Errorf("sessionID = %q, want %q", stops[0].sessionID, "sess-stop")
	}
	if stops[0].transcriptPath != tp {
		t.Errorf("transcriptPath = %q, want the reconstructed path %q", stops[0].transcriptPath, tp)
	}
}

// TestStopHook_ForwardsAssistantResponseAndWaitingCue pins that the turn text
// and the waiting-cue verdict actually reach HandleStopHook, computed from
// the payload's own assistant_response field.
func TestStopHook_ForwardsAssistantResponseAndWaitingCue(t *testing.T) {
	root := kiroSessionRoot(t)
	writeSessionTranscript(t, root, "sess-stop2")
	target := &mockTarget{}
	gate := keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}
	h := NewHookHandler(target, gate, mockLogger{})

	body, err := json.Marshal(kiroHookPayload{
		HookEventName:      HookStop,
		SessionID:          "sess-stop2",
		AssistantResponse:  "All done. Want me to continue?",
	})
	if err != nil {
		t.Fatal(err)
	}
	post(t, h, string(body))

	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("got %d stop dispatches, want 1", len(stops))
	}
	if !strings.Contains(stops[0].lastAssistantText, "All done") {
		t.Errorf("lastAssistantText = %q, want it to carry assistant_response", stops[0].lastAssistantText)
	}
	if !stops[0].waitingCue {
		t.Error("waitingCue = false for a final message ending in a question; want true")
	}
}

// TestPostToolUseHook_DispatchesBroadRelease pins that postToolUse goes
// through the generic name-keyed path under session.HookPostToolUse (Claude
// Code's own spelling, deliberately reused — see dispatchHookEvent's comment
// for why), which session.HookSignal resolves to a broad release.
func TestPostToolUseHook_DispatchesBroadRelease(t *testing.T) {
	root := kiroSessionRoot(t)
	writeSessionTranscript(t, root, "sess-tool")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload("sess-tool", HookPostToolUse))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	perms := target.perms()
	if len(perms) != 1 {
		t.Fatalf("got %d postToolUse dispatches, want 1", len(perms))
	}
	if perms[0].sessionID != "sess-tool" {
		t.Errorf("sessionID = %q, want %q", perms[0].sessionID, "sess-tool")
	}
	if perms[0].hookEventName != session.HookPostToolUse {
		t.Errorf("forwarded hook name = %q, want %q", perms[0].hookEventName, session.HookPostToolUse)
	}
	effect, ok := session.HookSignal(perms[0].hookEventName)
	if !ok {
		t.Fatalf("session.HookSignal(%q) has no row — the broad release depends on one", perms[0].hookEventName)
	}
	if effect.Signal != session.SignalPermissionPrompt || !effect.Release {
		t.Errorf("HookSignal(%q) = %+v, want a Release of SignalPermissionPrompt", perms[0].hookEventName, effect)
	}
}

// TestPreToolUseHook_IsNotInstalledAndCountsAsUnrecognized pins the central
// design decision of this adapter (hooks.go's package comment): preToolUse
// is not installed, so if kiro-cli ever sends it anyway, it must be counted
// as an unrecognized event, never silently dispatched and never silently
// dropped without being counted.
func TestPreToolUseHook_IsNotInstalledAndCountsAsUnrecognized(t *testing.T) {
	root := kiroSessionRoot(t)
	writeSessionTranscript(t, root, "sess-pretool")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload("sess-pretool", "preToolUse"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("preToolUse dispatched %d times, want 0 — it must never be treated as an assert or a release", n)
	}
}

// TestHeadlessSession_NoSessionIDIsNeverDispatched pins the audit's own
// finding: a headless run fires hooks but omits session_id (and writes no
// transcript at all), so there is nothing to correlate to. Never dispatched
// either way, but the STATUS is the shared "missing transcript_path"
// exception (hookjson.RejectPath's doc), not the confinement 2xx: an empty
// session_id makes resolveTranscriptPath return "", which Confine's own
// RejectEmptyPath reports as a 400 — the same status a genuinely malformed
// body gets, and the same one copilot's own Notification-with-no-id case
// would hit.
func TestHeadlessSession_NoSessionIDIsNeverDispatched(t *testing.T) {
	kiroSessionRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload("", HookStop))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing transcript_path)", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times with no session_id, want 0", n)
	}
}

// TestHostileSessionIDIsConfined is the security consequence of rebuilding a
// path from a caller-supplied id: the id is untrusted, so the reconstructed
// path must still go through the confiner.
func TestHostileSessionIDIsConfined(t *testing.T) {
	kiroSessionRoot(t)
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload("../../../../etc/passwd", HookStop))

	if rec.Code < 200 || rec.Code > 299 {
		t.Errorf("status = %d, want 2xx: a confinement refusal is reported by the log and "+
			"counter, not by a status code", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("a traversal session id dispatched %d times, want 0", n)
	}
}

// TestHookReceiver_DeniedHooksConsentDropsQuietly is the #570 gate at the
// receiving end.
func TestHookReceiver_DeniedHooksConsentDropsQuietly(t *testing.T) {
	root := kiroSessionRoot(t)
	writeSessionTranscript(t, root, "sess-stop")
	target := &mockTarget{}
	h := NewHookHandler(target, keyedGate{}, mockLogger{})

	rec := post(t, h, contractPayload("sess-stop", HookStop))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Fatalf("dispatched %d times while hooks consent was denied, want 0", n)
	}
}

// TestHookReceiver_DeniedTranscriptsConsentDropsQuietly pins the second gate.
func TestHookReceiver_DeniedTranscriptsConsentDropsQuietly(t *testing.T) {
	root := kiroSessionRoot(t)
	writeSessionTranscript(t, root, "sess-stop")
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true}, log)

	rec := post(t, h, contractPayload("sess-stop", HookStop))

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
			root := kiroSessionRoot(t)
			writeSessionTranscript(t, root, "sess-gate")
			body := contractPayload("sess-gate", HookStop)
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
	kiroSessionRoot(t)
	h, _ := newReceiver(t)
	req := httptest.NewRequest(http.MethodGet, HookEndpointPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

// TestHookReceiver_MalformedJSONIs400 is a LOCK: a body that will not decode
// is the one case that answers non-2xx.
func TestHookReceiver_MalformedJSONIs400(t *testing.T) {
	kiroSessionRoot(t)
	h, target := newReceiver(t)
	rec := post(t, h, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times on malformed JSON, want 0", n)
	}
}
