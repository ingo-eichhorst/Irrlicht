// Package services — accountpoller.go is issue #2003's daemon-wide poller:
// the one reviewed way irrlicht polls a provider's account-quota endpoint,
// shared across every provider adapter rather than one poller per adapter.
// The triage comment on #2003 settles this design point directly: §1.4's
// "more sessions and more dashboard refreshes must not multiply account API
// calls" requires deduplication across every session that shares a confirmed
// account, and a per-provider poller cannot see another provider's sessions
// to deduplicate against — so there is exactly one AccountPoller per daemon,
// constructed once and shared by every provider adapter's polling calls.
//
// This ticket adds no real provider — every credential resolver, transport,
// and permission wiring below is exercised in tests against a local stub
// server (core/adapters/outbound/accountquota) and a fake consent gate
// (core/internal/contracttesting.ConsentGate). A later provider ticket
// (#2007 for Muse, per epic #1977 §10 work package B) supplies a real
// CredentialResolver, registers its FixedDestination(s), and calls Poll with
// a Granted func bound to PermissionService.Granted(adapterName,
// agent.AccountAPIPermissionKey) — see that constant's doc comment in
// core/domain/agent/declaration.go for the permission-declaration shape this
// poller assumes.
package services

import (
	"context"
	"errors"
	"sync"
	"time"

	"irrlicht/core/ports/outbound"
)

// AccountQuotaKey identifies one account-quota polling bucket. It mirrors
// quotainherit.go's own (differently-scoped) AccountKey — Provider plus a
// confirmed account id — with one addition, Scope: issue #2003 §1.4 requires
// deduplication "by confirmed account AND quota scope" because a single
// account can carry more than one billable product (a base plan and a
// metered add-on, for example), and those must never share one poll result.
// Named distinctly (not AccountKey) only because that identifier is already
// taken in this package by quotainherit.go's rate-limit-inheritance key,
// which this type shares no fields or call sites with.
//
// There is no empty-Account sentinel, for the same reason quotainherit.go's
// AccountKey has none: an empty Account is simply unconfirmed, and Poll
// refuses to act on it rather than treating it as a key any two unconfirmed
// callers could collide on.
type AccountQuotaKey struct {
	Provider string
	Account  string
	Scope    string
}

// Observation is what one AccountQuotaKey's most recent poll produced.
//
// HasValue distinguishes "never published" from "published a zero/empty
// body" — the same shape issue #2005's fixture protects one layer over
// (tools/lib/pi-provider-absent-not-confirmed-mutations_test.sh: absent must
// never read as confirmed). FetchedAt is set ONLY by a successful poll
// (publish) and is never touched by a failure (recordFailure) — that is what
// keeps a stale value's "original observation time" original across
// repeated failures, per #2003 §1.4.
type Observation struct {
	HasValue      bool
	Body          []byte
	FetchedAt     time.Time // set only by publish; untouched by recordFailure
	Stale         bool      // true once a poll has failed since the value in Body was fetched
	LastAttempt   time.Time // the most recent poll attempt, success or failure
	FailureReason string    // the most recent failure's classification; "" once a poll succeeds
}

const (
	// pollFreshWindow is how long a successful observation is served without
	// a new Fetch — the mechanism behind "more dashboard refreshes must not
	// multiply account API calls" (#2003 §1.4) for the single-caller case;
	// the in-flight join below (pollEntry.inflight) is the mechanism for the
	// concurrent-callers case. Not a measured probe cost — a deliberate,
	// generous window chosen so a quota chip a user reloads repeatedly
	// doesn't re-poll a provider's account API every reload.
	pollFreshWindow = 5 * time.Minute

	// pollFailureBackoff is the minimum spacing between Fetch attempts
	// immediately after a failure, doubling per consecutive failure up to
	// pollFailureBackoffCeiling — #2003 §1.4's "retry ceiling". Deliberate
	// choices, not measured probe costs.
	pollFailureBackoff        = 30 * time.Second
	pollFailureBackoffCeiling = 30 * time.Minute
)

