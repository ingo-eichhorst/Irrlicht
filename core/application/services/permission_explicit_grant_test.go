package services_test

import (
	"context"
	"strings"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
)

// explicitGrantAgent declares one ordinary observe permission and one that
// requires an explicit grant, each recording whether its Apply ran.
func explicitGrantAgent(observeApplied, explicitApplied *bool) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: "browser-pseudo"},
		Permissions: []agent.Permission{
			{Key: "observe", Kind: permission.KindObserve,
				Apply: func() error { *observeApplied = true; return nil }, Remove: func() error { return nil }},
			{Key: "browser-session", Kind: permission.KindObserve, RequiresExplicitGrant: true,
				Apply: func() error { *explicitApplied = true; return nil }, Remove: func() error { return nil }},
		},
	}
}

// TestGrantAll_LeavesAnExplicitGrantPermissionUnanswered is the #2012 QA
// defect test: IRRLICHT_PERMISSION_MODE=grant-all auto-granted every declared
// permission, so a dev or recording daemon on the real $HOME granted Mistral
// Vibe's browser-session permission (and ran its effects) with nobody
// answering it. A permission declaring RequiresExplicitGrant keeps its stored
// state: pending on a fresh home, granted only when a person granted it.
func TestGrantAll_LeavesAnExplicitGrantPermissionUnanswered(t *testing.T) {
	observeApplied, explicitApplied := false, false
	log := &mockLogger{}
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{explicitGrantAgent(&observeApplied, &explicitApplied)},
		Store:     &mockPermStore{},
		Push:      &mockPush{},
		Log:       log,
		Mode:      config.PermissionModeGrantAll,
		Registrar: &mockRegistrar{},
	})
	svc.Start(context.Background())

	if !svc.Granted("browser-pseudo", "observe") || !observeApplied {
		t.Fatal("vacuity: grant-all did not grant the ordinary observe permission beside it")
	}
	if svc.Granted("browser-pseudo", "browser-session") {
		t.Error("grant-all auto-granted a permission that requires an explicit grant")
	}
	if explicitApplied {
		t.Error("grant-all ran the Apply of a permission that requires an explicit grant")
	}
	found := false
	for _, m := range log.infos {
		if strings.Contains(m, "1 left for an explicit grant") {
			found = true
		}
	}
	if !found {
		t.Errorf("the grant-all log line does not count the permission left for an explicit grant: %q", log.infos)
	}
}

// TestGrantAll_KeepsAStoredExplicitGrant: a person's earlier grant, persisted
// by an ask-mode daemon, still holds under grant-all.
func TestGrantAll_KeepsAStoredExplicitGrant(t *testing.T) {
	observeApplied, explicitApplied := false, false
	set := permission.Set{}
	set.Put("browser-pseudo", "browser-session", permission.StateGranted)
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:    []agent.Agent{explicitGrantAgent(&observeApplied, &explicitApplied)},
		Store:     &mockPermStore{set: set},
		Push:      &mockPush{},
		Log:       &mockLogger{},
		Mode:      config.PermissionModeGrantAll,
		Registrar: &mockRegistrar{},
	})
	svc.Start(context.Background())
	if !svc.Granted("browser-pseudo", "browser-session") || !explicitApplied {
		t.Fatal("grant-all dropped a stored explicit grant")
	}
}
