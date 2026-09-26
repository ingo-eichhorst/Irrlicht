// grantcredential.go is issue #2062: the credential is read once per
// permission grant, not once per account-API fetch.
package services

import (
	"context"
	"errors"
	"sync"

	outbound "irrlicht/core/ports/outbound"
)

// GrantCredentialCache wraps a CredentialResolver so that the wrapped
// resolver runs once between two Resets, and every Resolve in between is
// served that one outcome — a credential, or a failure keepFailure accepts.
//
// Why such a failure is cached too: Muse's Keychain route shells out to
// /usr/bin/security, which is not on the item's ACL, so every read raises a
// macOS dialog; an unanswered one is killed after keychainTimeout and comes
// back as an error (issue #2062, from the reporter's events.log). Retrying
// that error on the poller's backoff raised the dialog again every few
// minutes. With it cached, it is retried only after the owner calls Reset —
// museAccountAPIEffects does that on grant and on revoke, so revoking and
// granting the permission again (or restarting the daemon, which re-applies
// the grant) is the retry. A failure keepFailure rejects (one that raised no
// dialog, such as a missing auth.json) is not cached, so the next Resolve
// reads again. A panicking resolver's outcome stays cached until Reset.
//
// Concurrent callers share one read: the first caller after a Reset runs the
// wrapped resolver, and every caller arriving while it runs waits for that
// result (bounded by its own ctx). Reset never waits for a read in flight —
// it only drops the pointer, so a read that finishes afterwards lands in an
// entry nothing reads any more.
//
// The credential stays an outbound.Credential here: it is never unwrapped or
// logged, the same boundary the wrapped resolver keeps.
type GrantCredentialCache struct {
	inner       outbound.CredentialResolver
	keepFailure func(error) bool

	mu  sync.Mutex
	cur *credentialRead
	// reReadUsed records that InvalidateOnce has already dropped a
	// credential since the last Reset or CredentialAccepted.
	reReadUsed bool
}

// credentialRead is one run of the wrapped resolver. done is closed once
// cred/err hold its outcome.
type credentialRead struct {
	done chan struct{}
	cred outbound.Credential
	err  error
}

// errCredentialReadPanicked is what callers waiting on a read receive when
// the wrapped resolver panicked instead of returning.
var errCredentialReadPanicked = errors.New("services: credential resolver panicked")

// NewGrantCredentialCache wraps inner. keepFailure reports whether a failed
// read is cached until the next Reset; a nil keepFailure caches none.
func NewGrantCredentialCache(inner outbound.CredentialResolver, keepFailure func(error) bool) *GrantCredentialCache {
	return &GrantCredentialCache{inner: inner, keepFailure: keepFailure}
}

// Resolve returns the cached outcome, running the wrapped resolver only when
// nothing is cached.
func (c *GrantCredentialCache) Resolve(ctx context.Context) (outbound.Credential, error) {
	c.mu.Lock()
	r := c.cur
	if r == nil {
		r = &credentialRead{done: make(chan struct{})}
		c.cur = r
		c.mu.Unlock()
		c.read(ctx, r)
		if r.err != nil && !c.keeps(r.err) {
			c.drop(r)
		}
		return r.cred, r.err
	}
	c.mu.Unlock()
	select {
	case <-r.done:
		return r.cred, r.err
	case <-ctx.Done():
		return outbound.Credential{}, ctx.Err()
	}
}

// read runs the wrapped resolver into r. The deferred close keeps waiting
// callers from blocking forever when the resolver panics; the panic itself
// still propagates to this caller, who recovers it or not (the poller's
// runDoFetch does; museAccountAPIEffects' warm-up goroutine does not).
func (c *GrantCredentialCache) read(ctx context.Context, r *credentialRead) {
	r.err = errCredentialReadPanicked
	defer close(r.done)
	r.cred, r.err = c.inner.Resolve(ctx)
}

func (c *GrantCredentialCache) keeps(err error) bool {
	return c.keepFailure != nil && c.keepFailure(err)
}

// drop forgets r if it is still the cached read.
func (c *GrantCredentialCache) drop(r *credentialRead) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == r {
		c.cur = nil
	}
}

// Reset drops the cached outcome and re-arms InvalidateOnce, so the next
// Resolve reads the credential again. Called on grant and on revoke.
func (c *GrantCredentialCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = nil
	c.reReadUsed = false
}

// InvalidateOnce drops the cached credential after the provider rejected it
// (401/403), and reports whether it did. It does so once until the next
// Reset or CredentialAccepted: one fresh read picks up a re-login or a
// rotated token, and a rejection straight after that re-read leaves the
// cache alone, so a credential the provider keeps refusing is not re-read —
// and no dialog re-raised — until the permission is granted again.
//
// A read still in flight is left alone and the re-read is not used up: the
// rejection came from a credential read before it (for example the previous
// grant's), and that read is already the fresh one.
func (c *GrantCredentialCache) InvalidateOnce() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reReadUsed {
		return false
	}
	if c.cur != nil {
		select {
		case <-c.cur.done:
		default:
			return false
		}
	}
	c.reReadUsed = true
	c.cur = nil
	return true
}

// CredentialRejected implements CredentialFeedback: AccountPoller calls it
// after the provider rejected the credential (401/403).
func (c *GrantCredentialCache) CredentialRejected() { c.InvalidateOnce() }

// CredentialAccepted implements CredentialFeedback: AccountPoller calls it
// after the provider accepted the cached credential, which re-arms InvalidateOnce: a token that rotates again later
// gets its one re-read too. Each re-read then needs an accepted fetch in
// between, so a credential the provider keeps refusing still cannot loop.
func (c *GrantCredentialCache) CredentialAccepted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reReadUsed = false
}
