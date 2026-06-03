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
// agent's self-reported progress, calibrated by the real elapsed time the
// daemon already tracks:
//
//	perRound  = elapsedSeconds / CompletedRounds   (measured rate)
//	eta       = now + (TotalRounds − CompletedRounds) × perRound
//
// elapsedSeconds is since session start — a session that ran a while before
// its first marker skews early ETAs long. That is a documented v1 decision
// (acceptable for a rough chip); this function is the single seam to swap
// when the estimation approach evolves. Returns nil when no projection is
// possible: no estimate, no reported progress yet, or no elapsed time.
func ForecastTaskCompletion(est *TaskEstimate, elapsedSeconds int64, now time.Time) *time.Time {
	if est == nil || est.CompletedRounds <= 0 || elapsedSeconds <= 0 {
		return nil
	}
	perRound := float64(elapsedSeconds) / float64(est.CompletedRounds)
	remaining := max(est.TotalRounds-est.CompletedRounds, 0)
	eta := now.Add(time.Duration(float64(remaining) * perRound * float64(time.Second)))
	return &eta
}
