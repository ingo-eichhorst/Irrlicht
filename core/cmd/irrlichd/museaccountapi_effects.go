package main

import (
	"context"
	"fmt"

	"irrlicht/core/adapters/outbound/accountquota"
	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// museAccountAPIEffects builds the daemon-wide AccountPoller and Muse's
// CredentialResolver/AccountQuotaTransport once, plus the Apply/Remove
// closures museaccountapi.PermissionDeclaration needs — the gastownEffects
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
// so there is nothing here that can iterate live Muse sessions and poll
// each one — that per-session trigger (a periodic sweep, or a lazy poll at
// read time) is intentionally left for a follow-up; see this ticket's PR
// body "Left out" section. What Apply CAN usefully do without session
// context is fail fast: resolve the credential once, so a broken
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
// would tolerate before either path had to give up waiting anyway. The
// resolve still happens exactly once per grant; only the wait moved off the
// synchronous path.
func museAccountAPIEffects(logger outbound.Logger) (start, stop func() error) {
	// poller and resolver are used only by the start/stop closures below,
	// which capture them directly — returning them too would be dead
	// weight with no consumer (found by review, issue #2007: the sole
	// caller, main.go, blanked all three of poller/resolver/transport,
	// and `transport` specifically was constructed, returned, and never
	// read by anything after that).
	poller := services.NewAccountPoller()
	resolver := museaccountapi.NewCredentialResolver(museaccountapi.AuthPath)

	// Constructed for its validation side effect only — Destination() is a
	// fixed, reviewed literal, so a construction error here is a coding
	// mistake in this package, not a runtime condition, and is logged once
	// at daemon startup rather than deferred into a per-poll failure that
	// would look like a transient network problem. Not kept: nothing calls
	// Fetch on it yet (see this function's own doc comment on the
	// per-session poll trigger being a follow-up).
	if _, err := accountquota.NewHTTPTransport([]outbound.FixedDestination{museaccountapi.Destination()}); err != nil {
		logger.LogError("permissions", "", fmt.Sprintf("museaccountapi: building the account-quota transport: %v", err))
	}

	start = func() error {
		// Detached deliberately (mirrors main.go's own
		// `go runCapacityRefreshLoop(context.Background(), ...)`): Apply has
		// no shorter-lived parent context to thread through, and the
		// resolver's own credential_keychain_darwin.go already bounds the
		// one shellout this can reach (keychainTimeout, 15s, measured) —a
		// second, shorter timeout here would only race that one for no
		// benefit, which is exactly what the prior 5s version did.
		go func() {
			if _, err := resolver.Resolve(context.Background()); err != nil {
				logger.LogInfo("permissions", "", fmt.Sprintf("museaccountapi: credential not resolvable yet: %v", err))
			}
		}()
		return nil
	}
	stop = func() error {
		// services.MuseAccountQuotaKey stamps AccountQuotaKey.Provider as
		// session.ProviderMeta — Revoke's argument must match that field
		// exactly (accountpoller.go's Revoke deletes by key.Provider ==
		// provider), not museaccountapi.DestinationKey (that names the
		// destination/scope, a different field).
		poller.Revoke(session.ProviderMeta)
		return nil
	}
	return start, stop
}
