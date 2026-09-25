package services_test

import (
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Issue #2057: SetProviderRateLimit is how an account-API sweep (Muse's
// Meta quota) puts a snapshot on a session outside the transcript pipeline.

func newRateLimitDetector(t *testing.T) (*services.SessionDetector, *mockRepo, *mockBroadcaster) {
	t.Helper()
	repo := newMockRepo()
	bc := &mockBroadcaster{}
	deps := defaultSessionDetectorDeps(nil, repo, nil)
	deps.Broadcaster = bc
	return services.NewSessionDetector(nil, deps), repo, bc
}

func metaSnap(used float64) *session.RateLimitSnapshot {
	return &session.RateLimitSnapshot{
		Windows:            []session.RateLimitWindow{{UsedPercent: used, WindowMinutes: 300}},
		Provider:           session.ProviderMeta,
		AttributionQuality: session.AttributionQualityConfirmed,
		SampledAt:          100,
	}
}

func rateLimitOf(t *testing.T, repo *mockRepo, id string) *session.RateLimitSnapshot {
	t.Helper()
	s, err := repo.Load(id)
	if err != nil || s == nil {
		t.Fatalf("load %s: %v", id, err)
	}
	if s.Metrics == nil {
		return nil
	}
	return s.Metrics.RateLimit
}

func updatedBroadcasts(bc *mockBroadcaster) int {
	n := 0
	for _, m := range bc.messages() {
		if m.Type == outbound.PushTypeUpdated {
			n++
		}
	}
	return n
}

func TestSetProviderRateLimit_WritesAndBroadcasts(t *testing.T) {
	det, repo, bc := newRateLimitDetector(t)
	_ = repo.Save(&session.SessionState{SessionID: "s1", Adapter: "muse", State: session.StateWorking})

	if !det.SetProviderRateLimit("s1", session.ProviderMeta, metaSnap(4)) {
		t.Fatal("SetProviderRateLimit reported no change for a session with no snapshot")
	}
	got := rateLimitOf(t, repo, "s1")
	if got == nil || got.Provider != session.ProviderMeta || got.Windows[0].UsedPercent != 4 {
		t.Fatalf("persisted rate limit = %+v, want the meta snapshot", got)
	}
	if updatedBroadcasts(bc) != 1 {
		t.Fatalf("updated broadcasts = %d, want 1", updatedBroadcasts(bc))
	}
}

// Lock: re-applying the same reading (only timestamps differ) neither saves
// nor broadcasts, so a sweep tick with a cached poll result is silent.
func TestSetProviderRateLimit_SameReadingIsNoOp(t *testing.T) {
	det, repo, bc := newRateLimitDetector(t)
	_ = repo.Save(&session.SessionState{SessionID: "s1", Adapter: "muse"})
	det.SetProviderRateLimit("s1", session.ProviderMeta, metaSnap(4))
	later := metaSnap(4)
	later.SampledAt, later.LastSuccessAt, later.LastAttemptAt = 999, 999, 999
	if det.SetProviderRateLimit("s1", session.ProviderMeta, later) {
		t.Fatal("an identical reading reported a change")
	}
	if got := rateLimitOf(t, repo, "s1"); got == nil || got.SampledAt != 100 {
		t.Fatalf("snapshot = %+v, want the first observation (SampledAt 100) kept", got)
	}
	if updatedBroadcasts(bc) != 1 {
		t.Fatalf("updated broadcasts = %d, want 1", updatedBroadcasts(bc))
	}
}

func TestSetProviderRateLimit_NilClearsOnlyThatProvider(t *testing.T) {
	det, repo, _ := newRateLimitDetector(t)
	_ = repo.Save(&session.SessionState{SessionID: "meta", Adapter: "muse"})
	_ = repo.Save(&session.SessionState{SessionID: "claude", Adapter: "claude-code", Metrics: &session.SessionMetrics{
		RateLimit: &session.RateLimitSnapshot{Provider: session.ProviderAnthropic, Windows: []session.RateLimitWindow{{UsedPercent: 9}}},
	}})
	det.SetProviderRateLimit("meta", session.ProviderMeta, metaSnap(4))

	if !det.SetProviderRateLimit("meta", session.ProviderMeta, nil) {
		t.Fatal("clearing an existing meta snapshot reported no change")
	}
	if got := rateLimitOf(t, repo, "meta"); got != nil {
		t.Fatalf("meta snapshot not cleared: %+v", got)
	}
	if det.SetProviderRateLimit("claude", session.ProviderMeta, nil) {
		t.Fatal("clearing meta touched an anthropic snapshot")
	}
	if got := rateLimitOf(t, repo, "claude"); got == nil || got.Provider != session.ProviderAnthropic {
		t.Fatalf("anthropic snapshot = %+v, want it untouched", got)
	}
}

// Lock: a write never replaces another provider's snapshot, and an unknown
// session is not created.
func TestSetProviderRateLimit_NeverClobbersOtherProviderOrCreatesSession(t *testing.T) {
	det, repo, _ := newRateLimitDetector(t)
	_ = repo.Save(&session.SessionState{SessionID: "claude", Metrics: &session.SessionMetrics{
		RateLimit: &session.RateLimitSnapshot{Provider: session.ProviderAnthropic},
	}})
	if det.SetProviderRateLimit("claude", session.ProviderMeta, metaSnap(4)) {
		t.Fatal("a meta write replaced an anthropic snapshot")
	}
	if det.SetProviderRateLimit("missing", session.ProviderMeta, metaSnap(4)) {
		t.Fatal("a write to an unknown session reported a change")
	}
	if s, _ := repo.Load("missing"); s != nil {
		t.Fatal("a write created a session that did not exist")
	}
}
