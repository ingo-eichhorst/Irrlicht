package services

import (
	"fmt"
	"time"

	"irrlicht/core/domain/session"
)

// snapshotFromObservation finishes an account-quota refresh once obs holds
// a value: build parses the body, SampledAt and LastSuccessAt are the
// observation's own FetchedAt (build stamps them from it), LastAttemptAt is
// the poller's last attempt, and a stale observation — a failure after an
// earlier success — returns that reading stamped with RetrievalFailure
// alongside a non-nil error, so a caller can keep showing it without it
// passing for current (issue #2057). err is the Poll error, if any. what
// names the quota in error text. Shared by MuseAccountRefresh and
// ZaiAccountRefresh, each of which keeps its own no-value branch.
func snapshotFromObservation(obs Observation, err error, what string,
	build func([]byte, time.Time) (session.RateLimitSnapshot, error)) (*session.RateLimitSnapshot, error) {
	snap, perr := build(obs.Body, obs.FetchedAt)
	if perr != nil {
		return nil, fmt.Errorf("services: parsing %s: %w", what, perr)
	}
	if !obs.LastAttempt.IsZero() {
		snap.LastAttemptAt = obs.LastAttempt.Unix()
	}
	if obs.Stale {
		snap.RetrievalFailure = obs.FailureReason
		if err == nil {
			err = fmt.Errorf("services: %s is stale: %s", what, obs.FailureReason)
		} else {
			err = fmt.Errorf("services: polling %s (serving the last reading): %w", what, err)
		}
		return &snap, err
	}
	return &snap, nil
}
