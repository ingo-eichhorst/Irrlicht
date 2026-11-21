// mistralvibesweep.go is issue #2012's live trigger: it binds the Mistral
// Vibe plan reading to every live vibe session while the browser-session
// permission is granted, and unbinds it when it is not.
package services

import (
	"context"
	"time"

	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// MistralVibeSweeperDeps wires a MistralVibeSweeper. Browser resolves the
// current Chrome profile (mistralvibe.Browser.Session); Granted is bound to
// the browser-session permission; Adapter is the vibe adapter's name (passed
// in because this package must not import an inbound adapter).
type MistralVibeSweeperDeps struct {
	Sessions  sessionLister
	Writer    ProviderRateLimitWriter
	Poller    *AccountPoller
	Browser   func() (MistralBrowserSession, error)
	Transport outbound.AccountQuotaTransport
	Granted   func() bool
	Adapter   string
	Log       outbound.Logger
	Interval  time.Duration
}

// MistralVibeSweeper reconciles every live top-level vibe session's Mistral
// snapshot with the permission, once per Interval. Like MuseAccountSweeper
// it is level-triggered: each tick writes the current reading while granted
// and clears it while not, so a write a transcript pass saved over, or a
// reading resurrected after a revoke, settles within one Interval.
//
// The binding is the user's assertion (issue #2012): granting the permission
// says the Chrome profile's Mistral account is the vibe CLI's. So every vibe
// session gets the one reading while granted, and none keeps it while not.
// Every tick the sessions show exactly what the sweep can serve right now for
// the Chrome profile it resolves (the poller's reading for that profile, fresh
// or stale) or nothing: another profile's reading, one persisted before a
// restart, or one a writer saved over an earlier clear is cleared on every
// tick that has no reading for the current profile, and replaced on every
// tick that has one. A tick with no vibe session reads nothing from the
// browser and sends nothing. Polls run serially on the sweep's own goroutine.
type MistralVibeSweeper struct {
	deps MistralVibeSweeperDeps
	// seen is every account context (Chrome profile) polled since the last
	// forget, so their poller entries can be dropped when nothing needs them.
	// failing and listFailing log a failure streak once. All three are
	// touched only from the sweep goroutine.
	seen        map[string]bool
	failing     bool
	listFailing bool
}

// NewMistralVibeSweeper builds a sweeper; Run starts it.
func NewMistralVibeSweeper(deps MistralVibeSweeperDeps) *MistralVibeSweeper {
	return &MistralVibeSweeper{deps: deps, seen: map[string]bool{}}
}

// Run sweeps once immediately, then every Interval, until ctx is done.
func (s *MistralVibeSweeper) Run(ctx context.Context) {
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

// RunOnce runs one sweep tick synchronously, for a caller (the daemon's
// consent tests) that drives ticks itself.
func (s *MistralVibeSweeper) RunOnce(ctx context.Context) { s.sweep(ctx) }

func (s *MistralVibeSweeper) sweep(ctx context.Context) {
	sessions, err := s.deps.Sessions.ListAll()
	if err != nil {
		if !s.listFailing {
			s.listFailing = true
			s.deps.Log.LogError(logComponentMistralVibeSweep, "", "listing sessions: "+err.Error())
		}
		return
	}
	s.listFailing = false
	var vibe []*session.SessionState
	for _, st := range sessions {
		if st != nil && st.Adapter == s.deps.Adapter && st.ParentSessionID == "" {
			vibe = append(vibe, st)
		}
	}

	if !s.deps.Granted() {
		// Not granted: unbind. Nothing is read from the browser and nothing
		// is sent.
		s.unbind(vibe)
		s.forgetAll()
		return
	}
	if len(vibe) == 0 {
		s.forgetAll()
		return
	}

	browser, err := s.deps.Browser()
	if err != nil {
		// No profile to confirm a listed reading against: clear it.
		s.logFailure("no Chrome profile to read: " + err.Error())
		s.unbind(vibe)
		return
	}
	s.seen[browser.Account()] = true

	// On a failure after an earlier success the poller still serves that
	// reading, stamped with RetrievalFailure (MistralVibeRefresh); with no
	// reading to serve, snap is nil.
	snap, err := MistralVibeRefresh(ctx, s.deps.Poller, browser, s.deps.Transport, s.deps.Granted, vibe[0].SessionID)
	if err != nil {
		s.logFailure("no current Mistral Vibe reading: " + err.Error())
	} else {
		s.failing = false
	}
	if ctx.Err() != nil {
		return
	}
	if snap == nil {
		// Nothing current to show for this profile, so nothing is shown: a
		// listed reading (another profile's, one persisted before a restart,
		// one a writer saved over an earlier clear) would only look current.
		s.unbind(vibe)
		return
	}
	if revoked := !s.deps.Granted(); revoked {
		return
	}
	for _, st := range vibe {
		if listed := listedMistralSnapshot(st); listed == nil || !sameRateLimitReading(listed, snap) {
			s.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMistral, snap)
		}
	}
}

// unbind clears the Mistral reading from every listed vibe session that
// carries one.
func (s *MistralVibeSweeper) unbind(vibe []*session.SessionState) {
	for _, st := range vibe {
		if listedMistralSnapshot(st) != nil {
			s.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMistral, nil)
		}
	}
}

// forgetAll drops the poller entry of every account context polled since the
// last forget.
func (s *MistralVibeSweeper) forgetAll() {
	for account := range s.seen {
		s.deps.Poller.Forget(MistralVibeQuotaKey(account))
	}
	s.seen = map[string]bool{}
}

// listedMistralSnapshot returns st's rate-limit snapshot when it is a
// Mistral one.
func listedMistralSnapshot(st *session.SessionState) *session.RateLimitSnapshot {
	if st.Metrics == nil || st.Metrics.RateLimit == nil || st.Metrics.RateLimit.Provider != session.ProviderMistral {
		return nil
	}
	return st.Metrics.RateLimit
}

func (s *MistralVibeSweeper) logFailure(msg string) {
	if s.failing {
		return
	}
	s.failing = true
	s.deps.Log.LogInfo(logComponentMistralVibeSweep, "", msg)
}

const logComponentMistralVibeSweep = "mistralvibesweep"
