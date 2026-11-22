package main

import (
	"fmt"
	"time"

	"irrlicht/core/adapters/inbound/agents/vibe"
	"irrlicht/core/adapters/outbound/accountquota"
	"irrlicht/core/adapters/outbound/chromecookies"
	"irrlicht/core/adapters/outbound/mistralvibe"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// mistralVibeEffects builds issue #2012's browser-session route once per
// daemon over the daemon's shared AccountPoller (runDaemon builds the one
// poller, issue #2010): the once-per-grant Keychain key cache, the Chrome
// reader and the transport to the one reviewed destination, plus the
// Apply/Remove closures mistralvibe.PermissionDeclaration needs and the
// sweep that polls through them. It is museAccountAPIEffects' shape. The
// providers share only the poller, whose Revoke and Forget act on one
// provider's keys (Stop revokes session.ProviderMistral; the sweep forgets
// only MistralVibeQuotaKey keys), so neither grant disturbs the other's
// readings.
func mistralVibeEffects(logger outbound.Logger, poller *services.AccountPoller) mistralVibeAPI {
	keys := chromecookies.NewKeyCache(chromecookies.SafeStorageKey)
	browser := mistralvibe.Browser{UserDataDir: chromecookies.UserDataDir, Key: keys.Key}
	return newMistralVibeAPI(logger, poller, browser, keys)
}

// newMistralVibeAPI is mistralVibeEffects over a given poller, browser
// reader and key cache, so a test can drive it against a fake Chrome.
//
// Start only resets the key cache. It reads nothing: unlike Muse's Apply it
// does not warm a read, because the daemon re-applies every persisted grant
// at boot, and grant-all daemons (the recording rig, ir:test-mac) grant
// everything at boot, so a warm-up would raise the "Chrome Safe Storage"
// dialog at every start with no grant and no vibe session to explain it
// (review of #2012, finding 3). The first read happens on the sweep's first
// tick that has a live vibe session, which is when the reading is needed.
//
// Stop revokes the poller (which cancels a fetch in flight, and with it the
// cookie and Keychain read its resolve is running, and drops the cached
// reading) and resets the key cache, so nothing further is read or sent once
// it returns. The sweep clears the reading from each vibe session on its
// next tick.
func newMistralVibeAPI(logger outbound.Logger, poller *services.AccountPoller, browser mistralvibe.Browser, keys *chromecookies.KeyCache) mistralVibeAPI {
	// Destination() is a fixed, reviewed literal; an error here is a coding
	// mistake, logged once, and leaves no transport, so sweeper returns nil.
	var transport outbound.AccountQuotaTransport
	if t, err := accountquota.NewHTTPTransport([]outbound.FixedDestination{mistralvibe.Destination()}); err != nil {
		logger.LogError("permissions", "", fmt.Sprintf("mistralvibe: building the transport: %v", err))
	} else {
		transport = t
	}

	start := func() error {
		keys.Reset()
		return nil
	}
	stop := func() error {
		poller.Revoke(session.ProviderMistral)
		keys.Reset()
		return nil
	}
	return mistralVibeAPI{Start: start, Stop: stop, poller: poller, browser: browser, transport: transport}
}

// mistralVibeAPI is what mistralVibeEffects builds.
type mistralVibeAPI struct {
	Start, Stop func() error

	poller    *services.AccountPoller
	browser   mistralvibe.Browser
	transport outbound.AccountQuotaTransport
}

// mistralVibeSweepInterval is how often the sweep reconciles vibe sessions
// with the permission, and so bounds how long a revoked reading stays on a
// session (the permission text says "within a minute"). It is not the
// request rate: the poller serves a reading for its 5-minute fresh window.
const mistralVibeSweepInterval = time.Minute

// vibeSessionLister is the part of the session repository the sweep reads.
type vibeSessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// sweeper builds the sweep over these parts, gated on the browser-session
// permission as perms reports it, or returns nil when the transport could not
// be built. main.go passes the SessionDetector as writer and the cached
// repository as repo.
func (m mistralVibeAPI) sweeper(writer services.ProviderRateLimitWriter, repo vibeSessionLister, perms *services.PermissionService, logger outbound.Logger) *services.MistralVibeSweeper {
	if m.transport == nil {
		return nil
	}
	return services.NewMistralVibeSweeper(services.MistralVibeSweeperDeps{
		Sessions:  repo,
		Writer:    writer,
		Poller:    m.poller,
		Browser:   func() (services.MistralBrowserSession, error) { return m.browser.Session() },
		Transport: m.transport,
		Granted: func() bool {
			return perms.Granted(mistralvibe.Name, mistralvibe.PermissionKey)
		},
		Adapter:  vibe.AdapterName,
		Log:      logger,
		Interval: mistralVibeSweepInterval,
	})
}
