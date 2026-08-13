package main

import (
	"sort"

	"irrlicht/core/domain/lifecycle"
)

// runExtendedCheck compares the replayed state transitions against the sidecar's
// recorded transitions.
func runExtendedCheck(sidecarPath string, replayed []transition) (*extendedCheck, error) {
	all, err := loadAllLifecycleEvents(sidecarPath)
	if err != nil {
		return nil, err
	}

	primaryID := findPrimarySessionID(all)
	recorded := filterStateTransitions(all, primaryID)
	replayedReal := dropInitTransitions(replayed)

	check := &extendedCheck{
		SidecarPath:   sidecarPath,
		RecordedCount: len(recorded),
		ReplayedCount: len(replayedReal),
	}
	check.TimeDeltas, check.OrderedMismatches = compareOrdered(recorded, replayedReal)
	check.OrderedMatches = len(check.TimeDeltas)

	recordedKinds := uniqueTransitionKinds(recorded, func(e lifecycle.Event) (string, string) { return e.PrevState, e.NewState })
	replayedKinds := uniqueTransitionKinds(replayedReal, func(t transition) (string, string) { return t.PrevState, t.NewState })
	check.RecordedUniqueKinds = sortedKinds(recordedKinds)
	check.ReplayedUniqueKinds = sortedKinds(replayedKinds)
	check.MissingKinds, check.ExtraKinds = diffKinds(recordedKinds, replayedKinds)

	return check, nil
}

// Diverges reports whether this recording's replay disagreed with the daemon's
// own log about WHICH transitions happened, or in WHAT ORDER — the population
// every "N of 309 recordings diverge" figure in this package is counted over.
//
// It exists because that headline had no single definition in code. It was
// quoted in three doc comments, in tools/replay-fixtures.sh, and in every
// replay PR body, and was spelled differently at each site that computed it;
// #1478's author counted it with a plausible near-miss and every row of the
// resulting comparison table came out ONE LOW, which was invisible from the
// inside because the table was internally consistent (#1503).
//
// The definition is len(OrderedMismatches) > 0 and nothing else. Two near-miss
// spellings are worth naming, because both look like restatements and neither
// is:
//
//   - "the counts or the kind sets differ" MISSES an equal-length replay whose
//     transitions are the same SET in the wrong ORDER. Exactly one recording in
//     the committed catalog is that shape, which is where the one-low came
//     from; it is pinned by name in issue1503_census_test.go so the trap stays
//     falsifiable rather than described.
//   - "ordered OR kinds", main.go's own former spelling, is not wrong but is
//     redundant: an empty OrderedMismatches forces equal lengths and pairwise
//     equal transitions, hence identical kind SETS, so the extra disjuncts can
//     never fire on a check compareOrdered produced. That is a structural
//     argument, so it is also measured — the catalog census asserts the two
//     spellings agree on all 309 recordings rather than trusting the reasoning.
func (ec *extendedCheck) Diverges() bool { return len(ec.OrderedMismatches) > 0 }

// ReproducesNothing reports the #1342 population: the daemon logged transitions
// and the replay reproduced none, so the recording's golden asserts nothing.
//
// Named beside Diverges for the same reason it is: the count is quoted in doc
// comments and pasted into PR tables, and a predicate that lives only inline in
// one test's switch statement is one a second counter re-invents slightly
// differently.
func (ec *extendedCheck) ReproducesNothing() bool {
	return ec.RecordedCount > 0 && ec.ReplayedCount == 0
}

// Fabricates reports the opposite failure, and the worse one: the daemon logged
// nothing and the replay invented transitions anyway, so the golden asserts
// something FALSE rather than nothing.
func (ec *extendedCheck) Fabricates() bool {
	return ec.RecordedCount == 0 && ec.ReplayedCount > 0
}

// dropInitTransitions filters out the synthetic initial-state row (empty
// PrevState) that replay always emits first but the sidecar never records.
func dropInitTransitions(replayed []transition) []transition {
	out := make([]transition, 0, len(replayed))
	for _, t := range replayed {
		if t.PrevState == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// compareOrdered walks recorded and replayed transitions index-by-index up to
// the shorter slice's length, then reports the longer slice's tail as
// missing/extra.
//
// It returns the MATCHED pairs rather than a count of them, each carrying how
// far apart in time the two sides fired (#1480). The count callers used to get
// is len(matched). Those were two functions until the timing measurement's
// first draft, which rode this pairing from a second, identical loop and
// documented the coupling in three prose comments plus a runtime assertion that
// len(TimeDeltas) == OrderedMatches. One loop makes that equality hold by
// construction, so there is nothing left to remember or to assert: a pairing
// change cannot move the ordered figure and the timing figure apart, because
// they are now the same traversal.
//
// The timing of an UNMATCHED pair is deliberately not reported. The two sides
// are not the same transition there — that is what state_differs means — so
// subtracting their timestamps yields a number with no meaning, and counting it
// would report one sequence divergence twice.
func compareOrdered(recorded []lifecycle.Event, replayedReal []transition) (matched []timeDelta, mismatches []transitionMismatch) {
	n := min(len(recorded), len(replayedReal))
	matched = make([]timeDelta, 0, n)
	for i := 0; i < n; i++ {
		r, p := recorded[i], replayedReal[i]
		if r.PrevState == p.PrevState && r.NewState == p.NewState {
			matched = append(matched, timeDelta{
				Index: i,
				Kind:  r.PrevState + "→" + r.NewState,
				Delta: p.VirtualTime.Sub(r.Timestamp),
			})
			continue
		}
		mismatches = append(mismatches, transitionMismatch{
			Index:    i,
			Kind:     "state_differs",
			Recorded: r.PrevState + "→" + r.NewState,
			Replayed: p.PrevState + "→" + p.NewState,
		})
	}
	for i := n; i < len(recorded); i++ {
		r := recorded[i]
		mismatches = append(mismatches, transitionMismatch{
			Index:    i,
			Kind:     "missing_in_replay",
			Recorded: r.PrevState + "→" + r.NewState,
		})
	}
	for i := n; i < len(replayedReal); i++ {
		p := replayedReal[i]
		mismatches = append(mismatches, transitionMismatch{
			Index:    i,
			Kind:     "extra_in_replay",
			Replayed: p.PrevState + "→" + p.NewState,
		})
	}
	return matched, mismatches
}

// diffKinds returns the "prev→new" kind strings present in recordedKinds but
// not replayedKinds (missing) and vice versa (extra), each sorted.
func diffKinds(recordedKinds, replayedKinds map[string]bool) (missing, extra []string) {
	for k := range recordedKinds {
		if !replayedKinds[k] {
			missing = append(missing, k)
		}
	}
	for k := range replayedKinds {
		if !recordedKinds[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// uniqueTransitionKinds returns the set of "prev→new" strings in a slice.
func uniqueTransitionKinds[T any](items []T, fields func(T) (prev, next string)) map[string]bool {
	out := make(map[string]bool)
	for _, it := range items {
		prev, next := fields(it)
		out[prev+"→"+next] = true
	}
	return out
}

func sortedKinds(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
