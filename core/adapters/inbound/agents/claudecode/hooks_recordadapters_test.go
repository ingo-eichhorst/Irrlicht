package claudecode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// This file is the literal acceptance evidence for issue #1769: a rig daemon
// recording some OTHER adapter must not accept a claudecode-shaped hook POST
// at all. lib/spawn-record-daemon.sh:87 sets
// IRRLICHT_PERMISSION_MODE=grant-all unconditionally for every recording, and
// claudecode is one of righome.Unisolatable's structurally unrelocatable
// adapters (claudeSettingsPath joins os.UserHomeDir() unconditionally) — so,
// before this fix, recording e.g. a mistral-vibe cell repointed the
// operator's REAL ~/.claude/settings.json at the recording daemon, and any
// claudecode session on the machine (this one included, since Claude Code
// reloads its settings file live rather than snapshotting it at session
// start) would have its hook traffic accepted and turned into a recorded
// session, whatever cell was actually being recorded.
//
// This test wires the REAL claudecode.NewHookHandler to a REAL
// *services.PermissionService running in grant-all mode with
// RecordAdapters naming some other adapter, then POSTs a claudecode Stop
// hook payload and asserts the target — standing in for the recorder — is
// never called. It was seen red before PermissionService.Start's
// scopedOutByRecordAdapters check existed: the same POST reached
// target.HandleStopHook once.
//
// Importing services from an adapter's _test.go file runs opposite to the
// hexagonal layering (adapters -> application/services) that
// core/architecture_test.go statically enforces for non-test code — but
// that check walks only non-test imports (see its own header comment: "a
// pkg/**/*_test.go importing an adapter would [pass]"), and
// permission_shared_config_gate_test.go already sets the mirror-image
// precedent (a services test importing the kirocli adapter, with the same
// justification). This is the one place claudecode's real hook receiver and
// a real PermissionService's grant-all auto-grant meet.

// noopPermStore, noopPush, noopLogger and noopRegistrar are the minimal
// PermissionService dependencies this test needs — a fresh, empty consent
// store, no broadcast/log observation, and no watcher registration. None of
// PermissionService's behavior under test (grant-all auto-grant scoping)
// depends on what these do, only that they satisfy the ports.
type noopPermStore struct{}

func (noopPermStore) Load() (permission.Set, error) { return permission.Set{}, nil }
func (noopPermStore) Save(permission.Set) error     { return nil }

type noopPush struct{}

func (noopPush) Broadcast(outbound.PushMessage)       {}
func (noopPush) Subscribe() chan outbound.PushMessage { return nil }
func (noopPush) Unsubscribe(chan outbound.PushMessage) {
}

type noopLogger struct{}

func (noopLogger) LogInfo(string, string, string)                       {}
func (noopLogger) LogError(string, string, string)                      {}
func (noopLogger) LogProcessingTime(string, string, int64, int, string) {}
func (noopLogger) Close() error                                         { return nil }

type noopRegistrar struct{}

func (noopRegistrar) AddWatcher(context.Context, inbound.Watcher) {}

// fakeOtherAdapter is a minimal second agent standing in for "whichever
// adapter this recording run is actually for" — anything with a modify
// permission declaring a managed file would do; only its NAME (referenced by
// RecordAdapters below) matters to this test.
func fakeOtherAdapter() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: "other-adapter", DisplayName: "Other Adapter"},
		Process:  agent.Process{Match: agent.ExactName{Name: "other-adapter"}},
		Permissions: []agent.Permission{
			{
				Key:    "hooks",
				Kind:   permission.KindModify,
				Title:  "Install status hooks",
				Apply:  func() error { return nil },
				Remove: func() error { return nil },
				Writes: &agent.ManagedUserFile{
					Path:      func() (string, error) { return "/tmp/irr-1769-other-adapter.json", nil },
					Uninstall: func() (bool, error) { return false, nil },
				},
			},
		},
	}
}

// recordAdaptersGate boots a real PermissionService in grant-all mode scoped
// to recordAdapters — the exact shape a rig daemon runs in — and returns it
// as the ConsentGranter NewHookHandler is wired with in production
// (registerHookRoutes passes the daemon's own PermissionService).
func recordAdaptersGate(t *testing.T, recordAdapters []string) *services.PermissionService {
	t.Helper()
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents: []agent.Agent{Agent(), fakeOtherAdapter()},
		Store:  noopPermStore{},
		Push:   noopPush{},
		Log:    noopLogger{},
		Mode:   config.PermissionModeGrantAll,
		// Mirrors the rig setting IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1
		// unconditionally (#1449) — this test is about #1769's
		// adapter-scoping gate, not the separate isolated-home guard.
		AllowSharedConfigWrites: true,
		Registrar:               noopRegistrar{},
		RecordAdapters:          recordAdapters,
	})
	svc.Start(context.Background())
	return svc
}

// TestClaudeCodeHookRejectedWhenClaudeCodeIsNotTheRecordedAdapter is the
// literal #1769 acceptance evidence: a claudecode-shaped Stop hook POST,
// naming a session this daemon otherwise knows nothing about, must not reach
// the recorder when the daemon is scoped to record a DIFFERENT adapter.
func TestClaudeCodeHookRejectedWhenClaudeCodeIsNotTheRecordedAdapter(t *testing.T) {
	dir := claudeProjectsRoot(t)
	transcriptPath := writeContractTranscript(t, dir)
	gate := recordAdaptersGate(t, []string{"other-adapter"}) // recording "other-adapter", NOT claudecode

	target := &mockTarget{}
	handler := NewHookHandler(target, nil, gate, noopLogger{})

	req := httptest.NewRequest(http.MethodPost, HookEndpointPath,
		bytes.NewBufferString(contractPayload(transcriptPath, HookStop)))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a consent-denied hook is dropped quietly, not answered with an error)", rr.Code)
	}
	if calls := target.getCalls(); len(calls) != 0 {
		t.Errorf("HandlePermissionHook was called %d time(s); want 0", len(calls))
	}
	// stopCalls has no getter (nothing else needed one before this test);
	// read it directly — same package, and the request above has already
	// returned, so there is no concurrent writer left to race.
	if n := len(target.stopCalls); n != 0 {
		t.Errorf("HandleStopHook was called %d time(s) — the hook landed in the recording despite claudecode not being the recorded adapter (issue #1769)", n)
	}
}

// TestClaudeCodeHookAcceptedWhenClaudeCodeIsTheRecordedAdapter is the vacuity
// guard: the gate above must not refuse claudecode's OWN hook traffic when
// claudecode is (or is among) the recorded adapter(s) — otherwise the
// restriction would break the very recordings it exists to protect.
func TestClaudeCodeHookAcceptedWhenClaudeCodeIsTheRecordedAdapter(t *testing.T) {
	dir := claudeProjectsRoot(t)
	transcriptPath := writeContractTranscript(t, dir)
	gate := recordAdaptersGate(t, []string{AdapterName})

	target := &mockTarget{}
	handler := NewHookHandler(target, nil, gate, noopLogger{})

	req := httptest.NewRequest(http.MethodPost, HookEndpointPath,
		bytes.NewBufferString(contractPayload(transcriptPath, HookStop)))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if n := len(target.stopCalls); n != 1 {
		t.Fatalf("HandleStopHook was called %d time(s); want 1 — claudecode's own hook must still land when claudecode IS the recorded adapter", n)
	}
}
