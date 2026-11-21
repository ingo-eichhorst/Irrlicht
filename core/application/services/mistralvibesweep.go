// mistralvibesweep.go is issue #2012's live trigger: it binds the Mistral
// Vibe plan reading to every live vibe session while the browser-session
// permission is granted, and unbinds it when it is not.
package services

import (
	"context"
	"errors"
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
// A tick with no vibe session reads nothing from the browser and sends
// nothing. Polls run serially on the sweep's own goroutine.
type MistralVibeSweeper struct {
	deps MistralVibeSweeperDeps
	// account is the account context (Chrome profile) the last tick polled;
	// seen is every account context polled since the last forget, so their
	// poller entries can be dropped when nothing needs them. failing and
	// listFailing log a failure streak once. All four are touched only from
	// the sweep goroutine.
	account     string
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
		s.logFailure("no Chrome profile to read: " + err.Error())
		s.markStale(vibe, string(outbound.QuotaFailureCredential))
		return
	}
	account := browser.Account()
	// staleable is the sessions whose listed reading the sweep has no reason
	// to think belongs to another account context, so may be marked stale.
	staleable := vibe
	if s.account != "" && account != s.account {
		// A different Chrome profile is a different account context: the
		// previous profile's reading leaves every session before this
		// profile is polled, so it is never shown as this one's. Its poller
		// entry is kept (forgetAll drops it later), so switching back serves
		// that profile's own cached reading instead of fetching again.
		s.unbind(vibe)
		staleable = nil
	}
	s.account = account
	s.seen[account] = true

	// On a failure after an earlier success, snap is that reading stamped
	// with RetrievalFailure; with no earlier success it is nil, and a reading
	// a session already carries (from before a restart) is marked stale.
	snap, err := MistralVibeRefresh(ctx, s.deps.Poller, browser, s.deps.Transport, s.deps.Granted, vibe[0].SessionID)
	if err != nil {
		s.logFailure("no current Mistral Vibe reading: " + err.Error())
	} else {
		s.failing = false
	}
	if snap == nil {
		if reason, ok := vibeFailureReason(err); ok {
			s.markStale(staleable, reason)
		}
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

// markStale re-writes each listed Mistral reading, at its original time,
// stamped with reason, unless it already carries that reason.
func (s *MistralVibeSweeper) markStale(vibe []*session.SessionState, reason string) {
	for _, st := range vibe {
		listed := listedMistralSnapshot(st)
		if listed == nil || listed.RetrievalFailure == reason {
			continue
		}
		stale := *listed
		stale.RetrievalFailure = reason
		stale.LastAttemptAt = time.Now().Unix()
		s.deps.Writer.SetProviderRateLimit(st.SessionID, session.ProviderMistral, &stale)
	}
}

// forgetAll drops the poller entry of every account context polled since the
// last forget.
func (s *MistralVibeSweeper) forgetAll() {
	for account := range s.seen {
		s.deps.Poller.Forget(MistralVibeQuotaKey(account))
	}
	s.seen = map[string]bool{}
	s.account = ""
}

// vibeFailureReason returns the classification MistralVibeRefresh attached to
// a no-reading error, or false for one that says nothing about the reading
// (none fetched yet, or a revoke racing the tick).
func vibeFailureReason(err error) (string, bool) {
	var vf vibeFailure
	if errors.As(err, &vf) && vf != "" {
		return string(vf), true
	}
	return "", false
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
