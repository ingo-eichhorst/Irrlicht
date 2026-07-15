package tailer

import (
	"hash/fnv"
	"io"
	"time"
)

// This file owns rotation handling: deciding whether the tailer's resume point
// is still valid (resolveStartPos and the resume fingerprint below), and
// clearing the state that belongs to the previous contents once it isn't
// (resetAccumulatorsForRotation). Its tests live in rotation_reset_test.go.

// resolveStartPos decides where this pass should begin reading, and resets the
// accumulators as a side effect when the resume point turns out to be invalid.
//
// The tailer's resume has exactly one correctness precondition: the bytes below
// lastOffset are still the bytes it read. fileSize < lastOffset was never a
// check of that — only a cheap proxy catching the shrink-shaped subset — so it
// is paired here with a direct check of the precondition (issue #1104).
func (t *TranscriptTailer) resolveStartPos(r io.ReaderAt, fileSize int64) int64 {
	switch {
	case t.lastOffset == 0:
		// First open (or a resume with no persisted offset): read from the top.
		return 0

	case fileSize < t.lastOffset || t.resumePrefixChanged(r):
		// Rotated, truncated, or rewritten in place — the bytes this tailer
		// already consumed are gone or are no longer the bytes it consumed.
		// Reset cumulative accumulators and replay from byte 0 so tokens from
		// the previous contents aren't double-counted.
		//
		// The shrink half also covers Claude Code v2.1.208's transcript prune
		// ("up to 79x smaller" by dropping superseded file-history backups),
		// which rebuilds cumulative totals from the surviving lines only. That
		// is safe because the prune does not touch usage-carrying lines: it
		// targets the separate top-level `file-history-snapshot` event type,
		// whose records carry no `usage`, `requestId`, `uuid` or `parentUuid`
		// at all (0 of 3624 on a real machine) and sit outside the
		// uuid/parentUuid message chain. Each such record repeats the entire
		// cumulative trackedFileBackups map, so earlier ones are wholly
		// superseded — that redundancy is what the prune reclaims. Every one of
		// 109,248 assistant lines across that same corpus still carried
		// message.usage under 2.1.210. Re-verify if a future release starts
		// pruning assistant lines. See issue #1088.
		t.resetAccumulatorsForRotation()
		return 0

	default:
		// Normal incremental path: never skip ahead of the last processed byte.
		return t.lastOffset
	}
}

// resumeFingerprintWindow is how many bytes ending at lastOffset are hashed to
// anchor the resume point — wide enough that any rewrite which SHIFTS content
// disturbs it, small enough to stay a negligible fixed read on a per-write path.
//
// Residual gap: a SIZE-NEUTRAL edit further back than this window leaves the
// window byte-identical, so it still resumes incrementally over stale
// accumulators. Closing it needs a rolling hash of the whole consumed prefix —
// O(filesize) per pass, which this path cannot afford. Left open because every
// rewrite shape observed in-tree either shrinks the file (claudecode's rewind)
// or changes an edited message's length (vibe's _overwrite_messages_sync), and
// both are caught.
const resumeFingerprintWindow = 512

// resumePrefixChanged reports whether the already-consumed bytes ending at
// lastOffset differ from what this tailer actually consumed — i.e. the
// transcript was rewritten in place rather than appended to. It is the half of
// rotation detection that a size check cannot cover: a rewrite whose new size
// is >= lastOffset leaves the size test happy while the offset now points into
// unrelated content, so the tailer would resume mid-record and keep
// accumulating on top of pre-rewrite state (issue #1104).
//
// An append never rewrites bytes below lastOffset, so a matching window means
// a genuine append. Every uncertain case — no anchor yet, a pre-#1104 ledger,
// an unreadable window — reports false, keeping the historical
// resume-at-offset behavior rather than forcing a re-read.
func (t *TranscriptTailer) resumePrefixChanged(r io.ReaderAt) bool {
	if t.resumeFingerprint == 0 {
		return false
	}
	fp := fingerprintEndingAt(r, t.lastOffset)
	return fp != 0 && fp != t.resumeFingerprint
}

