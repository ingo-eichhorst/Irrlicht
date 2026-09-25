package services

import (
	"reflect"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// SetProviderRateLimit puts snap on sessionID's Metrics.RateLimit, or clears
// it when snap is nil, for a producer that observes quota outside the
// transcript pipeline — today the Muse account-API sweep (issue #2057,
// MuseAccountSweeper). It reports whether it saved (and broadcast) a change.
//
// It only ever touches a snapshot stamped with provider: a write refuses to
// replace another provider's snapshot, and a clear leaves any other
// provider's snapshot alone. An id that is not in the repo when it loads
// (reaped since the caller listed it) is a no-op. That does not cover a
// delete landing between its Load and Save: PIDManager.deleteSession calls
// repo.Delete without taking the session-state lock (pid_manager.go,
// deleteSession), so that Save can write the row back until the liveness
// sweep reaps the dead PID again. A tombstone check (deletedSessions) is not
// used to close it, because processActivity deliberately leaves a tombstone
// in place for a session revived by a turn-done hook, and that check would
// then block every write to a live session.
//
// A reading that differs from the stored one only in its timestamps is not
// re-saved, so a sweep tick that re-reads a cached poll result neither writes
// nor broadcasts, and the stored SampledAt stays the first observation's.
//
// The load-mutate-save runs under PIDManager.WithSessionStateLock, the lock
// processActivityLocked saves under. That does NOT make it race-free against
// a transcript pass: processActivity loads its copy BEFORE taking the lock
// (session_detector_activity.go, processActivity), so a pass that loaded
// before this write can save its older copy over it. The caller is expected
// to re-apply every tick (MuseAccountSweeper does) so a lost write returns on
// the next one. UpdatedAt is deliberately not touched: a quota reading is not
// session activity.
func (d *SessionDetector) SetProviderRateLimit(sessionID, provider string, snap *session.RateLimitSnapshot) bool {
	if snap != nil && snap.Provider != provider {
		return false
	}
	var saved *session.SessionState
	d.pidMgr.WithSessionStateLock(func() {
		state, err := d.repo.Load(sessionID)
		if err != nil || state == nil {
			return
		}
		var current *session.RateLimitSnapshot
		if state.Metrics != nil {
			current = state.Metrics.RateLimit
		}
		if current != nil && current.Provider != provider {
			return
		}
		switch {
		case snap == nil && current == nil:
			return
		case snap == nil:
			state.Metrics.RateLimit = nil
			state.Metrics.RateLimitForecastEta = nil
		case current != nil && sameRateLimitReading(current, snap):
			return
		default:
			if state.Metrics == nil {
				state.Metrics = &session.SessionMetrics{}
			}
			state.Metrics.RateLimit = snap
		}
		if err := d.repo.Save(state); err != nil {
			d.log.LogError(logComponentSessionDetector, sessionID, "saving provider rate limit: "+err.Error())
			return
		}
		saved = state
	})
	if saved == nil {
		return false
	}
	d.broadcast(outbound.PushTypeUpdated, saved)
	return true
}

// sameRateLimitReading reports whether a and b carry the same reading,
// ignoring the observation timestamps a re-poll always changes.
func sameRateLimitReading(a, b *session.RateLimitSnapshot) bool {
	ac, bc := *a, *b
	ac.SampledAt, bc.SampledAt = 0, 0
	ac.LastSuccessAt, bc.LastSuccessAt = 0, 0
	ac.LastAttemptAt, bc.LastAttemptAt = 0, 0
	return reflect.DeepEqual(ac, bc)
}
