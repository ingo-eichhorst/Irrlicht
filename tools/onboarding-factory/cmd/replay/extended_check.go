package main

import (
	"sort"

	"irrlicht/core/application/services"
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
// the "N of the catalog's recordings diverge" headline is counted over.
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
//     spellings agree on every recording it walks, rather than trusting the
//     reasoning.
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

// syntheticReasons is the set of reason strings that mark a transition as
// SYNTHESIZED — emitted to recover an episode the observer would otherwise have
// missed entirely (a collapsed waiting blip, a collapsed turn boundary, a turn
// that had already finished before discovery) rather than decided by the
// classifier ladder against what the transcript said.
//
// It names the exported constants rather than copying their text, so a rename
// is a compile error here. Completeness is the part a reference cannot give:
// a SIXTH synthesizer added to state_classifier.go would leave this set short
// and CrossMechanism silently under-reporting, which is the failure mode where
// "found nothing" and "could not look" produce the same output. That is why
// TestSyntheticReasonsNamesEveryConstant scans that file's own source instead
// of trusting this list — the same move TestAllHookEvents_CoversEveryConstant
// makes for hook events.
var syntheticReasons = map[string]bool{
	services.SyntheticWaitingReason:          true,
	services.SyntheticTurnSettleReason:       true,
	services.SyntheticQueuedTurnStartReason:  true,
	services.SyntheticCatchUpTurnStartReason: true,
	services.SyntheticCatchUpTurnDoneReason:  true,
}

// CrossMechanism reports the one reason-level disagreement that is not a
// mechanism NAME and cannot be produced by renaming a string: exactly one side
// of this pair is a SYNTHESIZED transition and the other is a classifier
// verdict (or the two sides are different synthesizers).
//
// The distinction matters because it is the only reason-level difference that
// survives compareOrdered's own argument. A synthesized transition recovers an
// episode that has no classifier verdict at all, so a pair that puts one
// against the other is describing two different things however well their state
// edges line up — where "transcript activity (ready → working)" against "force
// ready→working on first activity" is one transition under two names, measured
// (see compareOrdered).
//
// It is a REPORT, not a demotion. The pair keeps its timing delta, because
// removing it would silently shrink #1480's population; what the catalog gate
// asserts is that every such pair is already visible to an existing mechanism.
func (d timeDelta) CrossMechanism() bool {
	if d.RecordedReason == d.ReplayedReason {
		return false
	}
	return syntheticReasons[d.RecordedReason] || syntheticReasons[d.ReplayedReason]
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
//
// The REASON strings are carried out of this traversal but are deliberately NOT
// part of the match predicate, and #1707 is where that was measured rather than
// assumed. The tempting rule — "the two sides give different reasons, so they
// are not the same transition" — is false, because a reason names the MECHANISM
// that produced a transition and both mechanisms exist on both sides. The
// catalog's dominant shape is a session's FIRST ready→working, which the daemon
// reaches through the classifier's transcript_activity default while the replay
// reaches it through the force bounce; the same recording then agrees on
// "force" for every LATER ready→working. As of #1707: 49 of 836 kind-matched
// pairs differ on reason, 45 of them that one shape, and the differing
// population sits CLOSER in time than the agreeing one (77.6% within 1ms
// against 19.7%) — i.e. more provably the same transition, not less. Demoting
// them would inject 45 false divergences and delete four real drift
// measurements, three of them pinned by name in knownFirstTransitionDrift.
// Those figures are quoted as of that measurement, not as current counts, the
// same way driftThreshold's histogram is.
//
// What IS worth reporting is the narrow shape a rename cannot produce, and it
// has its own predicate rather than a second reading of these two strings — see
// CrossMechanism.
func compareOrdered(recorded []lifecycle.Event, replayedReal []transition) (matched []timeDelta, mismatches []transitionMismatch) {
	n := min(len(recorded), len(replayedReal))
	matched = make([]timeDelta, 0, n)
	for i := 0; i < n; i++ {
		r, p := recorded[i], replayedReal[i]
		if r.PrevState == p.PrevState && r.NewState == p.NewState {
			matched = append(matched, timeDelta{
				Index:          i,
				Kind:           r.PrevState + "→" + r.NewState,
				Delta:          p.VirtualTime.Sub(r.Timestamp),
				RecordedReason: r.Reason,
				ReplayedReason: p.Reason,
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
