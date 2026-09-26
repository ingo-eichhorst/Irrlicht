package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"irrlicht/core/adapters/inbound/agents/muse"
	"irrlicht/core/adapters/outbound/accountquota"
	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// museAccountAPIEffects builds the daemon-wide AccountPoller and Muse's
// CredentialResolver/AccountQuotaTransport once, plus the Apply/Remove
// closures museaccountapi.PermissionDeclaration needs and the per-session
// sweep that polls through them (issue #2057) — the gastownEffects
// shape (this file's sibling, startup.go's gastownEffects), for the same
// reason: the museaccountapi/services packages cannot construct a
// *services.AccountPoller for themselves (deliberately ONE per daemon — see
// accountpoller.go's own package doc), so the daemon's own wiring has to.
//
// agent.AccountAPIPermissionKey's doc comment (written by #2003, before any
// real provider existed) describes Apply as registering the provider "with"
// the poller — but AccountPoller has no registration surface at all: no
// Register method, no per-provider field, only a cache keyed lazily by
// whatever AccountQuotaKey a Poll call happens to pass (found by review,
// issue #2007). What Apply below actually does is described in its own
// paragraph next; Remove's poller.Revoke(session.ProviderMeta) call is the
// one half of that description that IS accurate as written.
//
// Apply's scope is deliberately narrow: a permission grant carries no
// session context (Apply/Remove are global toggles, not per-session calls),
// so the per-session polling lives in services.MuseAccountSweeper (issue
// #2057), built by museAccountAPI.sweeper below and started with the other
// background loops; it reads the grant on every tick rather than being
// switched on and off by these closures. What Apply CAN usefully do without
// session context is fail fast: resolve the credential once, so a broken
// auth.json/Keychain setup surfaces in the daemon log the moment consent is
// granted rather than silently deferring the first failure to whenever a
// session next polls. It never blocks or denies the grant on a resolve
// failure — this is an observe-kind permission, and a resolve failure here
// is exactly what a later poll would also report through
// services.MuseAccountRefresh.
//
// Apply runs the resolve in its own goroutine rather than awaiting it
// inline (found by review, issue #2007): PermissionService.runEffects calls
// every Apply closure SYNCHRONOUSLY, on two real paths —
// daemon startup under IRRLICHT_PERMISSION_MODE=grant-all (the recording
// rig, ir:test-mac, and every grant-all e2e boot; Muse's permission is
// KindObserve, so scopedOutByRecordAdapters' KindModify-only narrowing never
// excludes it) and the POST /api/v1/permissions/answer HTTP handler when a
// user grants Muse from the wizard. A synchronous wait here bought nothing
// either path needs — nothing downstream of Apply's return consumes its
// result, and the credential's own doc comment
// (museaccountapi/credential_keychain_darwin.go) measured this machine's
// real Keychain item taking ~9.5s, well past what a short inline bound
// would tolerate before either path had to give up waiting anyway.
//
// The credential is read once per grant (issue #2062). Every read on Muse's
// Keychain route raises a macOS dialog, and before #2062 the sweep read it on
// every fetch — once per session per poll window. resolver is a
// services.GrantCredentialCache shared by Apply's read and the sweep: Apply
// resets it and warms it, so that read is the one the sweep is then served,
// and a sweep tick that lands while it is still running joins it instead of
// starting a second. A failed Keychain read (museaccountapi.ErrKeychainRead)
// stays cached too, so an unanswered dialog is not raised again on the
// poller's backoff; Remove resets it, and the next grant reads again. That
// grant has to follow a revoke, or be the daemon's re-apply at startup:
// re-answering "granted" while already granted runs no Apply. A failure
// before the Keychain route (no auth.json yet) raised no dialog and is not
// cached. The other reset is the sweep's re-read after Meta rejects the
// credential (services.GrantCredentialCache.InvalidateOnce).
func museAccountAPIEffects(logger outbound.Logger) museAccountAPI {
	return newMuseAccountAPI(logger, museaccountapi.NewCredentialResolver(museaccountapi.AuthPath))
}

