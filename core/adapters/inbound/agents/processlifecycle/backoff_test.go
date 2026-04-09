package processlifecycle

import (
	"testing"
	"time"
)

func TestBackoffConstants(t *testing.T) {
	if BackoffInterval <= DefaultInterval {
		t.Errorf("BackoffInterval (%v) should be greater than DefaultInterval (%v)",
			BackoffInterval, DefaultInterval)
	}
	if stableThreshold <= 0 {
		t.Errorf("stableThreshold (%d) should be positive", stableThreshold)
	}
}

func TestScanner_BackoffFields(t *testing.T) {
	s := NewScanner("nonexistent-process", "test-adapter", 10*time.Millisecond)

	// Verify initial state.
	if s.stablePolls != 0 {
		t.Errorf("initial stablePolls = %d, want 0", s.stablePolls)
	}
	if s.lastPIDCount != 0 {
		t.Errorf("initial lastPIDCount = %d, want 0", s.lastPIDCount)
	}
	if s.interval != 10*time.Millisecond {
		t.Errorf("interval = %v, want 10ms", s.interval)
	}

	// Simulate reaching the stable threshold: after stableThreshold consecutive
	// polls with an unchanged PID count, the scanner should use BackoffInterval.
	s.stablePolls = stableThreshold
	if s.stablePolls < stableThreshold {
		t.Errorf("stablePolls (%d) should be >= stableThreshold (%d)", s.stablePolls, stableThreshold)
	}

	// Simulate a PID count change: reset stablePolls, interval should revert.
	s.stablePolls = 0
	s.lastPIDCount = 3
	if s.stablePolls >= stableThreshold {
		t.Errorf("after reset stablePolls (%d) should be < stableThreshold (%d)", s.stablePolls, stableThreshold)
	}
}

func TestNewScanner_DefaultInterval(t *testing.T) {
	// Passing 0 should use DefaultInterval.
	s := NewScanner("test", "adapter", 0)
	if s.interval != DefaultInterval {
		t.Errorf("interval = %v, want DefaultInterval (%v)", s.interval, DefaultInterval)
	}

	// Passing a negative value should also use DefaultInterval.
	s = NewScanner("test", "adapter", -1*time.Second)
	if s.interval != DefaultInterval {
		t.Errorf("interval = %v, want DefaultInterval (%v) for negative input", s.interval, DefaultInterval)
	}

	// Passing a positive value should use that value.
	s = NewScanner("test", "adapter", 2*time.Second)
	if s.interval != 2*time.Second {
		t.Errorf("interval = %v, want 2s", s.interval)
	}
}
