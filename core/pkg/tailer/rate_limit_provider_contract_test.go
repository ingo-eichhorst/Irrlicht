package tailer

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestRateLimitProviderConstantsAgree pins RateLimitSnapshot's mirrored
// identity-stamp constants (ProviderAnthropic, ProviderOpenAI,
// AttributionQualityConfirmed — parser.go) against their canonical
// counterparts in core/domain/session/rate_limit.go.
//
// The mirror is deliberate (RateLimitSnapshot's own doc comment: parsers in
// this package emit it without importing the domain), the same reason
// core/domain/session/userblocking_contract_test.go and this package's own
// TestUserBlockingListsAgree pin isUserBlockingTool against
// isUserBlockingToolName. An unpinned duplicate constant is exactly the
// "silently stale one change later" shape AGENTS.md warns about: today the
// two sets of literals agree because whoever wrote them copied correctly,
// but nothing stops session.ProviderOpenAI (say) from being renamed or
// retyped without this file's copy following it. This is a NEW guard — it
// has no "before the fix" to run red — so it earns its place by mutation,
// not by a red-first defect proof: see this repo's mutation fixture for it,
// tools/lib/tailer-provider-constants-mutations_test.sh.
func TestRateLimitProviderConstantsAgree(t *testing.T) {
	cases := []struct {
		name    string
		tailer  string
		session string
	}{
		{"ProviderAnthropic", ProviderAnthropic, session.ProviderAnthropic},
		{"ProviderOpenAI", ProviderOpenAI, session.ProviderOpenAI},
		{"AttributionQualityConfirmed", AttributionQualityConfirmed, session.AttributionQualityConfirmed},
	}
	for _, c := range cases {
		if c.tailer != c.session {
			t.Errorf("%s: tailer copy %q disagrees with session.%s %q", c.name, c.tailer, c.name, c.session)
		}
	}
}
