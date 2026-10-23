package services

import (
	"sync"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// AccountBinder stamps a session's rate-limit snapshot with the account it is
// confirmed against (#2095).
type AccountBinder struct {
	readers map[string]outbound.SessionAccountReader
	store   outbound.HomeAccountStore
	log     outbound.Logger
	now     func() time.Time

	mu sync.Mutex
	// homes is nil until the first homeFor loads it from the store.
	homes map[string]session.HomeAccount
}

// NewAccountBinder builds a binder. readers is keyed by adapter name; now may
// be nil.
func NewAccountBinder(readers map[string]outbound.SessionAccountReader, store outbound.HomeAccountStore, log outbound.Logger, now func() time.Time) *AccountBinder {
	if now == nil {
		now = time.Now
	}
	return &AccountBinder{readers: readers, store: store, log: log, now: now}
}

// homeFor records obs against the tracked state of its home and returns the
// home's current account. A home is tracked from its first observation; the
// account is re-dated only when it CHANGES (an identical Ref with a newer
// mtime is the agent rewriting the file, not a switch).
func (b *AccountBinder) homeFor(obs outbound.HomeAccountObservation) session.HomeAccount {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loadHomes()
	known, seen := b.homes[obs.Home]
	if seen && known.Ref == obs.Ref {
		return known
	}
	since := obs.ModTime
	if now := b.now().Unix(); since <= 0 || since > now {
		since = now
	}
	cur := session.HomeAccount{Ref: obs.Ref, Since: since}
	b.homes[obs.Home] = cur
	b.saveHomes()
	return cur
}

// loadHomes lazily loads the persisted homes on first use. The caller holds
// b.mu.
func (b *AccountBinder) loadHomes() {
	if b.homes != nil {
		return
	}
	b.homes = map[string]session.HomeAccount{}
	if b.store == nil {
		return
	}
	loaded, err := b.store.Load()
	if err != nil {
		b.log.LogError("account-binder", "", "load account-homes: "+err.Error())
		return
	}
	if loaded != nil {
		b.homes = loaded
	}
}

// saveHomes persists the homes. The caller holds b.mu.
func (b *AccountBinder) saveHomes() {
	if b.store == nil {
		return
	}
	if err := b.store.Save(b.homes); err != nil {
		b.log.LogError("account-binder", "", "save account-homes: "+err.Error())
	}
}

// sessionStart is the earliest time the session is known to have existed, 0
// when the transcript start cannot be read yet.
func sessionStart(state *session.SessionState, r outbound.SessionAccountReader) int64 {
	ts := r.TranscriptStart(state)
	if ts == 0 {
		return 0
	}
	if state.FirstSeen > 0 && state.FirstSeen < ts {
		return state.FirstSeen
	}
	return ts
}

// Stamp binds state to its home's account (when the rule allows) and stamps
// the session's rate-limit snapshot with the account it is confirmed against.
// It replaces the snapshot with a copy rather than mutating it: the pointer
// may be shared with the metrics collector's cache. A nil binder is a no-op,
// as is any adapter without a reader.
func (b *AccountBinder) Stamp(state *session.SessionState) {
	if b == nil || state == nil {
		return
	}
	r := b.readers[state.Adapter]
	if r == nil {
		return
	}
	obs, ok := r.ReadHomeAccount(state)
	if !ok {
		// Not allowed to look (or nothing to look at). The binding is
		// serialized with the session, so a bound session becomes permanently
		// unconfirmed (no ref kept anywhere, no re-bind after a re-grant: a
		// switch made while we could not look is unobservable). An undecided
		// binding stays undecided. The snapshot is scrubbed either way.
		if state.AccountBinding != nil && state.AccountBinding.Ref != "" {
			state.AccountBinding = &session.AccountBinding{Decided: true}
		}
		stampSnapshot(state, session.AccountBinding{}, obs)
		return
	}
	home := b.homeFor(obs)

	binding := session.AccountBinding{}
	if state.AccountBinding != nil {
		binding = *state.AccountBinding
	}
	var start int64
	if !binding.Decided {
		start = sessionStart(state, r)
		if start == 0 {
			// Transcript start unreadable: stay undecided and retry, rather
			// than guessing from FirstSeen alone.
			stampSnapshot(state, session.AccountBinding{}, obs)
			return
		}
	}
	// Advance ignores start once the binding is decided.
	binding = binding.Advance(home, start, b.now().Unix())
	state.AccountBinding = &binding
	stampSnapshot(state, binding, obs)
}

// stampSnapshot replaces the session's rate-limit snapshot with a copy carrying
// the account binding confirms for the reading's sample time.
func stampSnapshot(state *session.SessionState, binding session.AccountBinding, obs outbound.HomeAccountObservation) {
	if state.Metrics == nil || state.Metrics.RateLimit == nil {
		return
	}
	cp := *state.Metrics.RateLimit
	cp.ConfirmedAccountRef = binding.RefAt(cp.SampledAt)
	// Product only ever travels with a non-empty ref (wire contract). The
	// claude statusline snapshot carries none of its own (grep Product in
	// claudecode/statusline.go: no match), so clearing it here only drops a
	// value this binder stamped on a snapshot MergeMetrics carried forward.
	cp.Product = ""
	if cp.ConfirmedAccountRef != "" && cp.ConfirmedAccountRef == obs.Ref {
		cp.Product = obs.Product
	}
	state.Metrics.RateLimit = &cp
}
