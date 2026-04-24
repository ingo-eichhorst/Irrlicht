// Package config defines daemon-wide configuration.
package config

import "time"

// defaultMaxSessionAge is the default maximum age for sessions. Sessions
// whose transcript files have not been modified within this window are not
// loaded on startup and are silently dropped by the file-system watcher.
const defaultMaxSessionAge = 5 * 24 * time.Hour

// defaultReadySessionTTL is how long a "ready" (idle) session is kept before
// being automatically deleted. This is the primary cleanup mechanism for
// sessions where PID-based liveness cannot distinguish per-session state
// (e.g. when the daemon itself is the only process with the transcript open).
const defaultReadySessionTTL = 30 * time.Minute

// Config holds daemon-wide runtime configuration.
type Config struct {
	MaxSessionAge   time.Duration
	ReadySessionTTL time.Duration
}

// Default returns a Config populated with production defaults.
func Default() Config {
	return Config{
		MaxSessionAge:   defaultMaxSessionAge,
		ReadySessionTTL: defaultReadySessionTTL,
	}
}
