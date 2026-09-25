package session

import "testing"

// Lock for issue #2057: the Muse account-API sweep writes a Meta snapshot
// straight onto Metrics.RateLimit, and Muse's own transcript metrics never
// carry one. A later transcript pass (RateLimit nil) must keep the swept
// snapshot rather than erase it — carryForwardOverlayState is what does
// that, and this pins it. It passes before and after #2057.
func TestMergeMetrics_CarriesSweptRateLimitAcrossTranscriptPass(t *testing.T) {
	swept := &RateLimitSnapshot{Provider: ProviderMeta, Windows: []RateLimitWindow{{UsedPercent: 4, WindowMinutes: 300}}}
	old := &SessionMetrics{RateLimit: swept}
	fresh := &SessionMetrics{} // a Muse transcript pass: no RateLimit

	merged := MergeMetrics(fresh, old)
	if merged.RateLimit != swept {
		t.Fatalf("merged RateLimit = %+v, want the swept meta snapshot carried forward", merged.RateLimit)
	}

	// And a cleared snapshot stays cleared: nil on both sides is not re-filled.
	if got := MergeMetrics(&SessionMetrics{}, &SessionMetrics{}).RateLimit; got != nil {
		t.Fatalf("merge of two empty metrics produced RateLimit %+v", got)
	}
}
