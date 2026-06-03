package tailer

import (
	"strings"
	"testing"
	"time"
)

var estObservedAt = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

func TestScanTaskEstimate_HappyPath(t *testing.T) {
	text := `Working on it. <!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":2,"risk":"low","confidence":0.95} --> Next step.`
	est := ScanTaskEstimate(text, estObservedAt)
	if est == nil {
		t.Fatal("expected estimate, got nil")
	}
	if est.TotalRounds != 10 || est.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/10", est.CompletedRounds, est.TotalRounds)
	}
	if est.Risk != "low" {
		t.Errorf("Risk = %q, want low", est.Risk)
	}
	if est.Confidence == nil || *est.Confidence != 0.95 {
		t.Errorf("Confidence = %v, want 0.95", est.Confidence)
	}
	if est.ObservedAt != estObservedAt.Unix() {
		t.Errorf("ObservedAt = %d, want %d", est.ObservedAt, estObservedAt.Unix())
	}
}

func TestScanTaskEstimate_MarkerKeyVariants(t *testing.T) {
	for _, marker := range []string{"irrlicht-eta", "irrlicht_eta", "irrlicht-estimate", "irrlicht_estimate"} {
		text := `<!-- {"marker":"` + marker + `","total_rounds":5,"completed_rounds":1} -->`
		if ScanTaskEstimate(text, estObservedAt) == nil {
			t.Errorf("marker key %q should be accepted", marker)
		}
	}
}

func TestScanTaskEstimate_CompletedKeyDrift(t *testing.T) {
	for _, key := range []string{"completed_rounds", "done_rounds", "round"} {
		text := `<!-- {"marker":"irrlicht-eta","total_rounds":8,"` + key + `":3} -->`
		est := ScanTaskEstimate(text, estObservedAt)
		if est == nil || est.CompletedRounds != 3 {
			t.Errorf("alias %q: est = %+v, want CompletedRounds=3", key, est)
		}
	}
}

func TestScanTaskEstimate_TotalRoundsWithoutMarkerKey(t *testing.T) {
	// Acceptance gate: total_rounds alone qualifies (issue #558 parse contract).
	text := `<!-- {"total_rounds":6,"completed_rounds":2} -->`
	est := ScanTaskEstimate(text, estObservedAt)
	if est == nil || est.TotalRounds != 6 {
		t.Fatalf("est = %+v, want TotalRounds=6", est)
	}
}

func TestScanTaskEstimate_MissingCompletedDefaultsZero(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-eta","total_rounds":7} -->`
	est := ScanTaskEstimate(text, estObservedAt)
	if est == nil || est.CompletedRounds != 0 {
		t.Fatalf("est = %+v, want CompletedRounds=0", est)
	}
}

func TestScanTaskEstimate_MalformedIgnored(t *testing.T) {
	for name, text := range map[string]string{
		"truncated json":   `<!-- {"marker":"irrlicht-eta","total_rounds": -->`,
		"not json":         `<!-- hello world -->`,
		"no estimate keys": `<!-- {"note":"just a comment"} -->`,
		"marker only":      `<!-- {"marker":"irrlicht-eta"} -->`,
		"foreign marker":   `<!-- {"marker":"other-tool"} -->`,
		"string total":     `<!-- {"marker":"irrlicht-eta","total_rounds":"ten"} -->`,
	} {
		if est := ScanTaskEstimate(text, estObservedAt); est != nil {
			t.Errorf("%s: expected nil, got %+v", name, est)
		}
	}
}

func TestScanTaskEstimate_ClampRejection(t *testing.T) {
	for name, text := range map[string]string{
		"total zero":         `<!-- {"total_rounds":0,"completed_rounds":0} -->`,
		"total too large":    `<!-- {"total_rounds":101,"completed_rounds":1} -->`,
		"completed negative": `<!-- {"total_rounds":5,"completed_rounds":-1} -->`,
		"completed > total":  `<!-- {"total_rounds":5,"completed_rounds":6} -->`,
	} {
		if est := ScanTaskEstimate(text, estObservedAt); est != nil {
			t.Errorf("%s: expected rejection, got %+v", name, est)
		}
	}
}

func TestScanTaskEstimate_OutOfRangeConfidenceIgnoredNotRejected(t *testing.T) {
	// confidence is optional — an out-of-range value (e.g. the model emits
	// 85 meaning 85%) is dropped, but the valid total/completed progress is
	// kept, not thrown away.
	for _, text := range []string{
		`<!-- {"total_rounds":5,"completed_rounds":1,"confidence":1.5} -->`,
		`<!-- {"total_rounds":5,"completed_rounds":1,"confidence":85} -->`,
		`<!-- {"total_rounds":5,"completed_rounds":1,"confidence":-0.1} -->`,
	} {
		est := ScanTaskEstimate(text, estObservedAt)
		if est == nil || est.CompletedRounds != 1 || est.TotalRounds != 5 {
			t.Fatalf("est = %+v, want 1/5 kept", est)
		}
		if est.Confidence != nil {
			t.Errorf("out-of-range confidence should be dropped, got %v", *est.Confidence)
		}
	}
}

func TestScanTaskEstimate_LatestWins(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":1} -->
some progress prose
<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":4} -->`
	est := ScanTaskEstimate(text, estObservedAt)
	if est == nil || est.CompletedRounds != 4 {
		t.Fatalf("est = %+v, want latest marker (CompletedRounds=4)", est)
	}
}

func TestScanTaskEstimate_LatestInvalidDoesNotShadowEarlierValid(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":3} -->
<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":99} -->`
	est := ScanTaskEstimate(text, estObservedAt)
	if est == nil || est.CompletedRounds != 3 {
		t.Fatalf("est = %+v, want earlier valid marker (CompletedRounds=3)", est)
	}
}

func TestScanTaskEstimate_OversizeCommentSkipped(t *testing.T) {
	text := `<!-- {"marker":"irrlicht-eta","total_rounds":5,"completed_rounds":1,"pad":"` +
		strings.Repeat("x", maxTaskEstimateCommentLen) + `"} -->`
	if est := ScanTaskEstimate(text, estObservedAt); est != nil {
		t.Fatalf("oversize comment should be skipped, got %+v", est)
	}
}

func TestScanTaskEstimate_NoMarker(t *testing.T) {
	if est := ScanTaskEstimate("plain prose, no comments at all", estObservedAt); est != nil {
		t.Fatalf("expected nil, got %+v", est)
	}
}
