package services

import (
	"sync"
	"testing"

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
	var mu sync.Mutex
	applies, removes := 0, 0

	perm := agent.Permission{
		Key:   "hooks",
		Kind:  permission.KindModify,
		Title: "Install hooks",
		Apply: func() error {
			mu.Lock()
			applies++
			mu.Unlock()
			return nil
		},
		Remove: func() error {
			mu.Lock()
			removes++
			mu.Unlock()
			return nil
		},
		Hooks: &agent.HookInstall{
			ConfigPath: func() (string, error) { return "/tmp/irrelevant", nil },
			Uninstall:  func() (bool, error) { return false, nil },
			Verify:     func() (agent.HookEntryStatus, error) { return agent.HookEntryStatus{}, nil },
		},
	}
	decl := agent.Agent{
		Identity:    agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:     agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{perm},
	}

	svc := NewPermissionService(PermissionServiceDeps{
		Agents: []agent.Agent{decl},
		Store:  &gatePermStore{},
		Push:   &gatePush{},
		Log:    &gateLogger{},
	})

	// The user has revoked. This is the state RepairGrantedHookInstall's own
	// pre-check would have seen a moment later — but the repair got past it and
	// is now inside runEffects, holding a granted-target effect.
	svc.mu.Lock()
	svc.set.Put("testagent", "hooks", permission.StateDenied)
	svc.mu.Unlock()

	svc.runEffects([]pendingEffect{{agentName: "testagent", perm: perm, target: permission.StateGranted}})

	mu.Lock()
	defer mu.Unlock()
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
	var mu sync.Mutex
	applies := 0
	perm := agent.Permission{
		Key: "hooks", Kind: permission.KindModify, Title: "Install hooks",
		Apply: func() error {
			mu.Lock()
			applies++
			mu.Unlock()
			return nil
		},
		Hooks: &agent.HookInstall{
			ConfigPath: func() (string, error) { return "/tmp/irrelevant", nil },
			Verify:     func() (agent.HookEntryStatus, error) { return agent.HookEntryStatus{}, nil },
		},
	}
	decl := agent.Agent{
		Identity:    agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:     agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{perm},
	}
	svc := NewPermissionService(PermissionServiceDeps{
		Agents: []agent.Agent{decl},
		Store:  &gatePermStore{},
		Push:   &gatePush{},
		Log:    &gateLogger{},
	})
	svc.mu.Lock()
	svc.set.Put("testagent", "hooks", permission.StateGranted)
	svc.mu.Unlock()

	svc.runEffects([]pendingEffect{{agentName: "testagent", perm: perm, target: permission.StateGranted}})

	mu.Lock()
	defer mu.Unlock()
	if applies != 1 {
		t.Errorf("Apply ran %d times for a granted effect, want 1 — the refusal test above "+
			"would pass vacuously against a runEffects that runs nothing", applies)
	}
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
