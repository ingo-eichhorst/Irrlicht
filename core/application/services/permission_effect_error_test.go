package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/outbound"
)

// --- #1362: a failed Apply must be recorded, surfaced, and retryable ------
//
// Every assertion below reads the API projection (Snapshot marshalled to
// JSON) or counts closure invocations rather than touching a new Go field
// directly, so the whole file COMPILES against pre-fix main. That is
// deliberate: a compile error proves the field is missing, not that the
// behaviour is wrong. Reading through the wire format makes the red a
// behavioural one, and pins the JSON contract the two UIs consume.

// applyFailure is a modify-permission effect pair whose Apply fails a
// configurable number of times before succeeding. Remove always succeeds
// unless removeErr is set.
type applyFailure struct {
	mu           sync.Mutex
	applyCalls   int
	removeCalls  int
	failApplyFor int   // fail the first N Apply calls; -1 = always fail
	removeErr    error // when non-nil, Remove fails with this
}

var errApplyBoom = errors.New("settings.json is malformed: invalid character '}' looking for beginning of object key string")

func (f *applyFailure) apply() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyCalls++
	if f.failApplyFor < 0 || f.applyCalls <= f.failApplyFor {
		return errApplyBoom
	}
	return nil
}

func (f *applyFailure) remove() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	return f.removeErr
}

func (f *applyFailure) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applyCalls, f.removeCalls
}

func failingAgentDecl(f *applyFailure) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:  agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{
			{Key: "hooks", Kind: permission.KindModify, Title: "Install hooks",
				Apply: f.apply, Remove: f.remove},
			{Key: "transcripts", Kind: permission.KindObserve, Title: "Read transcripts"},
		},
	}
}

func newFailingService(f *applyFailure, store *mockPermStore, push *mockPush) *services.PermissionService {
	return services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{failingAgentDecl(f)},
		Store:     store,
		Push:      push,
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
		Factories: nil,
		HasLive:   nil,
	})
}

// permJSON marshals the snapshot and returns the raw JSON for one
// permission, so assertions read exactly what the web dashboard and the
// macOS app decode.
func permJSON(t *testing.T, svc *services.PermissionService, agentName, key string) string {
	t.Helper()
	snap := svc.Snapshot()
	for _, a := range snap.Agents {
		if a.Name != agentName {
			continue
		}
		for _, p := range a.Permissions {
			if p.Key != key {
				continue
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("marshal %s/%s: %v", agentName, key, err)
			}
			return string(b)
		}
	}
	t.Fatalf("permission %s/%s not in snapshot", agentName, key)
	return ""
}

// permState reads the state string out of the snapshot.
func permState(t *testing.T, svc *services.PermissionService, agentName, key string) string {
	t.Helper()
	snap := svc.Snapshot()
	for _, a := range snap.Agents {
		if a.Name != agentName {
			continue
		}
		for _, p := range a.Permissions {
			if p.Key == key {
				return p.State
			}
		}
	}
	t.Fatalf("permission %s/%s not in snapshot", agentName, key)
	return ""
}

// TestFailedApplyIsSurfacedInSnapshot is the core #1362 defect test. Today
// the reason is written to the log and dropped; the API projection the two
// UIs render says a bare "granted".
func TestFailedApplyIsSurfacedInSnapshot(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if applied, _ := f.calls(); applied != 1 {
		t.Fatalf("apply calls = %d, want 1", applied)
	}

	got := permJSON(t, svc, "testagent", "hooks")
	if !strings.Contains(got, "settings.json is malformed") {
		t.Fatalf("the Apply failure is invisible to every UI.\n"+
			"permission JSON = %s\nwant it to carry the reason %q", got, errApplyBoom)
	}
}

