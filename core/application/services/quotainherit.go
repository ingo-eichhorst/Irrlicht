// Package services — quotainherit.go implements cross-account rate-limit
// inheritance. Subscriptions are OAuth-account-scoped, not CLI-scoped:
// once any first-party CLI surfaces a quota snapshot for an account, every
// wrapper session (Pi, OpenCode) backed by the same confirmed account can
// read it.
//
// See issue #309 for the original design context, and issue #1994 / epic
// #1977 §3.2 for a correction to it: sharing a snapshot requires a
// CONFIRMED account match on both sides, never an assumed one.
//
//   - OpenAI (ChatGPT Plus/Pro): Codex CLI emits rate_limits directly in
//     its transcripts. Both Codex's ~/.codex/auth.json (snake_case
//     `account_id`) and Pi's ~/.pi/agent/auth.json (camelCase `accountId`
//     under the `openai-codex` provider) expose the same identifier in
//     plaintext, letting us key the donor map exactly. OpenCode's
//     `openai-oauth` entry exposes the same identity via its JWT payload
//     (openCodeJWTAccountID).
//   - Anthropic (Claude.ai Pro/Max): Claude Code's statusline hook is the
//     only source, and its OAuth account lives in the macOS keychain, not
//     in a plaintext file this package can read. #1994 removed the
//     "global singleton" behavior that used to donate any Claude Code
//     session's snapshot to any Pi(anthropic) or OpenCode(anthropic-oauth)
//     wrapper regardless of account — with no anchor to confirm, a Claude
//     Code snapshot is retained for its own session only, and an Anthropic
//     wrapper session gets no inherited quota chip until a real anchor
//     exists (a separate, not-yet-scheduled ticket).
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

// Canonical provider keys, aliased from the domain layer (issue #1994) so
// this package, the adapters that stamp RateLimitSnapshot.Provider
// (claudecode/statusline.go, codex/parser.go), and the tailer-side mirror
// (core/pkg/tailer) all trace back to ONE literal per provider — a typo at
// any of those sites fails the build instead of silently creating a
// phantom bucket.
const (
	ProviderAnthropic = session.ProviderAnthropic
	ProviderOpenAI    = session.ProviderOpenAI

	// authFileName is the credential file name shared by Codex, Pi, and
	// OpenCode's on-disk auth stores (each under a different parent
	// directory).
	authFileName = "auth.json"
)

// AccountKey identifies a subscription bucket: the provider name plus a
// confirmed account anchor. There is no empty-AccountID sentinel — issue
// #1994 / epic #1977 §3.2 removed the "singleton donor" behavior that used
// to let an empty AccountID match any same-provider wrapper regardless of
// its own account. A key with an empty AccountID is simply unconfirmed and
// must never be treated as matching another unconfirmed key of the same
// provider: donorKey and recipientKey both return `ok=false` instead of an
// empty-AccountID key when no account anchor is available, so buildDonorMap
// and applyDonors never see one.
type AccountKey struct {
	Provider  string // one of the Provider* constants above
	AccountID string // a confirmed provider account identifier; never empty when ok==true
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
	home, ok := resolveHome(userHome)
	if !ok {
		return
	}

	donors := buildDonorMap(sessions, home)
	if len(donors) == 0 {
		return
	}

	applyDonors(sessions, home, donors)
}

