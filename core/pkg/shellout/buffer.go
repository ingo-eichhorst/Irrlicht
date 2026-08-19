package shellout

import "time"

// WaitDelay bounds the extra time a bounded shellout waits for its child's
// output pipes to close after the context expires.
//
// Killing the process is not enough on its own: exec waits for stdout to reach
// EOF, and a grandchild that inherited the pipe — a credential helper, a
// core.fsmonitor daemon, a pager some global config forced on — holds it open
// after its parent dies. This is the difference between "fails fast" as an
// intention and as a guarantee.
//
// Unlike the CEILING, which is per-package and argued for as such on Answered
// (a consent-path version check and a `git log --all` are entitled to
// different budgets), this value is a property of os/exec's pipe handling
// rather than of the workload, so there is one. It is shared rather than
// copied because the copies had already started naming each other: #1543's
// first draft carried a `gitWaitDelay` whose doc comment ended "Same
// reasoning, same value, as core/pkg/cliprobe's waitDelay", and a comment that
// names its own duplicate is the drift signal, not the documentation.
//
// A measured consequence worth knowing before changing it: WaitDelay does
// NOTHING for a command built with exec.Command rather than exec.CommandContext
// — with no context there is nothing to expire, so the call blocks for the
// child's whole life. Measured, go1.25 darwin/arm64, against a child that exits
// promptly while a grandchild holds stdout: with WaitDelay the call returns in
// 500ms with "exec: WaitDelay expired before I/O complete", which Answered
// correctly reports as a NON-answer; without it the call returns after 30s with
// err == nil, which Answered correctly reports as an ANSWER — so the absence is
// not merely a hang, it publishes whatever partial output was collected as a
// complete successful read.
const WaitDelay = 500 * time.Millisecond

// CappedBuffer collects at most Limit bytes of a child's output and records
// whether anything was DROPPED.
//
// It exists because a ceiling bounds how long a misbehaving child runs, not how
// much it writes in that time — a process streaming to a pipe moves gigabytes
// in a few seconds, and these run inside a long-lived daemon.
//
// It never returns an error. exec's output-copying goroutine treats a write
// error as fatal to the command, which would turn "the output was too big" into
// a second, less specific failure; and returning short would make io.Copy report
// ErrShortWrite for a buffer that is behaving exactly as designed. Callers read
// Overflowed instead.
//
// **What to DO about an overflow is per-call-site and deliberately not decided
// here**, the same split Answered draws. core/pkg/cliprobe ignores Overflowed
// and keeps the prefix, because a truncated version banner still contains the
// version. core/adapters/outbound/git treats it as a NON-ANSWER, because a
// truncated commit list silently drops releases and reverts and DORA would then
// publish a smaller sample as though it were the whole one.
type CappedBuffer struct {
	// Limit is the maximum number of bytes retained. Set it before the first
	// Write; a zero Limit keeps nothing and reports every non-empty write as
	// an overflow.
	Limit int

	buf        []byte
	overflowed bool
}

// Write keeps as much of p as Limit allows and reports the full length so the
// caller's copy loop is never told it under-wrote.
//
// overflowed is set when bytes are DROPPED, which is the condition callers
// care about — not when the buffer merely REACHES Limit. Those are different:
// a complete read of exactly Limit bytes lost nothing, and reporting it as
// truncated is a false non-answer that blanks a chart the daemon read
// successfully. Keying on `len(buf) >= Limit` conflates them, and #1543's first
// draft did; keying on the write that actually drops is what makes the two
// inexpressible as one.
func (w *CappedBuffer) Write(p []byte) (int, error) {
	if room := w.Limit - len(w.buf); len(p) > room {
		if room > 0 {
			w.buf = append(w.buf, p[:room]...)
		}
		w.overflowed = true
	} else {
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

// Bytes returns the retained prefix. It is not a copy.
func (w *CappedBuffer) Bytes() []byte { return w.buf }

// String returns the retained prefix as a string.
func (w *CappedBuffer) String() string { return string(w.buf) }

// Overflowed reports whether any byte was dropped.
func (w *CappedBuffer) Overflowed() bool { return w.overflowed }
