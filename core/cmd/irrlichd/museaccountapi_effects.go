package main

import (
	"context"
	"fmt"
	"time"

	"irrlicht/core/adapters/outbound/accountquota"
	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// museAccountAPIResolveTimeout bounds Apply's one-shot, best-effort
// credential resolve below. Not a measured probe cost — a deliberate
// ceiling matching the daemon's other grant-time sanity checks
// (cmd/irrlichd/startup.go's own context.WithTimeout(context.Background(),
// 5*time.Second) at its consent_signal.go / detector-setup call sites).
const museAccountAPIResolveTimeout = 5 * time.Second

// museAccountAPIEffects builds the daemon-wide AccountPoller and Muse's
// CredentialResolver/AccountQuotaTransport once, plus the Apply/Remove
// closures museaccountapi.PermissionDeclaration needs — the gastownEffects
// shape (this file's sibling, startup.go's gastownEffects), for the same
// reason: the daemon's own wiring has to supply what
// agent.AccountAPIPermissionKey's doc comment describes ("Apply registers
// the provider with the daemon-wide poller; Remove calls
// AccountPoller.Revoke(providerName)") and the museaccountapi/services
// packages cannot construct for themselves (a *services.AccountPoller
// instance is deliberately ONE per daemon — see accountpoller.go's own
// package doc).
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
func museAccountAPIEffects(logger outbound.Logger) (poller *services.AccountPoller, resolver outbound.CredentialResolver, transport outbound.AccountQuotaTransport, start, stop func() error) {
	poller = services.NewAccountPoller()
	resolver = museaccountapi.NewCredentialResolver(museaccountapi.AuthPath)

	tr, err := accountquota.NewHTTPTransport([]outbound.FixedDestination{museaccountapi.Destination()})
	if err != nil {
		// museaccountapi.Destination() is a fixed, reviewed literal — a
		// construction error here is a coding mistake in this package, not
		// a runtime condition, so it is logged once at daemon startup
		// rather than deferred into a per-poll failure that would look like
		// a transient network problem.
		logger.LogError("permissions", "", fmt.Sprintf("museaccountapi: building the account-quota transport: %v", err))
	}
	transport = tr

	start = func() error {
		bounded, cancel := context.WithTimeout(context.Background(), museAccountAPIResolveTimeout)
		defer cancel()
		if _, err := resolver.Resolve(bounded); err != nil {
			logger.LogInfo("permissions", "", fmt.Sprintf("museaccountapi: credential not resolvable yet: %v", err))
		}
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
	return poller, resolver, transport, start, stop
}