// captureResumeFingerprint anchors the rewrite detector at the current
// lastOffset. A window that can't be read leaves the tailer unanchored, which
// resumePrefixChanged reads as "can't tell".
func (t *TranscriptTailer) captureResumeFingerprint(r io.ReaderAt) {
	t.resumeFingerprint = fingerprintEndingAt(r, t.lastOffset)
}

// fingerprintEndingAt hashes the up-to-resumeFingerprintWindow bytes ending at
// off, returning 0 ("unknown") when there is nothing to hash (off <= 0) or the
// read fails. FNV-1a is chosen over a cryptographic hash deliberately: this
// detects an accidental rewrite by a cooperating agent, not a forged collision.
func fingerprintEndingAt(r io.ReaderAt, off int64) uint64 {
	if off <= 0 {
		return 0
	}
	n := min(off, int64(resumeFingerprintWindow))
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, off-n); err != nil {
		return 0
	}
	h := fnv.New64a()
	h.Write(buf) // hash.Hash.Write never returns an error
	return h.Sum64()
}

// resetAccumulatorsForRotation clears every accumulator that belongs to the
// previous transcript file's contents. TailAndProcess calls this when it
// detects that the consumed bytes are gone or changed — the transcript was
// rotated, truncated, or rewritten in place — so replaying from byte 0 doesn't
// double-count tokens, resurrect stale background processes, or misattribute
// the previous file's tasks.
//
// This only resets the TAILER's own accumulators. A parser that tracks its
// own session-scoped state derived from an upstream monotonic counter (e.g.
// Mistral Vibe's token high-water mark, issue #1063) must reset that state
// too, or its next delta computation will be measured against the stale
// pre-rotation value. See the rotationResetter optional interface.
func (t *TranscriptTailer) resetAccumulatorsForRotation() {
	if resetter, ok := t.parser.(rotationResetter); ok {
		resetter.ResetForRotation()
	}
	t.cumInputTokens = 0
	t.cumOutputTokens = 0
	t.cumCacheReadTokens = 0
	t.cumCacheCreationTokens = 0
	t.lastRequestID = ""
	t.pendingSnapshot = nil
	t.cumByModel = make(map[string]*UsageBreakdown)
	t.cumProviderCostUSD = 0
	t.tasks = nil
	t.taskSeq = 0
	t.pendingTaskCreates = make(map[string]string)
	// Open tool calls belong to the prior file. Re-reading from byte 0
	// re-inserts every surviving tool_use by ID (the map is id-keyed, so that
	// much is idempotent), but a call left open in the *pre*-rotation content
	// has no surviving line to re-insert or close it — it would linger
	// forever. sweepOpenToolCallsOnTurnDone only half-heals that: it
	// deliberately preserves the surviveTurnDone names (Agent,
	// AskUserQuestion, ExitPlanMode), which are exactly the user-blocking ones
	// SessionMetrics.NeedsUserAttention reads — so a stale AskUserQuestion
	// pinned the session to `waiting` indefinitely. Same reasoning as
	// openBackgroundProcs below (issue #445). See issue #1088.
	t.openToolCalls = make(map[string]string)
	// Background-process set belongs to the prior file; drop it so a
	// rotated/truncated transcript doesn't keep a stale session `working`.
	// See issue #445.
	t.openBackgroundProcs = make(map[string]string)
	t.pendingBashPolls = make(map[string]string)
	// Drop the pre-rotation idle anchor so the post-scan idleFlusher
	// hook doesn't synthesize a phantom turn_done against stale time.
	t.lastLineSeenAt = time.Time{}
	// The resume anchor describes the previous contents, so it is meaningless
	// now. Clearing it also keeps TailAndProcess's skip-the-re-anchor guard
	// honest: a rotated pass always re-anchors, because unknown forces it to.
	t.resumeFingerprint = 0
}
