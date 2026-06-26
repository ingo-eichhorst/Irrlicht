// Package config defines daemon-wide configuration.
package config

import (
	"os"
	"strconv"
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

// Permission-wizard modes (issue #570). Ask is the production default:
// nothing is read or written until the user grants each permission.
// GrantAll auto-grants every declared permission at startup and never
// prompts — for demo, recording, and test daemons where fixtures must not
// hang on consent. Set via IRRLICHT_PERMISSION_MODE.
const (
	PermissionModeAsk      = "ask"
	PermissionModeGrantAll = "grant-all"
)

// Cache-bloat detector defaults (issue #374). The per-project p25 baseline of
// cache-creation-per-turn is computed over completed sessions in the trailing
// CacheBloatBaselineDays window; a working session trips the rule when its
// median cache-creation-per-turn exceeds baseline × CacheBloatThreshold, but
// not before CacheBloatMinTurns completed turns (variance guard). Version
// attribution fires only when two versions differ by more than
// CacheBloatVersionDeltaTokens. A threshold of 0 (or negative) disables the
// whole rule — the kill switch.
const (
	defaultCacheBloatBaselineDays      = 14
	defaultCacheBloatThreshold         = 1.4
	defaultCacheBloatVersionDeltaToken = 10000
	defaultCacheBloatMinTurns          = 3
)

// Config holds daemon-wide runtime configuration.
type Config struct {
	MaxSessionAge   time.Duration
	ReadySessionTTL time.Duration
	PermissionMode  string

	// Cache-bloat detector knobs (issue #374), overridable via env vars.
	CacheBloatBaselineDays       int
	CacheBloatThreshold          float64
	CacheBloatVersionDeltaTokens int64
	CacheBloatMinTurns           int
}

// Default returns a Config populated with production defaults, with the
// IRRLICHT_READY_SESSION_TTL and IRRLICHT_PERMISSION_MODE env overrides
// applied if set.
func Default() Config {
	ttl := defaultReadySessionTTL
	if raw := os.Getenv("IRRLICHT_READY_SESSION_TTL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			ttl = parsed
		}
	}
	mode := PermissionModeAsk
	if os.Getenv("IRRLICHT_PERMISSION_MODE") == PermissionModeGrantAll {
		mode = PermissionModeGrantAll
	}
	return Config{
		MaxSessionAge:   defaultMaxSessionAge,
		ReadySessionTTL: ttl,
		PermissionMode:  mode,

		CacheBloatBaselineDays:       envInt("IRRLICHT_CACHE_BLOAT_BASELINE_DAYS", defaultCacheBloatBaselineDays),
		CacheBloatThreshold:          envFloat("IRRLICHT_CACHE_BLOAT_THRESHOLD", defaultCacheBloatThreshold),
		CacheBloatVersionDeltaTokens: int64(envInt("IRRLICHT_CACHE_BLOAT_VERSION_DELTA", defaultCacheBloatVersionDeltaToken)),
		CacheBloatMinTurns:           envInt("IRRLICHT_CACHE_BLOAT_MIN_TURNS", defaultCacheBloatMinTurns),
	}
}

// envInt reads a non-negative integer env override, falling back to def when
// unset or unparseable. Negative values are rejected (fall back to def).
func envInt(key string, def int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			return v
		}
	}
	return def
}

// envFloat reads a float env override, falling back to def when unset or
// unparseable. Zero and negative values are honored (0 = kill switch).
func envFloat(key string, def float64) float64 {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	return def
}
