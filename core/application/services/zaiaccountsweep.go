// zaiaccountsweep.go is issue #2010 phase 3: the live trigger for the Z.ai
// quota. It walks the daemon's sessions on a ticker, binds the Z.ai quota to
// each session whose observed route names api.z.ai, and hands the reading to
// the detector — the MuseAccountSweeper shape, with the binding decided by
// route evidence instead of the adapter name.
package services

import (
	"context"
	"maps"
	"time"

	"irrlicht/core/adapters/outbound/zaiaccountapi"
	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// ZaiCredentialReader is the slice of zaiaccountapi.CredentialReader the
// sweep reads a session's key through.
type ZaiCredentialReader interface {
	Read(route *session.RouteObservation) (zaiaccountapi.Credential, error)
}

// ZaiAccountSweeperDeps wires a ZaiAccountSweeper. Granted is bound to the
// Z.ai account-API permission (zaiaccountapi.Name).
type ZaiAccountSweeperDeps struct {
	Sessions    sessionLister
	Writer      ProviderRateLimitWriter
	Poller      *AccountPoller
	Credentials ZaiCredentialReader
	Transport   outbound.AccountQuotaTransport
	Granted     func() bool
	Log         outbound.Logger
	Interval    time.Duration
}

// ZaiAccountSweeper reconciles every top-level Z.ai-routed session's Z.ai
// snapshot with the permission state, once per Interval — level-triggered
// for the same reason as MuseAccountSweeper (a transcript pass can save over
// a write; re-applying each tick restores it).
//
// A session qualifies only by its route: Status observed and the endpoint's
// parsed hostname exactly api.z.ai (zaiaccountapi.IsZaiRoute). The adapter
// name never qualifies a session, and neither does the transcript's model,
// which on this route is the requested Claude model (#2010 probe §2).
//
// The permission is checked before any key is read, and a key is read only
// from the settings file that set the route. A session whose key cannot be
// read gets no Z.ai reading — any it had is cleared — and one log line per
// streak naming the reason (zaiaccountapi.CredentialUnavailableError).
type ZaiAccountSweeper struct {
	deps ZaiAccountSweeperDeps
	// keys is every poll key the previous sweep polled under, so a key no
	// session uses any more is forgotten. failing holds the sessions in a
	// failure streak, logged once. Touched only from the sweep goroutine.
	keys    map[AccountQuotaKey]bool
	failing map[string]bool
	// listFailing marks a session-listing failure streak, logged once.
	listFailing bool
}

// NewZaiAccountSweeper builds a sweeper; Run starts it.
func NewZaiAccountSweeper(deps ZaiAccountSweeperDeps) *ZaiAccountSweeper {
	return &ZaiAccountSweeper{deps: deps, keys: map[AccountQuotaKey]bool{}, failing: map[string]bool{}}
}

// Run sweeps once immediately, then every Interval, until ctx is done.
func (s *ZaiAccountSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.deps.Interval)
	defer ticker.Stop()
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// zaiRouted reports whether st is bound to the Z.ai quota: a top-level
// session whose observed route names api.z.ai.
func zaiRouted(st *session.SessionState) bool {
	return st != nil && st.ParentSessionID == "" && st.Route != nil &&
		st.Route.Status == session.RouteObserved && zaiaccountapi.IsZaiRoute(st.Route.Endpoint)
}

func (s *ZaiAccountSweeper) sweep(ctx context.Context) {
	sessions, err := s.deps.Sessions.ListAll()
	if err != nil {
		if !s.listFailing {
			s.listFailing = true
			s.deps.Log.LogError(logComponentZaiAccountSweep, "", "listing sessions: "+err.Error())
		}
		return
	}
	s.listFailing = false
	prev := s.keys
	s.keys = map[AccountQuotaKey]bool{}
	seen := map[string]bool{}
	for _, st := range sessions {
		if ctx.Err() != nil {
			return
		}
		if !zaiRouted(st) {
			continue
		}
		seen[st.SessionID] = true
		listed := listedProviderSnapshot(st, session.ProviderZai)
		// Read live for every session, immediately before its key read: a
		// fetch can block for the transport timeout, and a revoke that lands
		// meanwhile must stop the next key read, not the next sweep's
		// (#2161 review; TestZaiAccountSweep_RevokeMidSweepStopsTheNextKeyRead
		// was seen red against a once-per-sweep read).
		if !s.deps.Granted() {
			s.clear(st.SessionID, listed)
			continue
		}
		cred, err := s.deps.Credentials.Read(st.Route)
		if err != nil {
			s.logFailure(st.SessionID, err)
			s.clear(st.SessionID, listed)
			continue
		}
		s.keys[ZaiAccountQuotaKey(cred.Fingerprint)] = true
		snap, err := ZaiAccountRefresh(ctx, s.deps.Poller, s.deps.Transport, s.deps.Granted, st.SessionID, cred)
		if err != nil {
			s.logFailure(st.SessionID, err)
		} else {
			delete(s.failing, st.SessionID)
		}
		switch {
		case snap != nil:
			if listed == nil || !sameRateLimitReading(listed, snap) {
				s.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderZai, snap)
			}
		case listed != nil:
			// The poller holds no reading for this key, so whatever the
			// session shows was not fetched with it — the key changed, or the
			// reading predates this daemon's salt. Clear it rather than show
			// it as this key's (#2161 review;
			// TestZaiAccountSweep_KeyChangeNeverKeepsTheOldKeysReading was
			// seen red before this branch existed). A stale reading under the
			// same key is not this case: ZaiAccountRefresh returns it, marked.
			s.clear(st.SessionID, listed)
		}
	}
	for key := range prev {
		if !s.keys[key] {
			s.deps.Poller.Forget(key)
		}
	}
	maps.DeleteFunc(s.failing, func(id string, _ bool) bool { return !seen[id] })
}

// clear removes the session's zai reading, if the listed copy holds one.
func (s *ZaiAccountSweeper) clear(sessionID string, listed *session.RateLimitSnapshot) {
	if listed != nil {
		s.deps.Writer.SetProviderRateLimit(sessionID, session.ProviderZai, nil)
	}
}

func (s *ZaiAccountSweeper) logFailure(sessionID string, err error) {
	if s.failing[sessionID] {
		return
	}
	s.failing[sessionID] = true
	s.deps.Log.LogInfo(logComponentZaiAccountSweep, sessionID, "no current Z.ai quota reading: "+err.Error())
}

const logComponentZaiAccountSweep = "zaiaccountsweep"
