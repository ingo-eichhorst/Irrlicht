// museaccountpoll.go is issue #2007's composition of the daemon-wide
// AccountPoller (accountpoller.go) with museaccountapi's parser
// (core/adapters/outbound/museaccountapi) — the first REAL caller of the
// poller #2003 built with no provider wired to it yet.
//
// Layering note: this package is allowed to import
// irrlicht/core/adapters/outbound/* (core/architecture_test.go's own rule
// forbids application/services reaching only into adapters/INBOUND) — no
// prior file in this package did, so this is this package's first such
// edge, made deliberately here rather than pushed up into cmd/irrlichd,
// because AccountPoller (this package) and museaccountapi's redaction-
// boundary parser belong on the same side of "how a Muse quota reading
// becomes a session.RateLimitSnapshot" as each other.
package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// MuseAccountQuotaKey is the ONE function that decides the AccountQuotaKey a
// Muse poll is admitted under — deliberately SESSION-scoped
// ("session:"+sessionID as Account), not provider-account-scoped. Issue
// #2007's live probe (PR body) found no stable provider-issued account
// identifier for Muse, and the epic's own resolution for that case (#1977
// line 107: "If account identity is unavailable, retain the observation for
// its original session or credential context... do not merge unidentified
// accounts") is what this key shape encodes: two different sessions can
// never collide into one AccountPoller cache entry, even though that means
// Muse gets none of #2003 §1.4's cross-session dedup benefit until a real
// account identifier exists. TestMuseAccountQuotaKey_TwoSessionsNeverCollide
// (mutation fixture #4) mutates this to return a fixed constant regardless
// of sessionID and confirms two sessions' polls collide into one entry,
// which is exactly what a session-scoped key must never do.
func MuseAccountQuotaKey(sessionID string) AccountQuotaKey {
	return AccountQuotaKey{
		Provider: session.ProviderMeta,
		Account:  "session:" + sessionID,
		Scope:    museaccountapi.DestinationKey,
	}
}

// MuseAccountRefresh polls (or reads the cached observation for) Muse's
// account-quota endpoint for sessionID and, on success, parses it into a
// session.RateLimitSnapshot through museaccountapi.BuildSnapshot.
//
// On ANY failure — Poll returning an error, an observation with no cached
// value yet, or BuildSnapshot rejecting the body — this returns (nil, err)
// and NEVER a manufactured session.RateLimitSnapshot{} zero value.
// TestMuseAccountRefresh_NeverPublishesZeroOnFailure (mutation fixture #3)
// mutates the no-cached-value branch to return an empty snapshot instead of
// an error and confirms the guard goes red — the same defect class issue
// #2003 §1.4 and its own recordFailure already guard at the poller layer
// (never setting HasValue on a failure); this is the Muse-layer half, for
// the caller-facing boundary this function itself is.
func MuseAccountRefresh(
	ctx context.Context,
	poller *AccountPoller,
	resolver outbound.CredentialResolver,
	transport outbound.AccountQuotaTransport,
	granted func() bool,
	sessionID string,
	now time.Time,
) (*session.RateLimitSnapshot, error) {
	if sessionID == "" {
		return nil, errors.New("services: MuseAccountRefresh needs a non-empty sessionID")
	}
	obs, err := poller.Poll(ctx, PollRequest{
		Key:            MuseAccountQuotaKey(sessionID),
		SessionID:      sessionID,
		Granted:        granted,
		Resolver:       resolver,
		Transport:      transport,
		DestinationKey: museaccountapi.DestinationKey,
		Auth:           museaccountapi.Auth(),
	})
	if err != nil {
		return nil, fmt.Errorf("services: polling Muse account quota: %w", err)
	}
	if !obs.HasValue {
		if obs.FailureReason != "" {
			return nil, fmt.Errorf("services: Muse account quota not available: %s", obs.FailureReason)
		}
		return nil, errors.New("services: Muse account quota not available yet")
	}
	snap, err := museaccountapi.BuildSnapshot(obs.Body, now)
	if err != nil {
		return nil, fmt.Errorf("services: parsing Muse account quota: %w", err)
	}
	return &snap, nil
}
