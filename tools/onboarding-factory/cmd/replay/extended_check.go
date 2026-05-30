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

	replayedReal := make([]transition, 0, len(replayed))
	for _, t := range replayed {
		if t.PrevState == "" {
			continue
		}
		replayedReal = append(replayedReal, t)
	}

	check := &extendedCheck{
		SidecarPath:   sidecarPath,
		RecordedCount: len(recorded),
		ReplayedCount: len(replayedReal),
	}

	n := min(len(recorded), len(replayedReal))
	for i := 0; i < n; i++ {
		r := recorded[i]
		p := replayedReal[i]
		if r.PrevState == p.PrevState && r.NewState == p.NewState {
			check.OrderedMatches++
			continue
		}
		check.OrderedMismatches = append(check.OrderedMismatches, transitionMismatch{
			Index:    i,
			Kind:     "state_differs",
			Recorded: r.PrevState + "→" + r.NewState,
			Replayed: p.PrevState + "→" + p.NewState,
		})
	}
	for i := n; i < len(recorded); i++ {
		r := recorded[i]
		check.OrderedMismatches = append(check.OrderedMismatches, transitionMismatch{
			Index:    i,
			Kind:     "missing_in_replay",
			Recorded: r.PrevState + "→" + r.NewState,
		})
	}
	for i := n; i < len(replayedReal); i++ {
		p := replayedReal[i]
		check.OrderedMismatches = append(check.OrderedMismatches, transitionMismatch{
			Index:    i,
			Kind:     "extra_in_replay",
			Replayed: p.PrevState + "→" + p.NewState,
		})
	}

	recordedKinds := uniqueTransitionKinds(recorded, func(e lifecycle.Event) (string, string) { return e.PrevState, e.NewState })
	replayedKinds := uniqueTransitionKinds(replayedReal, func(t transition) (string, string) { return t.PrevState, t.NewState })
	check.RecordedUniqueKinds = sortedKinds(recordedKinds)
	check.ReplayedUniqueKinds = sortedKinds(replayedKinds)
	for k := range recordedKinds {
		if !replayedKinds[k] {
			check.MissingKinds = append(check.MissingKinds, k)
		}
	}
	for k := range replayedKinds {
		if !recordedKinds[k] {
			check.ExtraKinds = append(check.ExtraKinds, k)
		}
	}
	sort.Strings(check.MissingKinds)
	sort.Strings(check.ExtraKinds)

	return check, nil
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
