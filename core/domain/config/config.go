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

// defaultYieldSweepInterval is how often the yield sweep correlates `git
// revert` commits back to the sessions that authored the reverted work (#373).
// Overridable via IRRLICHT_YIELD_SWEEP_INTERVAL (Go duration string).
const defaultYieldSweepInterval = 30 * time.Minute

// defaultHookReverifyInterval is how often the hook-entry re-verification loop
// (#1372) re-reads every granted install to see whether its entries are still
// present. It mirrors services.defaultReverifyInterval, which stays as the
// fallback for callers that construct the verifier directly (its unit tests).
//
// Overridable via IRRLICHT_HOOK_REVERIFY_INTERVAL (Go duration string) for the
// same reason IRRLICHT_READY_SESSION_TTL is: a test that has to observe what
// the loop does on its *next* pass cannot wait five minutes for one. The
// production cadence is deliberately unhurried — see the rationale on
// services.defaultReverifyInterval — so this is a shortening knob, not a
// tuning invitation.
const defaultHookReverifyInterval = 5 * time.Minute

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

// defaultHookSilentTurns is how many consecutive completed turns an adapter may
// produce with zero hook receipts before its hook channel is declared silent
// and the adapter falls back to TierTranscript (issue #1368).
//
// The rationale, and why five rather than one or fifty:
//
// The expected rate is at least one hook event per completed turn, not per
// session and not per hour. Both hook-receiving adapters install a Stop hook —
// claudecode's installedHookEvents includes HookStop, and codex's Stop is the
// authoritative turn-done push (#1171) — so on a healthy channel every turn
// boundary the daemon counts should be accompanied by at least one receipt, and
// a permission-heavy turn produces several. That makes zero-over-N a genuine
// signal rather than an inference from silence, and it is why the threshold is
// counted in TURNS: a wall-clock window cannot tell a dead channel from a user
// at lunch, which is precisely the ambiguity this exists to remove.
//
// One turn would already be suspicious and is far too twitchy to act on. The
// daemon's turn boundary is a rising edge of transcript-derived IsAgentDone,
// which is deliberately independent of the channel under test, so the two
// clocks can interleave: a Stop hook can land just after the pass that counted
// its turn, an install can complete mid-turn, and a session already in flight
// when consent was granted owes no receipt for the turn it was in. Each of
// those costs at most one or two turns. Five clears all of them with margin.
//
// The upper bound is set by what the demotion is FOR. Its most consequential
// effect is releasing hook-tier holds that would otherwise pin a session at
// waiting until #1360's 12-hour ceiling drops them. Five turns is minutes of
// real use, so the watchdog is the fast path to that same relief; a threshold
// in the tens would make it slower than the backstop it is meant to beat.
//
// The premise is an adapter fact, and it is not currently declarable: the
// watchdog arms on any adapter declaring a HookInstall, while the "at least one
// receipt per turn" expectation comes from both current adapters happening to
// install a Stop hook. A future #1355 adapter that installs only, say, a
// permission hook would owe no receipt on a quiet turn and be convicted at this
// threshold. Surfacing the installed event set on agent.HookInstall is the fix
// when that adapter arrives; until then the env var is the escape hatch.
//
// One caveat the count deliberately carries rather than hides: silent turns are
// aggregated PER ADAPTER, while the excuses above are per session. A session
// already in flight at the instant consent is granted owes no receipt for the
// turn it is in, so N such sessions can donate N uncredited turns at once. Two
// things bound that. The watchdog seeds a session's rising-edge memory on first
// observation rather than counting it (see hook_liveness.go), which removes the
// large source — a daemon restart re-reading every persisted working session —
// entirely; and recovery is immediate on the first receipt with no turn
// boundary required, so a burst at grant time self-corrects on the next hook
// rather than latching.
//
// 0 (or a negative, which envInt rejects back to this default) disables the
// watchdog entirely — the kill switch, matching CacheBloatThreshold's.
const defaultHookSilentTurns = 5

// Config holds daemon-wide runtime configuration.
type Config struct {
	MaxSessionAge      time.Duration
	ReadySessionTTL    time.Duration
	YieldSweepInterval time.Duration
	PermissionMode     string

	// Cache-bloat detector knobs (issue #374), overridable via env vars.
	CacheBloatBaselineDays       int
	CacheBloatThreshold          float64
	CacheBloatVersionDeltaTokens int64
	CacheBloatMinTurns           int

	// HookSilentTurns is the hook-liveness watchdog's threshold (issue #1368),
	// overridable via IRRLICHT_HOOK_SILENT_TURNS. 0 disables the watchdog.
	HookSilentTurns int

	// HookReverifyInterval is the hook-entry re-verification cadence (#1372),
	// overridable via IRRLICHT_HOOK_REVERIFY_INTERVAL.
	HookReverifyInterval time.Duration
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
	yieldInterval := defaultYieldSweepInterval
	if raw := os.Getenv("IRRLICHT_YIELD_SWEEP_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			yieldInterval = parsed
		}
	}
	reverifyInterval := defaultHookReverifyInterval
	if raw := os.Getenv("IRRLICHT_HOOK_REVERIFY_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			reverifyInterval = parsed
		}
	}
	mode := PermissionModeAsk
	if os.Getenv("IRRLICHT_PERMISSION_MODE") == PermissionModeGrantAll {
		mode = PermissionModeGrantAll
	}
	return Config{
		MaxSessionAge:      defaultMaxSessionAge,
		ReadySessionTTL:    ttl,
		YieldSweepInterval: yieldInterval,
		PermissionMode:     mode,

		CacheBloatBaselineDays:       envInt("IRRLICHT_CACHE_BLOAT_BASELINE_DAYS", defaultCacheBloatBaselineDays),
		CacheBloatThreshold:          envFloat("IRRLICHT_CACHE_BLOAT_THRESHOLD", defaultCacheBloatThreshold),
		CacheBloatVersionDeltaTokens: int64(envInt("IRRLICHT_CACHE_BLOAT_VERSION_DELTA", defaultCacheBloatVersionDeltaToken)),
		CacheBloatMinTurns:           envInt("IRRLICHT_CACHE_BLOAT_MIN_TURNS", defaultCacheBloatMinTurns),

		HookSilentTurns:      envInt("IRRLICHT_HOOK_SILENT_TURNS", defaultHookSilentTurns),
		HookReverifyInterval: reverifyInterval,
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