// newMuseAccountAPI is museAccountAPIEffects over a given credential
// resolver, so a test can count the reads the wiring makes.
func newMuseAccountAPI(logger outbound.Logger, credentials outbound.CredentialResolver) museAccountAPI {
	poller := services.NewAccountPoller()
	resolver := services.NewGrantCredentialCache(credentials, func(err error) bool {
		return errors.Is(err, museaccountapi.ErrKeychainRead)
	})

	// Destination() is a fixed, reviewed literal, so a construction error
	// here is a coding mistake in this package, not a runtime condition, and
	// is logged once at daemon startup rather than deferred into a per-poll
	// failure that would look like a transient network problem. On that
	// error transport stays nil and sweeper returns nil, so no sweep starts.
	var transport outbound.AccountQuotaTransport
	if t, err := accountquota.NewHTTPTransport([]outbound.FixedDestination{museaccountapi.Destination()}); err != nil {
		logger.LogError("permissions", "", fmt.Sprintf("museaccountapi: building the account-quota transport: %v", err))
	} else {
		transport = t
	}

	start := func() error {
		// Detached deliberately (mirrors main.go's own
		// `go runCapacityRefreshLoop(context.Background(), ...)`): Apply has
		// no shorter-lived parent context to thread through, and the
		// resolver's own credential_keychain_darwin.go already bounds the
		// one shellout this can reach (keychainTimeout, 15s, measured) —a
		// second, shorter timeout here would only race that one for no
		// benefit, which is exactly what the prior 5s version did.
		resolver.Reset()
		go func() {
			if _, err := resolver.Resolve(context.Background()); err != nil {
				logger.LogInfo("permissions", "", fmt.Sprintf("museaccountapi: credential not resolvable: %v", err))
			}
		}()
		return nil
	}
	stop := func() error {
		// services.MuseAccountQuotaKey stamps AccountQuotaKey.Provider as
		// session.ProviderMeta — Revoke's argument must match that field
		// exactly (accountpoller.go's Revoke deletes by key.Provider ==
		// provider), not museaccountapi.DestinationKey (that names the
		// destination/scope, a different field).
		// The chip itself goes on the sweep's next tick, which sees the
		// grant gone and clears each session's meta snapshot.
		poller.Revoke(session.ProviderMeta)
		// Reset after Revoke: Revoke cancels any fetch in flight, and a read
		// that fetch was running may still land in the cache before this
		// line — the reset drops it either way. Reset never waits on a read
		// in flight (GrantCredentialCache.Reset), so a revoke answered over
		// HTTP is not held up by an open Keychain dialog.
		resolver.Reset()
		return nil
	}
	return museAccountAPI{Start: start, Stop: stop, poller: poller, resolver: resolver, transport: transport}
}

// museAccountAPI is what museAccountAPIEffects builds: the permission's
// effects, and the shared poller/resolver/transport the sweep polls through.
type museAccountAPI struct {
	Start, Stop func() error

	poller    *services.AccountPoller
	resolver  *services.GrantCredentialCache
	transport outbound.AccountQuotaTransport
}

// museAccountSweepInterval is how often the sweep reconciles each Muse
// session's snapshot. Not the account-API call rate: AccountPoller serves a
// cached reading for its 5-minute fresh window, so most ticks make no call.
// It bounds how long a revoke, or a write a transcript pass overwrote, takes
// to settle.
const museAccountSweepInterval = time.Minute

// sweeper builds the per-session sweep over these parts, gated on the same
// permission museaccountapi.PermissionDeclaration declares, or returns nil
// when the transport could not be built.
func (m museAccountAPI) sweeper(detector *services.SessionDetector, repo outbound.SessionRepository, perms *services.PermissionService, logger outbound.Logger) *services.MuseAccountSweeper {
	if m.transport == nil {
		return nil
	}
	return services.NewMuseAccountSweeper(services.MuseAccountSweeperDeps{
		Sessions:  repo,
		Writer:    detector,
		Poller:    m.poller,
		Resolver:  m.resolver,
		Transport: m.transport,
		Granted: func() bool {
			return perms.Granted(museaccountapi.Name, agent.AccountAPIPermissionKey)
		},
		Adapter:  muse.AdapterName,
		Log:      logger,
		Interval: museAccountSweepInterval,
	})
}