// TestFailedApplyDoesNotRollBackTheGrant is a LOCK, not a defect test: it
// pins the single most important design constraint in #1362 — the user
// consented, the effect failed, and those are different facts. It passes on
// pre-fix main by construction, and its job is to fail if anyone "fixes"
// #1362 by reverting the grant.
func TestFailedApplyDoesNotRollBackTheGrant(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	store := &mockPermStore{}
	svc := newFailingService(f, store, &mockPush{})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	if !svc.Granted("testagent", "hooks") {
		t.Fatal("a failed Apply revoked the user's consent — grant must stand")
	}
	if got := permState(t, svc, "testagent", "hooks"); got != string(permission.StateGranted) {
		t.Fatalf("state = %q, want granted (consent is not conditional on the effect)", got)
	}
	if store.saveCount() != 1 {
		t.Fatalf("saves = %d, want 1 — the consent decision is persisted either way", store.saveCount())
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if persisted.Get("testagent", "hooks") != permission.StateGranted {
		t.Fatal("persisted state lost the grant after a failed Apply")
	}
}

// TestReGrantRetriesFailedApply covers scope item 3: a grant whose Apply
// failed must be re-appliable without revoking and re-granting. Today
// Answer short-circuits any same-state answer, so the retry never reaches
// the closure.
func TestReGrantRetriesFailedApply(t *testing.T) {
	f := &applyFailure{failApplyFor: 1} // fail once, then succeed
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	ans := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}}
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if applied, _ := f.calls(); applied != 1 {
		t.Fatalf("apply calls after first grant = %d, want 1", applied)
	}

	// The user hits Retry. Same answer, same state — but the effect never
	// took, so it must run again.
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer (retry): %v", err)
	}
	applied, _ := f.calls()
	if applied != 2 {
		t.Fatalf("apply calls after retry = %d, want 2 — a grant whose Apply "+
			"failed is not retryable, the user must revoke and re-grant", applied)
	}
}

// TestSuccessfulRetryClearsTheError pins the other half of retry: once the
// effect lands, the permission stops reporting a failure.
func TestSuccessfulRetryClearsTheError(t *testing.T) {
	f := &applyFailure{failApplyFor: 1}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	ans := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}}
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got := permJSON(t, svc, "testagent", "hooks"); !strings.Contains(got, "settings.json is malformed") {
		t.Fatalf("precondition: first Apply failure not recorded; JSON = %s", got)
	}
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer (retry): %v", err)
	}

	got := permJSON(t, svc, "testagent", "hooks")
	if strings.Contains(got, "settings.json is malformed") {
		t.Fatalf("a successful retry left the stale failure behind: %s", got)
	}
	if got := permState(t, svc, "testagent", "hooks"); got != string(permission.StateGranted) {
		t.Fatalf("state = %q, want granted", got)
	}
}

// TestSuccessfulApplyReportsNoError guards against the error field being
// populated unconditionally.
func TestSuccessfulApplyReportsNoError(t *testing.T) {
	f := &applyFailure{failApplyFor: 0} // never fails
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got := permJSON(t, svc, "testagent", "hooks"); strings.Contains(got, "effect_error") {
		t.Fatalf("a successful Apply reported an error: %s", got)
	}
}

// TestFailedRemoveIsSurfacedAndRetryable is the revoke-side twin: "denied
// but the hooks are still installed" is the same swallowed error and is at
// least as dangerous for consent.
func TestFailedRemoveIsSurfacedAndRetryable(t *testing.T) {
	f := &applyFailure{failApplyFor: 0, removeErr: errors.New("settings.json is read-only")}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	deny := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: false}}
	if err := svc.Answer(deny); err != nil {
		t.Fatalf("Answer (deny): %v", err)
	}

	if got := permJSON(t, svc, "testagent", "hooks"); !strings.Contains(got, "read-only") {
		t.Fatalf("the Remove failure is invisible to every UI: %s", got)
	}
	if got := permState(t, svc, "testagent", "hooks"); got != string(permission.StateDenied) {
		t.Fatalf("state = %q, want denied — the revoke decision stands", got)
	}
	if svc.Granted("testagent", "hooks") {
		t.Fatal("#570: a permission whose Remove failed must not read as granted")
	}
	// Retry the revoke.
	if err := svc.Answer(deny); err != nil {
		t.Fatalf("Answer (retry deny): %v", err)
	}
	if _, removed := f.calls(); removed != 2 {
		t.Fatalf("remove calls after retry = %d, want 2", removed)
	}
}