// resolveHome returns the home directory to use for auth-file lookups:
// userHome verbatim when set (test injection), otherwise the real user
// home directory. ok is false when neither is available.
func resolveHome(userHome string) (home string, ok bool) {
	if userHome != "" {
		return userHome, true
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return h, true
}

// hasOwnRateLimit reports whether s already carries its own rate_limit
// snapshot (donor-eligible, or ProviderForSession's existence gate). A
// snapshot here can be identity evidence alone — see hasOwnQuotaData below
// for the narrower "already has real quota data" question applyDonors asks.
func hasOwnRateLimit(s *session.SessionState) bool {
	return s != nil && s.Metrics != nil && s.Metrics.RateLimit != nil
}

// hasOwnQuotaData reports whether s's own rate_limit snapshot carries actual
// quota data — time-windows or a credits balance — rather than only an
// identity stamp (Provider/AttributionQuality with neither Windows nor
// Credits populated). Issue #2005's Pi model_change handler stamps exactly
// that identity-only shape: it names the route Pi selected, not a confirmed
// account, so it has no quota to publish (issue #2005 §1.3). Gating
// applyDonors on hasOwnRateLimit instead of this would make that stamp block
// a matching account's real quota donation the instant a Pi session reports
// any route — regression-proven by
// TestInheritRateLimits_PiIdentityStampDoesNotBlockQuotaDonation, which fails
// against a plain hasOwnRateLimit gate and passes against this one. Applying
// a donor snapshot in that case replaces the identity-only stamp wholesale
// with the donor's own (fuller) stamp, so ProviderForSession keeps resolving
// correctly — see applyDonors. This changes only "does this recipient
// already have data worth keeping", never which account may donate to which
// (donorKey, recipientKey, and AccountKey equality are untouched) — issue
// #2005's non-goals name the latter as out of scope, not the former.
func hasOwnQuotaData(s *session.SessionState) bool {
	if !hasOwnRateLimit(s) {
		return false
	}
	rl := s.Metrics.RateLimit
	return len(rl.Windows) > 0 || rl.Credits != nil
}

// donorEntry pairs one account's rate_limit snapshot with the account
// identifier it was actually confirmed against. donors groups these by
// provider so applyDonors can check account equality as an explicit
// comparison (see below) rather than relying solely on map-key equality —
// deleting that comparison is this ticket's committed mutation fixture:
// tools/lib/quotainherit-account-equality-mutations_test.sh confirms
// TestInheritRateLimits_PiNotInheritsOnAccountMismatch goes red without it.
type donorEntry struct {
	accountID string
	snapshot  *session.RateLimitSnapshot
}

// buildDonorMap collects the freshest rate_limit snapshot per (provider,
// account) pair across sessions that have one, grouped by provider. Prefer
// the freshest snapshot per account when more than one session can donate
// for it (multiple Codex sessions on the same account, for example).
func buildDonorMap(sessions []*session.SessionState, home string) map[string][]donorEntry {
	freshest := map[AccountKey]*session.RateLimitSnapshot{}
	for _, s := range sessions {
		if !hasOwnRateLimit(s) {
			continue
		}
		key, ok := donorKey(s, home)
		if !ok {
			continue
		}
		current, exists := freshest[key]
		if !exists || s.Metrics.RateLimit.SampledAt > current.SampledAt {
			freshest[key] = s.Metrics.RateLimit
		}
	}
	if len(freshest) == 0 {
		return nil
	}
	grouped := make(map[string][]donorEntry, len(freshest))
	for key, snap := range freshest {
		grouped[key.Provider] = append(grouped[key.Provider], donorEntry{accountID: key.AccountID, snapshot: snap})
	}
	return grouped
}

// applyDonors copies each matching donor snapshot into recipient sessions
// that don't already have their own snapshot — its own data is more
// authoritative than inherited data. A recipient only ever receives a
// snapshot whose donor account matches its own confirmed account exactly;
// see the account-equality comparison below.
func applyDonors(sessions []*session.SessionState, home string, donors map[string][]donorEntry) {
	for _, s := range sessions {
		if s == nil || hasOwnQuotaData(s) {
			continue
		}
		key, ok := recipientKey(s, home)
		if !ok {
			continue
		}
		for _, d := range donors[key.Provider] {
			if d.accountID != key.AccountID {
				continue
			}
			if s.Metrics == nil {
				s.Metrics = &session.SessionMetrics{}
			}
			s.Metrics.RateLimit = d.snapshot
			break
		}
	}
}

// donorKey returns the AccountKey under which this session's snapshot can be
// donated to wrappers. Returns (_, false) when the adapter has no usable
// donor mapping (e.g. aider, bedrock, vertex paths) OR when the adapter's
// account anchor isn't confirmable.
func donorKey(s *session.SessionState, home string) (AccountKey, bool) {
	switch s.Adapter {
	case "claude-code":
		// No confirmed account anchor is readable for Claude Code — its
		// OAuth tokens live in the macOS keychain, not in a plaintext file
		// this package can read. Issue #1994 / epic #1977 §3.2 removed the
		// empty-AccountID "singleton" that used to stand in for one: without
		// a real anchor to confirm, this snapshot is retained for its own
		// session only and never donated.
		return AccountKey{}, false
	case "codex":
		if id := readCodexAccountID(home); id != "" {
			return AccountKey{Provider: ProviderOpenAI, AccountID: id}, true
		}
	}
	return AccountKey{}, false
}

// recipientKey returns the AccountKey this session needs a snapshot for, by
// reading the wrapper's own auth.json to determine which subscription it's
// authenticated to. Returns (_, false) when no inheritable provider is
// configured or confirmable.
//
// This is used only by the rate-limit inheritance path (InheritRateLimits),
// where a match still requires an exact account-id equality against a real
// donor (applyDonors) — configuration alone never grants a snapshot. It is
// deliberately NOT used to resolve a wrapper's own billing provider for cost
// attribution (see ProviderForSession): a configured credential list
// establishes what a wrapper *could* be authenticated to, not which provider
// a specific session's spend actually belongs to.
func recipientKey(s *session.SessionState, home string) (AccountKey, bool) {
	switch s.Adapter {
	case "pi":
		return readPiInheritKey(home)
	case "opencode":
		return readOpenCodeInheritKey(home)
	}
	return AccountKey{}, false
}

// ProviderForSession resolves the billing provider ("anthropic"/"openai", or
// "" when unknown) a session's cost should be attributed to.
//
// Evidence precedence (issue #1994): a native quota snapshot the agent
// itself emitted is confirmed evidence for that provider. An adapter name
// alone is not evidence — claude-code can also run against Bedrock or
// Vertex, which never emit the Anthropic consumer statusline snapshot, so
// the adapter string can't be trusted by itself. A configured credential
// list (auth.json) is not evidence either — it says what a session *could*
// be authenticated to, not what it actually used. No third evidence source
// exists yet, so every session without its own snapshot — including every
// pi/opencode wrapper session, which never emits one itself — resolves to
// an explicit unknown ("").
//
// There is no adapter-name switch here at all: this reads the snapshot's
// own Provider and AttributionQuality, stamped at the moment an adapter
// observed native evidence for it (claudecode/statusline.go for Claude
// Code's statusline hook, codex/parser.go for Codex's transcript
// rate_limits — see RateLimitSnapshot's doc comment,
// core/domain/session/rate_limit.go). The session's adapter string is
// never consulted, so a claude-code session running against Bedrock or
// Vertex — whose snapshot, if it had one, would carry no Provider stamp —
// cannot be misattributed to Anthropic by adapter name alone.
//
// A pi/opencode session that inherited a donor's snapshot via
// InheritRateLimits (applyDonors) carries that donor's stamp verbatim, so
// it resolves too. This is expected, not a regression: applyDonors only
// ever shares a snapshot after confirming the two sessions' accounts match
// (issue #1994 / epic #1977 §3.2), so a wrapper resolving here is reading
// back the SAME evidence the sharing path already required — on the
// snapshot's own stamp, not on a configured credential list.
func ProviderForSession(s *session.SessionState) string {
	if !hasOwnRateLimit(s) {
		return ""
	}
	rl := s.Metrics.RateLimit
	if rl.AttributionQuality != session.AttributionQualityConfirmed {
		return ""
	}
	return rl.Provider
}

// readCodexAccountID parses ~/.codex/auth.json and returns
// tokens.account_id when the auth_mode is "chatgpt" (the OAuth/
// subscription path). Returns "" for API-key users or any read/parse
// failure — the caller treats absence as "no donor available", which
// is the safe default.
func readCodexAccountID(home string) string {
	entry, ok := readAuthCache(filepath.Join(home, ".codex", authFileName), parseCodexAuth)
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

// readPiInheritKey parses ~/.pi/agent/auth.json. Pi keys each provider block
// by name (e.g. "openai-codex", "anthropic") and tags OAuth entries with
// `type: "oauth"` plus an `accountId` (camelCase).
//
// Only "openai-codex" is checked: an "anthropic" block has no account id to
// confirm against (see donorKey's claude-code case — #1994 / epic #1977
// §3.2), so it could never match a donor and is not read here.
func readPiInheritKey(home string) (AccountKey, bool) {
	entry, ok := readAuthCache(filepath.Join(home, ".pi", "agent", authFileName), parsePiAuth)
	if !ok {
		return AccountKey{}, false
	}
	if v, ok := entry.piDoc["openai-codex"]; ok && v.Type == "oauth" && v.AccountID != "" {
		return AccountKey{Provider: ProviderOpenAI, AccountID: v.AccountID}, true
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

// readOpenCodeInheritKey parses ~/.local/share/opencode/auth.json. OpenCode
// names its OpenAI OAuth provider block `openai-oauth` per its upstream
// docs; the account_id is recovered from the JWT access_token's payload via
// openCodeJWTAccountID.
//
// OpenCode's `anthropic-oauth` block is not read: it carries no account id
// to confirm against (see donorKey's claude-code case — #1994 / epic #1977
// §3.2), so it could never match a donor.
func readOpenCodeInheritKey(home string) (AccountKey, bool) {
	entry, ok := readAuthCache(filepath.Join(home, ".local", "share", "opencode", authFileName), parseOpenCodeAuth)
	if !ok {
		return AccountKey{}, false
	}
	if v, ok := entry.openCodeDoc["openai-oauth"]; ok && v.Type == "oauth" {
		if entry.openCodeOpenAIAccount != "" {
			return AccountKey{Provider: ProviderOpenAI, AccountID: entry.openCodeOpenAIAccount}, true
		}
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
