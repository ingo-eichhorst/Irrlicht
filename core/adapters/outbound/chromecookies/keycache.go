package chromecookies

import (
	"context"
	"sync"
)

// KeyCache reads the cookie key once per permission grant.
//
// Each read of the "Chrome Safe Storage" item shells out to
// /usr/bin/security, and Muse's Keychain route measured that every such read
// raises a macOS dialog when security is not on the item's access list
// (issue #2062, museaccountapi/credential_keychain_darwin.go). So the outcome
// of one read, the key or the failure, is kept until Reset: a denied or
// unanswered dialog is not raised again on every poll, and revoking then
// re-granting the permission is the retry. The owner (the daemon's
// mistralvibe wiring) calls Reset on grant and on revoke.
//
// Callers that arrive while a read is in flight wait for it rather than
// starting a second one (and a second dialog). Reset never waits for a read
// in flight; it drops the pointer, so a read that finishes afterwards lands
// in an entry nothing reads any more. This is the same shape as
// services.GrantCredentialCache, which this package cannot import (an
// adapter may not depend on application/services) and which caches an
// outbound.Credential, not key material that must never be revealed.
type KeyCache struct {
	source func(context.Context) (Key, error)

	mu  sync.Mutex
	cur *keyRead
}

type keyRead struct {
	done chan struct{}
	key  Key
	err  error
}

// NewKeyCache caches the outcome of source.
func NewKeyCache(source func(context.Context) (Key, error)) *KeyCache {
	return &KeyCache{source: source}
}

// Key returns the cached outcome, calling source only when nothing is
// cached.
func (c *KeyCache) Key(ctx context.Context) (Key, error) {
	c.mu.Lock()
	r := c.cur
	if r == nil {
		r = &keyRead{done: make(chan struct{})}
		c.cur = r
		c.mu.Unlock()
		func() {
			r.err = ErrKeychain // what waiters see if source panics
			defer close(r.done)
			r.key, r.err = c.source(ctx)
		}()
		return r.key, r.err
	}
	c.mu.Unlock()
	select {
	case <-r.done:
		return r.key, r.err
	case <-ctx.Done():
		return Key{}, ctx.Err()
	}
}

// Reset drops the cached outcome, so the next Key reads again.
func (c *KeyCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = nil
}
