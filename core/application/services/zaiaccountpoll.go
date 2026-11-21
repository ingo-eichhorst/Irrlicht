// zaiaccountpoll.go is issue #2010's composition of the daemon-wide
// AccountPoller with zaiaccountapi's parser, the Z.ai counterpart of
// museaccountpoll.go.
package services

import (
	"context"
	"errors"
	"fmt"

	"irrlicht/core/adapters/outbound/zaiaccountapi"
	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// ZaiAccountQuotaKey is the one function that decides the AccountQuotaKey a
// Z.ai poll is admitted under: scoped to the CREDENTIAL, by the key's
// salted in-memory fingerprint (zaiaccountapi.Fingerprinter).
//
// Not session-scoped: two sessions sending the same key are billed to the
// same plan and share one poll and one reading. Never provider-wide: two
// different keys never share, even when they belong to one account — the
// response carries no account identifier (probe comment §1), so nothing
// could show that they do. This is #1977 §3.2's credential identity, not an
// account identity, and the manifest says so.
// tools/lib/zai-coding-plan-mutations_test.sh (S1) collapses this to a
// fixed Account and requires TestZaiAccountSweep_DifferentKeysNeverShareAReading
// to go red.
func ZaiAccountQuotaKey(fingerprint string) AccountQuotaKey {
	return AccountQuotaKey{
		Provider: session.ProviderZai,
		Account:  "credential:" + fingerprint,
		Scope:    zaiaccountapi.DestinationKey,
	}
}

// resolvedCredential hands the poller a key the sweep has already read. The
// key is read once per sweep per session from the session's settings file
// (zaiaccountapi.CredentialReader), because its fingerprint decides the
// poll key before the poller can be asked anything.
type resolvedCredential struct{ cred outbound.Credential }

func (r resolvedCredential) Resolve(context.Context) (outbound.Credential, error) {
	return r.cred, nil
}

// ZaiAccountRefresh polls (or reads the cached observation for) the Z.ai
// quota under cred and parses it. It mirrors MuseAccountRefresh: SampledAt
// is the observation's own FetchedAt, a failure with no earlier success
// returns (nil, err) and never a zero-value snapshot, and a failure after
// an earlier success returns that reading marked with RetrievalFailure
// alongside the error, keeping its original time.
func ZaiAccountRefresh(
	ctx context.Context,
	poller *AccountPoller,
	transport outbound.AccountQuotaTransport,
	granted func() bool,
	sessionID string,
	cred zaiaccountapi.Credential,
) (*session.RateLimitSnapshot, error) {
	if cred.Fingerprint == "" || cred.Key.IsZero() {
		return nil, errors.New("services: ZaiAccountRefresh needs a resolved credential")
	}
	obs, err := poller.Poll(ctx, PollRequest{
		Key:            ZaiAccountQuotaKey(cred.Fingerprint),
		SessionID:      sessionID,
		Granted:        granted,
		Resolver:       resolvedCredential{cred: cred.Key},
		Transport:      transport,
		DestinationKey: zaiaccountapi.DestinationKey,
		Auth:           zaiaccountapi.Auth(),
	})
	if err != nil && !(obs.HasValue && obs.Stale) {
		return nil, fmt.Errorf("services: polling the Z.ai quota: %w", err)
	}
	if !obs.HasValue {
		if obs.FailureReason != "" {
			return nil, fmt.Errorf("services: Z.ai quota not available: %s", obs.FailureReason)
		}
		return nil, errors.New("services: Z.ai quota not available yet")
	}
	snap, perr := zaiaccountapi.BuildSnapshot(obs.Body, obs.FetchedAt)
	if perr != nil {
		return nil, fmt.Errorf("services: parsing the Z.ai quota: %w", perr)
	}
	if !obs.LastAttempt.IsZero() {
		snap.LastAttemptAt = obs.LastAttempt.Unix()
	}
	if obs.Stale {
		snap.RetrievalFailure = obs.FailureReason
		if err == nil {
			err = fmt.Errorf("services: Z.ai quota is stale: %s", obs.FailureReason)
		} else {
			err = fmt.Errorf("services: polling the Z.ai quota (serving the last reading): %w", err)
		}
		return &snap, err
	}
	return &snap, nil
}
