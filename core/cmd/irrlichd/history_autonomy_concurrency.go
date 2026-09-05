package main

import (
	"sort"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Autonomy concurrency (#1905) — how many runs a project had working AT THE
// SAME TIME, derived from the span log BY OVERLAP.
//
// FROM THE SPANS, NOT FROM outbound.ConcurrencyReader, and the choice is not
// stylistic. That reader is fed by the lifecycle recordings, which are opt-in
// (--record / IRRLICHT_RECORD=1); a machine with no `recordings` directory has a
// blank Agents chart and a complete span log at the same moment. A concurrency
// figure taken from the recordings would therefore be missing on exactly the
// installs where every other figure in this section is present, and a missing
// number that renders as zero is indistinguishable from a measured one.
//
// WHAT THE NUMBER MEANS, and the caveat both clients print beside it: the daemon
// deliberately holds a PARENT session in `working` while its subagents run, so
// one agent with three subagents overlaps as four. Four things really were
// working — the number is not wrong — but they are not four independent agents,
// which is why the split into `3 + 2 sub` is shown wherever the data supports it
// rather than left for the reader to guess at.

// autonomyConcurrency is one peak reading: how many runs were working at the
// same instant, and — when every one of them said which kind it was — how that
// number splits between top-level sessions and subagents.
type autonomyConcurrency struct {
	peak     int
	topLevel int
	subagent int

	// splitKnown reports that topLevel + subagent == peak is a DERIVATION rather
	// than a guess: every run alive at the peak instant carried a kind this
	// build recognizes.
	//
	// False whenever one of them did not. Most rows written before 18 Aug 2026
	// carry session.AutonomyKindUnknown — they predate the classification, or
	// the back-fill rebuilt them from a source with no parent information — and
	// there is no way to recover which they were. A client shows the total alone
	// there. Splitting anyway would mean inventing the answer for the half of
	// the history that cannot answer.
	splitKnown bool
}

// autonomyEvent is one endpoint of one run on the sweep line.
type autonomyEvent struct {
	ts    int64
	delta int
	kind  string
}

// autonomyEventsOf builds the sorted endpoint list for one project's runs.
//
// HALF-OPEN INTERVALS. A run occupies [start, end): ends are applied before
// starts at the same instant, so a run that ends exactly as another begins is a
// handover and not an overlap. Getting that backwards would report a lone
// session working through a chain of turns as two agents at once.
//
// A run of no length is SKIPPED rather than given an instant of its own. It
// cannot overlap anything for a positive stretch of time, and an interval whose
// start and end coincide would otherwise be closed before it opened — driving
// the running count to -1 under the ordering above.
func autonomyEventsOf(spans []outbound.AutonomySpan) []autonomyEvent {
	out := make([]autonomyEvent, 0, len(spans)*2)
	for _, s := range spans {
		if s.End <= s.Start {
			continue
		}
		kind := session.AutonomyKindOrUnknown(s.Kind)
		out = append(out, autonomyEvent{ts: s.Start, delta: 1, kind: kind})
		out = append(out, autonomyEvent{ts: s.End, delta: -1, kind: kind})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ts != out[j].ts {
			return out[i].ts < out[j].ts
		}
		// -1 before +1 at the same instant: see the half-open rule above.
		return out[i].delta < out[j].delta
	})
	return out
}

// autonomySweep is the running count as the line advances, kept per kind so the
// composition at a peak can be read off without a second pass.
type autonomySweep struct {
	topLevel int
	subagent int
	unknown  int
}

func (s *autonomySweep) apply(e autonomyEvent) {
	switch e.kind {
	case session.AutonomyKindTopLevel:
		s.topLevel += e.delta
	case session.AutonomyKindSubagent:
		s.subagent += e.delta
	default:
		s.unknown += e.delta
	}
}

func (s autonomySweep) total() int { return s.topLevel + s.subagent + s.unknown }

// reading snapshots the sweep as a peak. splitKnown requires BOTH that nothing
// unclassified is alive and that something is: a "0 (0 + 0 sub)" is not a split
// anyone asked for.
func (s autonomySweep) reading() autonomyConcurrency {
	total := s.total()
	return autonomyConcurrency{
		peak:       total,
		topLevel:   s.topLevel,
		subagent:   s.subagent,
		splitKnown: total > 0 && s.unknown == 0,
	}
}

// autonomyPeaks returns the peak concurrency in each of n buckets of
// bucketSeconds starting at start.
//
// PEAK PER BUCKET, never an average: peak answers "how wide did I go", and an
// average over a day is dominated by the hours in which nothing ran at all.
//
// One sort and one linear pass over the whole bucket row, so the cost is
// O(R log R) per project for R runs — the sort — and not O(buckets × runs). Each
// bucket's peak is the greater of what was already running when the bucket
// opened (the carry-in) and every count reached at an endpoint inside it.
func autonomyPeaks(spans []outbound.AutonomySpan, start, bucketSeconds int64, n int) []autonomyConcurrency {
	out := make([]autonomyConcurrency, n)
	if n <= 0 || bucketSeconds <= 0 {
		return out
	}
	events := autonomyEventsOf(spans)
	var sweep autonomySweep
	ei := 0
	for i := 0; i < n; i++ {
		bucketStart := start + int64(i)*bucketSeconds
		bucketEnd := bucketStart + bucketSeconds
		// Everything that happened strictly before this bucket is carry-in: a
		// run that started last week and is still going counts here, which is
		// the whole reason the sweep is not restarted per bucket.
		for ei < len(events) && events[ei].ts < bucketStart {
			sweep.apply(events[ei])
			ei++
		}
		best := sweep.reading()
		for ei < len(events) && events[ei].ts < bucketEnd {
			sweep.apply(events[ei])
			ei++
			if r := sweep.reading(); r.peak > best.peak {
				best = r
			}
		}
		out[i] = best
	}
	return out
}

// autonomyWindowPeak is the peak concurrency across one whole window — the
// figure a panel header states as "N at once".
//
// The window as a SINGLE BUCKET, through the same sweep the per-bucket row uses,
// so the header can never report a number the bars below it contradict. It is
// also why the figure is bounded by the window: two runs that overlapped only
// before `start` are not a peak inside it.
func autonomyWindowPeak(spans []outbound.AutonomySpan, start, end int64) autonomyConcurrency {
	if end <= start {
		return autonomyConcurrency{}
	}
	return autonomyPeaks(spans, start, end-start, 1)[0]
}
