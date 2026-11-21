package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// zaiAccountAPISnapshot is the shape the #2010 Z.ai sweep writes onto a
// claude-code session routed to api.z.ai: provider "zai", the coding plan as
// Product, and no account reference, because the Z.ai response carries none.
func zaiAccountAPISnapshot(sampledAt int64) *session.RateLimitSnapshot {
	return &session.RateLimitSnapshot{
		Windows:             []session.RateLimitWindow{{UsedPercent: 1, WindowMinutes: 300}},
		SampledAt:           sampledAt,
		Provider:            "zai",
		Product:             "GLM Coding Plan",
		ObservationSource:   "account_api",
		AttributionEvidence: "zai_account_api",
		AttributionQuality:  session.AttributionQualityConfirmed,
	}
}

// TestAccountBinder_LeavesAnotherProvidersSnapshotAlone is #2010's binder
// defect. The binder stamps a claude-code session's snapshot with the
// Anthropic account its ~/.claude.json is logged into. A session routed to
// Z.ai carries a Z.ai snapshot instead, and that login says nothing about
// it: stamping would publish the Anthropic account id as the Z.ai reading's
// account and replace the Z.ai product with the Claude plan.
func TestAccountBinder_LeavesAnotherProvidersSnapshotAlone(t *testing.T) {
	t.Run("granted, bound session", func(t *testing.T) {
		g := newBinderRig(90)
		g.r.set("acct-anthropic", 50)
		g.now = 200
		s := snapState(150, nil)
		s.Metrics.RateLimit = zaiAccountAPISnapshot(150)

		g.b.Stamp(s)

		rl := s.Metrics.RateLimit
		if rl.ConfirmedAccountRef != "" {
			t.Errorf("ConfirmedAccountRef = %q on a zai snapshot; the Anthropic login is not its account", rl.ConfirmedAccountRef)
		}
		if rl.Product != "GLM Coding Plan" {
			t.Errorf("Product = %q, want GLM Coding Plan kept (the binder must not restamp another provider's product)", rl.Product)
		}
	})
	t.Run("consent not granted", func(t *testing.T) {
		g := newBinderRig(90)
		g.r.granted = false
		s := snapState(150, nil)
		s.Metrics.RateLimit = zaiAccountAPISnapshot(150)

		g.b.Stamp(s)

		if got := s.Metrics.RateLimit.Product; got != "GLM Coding Plan" {
			t.Errorf("Product = %q, want GLM Coding Plan kept: the scrub on a revoked identity read is for the binder's own stamp, not another provider's", got)
		}
	})
}
