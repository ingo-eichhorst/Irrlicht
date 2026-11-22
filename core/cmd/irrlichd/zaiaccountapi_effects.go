package main

import (
	"fmt"

	"irrlicht/core/adapters/inbound/agents/claudesettings"
	"irrlicht/core/adapters/outbound/accountquota"
	"irrlicht/core/adapters/outbound/zaiaccountapi"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// zaiAccountAPI is the Z.ai quota integration's daemon-side parts (issue
// #2010): the permission's effects and the poller, credential reader and
// transport its sweep polls through — museAccountAPI's shape.
type zaiAccountAPI struct {
	Start, Stop func() error

	poller      *services.AccountPoller
	credentials services.ZaiCredentialReader
	transport   outbound.AccountQuotaTransport
}

// zaiAccountAPIEffects builds the production parts over the daemon's shared
// poller: the reviewed transport to zaiaccountapi.Destination() only, and a
// credential reader over claudesettings.ReadEnv with a fresh per-daemon
// fingerprint salt. A construction failure is a coding mistake (a fixed
// destination) or a failed random read; it is logged once and leaves the
// parts nil, so no sweep starts.
func zaiAccountAPIEffects(logger outbound.Logger, poller *services.AccountPoller) zaiAccountAPI {
	var transport outbound.AccountQuotaTransport
	var credentials services.ZaiCredentialReader
	t, terr := accountquota.NewHTTPTransport([]outbound.FixedDestination{zaiaccountapi.Destination()})
	reader, rerr := zaiaccountapi.NewCredentialReader(claudesettings.ReadEnv)
	switch {
	case terr != nil:
		logger.LogError("permissions", "", fmt.Sprintf("zaiaccountapi: building the account-quota transport: %v", terr))
	case rerr != nil:
		logger.LogError("permissions", "", fmt.Sprintf("zaiaccountapi: %v", rerr))
	default:
		transport = t
		credentials = reader
	}
	return newZaiAccountAPI(poller, transport, credentials)
}

// newZaiAccountAPI is zaiAccountAPIEffects over given parts, so a test can
// hand it a transport that never leaves the machine.
//
// Apply has nothing to warm: unlike Muse's one daemon-wide credential, a
// Z.ai key is read per session from the settings file that routed it, and
// only by the sweep, after it has checked the grant. Remove revokes every
// Z.ai poll key — AccountPoller.Revoke deletes by AccountQuotaKey.Provider,
// which services.ZaiAccountQuotaKey stamps as session.ProviderZai. The
// readings already on sessions are cleared by the sweep's next tick, at most
// zaiAccountSweepInterval later. Before #2161's QA fix a transcript pass could
// save a cleared reading back, and the live trials saw it survive to the
// following tick in 2 of 3 revokes; processActivity now loads under the lock
// the clear takes. A clear lost to one of the remaining unlocked writers
// (SetProviderRateLimit's doc names them) is re-applied a tick later.
func newZaiAccountAPI(poller *services.AccountPoller, transport outbound.AccountQuotaTransport, credentials services.ZaiCredentialReader) zaiAccountAPI {
	return zaiAccountAPI{
		Start: func() error { return nil },
		Stop: func() error {
			poller.Revoke(session.ProviderZai)
			return nil
		},
		poller:      poller,
		credentials: credentials,
		transport:   transport,
	}
}

// zaiAccountSweepInterval is museAccountSweepInterval: it bounds how long a
// revoke or a lost write takes to settle, not the account-API call rate,
// which AccountPoller's fresh window bounds.
const zaiAccountSweepInterval = museAccountSweepInterval

// sweeper builds the per-session sweep, gated on the permission
// zaiaccountapi.PermissionDeclaration declares, or nil when the parts could
// not be built.
func (z zaiAccountAPI) sweeper(detector *services.SessionDetector, repo outbound.SessionRepository, perms *services.PermissionService, logger outbound.Logger) *services.ZaiAccountSweeper {
	if z.transport == nil || z.credentials == nil {
		return nil
	}
	return services.NewZaiAccountSweeper(services.ZaiAccountSweeperDeps{
		Sessions:    repo,
		Writer:      detector,
		Poller:      z.poller,
		Credentials: z.credentials,
		Transport:   z.transport,
		Granted: func() bool {
			return perms.Granted(zaiaccountapi.Name, agent.AccountAPIPermissionKey)
		},
		Log:      logger,
		Interval: zaiAccountSweepInterval,
	})
}
