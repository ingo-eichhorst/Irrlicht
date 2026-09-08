package desktopdriver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// RED-FIRST: with plain `poll`, this deadline error reads
// "X was not observed before its deadline: context deadline exceeded" for every
// reason it could have gone unmet. Cell 2-8 burned 12 minutes on exactly that
// message on 2026-09-07, and the recording had to be read afterwards to learn
// that the session existed and was simply unattributed.
func TestADeadlineCarriesTheReasonItsLastAttemptGave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := pollWithReason(ctx, "the thing", func() (bool, string, error) {
		return false, "the session is in state \"waiting\"", nil
	})
	if err == nil {
		t.Fatal("pollWithReason() = nil; the observation never became true")
	}
	if !strings.Contains(err.Error(), `the session is in state "waiting"`) {
		t.Fatalf("error = %v; want it to carry the reason the last attempt gave", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v; want it to stay a deadline error", err)
	}
}

// A wait that never got far enough to have a reason must say so, rather than
// carrying a stale one or an empty parenthesis.
func TestADeadlineWithNoReasonSaysNothingWasObserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := pollWithReason(ctx, "the thing", func() (bool, string, error) {
		return false, "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "nothing was observed at all") {
		t.Fatalf("error = %v; want it to say nothing was observed", err)
	}
}

func TestAMetObservationReturnsNoError(t *testing.T) {
	err := pollWithReason(context.Background(), "the thing", func() (bool, string, error) {
		return true, "", nil
	})
	if err != nil {
		t.Fatalf("pollWithReason() error = %v; the observation was met on the first attempt", err)
	}
}

// An observation that ERRORS is not an observation that went unmet. It must
// surface immediately rather than wait out the deadline.
func TestAnObservationErrorEndsTheWaitAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()

	err := pollWithReason(ctx, "the thing", func() (bool, string, error) {
		return false, "", errors.New("the daemon refused")
	})
	if err == nil || !strings.Contains(err.Error(), "the daemon refused") {
		t.Fatalf("error = %v; want the observation's own error", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the wait took %s; an error must end it at once", elapsed)
	}
}
