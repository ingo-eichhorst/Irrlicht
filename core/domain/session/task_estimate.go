// task_estimate.go holds the agent-authored task-progress estimate and the
// completion-ETA projection derived from it (issue #558). The agent emits its
// own estimate in-band as a hidden marker in its transcript; irrlicht parses
// it read-only — there is no daemon-side round counting (a "round" is the
// agent's own unit, ≈ a task phase, and tool-call counts don't match it).
package session

import "time"

// TaskEstimate is the agent's self-reported task progress, parsed from the
// most recent in-band marker. Mirrors tailer.TaskEstimate at the same adapter
// boundary that converts Task and RateLimitSnapshot.
type TaskEstimate struct {
	// TotalRounds is the agent's estimate of the task's phases.
	TotalRounds int `json:"total_rounds"`
	// CompletedRounds is how many of those phases it reports finished.
	CompletedRounds int `json:"completed_rounds"`
	// Risk and Confidence are optional passthroughs from the marker.
	Risk       string   `json:"risk,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	// UpdatedAt is the unix-seconds timestamp of the transcript event the
	// marker was last observed in. UIs use it to degrade a stale estimate
	// ("updated 42s ago") instead of letting the ETA drift forever.
	UpdatedAt int64 `json:"updated_at"`
}

// ForecastTaskCompletion projects the wall-clock completion time from the
// agent's self-reported progress. The projection is ANCHORED AT THE MARKER,
// not at the computing pass, so the eta is stable between markers and UIs
// can count the remaining time down in real time (eta drifting into the
// past is fine — clients clamp and present an upper bound).
//
// Rate preference, best first:
//
//  1. Marker deltas within the current task (base = the task's first
//     marker): perRound = (est.UpdatedAt − base.UpdatedAt) /
//     (est.CompletedRounds − base.CompletedRounds). Session elapsed
//     includes previous tasks and idle gaps in multi-task sessions and
//     inflated projections ~2× (and pre-marker time skewed even
//     single-task ETAs long).
//  2. Fallback when no usable base exists (single marker so far):
//     perRound = elapsedAtMarker / CompletedRounds, with the gap since the
//     marker subtracted from elapsedSeconds.
//
// This function is the single seam to swap when the estimation approach
// evolves. Returns nil when no projection is possible: no estimate, no
// reported progress yet, or no usable rate.
func ForecastTaskCompletion(est, base *TaskEstimate, elapsedSeconds int64, now time.Time) *time.Time {
	if est == nil || est.CompletedRounds <= 0 {
		return nil
	}
	remaining := max(est.TotalRounds-est.CompletedRounds, 0)

	var perRound float64
	anchor := now
	if est.UpdatedAt > 0 {
		anchor = time.Unix(est.UpdatedAt, 0)
	}
	switch {
	case base != nil && est.CompletedRounds > base.CompletedRounds && est.UpdatedAt > base.UpdatedAt:
		perRound = float64(est.UpdatedAt-base.UpdatedAt) / float64(est.CompletedRounds-base.CompletedRounds)
	case elapsedSeconds > 0:
		elapsedAtMarker := elapsedSeconds
		if est.UpdatedAt > 0 {
			if gap := now.Unix() - est.UpdatedAt; gap > 0 && gap < elapsedSeconds {
				elapsedAtMarker = elapsedSeconds - gap
			}
		}
		perRound = float64(elapsedAtMarker) / float64(est.CompletedRounds)
	default:
		return nil
	}

	eta := anchor.Add(time.Duration(float64(remaining) * perRound * float64(time.Second)))
	return &eta
}
