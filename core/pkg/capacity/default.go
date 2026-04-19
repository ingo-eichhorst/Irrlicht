package capacity

import "sync"

var (
	defaultOnce    sync.Once
	defaultManager *CapacityManager
)

// DefaultCapacityManager returns a process-wide CapacityManager backed by the
// LiteLLM cache at CachePath(). The first caller builds the singleton; all
// later callers share it. Subsequent GetModelCapacity calls transparently
// reload the cache when its mtime advances (e.g. after the daemon's daily
// RefreshRemoteDataIfStale tick).
//
// If the cache is missing or corrupt, the manager serves zero-value lookups
// until the cache becomes readable.
func DefaultCapacityManager() *CapacityManager {
	defaultOnce.Do(func() {
		cachePath, _ := CachePath()

		cm := &CapacityManager{
			cachePath: cachePath,
			config:    &CapacityConfig{Models: map[string]ModelCapacity{}},
		}
		// Prime from cache if available; missing cache is not fatal.
		cm.maybeReload()
		defaultManager = cm
	})
	return defaultManager
}
