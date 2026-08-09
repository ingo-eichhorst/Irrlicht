package services_test

import (
	"context"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
)

// --- #1368: what "the hook channel is expected to deliver" means ----------
//
// HookChannelReady is the predicate that ARMS the liveness watchdog, so every
// case it gets wrong is a wrong verdict downstream: a false positive convicts a
// channel that was never installed, and a false negative leaves a genuinely
// dead one unwatched.

// hookAgentDecl declares an agent whose "hooks" permission is a real hook
// install, plus a modify permission that is NOT one. The second is the case
// that separates "this agent has a granted permission" from "this agent's hook
// channel is installed", and it is the easy one to conflate.
func hookAgentDecl(f *applyFailure) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: "testagent", DisplayName: "Test Agent"},
		Process:  agent.Process{Match: agent.ExactName{Name: "testagent"}},
		Permissions: []agent.Permission{
			{Key: "hooks", Kind: permission.KindModify, Title: "Install hooks",
				Hooks: &agent.HookInstall{}, Apply: f.apply, Remove: f.remove},
			{Key: "statusline", Kind: permission.KindModify, Title: "Install statusline",
				Apply: func() error { return nil }, Remove: func() error { return nil }},
		},
	}
}

func newHookChannelService(t *testing.T, f *applyFailure) *services.PermissionService {
	t.Helper()
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{hookAgentDecl(f)},
		Store:     &mockPermStore{},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeAsk,
		Registrar: &mockRegistrar{},
	})
	svc.Start(context.Background())
	return svc
}

// A permission nobody has answered is not an installed channel.
func TestHookChannelReady_FalseWhilePending(t *testing.T) {
	svc := newHookChannelService(t, &applyFailure{})
	if svc.HookChannelReady("testagent") {
		t.Error("a pending hook permission must not arm the watchdog — there is nothing installed to be silent")
	}
}

// The positive case: granted, and the install closure succeeded.
func TestHookChannelReady_TrueOnceGrantedAndApplied(t *testing.T) {
	f := &applyFailure{}
	svc := newHookChannelService(t, f)

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitFor(t, "the hook channel to read ready", func() bool { return svc.HookChannelReady("testagent") })
}

// TestHookChannelReady_FalseWhenTheInstallFailed is the #1362 boundary, and the
// load-bearing one.
//
// A granted permission whose Apply failed means the user said yes and the
// entries were NOT written. Arming the watchdog there would let it convict the
// channel for not delivering through entries that never existed — reporting, in
// vaguer words and against a different issue number, a failure that #1362
// already reports accurately with its actual error string attached.
func TestHookChannelReady_FalseWhenTheInstallFailed(t *testing.T) {
	f := &applyFailure{failApplyFor: -1}
	svc := newHookChannelService(t, f)

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "hooks", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitFor(t, "the failing Apply to run", func() bool { applied, _ := f.calls(); return applied > 0 })

	if svc.HookChannelReady("testagent") {
		t.Error("a granted permission whose Apply failed reads as an installed channel — " +
			"the watchdog would report 'installed but silent' for entries that were never written")
	}
}

// A granted permission that is not a hook install must not vouch for the hook
// channel. Without the Hooks!=nil term, granting a statusline or a transcripts
// permission would arm the watchdog against an adapter with no hooks at all.
func TestHookChannelReady_IgnoresNonHookPermissions(t *testing.T) {
	svc := newHookChannelService(t, &applyFailure{})

	if err := svc.Answer([]services.PermissionAnswer{
		{Agent: "testagent", Permission: "statusline", Grant: true},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitFor(t, "the statusline grant to settle", func() bool {
		return svc.Granted("testagent", "statusline")
	})

	if svc.HookChannelReady("testagent") {
		t.Error("a granted non-hook permission armed the hook watchdog")
	}
}

// An adapter the registry does not know cannot have a channel.
func TestHookChannelReady_FalseForAnUnknownAgent(t *testing.T) {
	svc := newHookChannelService(t, &applyFailure{})
	if svc.HookChannelReady("no-such-agent") {
		t.Error("an unknown agent must not read as ready")
	}
}
