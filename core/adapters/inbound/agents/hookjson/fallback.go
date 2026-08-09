package hookjson

// Observability for the splicer's degradations (issue #1371).
//
// jsonc.go rewrites the user's agent config by hand-computed byte arithmetic,
// and it protects itself two ways: an edit it cannot make safely falls back to
// re-encoding the whole document, and an edit it did make is re-decoded and
// compared against the document it claims to represent before being returned.
// Both are the right mitigation. Both are also silent — the file stays valid,
// the install succeeds, and the only thing the user loses is the comments and
// key order this package exists to preserve.
//
// A mitigation nobody can see is a mitigation nobody learns from. Review of
// PR #1381 found four separate ways the arithmetic could corrupt a config, one
// of them firing on ~11% of randomly shaped documents and reachable by an
// ordinary permission toggle; every one was found by a test, because there was
// nothing in the field to find it with. So each degradation is counted.
//
// FallbackVerifyFailed is the one that matters. The other three are designed
// paths — shapes the splicer knowingly declines. A non-zero verify_failed means
// the splicer produced output its own verifier rejected: a bug in jsonc.go, not
// a quirk of the user's file.
//
// This mirrors PathConfiner's rejection counters in confine.go deliberately —
// same package, same problem, same shape: make a refusal countable rather than
// merely returned.

import "sync/atomic"

// FallbackReason names why a document was re-encoded whole instead of spliced.
type FallbackReason string

const (
	// FallbackNone is the zero value: the splice was used.
	FallbackNone FallbackReason = ""
	// FallbackUnsupportedShape — the document, or a container in it, has no
	// in-place edit this package knows how to make.
	FallbackUnsupportedShape FallbackReason = "unsupported_shape"
	// FallbackOverlappingEdits — two edits claimed the same bytes, which means
	// the separator arithmetic disagrees with itself.
	FallbackOverlappingEdits FallbackReason = "overlapping_edits"
	// FallbackVerifyFailed — the spliced bytes did not decode back to the
	// document they were built from. The splice was discarded. This one is a
	// defect in jsonc.go; the others are not.
	FallbackVerifyFailed FallbackReason = "verify_failed"
	// FallbackArrayRewrite — one array was re-encoded rather than edited,
	// because a new element belongs somewhere other than its tail or the array
	// is too large to align. Scoped to that array; the rest of the document is
	// still spliced.
	FallbackArrayRewrite FallbackReason = "array_rewrite"
)

// fallbackReasons is every reason in a fixed order, so each has a stable
// counter slot. FallbackNone is deliberately absent — it is not a fallback.
var fallbackReasons = [...]FallbackReason{
	FallbackUnsupportedShape,
	FallbackOverlappingEdits,
	FallbackVerifyFailed,
	FallbackArrayRewrite,
}

// fallbackCounts holds one counter per reason, indexed by fallbackReasons
// order. Package-level rather than per-instance because the splicer is reached
// through package functions (ReadSettings/WriteSettings are what the adapters
// and the statusline installer share), and atomics rather than a mutex-guarded
// map for the same reason confine.go gives: these exist to observe a burst, so
// instrumentation that serializes exactly when events arrive together would
// blunt what it measures.
var fallbackCounts [len(fallbackReasons)]atomic.Uint64

// countFallback records one degradation and returns the reason, so a call site
// can both count and report in one expression.
func countFallback(reason FallbackReason) FallbackReason {
	for i, r := range fallbackReasons {
		if r == reason {
			fallbackCounts[i].Add(1)
			return reason
		}
	}
	return reason
}

// noSafeSplice counts a whole-document fallback and returns the sentinel that
// routes the caller to encodeDocument.
func noSafeSplice(reason FallbackReason) error {
	countFallback(reason)
	return errNoSafeSplice
}

// Fallbacks returns a snapshot of the per-reason counts, omitting zeros.
// Nothing surfaces these to a UI yet — there is no daemon-wide counter registry
// to publish into — but they make "degraded and counted" an observable fact
// rather than a claim, which is what this package's tests check.
//
// A non-zero FallbackVerifyFailed is the signal worth acting on: it says this
// package built bytes that failed its own verification, and the user silently
// lost the comments and key order the splice existed to keep.
func Fallbacks() map[FallbackReason]uint64 {
	out := make(map[FallbackReason]uint64, len(fallbackReasons))
	for i, reason := range fallbackReasons {
		if n := fallbackCounts[i].Load(); n > 0 {
			out[reason] = n
		}
	}
	return out
}

// FallbackCount is the total across every reason.
func FallbackCount() uint64 {
	var total uint64
	for i := range fallbackReasons {
		total += fallbackCounts[i].Load()
	}
	return total
}
