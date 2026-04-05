package filesystem

import (
	"encoding/json"
	"sync"
	"time"

	"irrlicht/core/domain/session"
)

// CachedSessionRepository wraps a SessionRepository with a short-lived cache
// for ListAll() to avoid redundant filesystem scans from concurrent callers.
type CachedSessionRepository struct {
	inner    *SessionRepository
	mu       sync.Mutex
	cache    []*session.SessionState
	cachedAt time.Time
	ttl      time.Duration
}

// NewCachedSessionRepository returns a caching decorator around inner with the
// given TTL for ListAll() results.
func NewCachedSessionRepository(inner *SessionRepository, ttl time.Duration) *CachedSessionRepository {
	return &CachedSessionRepository{inner: inner, ttl: ttl}
}

// Load delegates directly — single file reads are cheap.
func (c *CachedSessionRepository) Load(sessionID string) (*session.SessionState, error) {
	return c.inner.Load(sessionID)
}

// Save invalidates the cache then delegates to avoid stale reads.
func (c *CachedSessionRepository) Save(state *session.SessionState) error {
	c.invalidate()
	return c.inner.Save(state)
}

// Delete invalidates the cache then delegates to avoid stale reads.
func (c *CachedSessionRepository) Delete(sessionID string) error {
	c.invalidate()
	return c.inner.Delete(sessionID)
}

// ListAll returns cached results if fresh, otherwise performs the filesystem
// scan and caches the result. Returns deep copies so callers can safely mutate.
func (c *CachedSessionRepository) ListAll() ([]*session.SessionState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cache != nil && time.Since(c.cachedAt) < c.ttl {
		return deepCopySessions(c.cache), nil
	}

	states, err := c.inner.ListAll()
	if err != nil {
		return nil, err
	}
	c.cache = states
	c.cachedAt = time.Now()
	return deepCopySessions(states), nil
}

// InstancesDir exposes the underlying directory path.
func (c *CachedSessionRepository) InstancesDir() string {
	return c.inner.InstancesDir()
}

// PruneStale delegates to inner and invalidates cache.
func (c *CachedSessionRepository) PruneStale(maxAge time.Duration) (int, error) {
	n, err := c.inner.PruneStale(maxAge)
	if n > 0 {
		c.invalidate()
	}
	return n, err
}

func (c *CachedSessionRepository) invalidate() {
	c.mu.Lock()
	c.cache = nil
	c.cachedAt = time.Time{}
	c.mu.Unlock()
}

// deepCopySessions returns independent copies of session states via JSON
// round-trip. This is simple and correct — the overhead is negligible compared
// to the filesystem scan it replaces.
func deepCopySessions(src []*session.SessionState) []*session.SessionState {
	if src == nil {
		return nil
	}
	result := make([]*session.SessionState, len(src))
	for i, s := range src {
		data, _ := json.Marshal(s)
		var cp session.SessionState
		_ = json.Unmarshal(data, &cp)
		result[i] = &cp
	}
	return result
}
