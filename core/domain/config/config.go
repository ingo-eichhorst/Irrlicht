// Package config defines daemon-wide configuration.
package config

import (
	"os"
	"time"
)

// defaultMaxSessionAge is the default maximum age for sessions. Sessions
// whose transcript files have not been modified within this window are not
// loaded on startup and are silently dropped by the file-system watcher.
const defaultMaxSessionAge = 5 * 24 * time.Hour

// defaultReadySessionTTL is how long a "ready" (idle) session is kept before
// being automatically deleted. This is the primary cleanup mechanism for
// sessions where PID-based liveness cannot distinguish per-session state
// (e.g. when the daemon itself is the only process with the transcript open).
//
// Overridable via IRRLICHT_READY_SESSION_TTL (Go duration string, e.g. "15s",
// "5m") so the onboarding factory can record the long-idle-live-session
// scenario in seconds instead of half an hour. Default in production
// remains 30 min.
const defaultReadySessionTTL = 30 * time.Minute

// Config holds daemon-wide runtime configuration.
type Config struct {
	MaxSessionAge   time.Duration
	ReadySessionTTL time.Duration
}

// Default returns a Config populated with production defaults, with the
// IRRLICHT_READY_SESSION_TTL env override applied if set.
func Default() Config {
	ttl := defaultReadySessionTTL
	if raw := os.Getenv("IRRLICHT_READY_SESSION_TTL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			ttl = parsed
		}
	}
	return Config{
		MaxSessionAge:   defaultMaxSessionAge,
		ReadySessionTTL: ttl,
	}
}
