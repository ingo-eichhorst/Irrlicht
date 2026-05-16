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
	"encoding/json"
	"os"
	"path/filepath"

	"irrlicht/core/domain/session"
)

// AccountKey identifies a subscription bucket: the provider name plus
// an account anchor (empty for the Anthropic singleton case).
type AccountKey struct {
	Provider  string // "anthropic" | "openai"
	AccountID string // empty when the provider's anchor isn't reachable in plaintext
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
		return AccountKey{Provider: "anthropic"}, true
	case "codex":
		if id := readCodexAccountID(home); id != "" {
			return AccountKey{Provider: "openai", AccountID: id}, true
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
	data, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		return ""
	}
	var doc struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if doc.AuthMode != "chatgpt" {
		return ""
	}
	return doc.Tokens.AccountID
}

// readPiInheritKey parses ~/.pi/agent/auth.json. Pi keys each provider
// block by name (e.g. "openai-codex", "anthropic") and tags OAuth
// entries with `type: "oauth"` plus an `accountId` (camelCase). We
// prefer OpenAI over Anthropic when both are configured; either choice
// is defensible for a single-provider Pi user.
func readPiInheritKey(home string) (AccountKey, bool) {
	data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "auth.json"))
	if err != nil {
		return AccountKey{}, false
	}
	var doc map[string]struct {
		Type      string `json:"type"`
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return AccountKey{}, false
	}
	if entry, ok := doc["openai-codex"]; ok && entry.Type == "oauth" && entry.AccountID != "" {
		return AccountKey{Provider: "openai", AccountID: entry.AccountID}, true
	}
	if entry, ok := doc["anthropic"]; ok && entry.Type == "oauth" {
		// Anthropic singleton — AccountID intentionally empty so the
		// key matches Claude Code's donor entry.
		_ = entry
		return AccountKey{Provider: "anthropic"}, true
	}
	return AccountKey{}, false
}

// readOpenCodeInheritKey parses ~/.local/share/opencode/auth.json.
// OpenCode names OAuth providers `anthropic-oauth` and `openai-oauth`
// per its upstream docs; we map them onto irrlicht's canonical
// "anthropic" / "openai" provider keys. Account anchor isn't surfaced
// in the file we've seen — Anthropic always lands on the singleton
// key, OpenAI is best-effort (would need to read the JWT to recover
// the chatgpt account_id; out of scope for v1, so OpenCode→OpenAI
// just doesn't inherit until that lands).
func readOpenCodeInheritKey(home string) (AccountKey, bool) {
	data, err := os.ReadFile(filepath.Join(home, ".local", "share", "opencode", "auth.json"))
	if err != nil {
		return AccountKey{}, false
	}
	var doc map[string]struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return AccountKey{}, false
	}
	if entry, ok := doc["openai-oauth"]; ok && entry.Type == "oauth" {
		_ = entry
		// AccountID unavailable in the OpenCode auth file shape
		// observed so far — would need to decode the JWT access token
		// to recover it. Leave inheritance disabled for this branch
		// until we have data to verify the JWT extraction against.
		return AccountKey{}, false
	}
	if entry, ok := doc["anthropic-oauth"]; ok && entry.Type == "oauth" {
		_ = entry
		return AccountKey{Provider: "anthropic"}, true
	}
	return AccountKey{}, false
}
