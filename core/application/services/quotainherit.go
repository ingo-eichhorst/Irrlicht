// Package services — quotainherit.go implements cross-account rate-limit
// inheritance. Subscriptions are OAuth-account-scoped, not CLI-scoped:
// once any first-party CLI surfaces a quota snapshot for an account, every
// wrapper session (Pi, OpenCode) backed by the same account can read it.
//
// See issue #309 for the design context and supported matrix:
//
//   - Anthropic (Claude.ai Pro/Max): Claude Code's statusline hook is the
//     only source. Account anchor lives in macOS keychain — irrlicht treats
//     the snapshot as a global singleton, donating from any Claude Code
//     session with rate_limit data to any Pi(anthropic) or
//     OpenCode(anthropic-oauth) wrapper session that doesn't have one.
//   - OpenAI (ChatGPT Plus/Pro): Codex CLI emits rate_limits directly in
//     its transcripts. Both Codex's ~/.codex/auth.json (snake_case
//     `account_id`) and Pi's ~/.pi/agent/auth.json (camelCase `accountId`
//     under the `openai-codex` provider) expose the same identifier in
//     plaintext, letting us key the donor map exactly.
//
// Wrappers without a matching first-party donor session keep their
// existing (usually empty) rate_limit. The inheritance pass is a one-way
// copy: it never overwrites a session that already has its own snapshot.
package services

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/session"
)

// authFileCache memoizes parsed auth-file lookups by path so the
// inheritance pass doesn't reread three small JSON files on every
// `/api/v1/sessions` hit. Validated by mtime: a stale entry is dropped
// when the file's modification time changes. Concurrent readers are
// fine — the cache is keyed by path and writes are coordinated by mu.
//
// Entries are kept indefinitely; the working set is at most three
// files (~/.codex, ~/.pi/agent, ~/.local/share/opencode), so there's
// no eviction need.
var authFileCache struct {
	mu      sync.Mutex
	entries map[string]authCacheEntry
}

type authCacheEntry struct {
	// missing is true when the file isn't present (negative cache);
	// mtime and the parsed-doc fields below are then meaningless.
	// Negative caching keeps `/api/v1/sessions` from re-stat'ing
	// non-existent paths on every hit for users who don't have all
	// three wrapper CLIs installed (the common case).
	missing bool
	mtime   time.Time
	// One of the parsed-doc fields below is populated, depending on
	// which reader filled the entry. Distinguishing by path keeps the
	// readers type-safe without a generic any-typed payload.
	codexAccountID        string
	piDoc                 map[string]piAuthEntry
	openCodeDoc           map[string]openCodeAuthEntry
	openCodeOpenAIAccount string // extracted from JWT on parse, "" if absent/invalid
}

type piAuthEntry struct {
	Type      string `json:"type"`
	AccountID string `json:"accountId"`
}

type openCodeAuthEntry struct {
	Type        string `json:"type"`
	AccessToken string `json:"access_token"`
}

// readAuthCache returns the parsed entry for path, populating the
// cache via parse if the file is new or has been modified since the
// last read. Returns (zero, false) when the file is missing or parses
// fail. Negative results (missing file) are cached so subsequent
// calls don't restat — a user without all three wrappers installed
// hits this path on every `/api/v1/sessions`.
func readAuthCache(path string, parse func([]byte) (authCacheEntry, bool)) (authCacheEntry, bool) {
	authFileCache.mu.Lock()
	defer authFileCache.mu.Unlock()
	if authFileCache.entries == nil {
		authFileCache.entries = map[string]authCacheEntry{}
	}
	stat, err := os.Stat(path)
	if err != nil {
		// Negative cache: remember the absence. If the file appears
		// later, the next Stat will succeed and the cached `missing`
		// entry gets overwritten below.
		authFileCache.entries[path] = authCacheEntry{missing: true}
		return authCacheEntry{}, false
	}
	if entry, ok := authFileCache.entries[path]; ok && !entry.missing && entry.mtime.Equal(stat.ModTime()) {
		return entry, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return authCacheEntry{}, false
	}
	entry, ok := parse(data)
	if !ok {
		return authCacheEntry{}, false
	}
	entry.mtime = stat.ModTime()
	authFileCache.entries[path] = entry
	return entry, true
}

