package gittest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A binary that is not there is a primer failure, not a skip: PrimeOrExit
// turns this error into a non-zero exit of the whole test binary.
func TestPrimeReportsAMissingBinary(t *testing.T) {
	_, err := prime("/nonexistent/irrlicht-2047-no-such-git", 5*time.Second)
	if err == nil {
		t.Fatal("priming a binary that does not exist returned no error")
	}
	if !strings.Contains(err.Error(), "failed after") {
		t.Errorf("error does not carry the elapsed time: %v", err)
	}
}

// A binary that outlives the deadline is reported as a non-answer with the
// elapsed time, the shape a cold xcrun stub would take.
func TestPrimeReportsAKillAtTheDeadline(t *testing.T) {
	slow := filepath.Join(t.TempDir(), "slow-git")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	elapsed, err := prime(slow, 200*time.Millisecond)
	if err == nil {
		t.Fatal("a binary that never answered was reported as primed")
	}
	if !strings.Contains(err.Error(), "did not answer within 200ms") {
		t.Errorf("error does not name the deadline: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the primer waited %s for a 200ms deadline", elapsed)
	}
}

// A binary whose child inherits the output pipe and outlives the kill must not
// hold the primer past its deadline (review finding on #2047's PR; the adapter
// measured the same shape at 30.01s in shellout_test.go before #1543's WaitDelay).
func TestPrimeIsBoundedWhenAChildHoldsThePipe(t *testing.T) {
	leaky := filepath.Join(t.TempDir(), "leaky-git")
	if err := os.WriteFile(leaky, []byte("#!/bin/sh\nsleep 30 &\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	elapsed, err := prime(leaky, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not answer within 200ms") {
		t.Fatalf("want a non-answer at the 200ms deadline, got %v", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the primer waited %s for a 200ms deadline — a grandchild holding the pipe outlived the kill", elapsed)
	}
}

// The real binary answers. This is the call every primed package's TestMain
// makes, run here once more so a broken resolution fails in this package too.
func TestPrimeGitAnswers(t *testing.T) {
	if _, err := PrimeGit(); err != nil {
		t.Fatal(err)
	}
}
