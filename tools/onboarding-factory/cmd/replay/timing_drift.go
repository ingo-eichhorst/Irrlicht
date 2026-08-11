package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"irrlicht/core/domain/lifecycle"
)

// Issue #1480: replay goldens record a virtual_time on every transition and
// nothing compared it to anything. compareOrdered walks prev_state/new_state
// index-by-index and never reads the time; assertReproducesRecordedTransitions
// checks counts and kind-sets; the "N of 309 recordings diverge" headline every
// replay PR quotes is counts and kinds only. So a transition reproduced at the
// right position in the ORDER but 31 seconds from when the daemon made it was a
// full pass on every gate in this package, and the golden then pinned it as
// correct.
//
// This file is the measurement. It is deliberately NOT a tolerance gate — see
// driftThreshold below for why the distribution had to be printed first.

// timeDelta is one paired transition's timing divergence: how far the replay's
// virtual_time sits from the ts the daemon logged for the SAME transition.
//
// The sign is load-bearing and is stated here once so no caller has to
// rediscover it: NEGATIVE means the replay fired EARLY — ahead of the daemon —
// which is the direction readBoundaryFor's known cost pushes (it widens the
// read boundary by the next stat's SIZE, never its TIME, so it classifies
// against bytes that did not exist yet). POSITIVE means the replay fired LATE,
// which is what a synthesized debounce-deadline stamp does when the daemon's
// real timer fired earlier than the replay's reconstruction of it.
type timeDelta struct {
	Index int
	Kind  string
	Delta time.Duration
}

// Abs is |Delta|, the quantity every bucket and percentile below is taken over.
// Magnitude rather than signed value because the two directions have different
// causes but the same cost: a golden pinning a time the daemon never produced.
func (d timeDelta) Abs() time.Duration {
	if d.Delta < 0 {
		return -d.Delta
	}
	return d.Delta
}

// driftThreshold is the boundary this package calls "drifted".
//
// It is chosen from the measured distribution rather than picked, because the
// issue is explicit that a tolerance cannot be chosen for a distribution nobody
// has printed. Measured over all 818 kind-matched paired transitions in the
// committed catalog, |delta| is sharply bimodal, and the two modes are
// separated by a near-empty decade:
//
//	<1ms       162   19.8%   ┐
//	1-10ms     324   39.6%   ├ 70.0% under 100ms — the daemon's own read latency
//	10-100ms    87   10.6%   ┘
//	0.1-1s      10    1.2%   ← the trough: ONE HUNDREDTH of the population
//	1-5s       119   14.5%   ┐
//	5-10s       39    4.8%   │
//	10-30s      58    7.1%   ├ 28.7% above 1s — a different phenomenon entirely
//	30-60s      13    1.6%   │
//	>60s         6    0.7%   ┘
//
// p50 3.2ms, p75 2.0s, p90 8.8s, p95 20.5s, p99 43.3s, max 1m54.9s. The p75
// sitting at almost exactly 2s is not a coincidence and is worth knowing
// before reading any of the above: it is one debounce window, the synthesized
// deadline stamp a coalesced flush carries.
//
// 1s sits in that trough. It is not a claim that 999ms is acceptable — it is
// the observation that essentially nothing lands between 100ms and 1s, so any
// cut in that decade partitions the same two populations and the exact value
// cannot change the verdict for more than ~1% of transitions. A threshold
// anywhere inside the dense regions WOULD be a guess; this one is not.
const driftThreshold = time.Second

// driftBucketEdges/driftBucketLabels are the histogram the report prints. The
// edges are decade-ish rather than uniform because the population spans five
// orders of magnitude (sub-millisecond to ~15 minutes) and a linear histogram
// over that range is a single spike with a flat tail — which is exactly the
// shape that let this cost stay invisible.
var driftBucketEdges = []time.Duration{
	time.Millisecond,
	10 * time.Millisecond,
	100 * time.Millisecond,
	time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
}

var driftBucketLabels = []string{
	"<1ms", "1-10ms", "10-100ms", "0.1-1s", "1-5s", "5-10s", "10-30s", "30-60s", ">60s",
}

// transitionTimeDeltas measures, per paired transition, how far the replay's
// virtual_time sits from the timestamp the daemon logged for it.
//
// It pairs EXACTLY as compareOrdered does — index by index, up to the shorter
// slice — and that is a requirement rather than a convenience. A timing figure
// computed over a different pairing would describe a different set of
// transitions than the ordered-divergence figure it is reported beside, and the
// two would disagree in ways no reader could attribute. If compareOrdered's
// pairing ever changes, this must change with it.
//
// Only KIND-MATCHED pairs are measured, and the exclusion is the substantive
// decision in this function. Where compareOrdered reports state_differs, the
// two sides are not the same transition — subtracting their timestamps yields a
// number with no meaning, and counting it as timing drift would report one
// sequence divergence twice, once in ordered_mismatches and again here. The
// catalog measurement was run both ways while this was written: including
// unmatched pairs adds 80 of 898 pairs (8.9%) and leaves the #1476 enumeration
// below bit-for-bit identical, so the choice costs nothing and buys a figure
// that means one thing.
//
// The recorded side must already be filtered by filterStateTransitions (primary
// session, non-empty prev_state) and the replayed side by dropInitTransitions,
// same as compareOrdered's inputs — the synthetic init row has no counterpart
// in the daemon's log and would pair index 0 against the wrong transition.
func transitionTimeDeltas(recorded []lifecycle.Event, replayedReal []transition) []timeDelta {
	n := min(len(recorded), len(replayedReal))
	out := make([]timeDelta, 0, n)
	for i := 0; i < n; i++ {
		r, p := recorded[i], replayedReal[i]
		if r.PrevState != p.PrevState || r.NewState != p.NewState {
			continue
		}
		out = append(out, timeDelta{
			Index: i,
			Kind:  r.PrevState + "→" + r.NewState,
			Delta: p.VirtualTime.Sub(r.Timestamp),
		})
	}
	return out
}