// Canonical provider keys. Centralised so a typo at any inheritance
// site fails the build instead of silently creating a phantom bucket.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// AccountKey identifies a subscription bucket: the provider name plus
// an account anchor. An empty `AccountID` is a sentinel meaning
// "singleton donor for this provider, no account-level disambiguation"
// — currently only Anthropic uses this because Claude Code stores its
// OAuth tokens in the macOS keychain rather than in a plaintext file
// we can read.
//
// Footgun warning: a future provider whose anchor isn't reachable in
// plaintext would, if added with `AccountID == ""`, silently start
// inheriting from / donating to every other empty-AccountID provider
// of the same name. Always either populate `AccountID` from the
// account anchor, or document the singleton intent explicitly (and
// gate it via IsSingleton below). See issue #309.
type AccountKey struct {
	Provider  string // one of the Provider* constants above
	AccountID string // empty only for the documented singleton case (currently Anthropic)
}

// IsSingleton reports whether this key is the no-account-anchor
// sentinel — used by the donor map to match any wrapper recipient of
// the same provider regardless of the wrapper's own account hint.
func (k AccountKey) IsSingleton() bool {
	return k.AccountID == ""
}

// InheritRateLimits walks the given sessions, builds a donor map of
// rate_limit snapshots from sessions that have one, then copies the
// matching donor snapshot into wrapper sessions that don't. Mutates
// each recipient's Metrics in place; non-matching sessions are
// untouched.
//
// `userHome` lets tests pin a synthetic HOME directory without
// monkeypatching os.UserHomeDir; production callers pass "" to use the
// real home.
func InheritRateLimits(sessions []*session.SessionState, userHome string) {
	home := userHome
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return
		}
		home = h
	}

	// Build donor map. Prefer the freshest snapshot per key when more
	// than one session can donate (multiple Claude Code sessions all
	// share the anthropic singleton, for example).
	donors := map[AccountKey]*session.RateLimitSnapshot{}
	for _, s := range sessions {
		if s == nil || s.Metrics == nil || s.Metrics.RateLimit == nil {
			continue
		}
		key, ok := donorKey(s, home)
		if !ok {
			continue
		}
		current, exists := donors[key]
		if !exists || s.Metrics.RateLimit.SampledAt > current.SampledAt {
			donors[key] = s.Metrics.RateLimit
		}
	}
	if len(donors) == 0 {
		return
	}

	// Apply to recipients. Skip any session that already has a snapshot
	// — its own data is more authoritative than inherited data.
	for _, s := range sessions {
		if s == nil {
			continue
		}
		if s.Metrics != nil && s.Metrics.RateLimit != nil {
			continue
		}
		key, ok := recipientKey(s, home)
		if !ok {
			continue
		}
		donor, found := donors[key]
		if !found {
			continue
		}
		if s.Metrics == nil {
			s.Metrics = &session.SessionMetrics{}
		}
		s.Metrics.RateLimit = donor
	}
}

// donorKey returns the AccountKey under which this session's snapshot
// can be donated to wrappers. Returns (_, false) when the adapter has
// no usable donor mapping (e.g. aider, bedrock, vertex paths).
func donorKey(s *session.SessionState, home string) (AccountKey, bool) {
	switch s.Adapter {
	case "claude-code":
		// Anthropic singleton: account anchor isn't on disk for Claude
		// Code (lives in keychain), so we deliberately leave AccountID
		// empty. Wrappers' recipientKey returns the matching singleton
		// shape, so the lookup still hits.
		return AccountKey{Provider: ProviderAnthropic}, true
	case "codex":
		if id := readCodexAccountID(home); id != "" {
			return AccountKey{Provider: ProviderOpenAI, AccountID: id}, true
		}
	}
	return AccountKey{}, false
}

// recipientKey returns the AccountKey this session needs a snapshot
// for, by reading the wrapper's own auth.json to determine which
// subscription it's authenticated to. Returns (_, false) when no
// inheritable provider is configured.
//
// Pi and OpenCode both let a user configure several providers at once.
// We pick OpenAI over Anthropic when both are present — empirically,
// users who run both first-party CLIs tend to use the wrappers for
// OpenAI overflow rather than Anthropic. Either choice is defensible
// for a v1; the user-visible behaviour is identical for the common
// single-provider case.
func recipientKey(s *session.SessionState, home string) (AccountKey, bool) {
	switch s.Adapter {
	case "pi":
		return readPiInheritKey(home)
	case "opencode":
		return readOpenCodeInheritKey(home)
	}
	return AccountKey{}, false
}