// TestFailedApplyKeepsPendingAndDeniedInert re-pins the #570 guarantee
// across the new error path: nothing is exercised while pending or denied.
// A LOCK — it passes on pre-fix main.
func TestFailedApplyKeepsPendingAndDeniedInert(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	// Pending: nothing ran.
	if applied, removed := f.calls(); applied != 0 || removed != 0 {
		t.Fatalf("pending state ran effects: applied=%d removed=%d", applied, removed)
	}
	if svc.Granted("testagent", "hooks") || svc.ObserveGranted("testagent") {
		t.Fatal("pending must not read as granted")
	}

	// Denied: Remove runs, but nothing is granted and no Apply happens.
	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: false},
		{Agent: "testagent", Permission: "transcripts", Grant: false},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if applied, _ := f.calls(); applied != 0 {
		t.Fatalf("deny ran Apply %d times", applied)
	}
	if svc.Granted("testagent", "hooks") || svc.ObserveGranted("testagent") {
		t.Fatal("denied must not read as granted")
	}
}

// TestFailedApplyAtStartupIsSurfaced covers the other entry point into
// runEffects: the daemon re-applies every granted permission on Start, and
// a failure there is exactly the "hooks silently absent" case a user hits
// after editing settings.json by hand.
func TestFailedApplyAtStartupIsSurfaced(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	store := &mockPermStore{set: permission.Set{
		"testagent": {"hooks": permission.StateGranted},
	}}
	svc := newFailingService(f, store, &mockPush{})
	svc.Start(context.Background())

	if applied, _ := f.calls(); applied != 1 {
		t.Fatalf("apply calls at start = %d, want 1", applied)
	}
	got := permJSON(t, svc, "testagent", "hooks")
	if !strings.Contains(got, "settings.json is malformed") {
		t.Fatalf("a startup re-apply failure is invisible to every UI: %s", got)
	}
	if !svc.Granted("testagent", "hooks") {
		t.Fatal("a failed startup re-apply revoked consent")
	}
}

// TestConflictingAnswersConvergeWithFailingEffects is the failing-effect
// twin of TestPermissionServiceConflictingAnswersConverge. That test only
// ever uses succeeding closures, so nothing pinned the invariant that
// matters here: the RECORDED error must always describe the FINAL state,
// never a superseded one. runEffects skips stale effects without recording
// a result, which is what makes that true — this proves it under -race.
func TestConflictingAnswersConvergeWithFailingEffects(t *testing.T) {
	f := &applyFailure{failApplyFor: -1, removeErr: errors.New("remove exploded")}
	svc := newFailingService(f, &mockPermStore{}, &mockPush{})
	svc.Start(context.Background())

	for i := 0; i < 100; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = svc.Answer([]services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}})
		}()
		go func() {
			defer wg.Done()
			_ = svc.Answer([]services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: false}})
		}()
		wg.Wait()

		// Both closures always fail, so whatever state won, its own
		// failure — and only its own — must be the one on record.
		granted := svc.Granted("testagent", "hooks")
		got := permJSON(t, svc, "testagent", "hooks")
		if granted && !strings.Contains(got, "settings.json is malformed") {
			t.Fatalf("round %d: granted, but the recorded error is not the Apply failure: %s", i, got)
		}
		if !granted && !strings.Contains(got, "remove exploded") {
			t.Fatalf("round %d: denied, but the recorded error is not the Remove failure: %s", i, got)
		}
	}
}

// TestRetryBroadcastsSoBothSurfacesRefresh: the wizard on the other surface
// has to learn the error cleared.
func TestRetryBroadcastsSoBothSurfacesRefresh(t *testing.T) {
	f := &applyFailure{failApplyFor: 1}
	push := &mockPush{}
	svc := newFailingService(f, &mockPermStore{}, push)
	svc.Start(context.Background())

	ans := []services.PermissionAnswer{{Agent: "testagent", Permission: "hooks", Grant: true}}
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if err := svc.Answer(ans); err != nil {
		t.Fatalf("Answer (retry): %v", err)
	}
	if got := push.count(outbound.PushTypePermissionsUpdated); got != 2 {
		t.Fatalf("permissions_updated broadcasts = %d, want 2 (grant + retry)", got)
	}
}
