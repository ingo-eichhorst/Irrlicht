package services

import (
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/outbound"
)

// The third consent gate on the #1372 repair path, tested where it can actually
// be reached.
//
// RepairGrantedHookInstall has three independent refusals and they cover
// different windows:
//
//  1. the verifier loop asks Granted before it reads the file (proved in
//     hook_reverify_test.go, and by deleting that check: the revoke test goes
//     red);
//  2. RepairGrantedHookInstall re-reads the recorded state before it queues
//     anything (proved by deleting it: the on-disk test's direct-call arm goes
//     red);
//  3. runEffects re-reads the state a THIRD time, under effectMu, immediately
//     before running the closure.
//
// Gate 3 is the one that matters for a real revoke, because gates 1 and 2 can
// only be as fresh as the instant they were asked: a repair that passes both
// and then waits on effectMu behind an in-flight wizard answer would otherwise
// write to a config the user revoked while it waited. It is also the only gate
// unreachable from outside the package — by construction, gate 2 stops every
// non-racing caller — so a black-box test of it would either need a scheduling
// race (flaky, and passes vacuously when it loses) or would not test it at all.
//
// So this test does what the race would do, deterministically: it hands
// runEffects the exact pendingEffect RepairGrantedHookInstall builds, with the
// state already denied, and asserts the closure never runs. That is the same
// stale-effect skip an interactive superseding answer relies on; #1372 reuses
// it rather than adding a fourth check that could disagree with it.
func TestRunEffects_SkipsAGrantEffectWhoseStateIsNoLongerGranted(t *testing.T) {
	// The user has revoked. This is the state RepairGrantedHookInstall's own
	// pre-check would have seen a moment later — but the repair got past it and
	// is now inside runEffects, holding a granted-target effect.
	f := newGateFixture(permission.StateDenied)

	f.svc.runEffects([]pendingEffect{f.grantEffect()})

	applies, removes := f.counts()
	if applies != 0 {
		t.Errorf("Apply ran %d times for a grant effect whose permission is DENIED. "+
			"A background repair that wins the race to effectMu and then writes anyway is "+
			"the #570 violation #1372 must not introduce — the user revoked while it waited.",
			applies)
	}
	if removes != 0 {
		t.Errorf("Remove ran %d times; a skipped effect must run neither closure", removes)
	}
}

// Vacuity guard for the test above: the same call with the state still granted
// MUST run Apply. Without this, a runEffects that skipped everything — or a
// pendingEffect built wrong — would satisfy the assertion above while proving
// nothing at all.
func TestRunEffects_RunsAGrantEffectWhoseStateIsStillGranted(t *testing.T) {
	f := newGateFixture(permission.StateGranted)

	f.svc.runEffects([]pendingEffect{f.grantEffect()})

	if applies, _ := f.counts(); applies != 1 {
		t.Errorf("Apply ran %d times for a granted effect, want 1 — the refusal test above "+
			"would pass vacuously against a runEffects that runs nothing", applies)
	}
}

// --- review follow-up: a SKIPPED effect must not read as a successful one ---

// runEffects returning "how many did you run" is what lets the out-of-band
// #1372 repair tell gate 3's refusal apart from a successful re-install.
//
// Without it, a skip left effectErrs untouched, RepairGrantedHookInstall's
// post-call read of that slot returned "" — or, worse, a stale error from an
// earlier attempt — and the caller banked a repair that never happened: a
// lifetime counter, an armed back-off, and an error-level log line blaming
// something outside irrlicht for a deletion that had not occurred.
func TestRunEffects_ReportsZeroWhenEveryEffectIsSkipped(t *testing.T) {
	f := newGateFixture(permission.StateDenied)

	if ran := f.svc.runEffects([]pendingEffect{f.grantEffect()}); ran != 0 {
		t.Errorf("runEffects reported %d effects run while skipping all of them; the caller "+
			"cannot then tell a refusal from a success", ran)
	}
	if applies, _ := f.counts(); applies != 0 {
		t.Errorf("Apply ran %d times, want 0", applies)
	}

	// Vacuity guard: it must count the one it DOES run.
	f.setState(permission.StateGranted)
	if ran := f.svc.runEffects([]pendingEffect{f.grantEffect()}); ran != 1 {
		t.Errorf("runEffects reported %d for one executed effect, want 1 — the assertion above "+
			"would pass against a function that always returns 0", ran)
	}
}

