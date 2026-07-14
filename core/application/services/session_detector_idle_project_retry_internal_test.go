package services

import "testing"

// TestShouldRetryIdleProjectResolution_CapsAtMax pins the exact bound
// maxIdleProjectResolveAttempts places on shouldRetryIdleProjectResolution
// (#1021): the first maxIdleProjectResolveAttempts calls for a session must
// each report "retry", and every call after that must report "give up" —
// forever, not just once past the cap.
func TestShouldRetryIdleProjectResolution_CapsAtMax(t *testing.T) {
	d := &SessionDetector{idleProjectRetryAttempts: map[string]int{}}

	for i := range maxIdleProjectResolveAttempts {
		if !d.shouldRetryIdleProjectResolution("s") {
			t.Fatalf("attempt %d: expected retry allowed within the cap", i+1)
		}
	}

	for i := range 3 {
		if d.shouldRetryIdleProjectResolution("s") {
			t.Fatalf("attempt past cap (extra call %d): expected retry to be refused", i+1)
		}
	}

	if got := d.idleProjectRetryAttempts["s"]; got != maxIdleProjectResolveAttempts {
		t.Fatalf("attempt counter = %d, want it to stay pinned at %d", got, maxIdleProjectResolveAttempts)
	}
}

// TestShouldRetryIdleProjectResolution_PerSessionIndependent verifies the
// retry budget is tracked independently per session, so a capped-out session
// doesn't starve a different session's retries.
func TestShouldRetryIdleProjectResolution_PerSessionIndependent(t *testing.T) {
	d := &SessionDetector{idleProjectRetryAttempts: map[string]int{
		"exhausted": maxIdleProjectResolveAttempts,
	}}

	if d.shouldRetryIdleProjectResolution("exhausted") {
		t.Fatal("expected the exhausted session to be refused")
	}
	if !d.shouldRetryIdleProjectResolution("fresh") {
		t.Fatal("expected a different session to still get its own retry budget")
	}
}
