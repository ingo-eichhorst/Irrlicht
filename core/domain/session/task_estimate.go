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
	// Source is where the estimate came from: "marker" for the agent's
	// in-band self-report, "tasks" when derived from the task list (#604).
	// UIs use it to attribute the estimate in tooltips.
	Source string `json:"source,omitempty"`
}

// MarkerEstimateSource and TasksEstimateSource tag where an estimate came
// from: the agent's own in-band marker or the task-list derivation.
// SubagentEstimateSource (subagent_estimate.go) completes the set.
const (
	MarkerEstimateSource = "marker"
	TasksEstimateSource  = "tasks"
)

// TaskEstimateGraceAge is how long the preferred (more holistic) estimate
// source dominates before a strictly fresher alternative may take over
// (#622). Mirrors the chips' stale-dimming threshold (180s) in the macOS
// and web UIs: once the chip would dim as stale anyway, a live signal
// beats a dimmed one.
const TaskEstimateGraceAge = 180 * time.Second

// TaskRoundPriorSeconds is the corpus-derived median wall-clock duration of one
// agent round (issue #753, measured by tools/eta-research over the local
// transcript corpus). It seeds the projection before any round has completed so
// the ETA chip shows a real number at the very first marker (completed_rounds:0)
// instead of "estimating…", and it is the shrinkage target once rounds land
// (see TaskRoundPriorWeight).
//
// Re-measured for #977 at 240s, up from #753's 70s: agent rounds have gotten
// substantially longer. A stale prior is not cosmetic now that it is also the
// shrinkage target — the harness's `production` row scored ~10m worse at the
// median while this still read 70s. REPORT.md prints the live drift between
// this constant and the corpus median on every run; re-read it when that grows.
const TaskRoundPriorSeconds = 240.0

// TaskRoundPriorWeight is the prior's strength in pseudo-rounds when shrinking
// an observed per-round rate toward it (issue #977). The observed rate from a
// single round is a one-sample estimate and was the biggest source of blown
// forecasts: one unrepresentative first round anchored hours of projection in
// either direction (a fast round projecting two days short, a slow setup-heavy
// round projecting 6h for 15min of work). Blending at weight w means the prior
// dominates until roughly w rounds have been observed, then the measurement
// takes over — swept over the corpus by tools/eta-research.
const TaskRoundPriorWeight = 2.0

// Deliberately absent: a wrap-up allowance at completed==total. #977 proposed
// padding the forecast there, on the theory that most tasks keep verifying and
// opening PRs after reporting their last round. tools/eta-research's last-mile
// section measured it and the padding lost at every idle cutoff tried: the tail
// is bimodal, not centred — most completed episodes stop dead, a minority run
// for the better part of an hour — so zero is the median-optimal prediction and
// any blanket floor is a regression for the majority. What was actually wrong
// at the finish line is presentational, and is fixed there: the UIs render
// "wrapping up" rather than a confident "<1m left" once every round is done.

// FresherTaskEstimate picks the estimate to display when two sources are
// available. preferred is the more holistic signal (the agent's own marker
// over the task-list derivation; the parent's own estimate over a subagent
// aggregate) and wins while fresher than TaskEstimateGraceAge; past that, a
// strictly newer challenger takes over. Either side may be nil. An
// unstamped preferred (UpdatedAt 0) counts as stale immediately and yields
// to any stamped challenger.
func FresherTaskEstimate(preferred, challenger *TaskEstimate, now time.Time) *TaskEstimate {
	if preferred == nil {
		return challenger
	}
	if challenger == nil {
		return preferred
	}
	stale := now.Unix()-preferred.UpdatedAt > int64(TaskEstimateGraceAge/time.Second)
	if stale && challenger.UpdatedAt > preferred.UpdatedAt {
		return challenger
	}
	return preferred
}

// TaskEstimateFromTasks derives a fallback estimate from the session's task
// list (#604): claude ≥2.1.162 drops assistant text blocks followed by
// interleaved thinking from the transcript, so in-band markers rarely
// survive mid-task — but TaskCreate/TaskUpdate tool calls always persist.
// One task ≈ one round. Returns (nil, nil) until a task has completed.
//
// The pair feeds ForecastTaskCompletion unchanged: est.UpdatedAt anchors at
// the latest stamped completion, and base reconstructs the state at the
// FIRST stamped completion so the delta rate spans (latest − first) over
// the completions between them. Tasks completed before stamping existed
// (CompletedAt == 0, e.g. restored from an older ledger) still count toward
// progress but are treated as completed before the first stamp.
func TaskEstimateFromTasks(tasks []Task) (est, base *TaskEstimate) {
	completed, stamped := 0, 0
	var first, latest int64
	for _, t := range tasks {
		if t.Status != "completed" {
			continue
		}
		completed++
		if t.CompletedAt <= 0 {
			continue
		}
		stamped++
		if first == 0 || t.CompletedAt < first {
			first = t.CompletedAt
		}
		if t.CompletedAt > latest {
			latest = t.CompletedAt
		}
	}
	if completed == 0 {
		return nil, nil
	}
	est = &TaskEstimate{
		TotalRounds:     len(tasks),
		CompletedRounds: completed,
		UpdatedAt:       latest,
		Source:          TasksEstimateSource,
	}
	if stamped >= 2 {
		// At the first stamp, every unstamped completion plus that task
		// itself was done: perRound = (latest − first) / (stamped − 1).
		base = &TaskEstimate{
			TotalRounds:     len(tasks),
			CompletedRounds: completed - stamped + 1,
			UpdatedAt:       first,
			Source:          TasksEstimateSource,
		}
	}
	return est, base
}