// The composite: a repair that passes gates 1 and 2 and is then overtaken by a
// revoke while it waits on effectMu must report attempted=false, so the loop
// counts a consent refusal rather than a phantom re-install.
//
// The test holds effectMu itself, so a repair that has passed the pre-check is
// GUARANTEED to be parked exactly where gate 3 catches it. The one thing it
// cannot pin down is whether the goroutine got that far before the revoke — and
// it does not need to: if it did not, gate 2 refuses instead and the assertion
// holds for that reason. Losing the race costs coverage, never a false failure,
// which is why the deterministic ran==0 test above is the real proof.
func TestRepairGrantedHookInstall_RevokedWhileWaitingOnTheEffectLockIsNotAttempted(t *testing.T) {
	f := newGateFixture(permission.StateGranted)

	f.svc.effectMu.Lock() // stand in for an in-flight wizard answer

	type result struct {
		attempted bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		a, err := f.svc.RepairGrantedHookInstall("testagent", "hooks")
		done <- result{a, err}
	}()

	// Give the repair time to clear the pre-check and park on effectMu.
	time.Sleep(50 * time.Millisecond)

	f.setState(permission.StateDenied)
	f.svc.effectMu.Unlock()

	select {
	case got := <-done:
		if got.attempted {
			t.Errorf("RepairGrantedHookInstall reported attempted=true (err=%v) for a repair the "+
				"service refused because consent was withdrawn while it waited on effectMu. The "+
				"loop then banks a repair that never happened and logs that something outside "+
				"irrlicht deleted the entries.", got.err)
		}
		if got.err != nil {
			t.Errorf("a refusal returned an error (%v); declining is not failing", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RepairGrantedHookInstall did not return; deadlock on effectMu")
	}

	if applies, _ := f.counts(); applies != 0 {
		t.Errorf("Apply ran %d times for a revoked permission, want 0", applies)
	}
}

// --- fixture ---------------------------------------------------------------

// gateFixture is the one setup every test in this file needs: a single
// modify-kind hooks permission whose Apply and Remove are counted, wired into a
// real PermissionService.
//
// Factored because four near-identical copies of it is exactly the shape that
// makes a later change to the declaration land in three of them and get missed
// in the fourth — and a consent test that silently stops constructing the thing
// it means to test still passes.
type gateFixture struct {
	svc  *PermissionService
	perm agent.Permission

	mu      sync.Mutex
	applies int
	removes int
}

// newGateFixture builds the service with the permission already in state.
func newGateFixture(state permission.State) *gateFixture {
	f := &gateFixture{}
	f.perm = agent.Permission{
		Key:   "hooks",
		Kind:  permission.KindModify,
		Title: "Install hooks",
		Apply: func() error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.applies++
			return nil
		},
		Remove: func() error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.removes++
			return nil
		},
		Writes: &agent.ManagedUserFile{
			Path:      func() (string, error) { return "/tmp/irrelevant", nil },
			Uninstall: func() (bool, error) { return false, nil },
			Verify:    func() (agent.HookEntryStatus, error) { return agent.HookEntryStatus{}, nil },
		},
	}
	f.svc = NewPermissionService(PermissionServiceDeps{
		Agents: []agent.Agent{{
			Identity:    agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
			Process:     agent.Process{Match: agent.ExactName{Name: "testagent"}},
			Permissions: []agent.Permission{f.perm},
		}},
		Store: &gatePermStore{}, Push: &gatePush{}, Log: &gateLogger{},
	})
	f.setState(state)
	return f
}

// setState records a consent answer directly, standing in for whatever gesture
// would have produced it.
func (f *gateFixture) setState(state permission.State) {
	f.svc.mu.Lock()
	f.svc.set.Put("testagent", "hooks", state)
	f.svc.mu.Unlock()
}

// grantEffect is the pendingEffect RepairGrantedHookInstall builds.
func (f *gateFixture) grantEffect() pendingEffect {
	return pendingEffect{agentName: "testagent", perm: f.perm, target: permission.StateGranted}
}

func (f *gateFixture) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applies, f.removes
}

// --- minimal in-package doubles (the services_test ones are in the other
// package and cannot be reached from here) ---

type gatePermStore struct{ set permission.Set }

func (s *gatePermStore) Load() (permission.Set, error) {
	if s.set == nil {
		return permission.Set{}, nil
	}
	return s.set, nil
}
func (s *gatePermStore) Save(set permission.Set) error { s.set = set; return nil }

type gatePush struct{}

func (p *gatePush) Broadcast(outbound.PushMessage)        {}
func (p *gatePush) Subscribe() chan outbound.PushMessage  { return make(chan outbound.PushMessage, 1) }
func (p *gatePush) Unsubscribe(chan outbound.PushMessage) {}

type gateLogger struct{}

func (l *gateLogger) LogInfo(_, _, _ string)                                  {}
func (l *gateLogger) LogError(_, _, _ string)                                 {}
func (l *gateLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *gateLogger) Close() error                                            { return nil }
