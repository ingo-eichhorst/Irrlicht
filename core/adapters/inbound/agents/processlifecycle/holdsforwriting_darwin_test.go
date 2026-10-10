//go:build darwin

package processlifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The darwin half of HoldsForWriting's coverage: the outcomes of the lsof child
// itself, which a real holder cannot be arranged into (holdsforwriting_test.go
// covers the answers a real holder produces, on darwin and linux alike).

// TestHoldsForWritingCouldNotAskIsAnError is the third answer. An lsof that
// never started knows nothing about the pid, and reporting it as "does not
// hold" is the #1537 collapse WriterOf was fixed for.
func TestHoldsForWritingCouldNotAskIsAnError(t *testing.T) {
	before := readProbeLedger()
	held, err := holdsForWritingVia(4242, "/tmp/probe-2079", missingBinaryCmd())
	if err == nil {
		t.Fatalf("an lsof that never started answered (%v, nil) — could-not-ask must be an error, not a false", held)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"lsof.holds_for_writing": {Probe: "lsof.holds_for_writing", Unanswered: 1},
	}, "holdsForWritingVia's shellout is the lsof.holds_for_writing probe")
}

// TestHoldsForWritingExitOneIsAnAnswer pins lsof's "nothing to report" (exit
// 1, lsofNothingToReport) as a real (false, nil): it is what lsof says for a
// pid that does not hold the path, for a pid that does not exist, and for a
// path that does not exist.
func TestHoldsForWritingExitOneIsAnAnswer(t *testing.T) {
	before := readProbeLedger()
	// The asked pid is this process, so the no-rows checks behind exit 1 see
	// a pid this user may inspect and a path that does not exist.
	held, err := holdsForWritingVia(os.Getpid(), "/tmp/probe-2079-missing", answeringCmd("1"))
	if err != nil || held {
		t.Fatalf("lsof exit 1 = (%v, %v), want (false, nil)", held, err)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"lsof.holds_for_writing": {Probe: "lsof.holds_for_writing", Answered: 1},
	}, "an lsof that ran and found nothing answered")
}

// TestHoldsForWritingNamesNothingStartsNoChild pins holdsForWritingVia's guard
// (its doc says what lsof does without it). A non-positive pid or an empty
// path names nothing that can hold a file, so the answer is (false, nil) with
// no child started.
func TestHoldsForWritingNamesNothingStartsNoChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.jsonl")
	for _, tc := range []struct {
		name string
		pid  int
		path string
	}{
		{name: "zero pid", pid: 0, path: path},
		{name: "negative pid", pid: -1, path: path},
		{name: "empty path", pid: 4242, path: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := readProbeLedger()
			held, err := HoldsForWriting(tc.pid, tc.path)
			if err != nil || held {
				t.Fatalf("HoldsForWriting(%d, %q) = (%v, %v), want (false, nil)", tc.pid, tc.path, held, err)
			}
			assertProbesMoved(t, before, map[string]ProbeCount{},
				"a call that names nothing must start no lsof child")
		})
	}
}

// TestHoldsForWritingAnotherUsersProcessIsAnError pins the case lsof answers
// with silence: without root it cannot inspect another user's process, and
// prints no rows and exits 1 for it, as for "nothing to report" (as uid 501,
// `lsof -a -p 1 -- /dev/null` does exactly that). Not being able to look is an
// error, never a "does not hold".
func TestHoldsForWritingAnotherUsersProcessIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which lsof lets inspect every process")
	}
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("kill(1, 0) = %v, want EPERM: pid 1 is not another user's process here, so this case tests nothing", err)
	}
	held, err := HoldsForWriting(1, "/dev/null")
	if err == nil {
		t.Fatalf("HoldsForWriting(1, /dev/null) = (%v, nil), want an error: lsof cannot inspect another user's process", held)
	}
}