// firstDrift reports whether the FIRST kind-matched pair — pair 0, the first
// transition the replay and the daemon agree happened — is further than
// driftThreshold from the daemon's own timestamp.
//
// It tests pair 0 and nothing else, which is the whole point and is easy to get
// wrong: scanning for "the first pair that drifts" is a different predicate
// (it answers "does ANY pair drift", just wearing the word first) and measures
// 119 recordings where this one measures 65. Both quantities are useful and the
// catalog test carries both — this one as the named enumeration, the other as
// the aggregate ratchet — but conflating them costs the named list its meaning.
//
// Pair 0 specifically, because that is the transition a READ BOUNDARY decides:
// it is the figure readBoundaryFor moves, and the figure #1476 reported when it
// wrote "20 goldens now pin a FIRST transition more than 1s ahead of their own
// events.jsonl". Checking this predicate against the goldens as they stood at
// #1476's parent commit reproduces that PR's 20 exactly, worst case included
// (mistral-vibe/2-12_context-compaction, -30.976s), which is the evidence that
// this is the predicate #1476 meant.
func firstDrift(deltas []timeDelta) (timeDelta, bool) {
	if len(deltas) == 0 {
		return timeDelta{}, false
	}
	if deltas[0].Abs() <= driftThreshold {
		return timeDelta{}, false
	}
	return deltas[0], true
}

// worstDrift returns the pair with the largest |delta|, or false when there are
// no kind-matched pairs at all.
func worstDrift(deltas []timeDelta) (timeDelta, bool) {
	var worst timeDelta
	var found bool
	for _, d := range deltas {
		if !found || d.Abs() > worst.Abs() {
			worst, found = d, true
		}
	}
	return worst, found
}

// driftDistribution is the aggregate printed by the catalog measurement.
type driftDistribution struct {
	N           int
	BucketCount []int
	Percentiles map[int]time.Duration
}

// newDriftDistribution buckets and ranks |delta| over every kind-matched pair
// handed to it.
func newDriftDistribution(deltas []timeDelta) driftDistribution {
	abs := make([]time.Duration, 0, len(deltas))
	for _, d := range deltas {
		abs = append(abs, d.Abs())
	}
	sort.Slice(abs, func(i, j int) bool { return abs[i] < abs[j] })

	dist := driftDistribution{
		N:           len(abs),
		BucketCount: make([]int, len(driftBucketLabels)),
		Percentiles: map[int]time.Duration{},
	}
	for _, a := range abs {
		dist.BucketCount[bucketIndex(a)]++
	}
	for _, p := range []int{50, 75, 90, 95, 99, 100} {
		dist.Percentiles[p] = percentile(abs, p)
	}
	return dist
}

// bucketIndex maps a magnitude onto driftBucketLabels. Edges are upper-
// exclusive, so a delta of exactly 1s lands in "1-5s" rather than "0.1-1s" —
// consistent with driftThreshold, which treats "drifted" as strictly greater.
func bucketIndex(d time.Duration) int {
	for i, edge := range driftBucketEdges {
		if d < edge {
			return i
		}
	}
	return len(driftBucketLabels) - 1
}

// percentile returns the p-th percentile of a pre-sorted ascending slice by
// linear interpolation between ranks. Returns 0 for an empty slice rather than
// panicking: a catalog with no kind-matched pairs is a vacuity failure the
// caller reports with a far better message than an index-out-of-range.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := float64(len(sorted)-1) * float64(p) / 100.0
	lo := int(pos)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + time.Duration(float64(sorted[hi]-sorted[lo])*frac)
}

// String renders the distribution as the block the catalog measurement prints.
func (d driftDistribution) String() string {
	if d.N == 0 {
		return "no kind-matched transition pairs — the measurement is vacuous"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "|delta| over %d kind-matched paired transitions\n", d.N)
	for _, p := range []int{50, 75, 90, 95, 99, 100} {
		fmt.Fprintf(&b, "  p%-3d %12s\n", p, d.Percentiles[p])
	}
	b.WriteString("  ---- histogram ----\n")
	for i, label := range driftBucketLabels {
		c := d.BucketCount[i]
		fmt.Fprintf(&b, "  %-9s %5d  %5.1f%%\n", label, c, 100*float64(c)/float64(d.N))
	}
	return b.String()
}

// driftSummary is the one-line per-recording figure printSummary appends to the
// existing [extended-check: ...] line, so the sweep reports timing beside the
// divergence figure it already reports.
func driftSummary(deltas []timeDelta) string {
	if len(deltas) == 0 {
		return "timing n/a"
	}
	worst, _ := worstDrift(deltas)
	return fmt.Sprintf("timing %d pairs worst %+.3fs@%d", len(deltas), worst.Delta.Seconds(), worst.Index)
}
