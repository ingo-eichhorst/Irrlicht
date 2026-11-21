// mistralvibepoll.go is issue #2012's composition of the AccountPoller with
// the Mistral Vibe browser-session route (core/adapters/outbound/mistralvibe).
// It is museaccountpoll.go's shape with one difference that matters: the
// poll key is the Chrome profile, not the session, so every vibe session
// shares one poll (#2012's design: "one poll per browser profile").
package services

import (
	"context"
	"errors"
	"fmt"

	"irrlicht/core/adapters/outbound/mistralvibe"
	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// MistralBrowserSession is one sweep tick's Chrome profile
// (mistralvibe.Session): the account context the poll is keyed on, and the
// resolver that reads that profile's cookies when, and only when, the poller
// fetches.
type MistralBrowserSession interface {
	outbound.CredentialResolver
	Account() string
}

// MistralVibeQuotaKey is the ONE function that decides the key a Mistral
// Vibe poll is admitted under: the browser session's account context, which
// is the Chrome profile (mistralvibe.Session.Account). Two vibe sessions on
// one profile share it; a second profile is a second key and never shares.
func MistralVibeQuotaKey(account string) AccountQuotaKey {
	return AccountQuotaKey{
		Provider: session.ProviderMistral,
		Account:  account,
		Scope:    mistralvibe.DestinationKey,
	}
}

// MistralVibeRefresh polls (or serves the cached reading of) the Vibe usage
// endpoint for browser's profile and parses it into a snapshot. sessionID is
// carried for attribution only. The snapshot's SampledAt and LastSuccessAt
// are the observation's own FetchedAt, so a cached reading keeps the time it
// was fetched.
//
// With no reading to serve (a first poll that failed, an observation with
// no value yet, or a body the parser refuses) it returns (nil, err) and
// NEVER a manufactured zero snapshot: a 401 from a signed-out browser is a
// classified auth failure, not 0 %. TestMistralVibeRefresh_NeverPublishesZeroOnAuthFailure
// is the target of tools/lib/mistralvibe-mutations_test.sh's zero (B1)
// mutation, which makes the no-value branch return an empty snapshot.
//
// When a fetch fails after an earlier success, the poller still holds that
// reading, marked stale; it is returned stamped with RetrievalFailure
// alongside a non-nil error, so it keeps its original time and cannot pass
// for current.
func MistralVibeRefresh(
	ctx context.Context,
	poller *AccountPoller,
	browser MistralBrowserSession,
	transport outbound.AccountQuotaTransport,
	granted func() bool,
	sessionID string,
) (*session.RateLimitSnapshot, error) {
	obs, err := poller.Poll(ctx, PollRequest{
		Key:            MistralVibeQuotaKey(browser.Account()),
		SessionID:      sessionID,
		Granted:        granted,
		Resolver:       browser,
		Transport:      transport,
		DestinationKey: mistralvibe.DestinationKey,
		Auth:           mistralvibe.Auth(),
	})
	// A revoke landing while a fetch is in flight makes the poller hand back
	// the reading it already holds (AccountPoller.notPublished), stale or not,
	// beside errRevokedBeforePublish. A revoked refresh returns nothing.
	if !granted() {
		return nil, errors.New("services: Mistral Vibe usage: the browser-session permission is not granted")
	}
	if err != nil && !(obs.HasValue && obs.Stale) {
		return nil, fmt.Errorf("services: polling Mistral Vibe usage: %w", err)
	}
	if !obs.HasValue {
		if obs.FailureReason != "" {
			return nil, fmt.Errorf("services: Mistral Vibe usage not available: %s", obs.FailureReason)
		}
		return nil, errors.New("services: Mistral Vibe usage not available yet")
	}
	snap, perr := mistralvibe.BuildSnapshot(obs.Body, obs.FetchedAt)
	if perr != nil {
		return nil, fmt.Errorf("services: parsing Mistral Vibe usage: %w", perr)
	}
	if !obs.LastAttempt.IsZero() {
		snap.LastAttemptAt = obs.LastAttempt.Unix()
	}
	if obs.Stale {
		snap.RetrievalFailure = obs.FailureReason
		if err == nil {
			err = fmt.Errorf("services: Mistral Vibe usage is stale: %s", obs.FailureReason)
		} else {
			err = fmt.Errorf("services: polling Mistral Vibe usage (serving the last reading): %w", err)
		}
		return &snap, err
	}
	return &snap, nil
}
