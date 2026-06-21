package relay

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"irrlicht/core/ports/outbound"
)

// PublishController owns the relay forwarder's lifecycle so publishing can be
// turned on, off, or reconfigured on a running daemon without a relaunch
// (issue #722). The macOS app drives it over the daemon's loopback
// PUT /api/v1/relay/publish; a headless/standalone daemon seeds it once from
// IRRLICHT_RELAY_URL / IRRLICHT_RELAY_TOKEN at startup (Apply below), so its
// behavior is unchanged from the env-only path that preceded this.
//
// At most one Forwarder runs at a time. Reconfiguring cancels the current
// forwarder's context and starts a fresh one — the forwarder is already
// ctx-cancelable, so this reuses its existing shutdown path.
type PublishController struct {
	parentCtx context.Context
	identity  Identity
	push      outbound.PushBroadcaster
	snapshot  SnapshotFunc
	logger    outbound.Logger

	mu     sync.Mutex
	fwd    *Forwarder         // non-nil only while publishing is enabled
	cancel context.CancelFunc // cancels fwd's Run; nil when stopped
	url    string             // last applied (trimmed) url, for idempotency
	token  string             // last applied (trimmed) token, for idempotency
}

// NewPublishController builds a controller in the stopped state. parentCtx
// bounds every forwarder it starts, so cancelling it (on daemon shutdown)
// stops publishing. identity, push, and snapshot are the forwarder
// dependencies that never change across reconfigures; logger may be nil.
func NewPublishController(parentCtx context.Context, id Identity, push outbound.PushBroadcaster, snapshot SnapshotFunc, logger outbound.Logger) *PublishController {
	return &PublishController{
		parentCtx: parentCtx,
		identity:  id,
		push:      push,
		snapshot:  snapshot,
		logger:    logger,
	}
}

// Apply reconciles the running forwarder to the requested publish config.
// enabled with a non-empty url starts (or reconfigures) the forwarder; either
// flag falsy stops it. It is idempotent: re-applying the config already in
// effect is a no-op, so the macOS app can POST on every settings nudge without
// churning the relay link. Mutex-guarded so concurrent PUTs serialize.
func (c *PublishController) Apply(enabled bool, url, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	url = strings.TrimSpace(url)
	token = strings.TrimSpace(token)
	// A blank URL means "off" even when the toggle is on — mirrors the macOS
	// env builder's old semantics so an empty URL never activates a forwarder.
	wantOn := enabled && url != ""

	if !wantOn {
		if c.fwd == nil {
			return // already stopped — nothing to do
		}
		c.cancel()
		c.cancel = nil
		c.fwd = nil
		c.logInfo("relay publishing stopped")
		return
	}

	// Running with the same effective config — leave the link untouched.
	if c.fwd != nil && c.url == url && c.token == token {
		return
	}

	// Start fresh: stop any existing forwarder first so only one link runs.
	if c.cancel != nil {
		c.cancel()
	}
	fwd := NewForwarder(url, c.identity, token, c.push, c.snapshot, c.logger)
	ctx, cancel := context.WithCancel(c.parentCtx)
	c.fwd = fwd
	c.cancel = cancel
	c.url = url
	c.token = token
	go fwd.Run(ctx)
	c.logInfo(fmt.Sprintf("relay publishing → %s (daemon %q / %s)", fwd.Status().URL, c.identity.DaemonLabel, c.identity.DaemonID))
}

// Status reports whether publishing is enabled and, when it is, the live link
// state of the running forwarder. When stopped it returns (false, zero Status)
// so the endpoint renders {"enabled":false}.
func (c *PublishController) Status() (enabled bool, status Status) {
	c.mu.Lock()
	fwd := c.fwd
	c.mu.Unlock()
	if fwd == nil {
		return false, Status{}
	}
	return true, fwd.Status()
}

func (c *PublishController) logInfo(msg string) {
	if c.logger != nil {
		c.logger.LogInfo("relay-publish", "", msg)
	}
}
