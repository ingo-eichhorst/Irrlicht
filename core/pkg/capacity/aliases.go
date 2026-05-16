package capacity

// modelAliases maps frontend-rewritten model strings to canonical LiteLLM keys
// so sessions running through Cursor, OMP, Antigravity, GitHub Copilot, and
// other frontends price correctly instead of falling through to $0.
//
// Upstream reference: codeburn's BUILTIN_ALIASES at
// https://github.com/getagentseal/codeburn/blob/main/src/models.ts. Re-sync via
// the /ir:refresh-aliases skill when codeburn adds new entries — the canonical
// targets must also exist in the LiteLLM pricing table the daemon fetches at
// runtime, otherwise the alias still resolves to a zero-value capacity.
//
// Exact-match only: no prefix/fuzzy logic. Resolution happens inside
// GetModelCapacity before the existing lookup. When the canonical isn't in
// LiteLLM either, the daemon logs the alias → canonical mapping on miss so
// the gap is observable.
//
// Entries trailing with `// LOCAL_OVERRIDE: <reason>` intentionally deviate
// from codeburn's BUILTIN_ALIASES because the codeburn-chosen canonical isn't
// in LiteLLM's pricing table. The /ir:refresh-aliases skill recognizes the
// marker and does not flag these as upstream "Changed" on each sync.
var modelAliases = map[string]string{
	// OMP / SAP AI Core — double-dash provider prefix, dot-version, tier-last.
	"anthropic--claude-4.6-opus":   "claude-opus-4-6",
	"anthropic--claude-4.6-sonnet": "claude-sonnet-4-6",
	"anthropic--claude-4.5-opus":   "claude-opus-4-5",
	"anthropic--claude-4.5-sonnet": "claude-sonnet-4-5",
	"anthropic--claude-4.5-haiku":  "claude-haiku-4-5",

	// Anthropic dot-version forms emitted by various wrappers.
	"claude-sonnet-4.6": "claude-sonnet-4-6",
	"claude-sonnet-4.5": "claude-sonnet-4-5",
	"claude-opus-4.7":   "claude-opus-4-7",
	"claude-opus-4.6":   "claude-opus-4-6",
	"claude-opus-4.5":   "claude-opus-4-5",

	// Auto routers — frontends that pick a model on the user's behalf.
	// Default-route to current Sonnet (codeburn's choice; refresh if upstream
	// re-points these).
	"cursor-auto":            "claude-sonnet-4-5",
	"cursor-agent-auto":      "claude-sonnet-4-5",
	"copilot-auto":           "claude-sonnet-4-5",
	"copilot-openai-auto":    "gpt-5", // LOCAL_OVERRIDE: codeburn → gpt-5.3-codex (not in LiteLLM). Best-guess fallback; Copilot's actual OpenAI route is unverified and likely a different gen — pricing for this alias may be inaccurate (probably under-priced) until LiteLLM ships codex/route-specific entries.
	"copilot-anthropic-auto": "claude-sonnet-4-5",
	"ibm-bob-auto":           "claude-sonnet-4-5",
	"kiro-auto":              "claude-sonnet-4-5",
	"cline-auto":             "claude-sonnet-4-5",
	"openclaw-auto":          "claude-sonnet-4-5",
	"qwen-auto":              "claude-sonnet-4-5",

	// Cursor — dot-version tier-last names plus tier/reasoning suffixes
	// (-high, -low, -medium, -thinking, -high-thinking, -fast-mode) that
	// LiteLLM does not index. Sources: Cursor's public model docs and forum
	// posts quoting literal slugs.
	"claude-4-sonnet":                 "claude-sonnet-4-20250514", // LOCAL_OVERRIDE: codeburn → claude-sonnet-4; LiteLLM only ships the date-suffixed key.
	"claude-4-sonnet-1m":              "claude-sonnet-4-20250514", // LOCAL_OVERRIDE: codeburn → claude-sonnet-4; LiteLLM only ships the date-suffixed key.
	"claude-4-sonnet-thinking":        "claude-sonnet-4-5",
	"claude-4.5-sonnet":               "claude-sonnet-4-5",
	"claude-4.5-sonnet-thinking":      "claude-sonnet-4-5",
	"claude-4.6-sonnet":               "claude-sonnet-4-6",
	"claude-4.6-sonnet-high":          "claude-sonnet-4-6",
	"claude-4.6-sonnet-low":           "claude-sonnet-4-6",
	"claude-4.6-sonnet-thinking":      "claude-sonnet-4-6",
	"claude-4.6-sonnet-high-thinking": "claude-sonnet-4-6",
	"claude-4-opus":                   "claude-opus-4-20250514", // LOCAL_OVERRIDE: codeburn → claude-opus-4; LiteLLM only ships the date-suffixed key.
	"claude-4.5-opus":                 "claude-opus-4-5",
	"claude-4.5-opus-high":            "claude-opus-4-5",
	"claude-4.5-opus-low":             "claude-opus-4-5",
	"claude-4.5-opus-medium":          "claude-opus-4-5",
	"claude-4.5-opus-high-thinking":   "claude-opus-4-5",
	"claude-4.6-opus":                 "claude-opus-4-6",
	"claude-4.6-opus-fast-mode":       "claude-opus-4-6",
	"claude-4.6-opus-high":            "claude-opus-4-6",
	"claude-4.6-opus-low":             "claude-opus-4-6",
	"claude-4.6-opus-medium":          "claude-opus-4-6",
	"claude-4.6-opus-high-thinking":   "claude-opus-4-6",
	"claude-4.7-opus":                 "claude-opus-4-7",
	// Dash form (not dot) — separate Cursor codepath.
	"claude-opus-4-7-thinking-high": "claude-opus-4-7",
	"claude-4.5-haiku":              "claude-haiku-4-5",
	"claude-4.6-haiku":              "claude-haiku-4-5",

	// Cursor in-house Composer family — no LiteLLM entry; price as the
	// underlying Sonnet generation per Cursor's docs.
	"composer-1":   "claude-sonnet-4-5",
	"composer-1.5": "claude-sonnet-4-5",
	"composer-2":   "claude-sonnet-4-6",

	// Cursor / OpenAI variants of GPT-5 not (yet) tracked in LiteLLM.
	// gpt-4.1 is omitted (self-alias in codeburn; no-op for us — refresh
	// skill will pull it back if codeburn's normalization changes).
	"gpt-5-fast":         "gpt-5",
	"gpt-5.2-low":        "gpt-5",
	"gpt-5.1-codex-high": "gpt-5.1", // LOCAL_OVERRIDE: codeburn → gpt-5.3-codex (not in LiteLLM). Version-aligned with the input slug, but the codex / high-reasoning tier is unpriced — this under-prices by whatever premium Cursor's codex tier carries over base 5.1.

	// Moonshot Kimi family — codeburn maps three coding-focused aliases to
	// "kimi-k2-thinking", which LiteLLM ships only under the dotted
	// `moonshot.kimi-k2-thinking` key.
	"kimi-auto":       "moonshot.kimi-k2-thinking", // LOCAL_OVERRIDE: codeburn → kimi-k2-thinking (not in LiteLLM).
	"kimi-code":       "moonshot.kimi-k2-thinking", // LOCAL_OVERRIDE: codeburn → kimi-k2-thinking (not in LiteLLM).
	"kimi-for-coding": "moonshot.kimi-k2-thinking", // LOCAL_OVERRIDE: codeburn → kimi-k2-thinking (not in LiteLLM).

	// Antigravity Gemini IDs resolve to preview-priced entries.
	"gemini-3.1-pro":         "gemini-3.1-pro-preview",
	"gemini-3-flash":         "gemini-3-flash-preview",
	"gemini-3.1-pro-high":    "gemini-3.1-pro-preview",
	"gemini-3.1-pro-low":     "gemini-3.1-pro-preview",
	"gemini-3-flash-agent":   "gemini-3-flash-preview",
	"gemini-3-pro":           "gemini-3-pro-preview",
	"gemini-3.1-flash-image": "gemini-3.1-flash-image-preview",
	"gemini-3.1-flash-lite":  "gemini-3.1-flash-lite-preview",
}
