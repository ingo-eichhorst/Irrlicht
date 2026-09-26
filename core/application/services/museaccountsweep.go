// museaccountsweep.go is issue #2057: the live trigger #2007 left out. It
// walks the daemon's live Muse sessions on a ticker, polls Meta's account
// API for each through MuseAccountRefresh, and hands the reading to the
// detector so the quota chip reaches the websocket clients.
package services

import (
	"context"
	"time"

	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// ProviderRateLimitWriter is the slice of SessionDetector the sweep writes
// through (SessionDetector.SetProviderRateLimit).
type ProviderRateLimitWriter interface {
	SetProviderRateLimit(sessionID, provider string, snap *session.RateLimitSnapshot) bool
}

// MuseAccountSweeperDeps wires a MuseAccountSweeper. Poller, Resolver and
// Transport are the ones museAccountAPIEffects builds for the permission —
// Resolver is the daemon's one GrantCredentialCache, which its Apply/Remove
// closures reset and AccountPoller tells about rejections;
// Granted is bound to the Muse account-API permission; Adapter is the Muse
// adapter's name (passed in because this package must not import an inbound
// adapter).
type MuseAccountSweeperDeps struct {
	Sessions  sessionLister
	Writer    ProviderRateLimitWriter
	Poller    *AccountPoller
	Resolver  outbound.CredentialResolver
	Transport outbound.AccountQuotaTransport
	Granted   func() bool
	Adapter   string
	Log       outbound.Logger
	Interval  time.Duration
}

// MuseAccountSweeper reconciles every live top-level Muse session's Meta
// quota snapshot with the permission state, once per Interval.
//
// It is level-triggered rather than edge-triggered: each tick writes the
// current reading while the permission is granted and clears any meta
// snapshot while it is not. SetProviderRateLimit's own doc comment names the
// race that makes this necessary — a transcript pass that loaded its copy
// before a write can save over it — and re-applying every tick is what brings
// a lost write (or a resurrected snapshot after a revoke) back within one
// Interval. A tick costs no extra account-API call while the poller's cached
// reading is fresh (AccountPoller.pollFreshWindow), and an unchanged reading
// is not re-saved or re-broadcast.
//
// Polls run serially on the sweep's own goroutine, never on the event or
// HTTP path. Each session has its own poller key, so every session's fetch
// resolves the credential; Resolver (a GrantCredentialCache) serves all of
// them from one read per grant, because each read on the Keychain route
// raises a macOS dialog (issue #2062).
type MuseAccountSweeper struct {
	deps MuseAccountSweeperDeps
	// polled is every session this sweeper has polled and not yet seen end,
	// so its poller entry can be forgotten once it does. failing holds the
	// sessions in a failure streak, so a streak is logged once however its
	// error text varies between fetching and backoff ticks. listFailing does
	// the same for the session listing. All three are touched only from the
	// sweep goroutine.
	polled      map[string]bool
	failing     map[string]bool
	listFailing bool
}

// NewMuseAccountSweeper builds a sweeper; Run starts it.
func NewMuseAccountSweeper(deps MuseAccountSweeperDeps) *MuseAccountSweeper {
	return &MuseAccountSweeper{deps: deps, polled: map[string]bool{}, failing: map[string]bool{}}
}

// Run sweeps once immediately, then every Interval, until ctx is done.
func (s *MuseAccountSweeper) Run(ctx context.Context) {
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

func (s *MuseAccountSweeper) sweep(ctx context.Context) {
	sessions, err := s.deps.Sessions.ListAll()
	if err != nil {
		if !s.listFailing {
			s.listFailing = true
			s.deps.Log.LogError(logComponentMuseAccountSweep, "", "listing sessions: "+err.Error())
		}
		return
	}
	s.listFailing = false
	granted := s.deps.Granted()
	seen := make(map[string]bool, len(sessions))
	for _, st := range sessions {
		if ctx.Err() != nil {
			return
		}
		if st == nil || st.Adapter != s.deps.Adapter || st.ParentSessionID != "" {
			continue
		}
		seen[st.SessionID] = true
		// The listed copy is at most the repo cache's TTL old, well inside
		// one Interval, so it is enough to skip a write that would change
		// nothing; SetProviderRateLimit re-checks against the stored row.
		listed := listedMetaSnapshot(st)
		if !granted {
			if listed != nil {
				s.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMeta, nil)
			}
			continue
		}
		s.polled[st.SessionID] = true
		// On a failure after an earlier success, snap is that reading stamped
		// with RetrievalFailure (see MuseAccountRefresh); with no earlier
		// success it is nil and nothing is written.
		snap, err := MuseAccountRefresh(ctx, s.deps.Poller, s.deps.Resolver, s.deps.Transport,
			s.deps.Granted, st.SessionID)
		if err != nil {
			s.logFailure(st.SessionID, err)
		} else {
			delete(s.failing, st.SessionID)
		}
		if snap != nil && (listed == nil || !sameRateLimitReading(listed, snap)) {
			s.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMeta, snap)
		}
	}
	for id := range s.polled {
		if !seen[id] {
			s.deps.Poller.Forget(MuseAccountQuotaKey(id))
			delete(s.polled, id)
			delete(s.failing, id)
		}
	}
}

// listedMetaSnapshot returns st's rate-limit snapshot when it is a meta one.
func listedMetaSnapshot(st *session.SessionState) *session.RateLimitSnapshot {
	if st.Metrics == nil || st.Metrics.RateLimit == nil || st.Metrics.RateLimit.Provider != session.ProviderMeta {
		return nil
	}
	return st.Metrics.RateLimit
}

func (s *MuseAccountSweeper) logFailure(sessionID string, err error) {
	if s.failing[sessionID] {
		return
	}
	s.failing[sessionID] = true
	s.deps.Log.LogInfo(logComponentMuseAccountSweep, sessionID, "no current Meta quota reading: "+err.Error())
}

const logComponentMuseAccountSweep = "museaccountsweep"