var (
	// errUnknownAccount is returned when Poll is asked to act on a key with
	// no confirmed account — #2003 §6's "a session whose account identity is
	// unknown: no poll, and no shared result". No cache entry is read or
	// written for this key.
	errUnknownAccount = errors.New("accountpoller: refusing to poll — no confirmed account")

	// errNotGranted is returned whenever the caller's permission is not
	// currently granted (pending, denied, or revoked look identical to Poll
	// — #2003 §1.1: "a pending permission permits nothing; a denied
	// permission permits nothing"). No credential is read and no request is
	// sent.
	errNotGranted = errors.New("accountpoller: permission not granted — no credential read, no request sent")

	// errRevokedBeforePublish is returned when a fetch that started while
	// granted completed successfully, but the permission was no longer
	// granted by the time the result would have been published. The result
	// is discarded — see doFetch's final Granted() check, which is what
	// tools/lib/provapi-revoke-blocks-publish-mutations_test.sh mutates away.
	errRevokedBeforePublish = errors.New("accountpoller: permission revoked before the result could be published — discarded")
)

// PollRequest is one caller's ask to poll AccountQuotaKey Key, bundling exactly
// the axes #2003 §1.3 requires kept separate: a credential resolver
// (location), a transport (destination + wire format), and Auth
// (authentication method) are independent, swappable inputs — a provider
// ticket assembles them, the poller never constructs any of them itself.
type PollRequest struct {
	Key AccountQuotaKey

	// SessionID is the CALLING session's id, carried through to
	// outbound.AccountQuotaRequest for attribution/logging only. It plays no
	// part in Key and must never be used to key the cache — that is exactly
	// what tools/lib/provapi-cache-key-account-mutations_test.sh mutates
	// Poll's key construction to do, and the "ten sessions, one poll" test is
	// what catches it.
	SessionID string

	// Granted reports whether the caller's permission is CURRENTLY granted.
	// Poll calls it at admission (before any credential read) and again
	// immediately before publishing a successful result, so a revoke that
	// lands after admission but before the response arrives still stops the
	// publish. Required; a nil Granted is treated as "never granted".
	Granted func() bool

	Resolver       outbound.CredentialResolver
	Transport      outbound.AccountQuotaTransport
	DestinationKey string
	Auth           outbound.AuthMethod
}

// inflightPoll is the join point for every caller that asks for the same
// AccountQuotaKey while a fetch is already in progress — the mechanism behind
// #2003 §1.4's "ten sessions on one confirmed account: one poll, not ten".
// The leader (the caller that found no inflight and started one) runs
// doFetch and populates obs/err before closing done; every other caller
// blocks on done and returns the identical result.
type inflightPoll struct {
	done   chan struct{}
	obs    Observation
	err    error
	cancel context.CancelFunc
}

// pollEntry is one AccountQuotaKey's cached state.
type pollEntry struct {
	obs              Observation
	consecutiveFails int
	nextAllowedAt    time.Time // zero = no backoff in effect
	inflight         *inflightPoll
}

// AccountPoller is the single daemon-wide poller instance — see the package
// header for why there is exactly one per daemon.
type AccountPoller struct {
	mu    sync.Mutex
	cache map[AccountQuotaKey]*pollEntry

	// now is overridable so tests can drive pollFreshWindow/backoff without
	// a real wall-clock wait.
	now func() time.Time
}

// NewAccountPoller returns an AccountPoller ready to serve every provider
// adapter's Poll calls.
func NewAccountPoller() *AccountPoller {
	return &AccountPoller{cache: map[AccountQuotaKey]*pollEntry{}, now: time.Now}
}

// cacheKeyFor is the ONLY place that decides what key admits req into the
// cache/dedup map — every call in Poll and doFetch threads its RESULT
// through rather than re-deriving one, so there is exactly one place this
// property can break. It returns req.Key (the confirmed account/scope the
// caller was ADMITTED under) verbatim, never req.SessionID, which
// PollRequest carries for attribution/logging only (issue #2003 §1.4: "more
// sessions ... must not multiply account API calls" — that only holds if the
// cache is keyed by account, not by the session asking). Keying by session
// instead is exactly
// tools/lib/provapi-cache-key-account-mutations_test.sh's mutation, caught
// by TestAccountPoller_TenSessionsOneConfirmedAccountIsOnePoll (ten distinct
// sessions would then produce ten distinct keys instead of one).
func cacheKeyFor(req PollRequest) AccountQuotaKey {
	return req.Key
}