// readCodexAccountID parses ~/.codex/auth.json and returns
// tokens.account_id when the auth_mode is "chatgpt" (the OAuth/
// subscription path). Returns "" for API-key users or any read/parse
// failure — the caller treats absence as "no donor available", which
// is the safe default.
func readCodexAccountID(home string) string {
	entry, ok := readAuthCache(filepath.Join(home, ".codex", "auth.json"), parseCodexAuth)
	if !ok {
		return ""
	}
	return entry.codexAccountID
}

func parseCodexAuth(data []byte) (authCacheEntry, bool) {
	var doc struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return authCacheEntry{}, false
	}
	if doc.AuthMode != "chatgpt" {
		// Valid file, not a subscription user — cache the empty
		// account_id to avoid re-parsing on every request.
		return authCacheEntry{codexAccountID: ""}, true
	}
	return authCacheEntry{codexAccountID: doc.Tokens.AccountID}, true
}

// readPiInheritKey parses ~/.pi/agent/auth.json. Pi keys each provider
// block by name (e.g. "openai-codex", "anthropic") and tags OAuth
// entries with `type: "oauth"` plus an `accountId` (camelCase). We
// prefer OpenAI over Anthropic when both are configured; either choice
// is defensible for a single-provider Pi user.
func readPiInheritKey(home string) (AccountKey, bool) {
	entry, ok := readAuthCache(filepath.Join(home, ".pi", "agent", "auth.json"), parsePiAuth)
	if !ok {
		return AccountKey{}, false
	}
	if v, ok := entry.piDoc["openai-codex"]; ok && v.Type == "oauth" && v.AccountID != "" {
		return AccountKey{Provider: ProviderOpenAI, AccountID: v.AccountID}, true
	}
	if v, ok := entry.piDoc["anthropic"]; ok && v.Type == "oauth" {
		// Anthropic singleton — AccountID intentionally empty so the
		// key matches Claude Code's donor entry.
		return AccountKey{Provider: ProviderAnthropic}, true
	}
	return AccountKey{}, false
}

func parsePiAuth(data []byte) (authCacheEntry, bool) {
	var doc map[string]piAuthEntry
	if err := json.Unmarshal(data, &doc); err != nil {
		return authCacheEntry{}, false
	}
	return authCacheEntry{piDoc: doc}, true
}

// readOpenCodeInheritKey parses ~/.local/share/opencode/auth.json.
// OpenCode names OAuth providers `anthropic-oauth` and `openai-oauth`
// per its upstream docs; we map them onto irrlicht's canonical
// "anthropic" / "openai" provider keys. Anthropic uses a singleton key
// (no account anchor). OpenAI account_id is recovered from the JWT
// access_token's payload via openCodeJWTAccountID.
func readOpenCodeInheritKey(home string) (AccountKey, bool) {
	entry, ok := readAuthCache(filepath.Join(home, ".local", "share", "opencode", "auth.json"), parseOpenCodeAuth)
	if !ok {
		return AccountKey{}, false
	}
	if v, ok := entry.openCodeDoc["openai-oauth"]; ok && v.Type == "oauth" {
		if entry.openCodeOpenAIAccount != "" {
			return AccountKey{Provider: ProviderOpenAI, AccountID: entry.openCodeOpenAIAccount}, true
		}
	}
	if v, ok := entry.openCodeDoc["anthropic-oauth"]; ok && v.Type == "oauth" {
		return AccountKey{Provider: ProviderAnthropic}, true
	}
	return AccountKey{}, false
}

func parseOpenCodeAuth(data []byte) (authCacheEntry, bool) {
	var doc map[string]openCodeAuthEntry
	if err := json.Unmarshal(data, &doc); err != nil {
		return authCacheEntry{}, false
	}
	entry := authCacheEntry{openCodeDoc: doc}
	if v, ok := doc["openai-oauth"]; ok {
		entry.openCodeOpenAIAccount = openCodeJWTAccountID(v.AccessToken)
	}
	return entry, true
}

// openCodeJWTAccountID extracts https://api.openai.com/auth.chatgpt_account_id
// from the payload segment of an OpenID Connect access token. The signature is
// not verified — we only need the identity claim.
func openCodeJWTAccountID(token string) string {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	raw, ok := claims["https://api.openai.com/auth.chatgpt_account_id"]
	if !ok {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return ""
	}
	return id
}
