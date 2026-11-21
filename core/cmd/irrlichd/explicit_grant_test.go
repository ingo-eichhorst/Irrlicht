package main

import (
	"context"
	"slices"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
)

// TestConsentCatalog_ExplicitGrantPermissionsSurviveGrantAll is the registry
// half of agent.Permission.RequiresExplicitGrant (#2012 QA). It pins which
// declarations in the consent catalog require an explicit grant, so the flag
// cannot silently drop off one, and runs a grant-all PermissionService over
// copies of every declaration, with every Apply/Remove stripped so nothing
// real runs, to show each of them stays unanswered while everything else is
// granted.
func TestConsentCatalog_ExplicitGrantPermissionsSurviveGrantAll(t *testing.T) {
	var explicit []string
	var copies []agent.Agent
	for _, a := range declaredConsentCatalog() {
		c := agent.Agent{Identity: a.Identity}
		for _, p := range a.Permissions {
			if p.RequiresExplicitGrant {
				explicit = append(explicit, a.Identity.Name+"/"+p.Key)
			}
			c.Permissions = append(c.Permissions, agent.Permission{Key: p.Key, Kind: p.Kind, RequiresExplicitGrant: p.RequiresExplicitGrant})
		}
		copies = append(copies, c)
	}
	if want := []string{"mistral-vibe-browser-session/browser-session"}; !slices.Equal(explicit, want) {
		t.Fatalf("permissions requiring an explicit grant = %q, want %q", explicit, want)
	}

	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents: copies,
		Store:  filesystem.NewPermissionStore(t.TempDir()),
		Push:   services.NewPushService(),
		Log:    e2eLog{},
		Mode:   config.PermissionModeGrantAll,
	})
	svc.Start(context.Background())
	granted := 0
	for _, a := range copies {
		for _, p := range a.Permissions {
			on := svc.Granted(a.Identity.Name, p.Key)
			switch {
			case p.RequiresExplicitGrant && on:
				t.Errorf("grant-all granted %s/%s, which requires an explicit grant", a.Identity.Name, p.Key)
			case on:
				granted++
			}
		}
	}
	if granted == 0 {
		t.Fatal("vacuity: grant-all granted nothing at all")
	}
}