// Poll returns the current Observation for req.Key, fetching a fresh one
// when needed (and permitted) or joining an in-flight fetch another caller
// already started for the identical key.
func (p *AccountPoller) Poll(ctx context.Context, req PollRequest) (Observation, error) {
	if req.Key.Account == "" {
		return Observation{}, errUnknownAccount
	}
	if req.Granted == nil || !req.Granted() {
		return Observation{}, errNotGranted
	}
	key := cacheKeyFor(req)

	p.mu.Lock()
	e := p.cache[key]
	if e == nil {
		e = &pollEntry{}
		p.cache[key] = e
	}
	now := p.now()

	if e.inflight != nil {
		inflight := e.inflight
		p.mu.Unlock()
		<-inflight.done
		return inflight.obs, inflight.err
	}
	if e.obs.HasValue && now.Sub(e.obs.FetchedAt) < pollFreshWindow {
		obs := e.obs
		p.mu.Unlock()
		return obs, nil
	}
	if !e.nextAllowedAt.IsZero() && now.Before(e.nextAllowedAt) {
		obs := e.obs
		p.mu.Unlock()
		return obs, nil
	}

	fetchCtx, cancel := context.WithCancel(ctx)
	inflight := &inflightPoll{done: make(chan struct{}), cancel: cancel}
	e.inflight = inflight
	p.mu.Unlock()

	obs, err := p.doFetch(fetchCtx, req, key, inflight)
	inflight.obs, inflight.err = obs, err
	close(inflight.done)
	cancel()

	return obs, err
}

// doFetch performs the leader's actual credential resolve + transport fetch
// for one Poll call and records the result via completeInflight, which is
// what stops a completion racing a Revoke from resurrecting the entry
// Revoke just deleted (see completeInflight's doc comment). key is Poll's
// OWN cacheKeyFor(req) result, threaded through rather than recomputed, and
// inflight is this call's OWN inflightPoll — every completeInflight call
// below confirms the cache still points at exactly this inflight, under
// exactly this key, before writing anything.
func (p *AccountPoller) doFetch(ctx context.Context, req PollRequest, key AccountQuotaKey, inflight *inflightPoll) (Observation, error) {
	cred, err := req.Resolver.Resolve(ctx)
	if err != nil {
		return p.recordFailure(key, inflight, outbound.QuotaFailureNetwork), err
	}

	resp, err := req.Transport.Fetch(ctx, outbound.AccountQuotaRequest{
		DestinationKey: req.DestinationKey,
		Credential:     cred,
		Auth:           req.Auth,
		SessionID:      req.SessionID,
	})
	if err != nil {
		reason := outbound.QuotaFailureNetwork
		var qerr *outbound.QuotaError
		if errors.As(err, &qerr) {
			reason = qerr.Reason
		}
		return p.recordFailure(key, inflight, reason), err
	}

	// The final consent check before publishing (issue #2003 §1.1:
	// "[revocation must] prevent a later publication of a result that work
	// already produced"). A fetch that was admitted while granted can still
	// complete after a revoke arrives — Revoke's context cancellation
	// (below) closes most of that window, but a response that already
	// finished arriving races ahead of a cancellation in flight; this check
	// is what closes it regardless of that race. Removing it is exactly
	// tools/lib/provapi-revoke-blocks-publish-mutations_test.sh's mutation.
	if req.Granted == nil || !req.Granted() {
		return p.notPublished(key, inflight), errRevokedBeforePublish
	}

	return p.publish(key, inflight, resp.Body), nil
}

// backoffFor returns the retry delay after consecutiveFails failures in a
// row, doubling from pollFailureBackoff up to pollFailureBackoffCeiling.
func backoffFor(consecutiveFails int) time.Duration {
	d := pollFailureBackoff
	for i := 1; i < consecutiveFails; i++ {
		if d >= pollFailureBackoffCeiling {
			return pollFailureBackoffCeiling
		}
		d *= 2
	}
	if d > pollFailureBackoffCeiling {
		return pollFailureBackoffCeiling
	}
	return d
}