// ObservedRoundRate measures the per-round wall-clock rate from the agent's own
// markers, best source first. It returns the seconds-per-round and the number
// of round observations behind it; (0, 0) means no rate is measurable yet.
// The observation count is what lets callers weigh a one-round measurement
// differently from a ten-round one (see BlendRoundRate).
//
//  1. Marker deltas within the current task (base = the task's first marker):
//     perRound = (est.UpdatedAt − base.UpdatedAt) / (est.CompletedRounds −
//     base.CompletedRounds). Session elapsed includes previous tasks and idle
//     gaps in multi-task sessions and inflated projections ~2× (and pre-marker
//     time skewed even single-task ETAs long).
//  2. Fallback when no usable base exists (single marker so far):
//     perRound = elapsedAtMarker / CompletedRounds, with the gap since the
//     marker subtracted from elapsedSeconds.
//
// tools/eta-research scores the production forecast through this exact
// function rather than a copy of it, so the harness cannot drift from what
// ships.
func ObservedRoundRate(est, base *TaskEstimate, elapsedSeconds int64, now time.Time) (perRound float64, observations int) {
	if est == nil {
		return 0, 0
	}
	switch {
	case base != nil && est.CompletedRounds > base.CompletedRounds && est.UpdatedAt > base.UpdatedAt:
		n := est.CompletedRounds - base.CompletedRounds
		return float64(est.UpdatedAt-base.UpdatedAt) / float64(n), n
	case elapsedSeconds > 0 && est.CompletedRounds > 0:
		elapsedAtMarker := elapsedSeconds
		if est.UpdatedAt > 0 {
			if gap := now.Unix() - est.UpdatedAt; gap > 0 && gap < elapsedSeconds {
				elapsedAtMarker = elapsedSeconds - gap
			}
		}
		return float64(elapsedAtMarker) / float64(est.CompletedRounds), est.CompletedRounds
	}
	return 0, 0
}

// BlendRoundRate shrinks an observed per-round rate toward the population prior
// in proportion to how little evidence backs it: (w·prior + n·observed)/(w+n),
// with n the observation count from ObservedRoundRate and w the prior's
// strength in pseudo-rounds. At n=0 it is the prior; by n≫w it is the
// measurement. This is what stops one unrepresentative round from anchoring an
// entire forecast (issue #977, failure mode #2). weight is a parameter rather
// than the constant so tools/eta-research can sweep it over the corpus.
func BlendRoundRate(observed float64, observations int, prior, weight float64) float64 {
	if observations <= 0 || observed <= 0 {
		return prior
	}
	n := float64(observations)
	return (weight*prior + n*observed) / (weight + n)
}

// ForecastTaskCompletion projects the wall-clock completion time from the
// agent's self-reported progress. The projection is ANCHORED AT THE MARKER,
// not at the computing pass, so the eta is stable between markers and UIs
// can count the remaining time down in real time (eta drifting into the
// past is fine — clients clamp and present an upper bound).
//
// eta = marker + remainingRounds × perRound, where perRound is the observed
// rate (ObservedRoundRate) shrunk toward the corpus prior (BlendRoundRate), or
// the bare prior when no round has completed yet.
//
//   - The prior seeds the zero-round case (#753) so a real eta appears at the
//     very first marker — total_rounds × prior — instead of "estimating…". The
//     UI widens that range (2×) to signal a population prior, not a measurement.
//   - Shrinking toward the prior afterwards (#977) makes the estimate converge:
//     the first measured round moves the number a little, the fifth moves it a
//     lot, so more information can no longer make the forecast dramatically
//     worse than the round before it.
//   - remaining clamps at 0, so completed==total projects the marker itself.
//     That is the median-optimal prediction (see the TaskWrapUpRounds comment
//     above for why padding it lost); the UIs relabel it rather than pad it.
//
// This function is the single seam to swap when the estimation approach
// evolves, and tools/eta-research scores it directly. Returns nil when there is
// no estimate, or when progress is reported but no rate is measurable (no
// usable base and no elapsed) — the prior stays confined to the zero-round case
// rather than papering over a missing measurement.
func ForecastTaskCompletion(est, base *TaskEstimate, elapsedSeconds int64, now time.Time) *time.Time {
	if est == nil {
		return nil
	}
	observed, observations := ObservedRoundRate(est, base, elapsedSeconds, now)
	var perRound float64
	switch {
	case observations > 0 && observed > 0:
		perRound = BlendRoundRate(observed, observations, TaskRoundPriorSeconds, TaskRoundPriorWeight)
	case est.CompletedRounds == 0:
		perRound = TaskRoundPriorSeconds
	default:
		return nil
	}

	remaining := max(est.TotalRounds-est.CompletedRounds, 0)

	anchor := now
	if est.UpdatedAt > 0 {
		anchor = time.Unix(est.UpdatedAt, 0)
	}
	eta := anchor.Add(time.Duration(float64(remaining) * perRound * float64(time.Second)))
	return &eta
}
