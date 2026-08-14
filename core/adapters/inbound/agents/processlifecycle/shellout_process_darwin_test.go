//go:build darwin

package processlifecycle

import (
	"testing"
	"time"
)

// TestRunPgrepVia_NonAnswerStaysAnError is the lock runPgrep never had.
//
// Before #1538 runPgrep had NO test of any kind — its exit-1 rule was
// unverified — and that absence was measured rather than assumed: replacing
// `probeAnswered(err, pgrepNoMatch)` with the unrestricted `probeAnswered(err)`
// left `go test ./...` fully green, so the tool-specific half of the predicate
// was free to be widened at this call site by anybody, including by #1538
// itself. That is the shape #1517 shipped two gate defects through: a widening
// with slack in the gates meant to catch it.
//
// Rows 1-2 are LOCKS on the pre-#1538 behaviour (a real answer parses; exit 1
// is the no-match answer, not a failure). Rows 3-5 pin the opposite mutation —
// a classifier that calls every failure an answer — and each is a case the
// collapse spelling `if err != nil { return nil, nil }` would report as "no
// process matched", which is #1485's sentence with pgrep's tool name in it.
//
// Row 1 is also the vacuity guard: a runPgrepVia hard-wired to return an error
// would satisfy rows 3-5 while proving nothing.
func TestRunPgrepVia_NonAnswerStaysAnError(t *testing.T) {
	cases := []struct {
		name    string
		build   shelloutCmd
		wantErr bool
		wantN   int
		why     string
	}{
		{
			name: "a real answer", build: shellCmd("echo 4711; echo 4712"),
			wantErr: false, wantN: 2,
			why: "the vacuity guard: without a row that parses PIDs, every want-error row below passes for the wrong reason",
		},
		{
			name: "exit 1 — pgrep's no-match", build: shellCmd("exit 1"),
			wantErr: false, wantN: 0,
			why: "pgrep exits 1 when nothing matched; that is an answer and the caller must see (nil, nil)",
		},
		{
			name: "exit 2 — pgrep's usage/error status", build: shellCmd("exit 2"),
			wantErr: true,
			why:     "the allowlist is exactly {1}: any other normal exit is not evidence about which processes exist. Widening this to 'any normal exit' was green against the whole suite before this test existed",
		},
		{
			name: "killed by a signal", build: shellCmd("kill -9 $$"),
			wantErr: true,
			why:     "a killed pgrep did not answer, and reporting nil matches would say 'that agent is not running' about a process nobody looked for",
		},
		{
			name: "binary missing", build: missingCmd("pgrep-1538"),
			wantErr: true,
			why:     "a fork/exec failure never reached the question",
		},
	}

	for _, tc := range cases {
		pids, err := runPgrepVia("-x", "claude", tc.build)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v — %s", tc.name, err, tc.wantErr, tc.why)
			continue
		}
		if !tc.wantErr && len(pids) != tc.wantN {
			t.Errorf("%s: got %d pids %v, want %d — %s", tc.name, len(pids), pids, tc.wantN, tc.why)
		}
	}
}

// TestRunPgrepVia_CeilingIsAnErrorNotAnEmptyResult is the #1485-shaped row that
// cannot be written with a fast fixture: it costs the real shelloutTimeout,
// because the thing under test IS the ceiling firing mid-run.
//
// It is the twin of TestBundleIDVia_CeilingActuallyFires, and it exists for the
// same reason: the rows above reach the pre-Start branch or a normal exit, and
// a mid-run SIGKILL is a different error shape (*exec.ExitError "signal:
// killed", which does NOT wrap the context's error — see probeAnswered). The
// elapsed-time assertion is what proves the row did not pass on the cheap
// branch.
func TestRunPgrepVia_CeilingIsAnErrorNotAnEmptyResult(t *testing.T) {
	t.Parallel() // spends the real 2s ceiling and shares no state
	// stalledChild takes the ctx runPgrepVia passes, on purpose: this must be
	// killed by runPgrepVia's own ceiling, not by a deadline a fixture invented.
	start := time.Now()
	pids, err := runPgrepVia("-x", "claude", stalledChild)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("a pgrep killed by its own ceiling reported %v matches and no error — that is #1485 with pgrep's name on it", pids)
	}
	if elapsed < shelloutTimeout/2 {
		t.Errorf("returned after %v, well under the %v ceiling: this row is passing on the pre-start branch, not on the mid-run kill it is about", elapsed, shelloutTimeout)
	}
}

// TestWriterOfVia_NonAnswerIsNotAnUnwrittenFile is #1537's fifth instance of
// the #1485 family, and it is the same tool as #1485: an lsof that could not be
// asked reported as "nobody has this transcript open".
//
// Row 1 is the vacuity guard AND the lock: lsof's exit 1 is a real answer here
// — genuinely nobody holds the file — and it must keep returning (0, nil). It
// is what stops the fix from being "any non-zero exit is an error", which would
// turn every unwritten transcript into a discovery failure.
//
// Note what this test does NOT claim. Both consumers of the value were read
// (services.PIDManager.TryDiscoverPID and .AllowsSession) and BOTH treat
// `err != nil` and `pid <= 0` identically — retry, and admit, respectively — so
// no user-visible behaviour changes today. What changes is that the ProcessObserver
// port stops being violated: its contract already says "a missing/unopened file
// is not an error — it returns 0, nil", which is only meaningful if a probe
// that could not RUN is one.
func TestWriterOfVia_NonAnswerIsNotAnUnwrittenFile(t *testing.T) {
	cases := []struct {
		name    string
		build   shelloutCmd
		wantErr bool
		wantPID int
		why     string
	}{
		{
			name: "lsof answers with a writer", build: shellCmd(
				`echo "COMMAND  PID USER  FD   TYPE DEVICE SIZE/OFF NODE NAME"; echo "codex  24454 ingo  14w  REG  1,18   3330     99  /tmp/t.jsonl"`),
			wantErr: false, wantPID: 24454,
			why: "the vacuity guard: the only row that reaches writerPIDFromLsof at all, so a writerOfVia hard-wired to 0 " +
				"would satisfy every other row here",
		},
		{
			name: "exit 1 — lsof looked and nobody holds it", build: shellCmd("exit 1"),
			wantErr: false,
			why:     "the LOCK: an unwritten transcript is an answer, and (0, nil) is the right one",
		},
		{
			name: "killed by a signal", build: shellCmd("kill -9 $$"),
			wantErr: true,
			why:     "#1537: a killed lsof knows nothing about who holds the transcript",
		},
		{
			name: "binary missing", build: missingCmd("lsof-1537"),
			wantErr: true,
			why:     "a fork/exec failure never reached the question",
		},
	}

	for _, tc := range cases {
		pid, err := writerOfVia("/tmp/transcript-1537.jsonl", tc.build)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: writerOfVia = (%d, %v), wantErr %v — %s", tc.name, pid, err, tc.wantErr, tc.why)
		}
		if pid != tc.wantPID {
			t.Errorf("%s: got pid %d, want %d — %s", tc.name, pid, tc.wantPID, tc.why)
		}
	}
}