// completeInflight is the single place doFetch's three outcomes
// (recordFailure/publish/notPublished) land — and the guard that stops a
// completion racing a Revoke from resurrecting the entry Revoke just
// deleted. It confirms the cache's CURRENT entry for key is still the exact
// one myInflight was issued against (by pointer identity) before running
// mutate; if the entry is gone (Revoke deleted it) or now belongs to a
// different, newer inflight fetch, this completion is stale and is
// discarded — nothing is written, and the zero Observation is returned.
// Without this check, recordFailure's OWN "create if missing" branch would
// otherwise recreate exactly the entry Revoke just deleted, one Revoke
// mid-flight scenario Poll's own admission checks cannot reach because they
// only run at the START of Poll, not at completion — measured directly:
// TestAccountPoller_RevokeCancelsInFlightRequest failed with "expected
// Revoke to wipe the cache entry entirely" before this guard existed.
func (p *AccountPoller) completeInflight(key AccountQuotaKey, myInflight *inflightPoll, mutate func(e *pollEntry)) Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.cache[key]
	if e == nil || e.inflight != myInflight {
		return Observation{}
	}
	mutate(e)
	e.inflight = nil
	return e.obs
}

// recordFailure records a classified failure for key, retaining whatever
// value was already cached (marking it Stale, never touching its Body or
// FetchedAt) rather than overwriting it with anything derived from this
// failure.
//
// The "no prior success" case — where completeInflight's e.obs is still its
// zero value — is deliberately left there (HasValue: false, Body: nil): this
// is the entire guard tools/lib/provapi-no-zero-on-401-mutations_test.sh
// mutates away, by making that case manufacture a HasValue:true empty
// Observation instead, which is exactly an auth failure "publishing a zero
// usage" (#2003 §1.4).
func (p *AccountPoller) recordFailure(key AccountQuotaKey, inflight *inflightPoll, reason outbound.QuotaFailureReason) Observation {
	return p.completeInflight(key, inflight, func(e *pollEntry) {
		now := p.now()
		e.obs.LastAttempt = now
		e.obs.FailureReason = string(reason)
		if e.obs.HasValue {
			e.obs.Stale = true
		}
		e.consecutiveFails++
		e.nextAllowedAt = now.Add(backoffFor(e.consecutiveFails))
	})
}

// publish records a successful fetch, replacing whatever was cached (a
// success always supersedes a prior stale/failed state) and resetting the
// backoff.
func (p *AccountPoller) publish(key AccountQuotaKey, inflight *inflightPoll, body []byte) Observation {
	return p.completeInflight(key, inflight, func(e *pollEntry) {
		now := p.now()
		e.obs = Observation{HasValue: true, Body: body, FetchedAt: now, LastAttempt: now}
		e.consecutiveFails = 0
		e.nextAllowedAt = time.Time{}
	})
}

// notPublished returns key's current cached state without modifying it —
// used when a fetch succeeded but consent was revoked before publish, so the
// revocation itself (not this fetch) owns whatever the cache holds.
func (p *AccountPoller) notPublished(key AccountQuotaKey, inflight *inflightPoll) Observation {
	return p.completeInflight(key, inflight, func(*pollEntry) {
		// Nothing to mutate: e.obs is returned exactly as completeInflight
		// found it (still owned by this inflight, or the guard above would
		// already have refused).
	})
}

// Revoke cancels every in-flight fetch for provider and wipes every cached
// observation for it, across every account and scope. Issue #2003's
// high-level design declares one permission PER PROVIDER, covering the
// credential read and the egress together — so a revoke is scoped to
// "everything that permission covered", not to one account, because the
// daemon has no narrower unit to revoke against. A provider's own Remove
// closure calls this.
func (p *AccountPoller) Revoke(provider string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, e := range p.cache {
		if key.Provider != provider {
			continue
		}
		if e.inflight != nil {
			e.inflight.cancel()
		}
		delete(p.cache, key)
	}
}
