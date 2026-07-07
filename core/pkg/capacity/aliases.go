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

// Canonical model IDs referenced repeatedly by the alias map below. Named so
// SonarQube's string-literal-duplication check (go:S1192) doesn't flag the
// repeated values, and so a rename touches one line instead of every entry
// that resolves to it.
const (
	modelClaudeOpus46        = "claude-opus-4-6"
	modelClaudeSonnet46      = "claude-sonnet-4-6"
	modelClaudeOpus45        = "claude-opus-4-5"
	modelClaudeSonnet45      = "claude-sonnet-4-5"
	modelClaudeHaiku45       = "claude-haiku-4-5"
	modelClaudeOpus47        = "claude-opus-4-7"
	modelKimiK2Thinking      = "moonshot.kimi-k2-thinking"
	modelGemini31ProPreview  = "gemini-3.1-pro-preview"
	modelGemini3FlashPreview = "gemini-3-flash-preview"
	modelGemini35Flash       = "gemini-3.5-flash"
	modelGPT53Codex          = "gpt-5.3-codex"
)

var modelAliases = map[string]string{
	// OMP / SAP AI Core — double-dash provider prefix, dot-version, tier-last.
	"anthropic--claude-4.6-opus":   modelClaudeOpus46,
	"anthropic--claude-4.6-sonnet": modelClaudeSonnet46,
	"anthropic--claude-4.5-opus":   modelClaudeOpus45,
	"anthropic--claude-4.5-sonnet": modelClaudeSonnet45,
	"anthropic--claude-4.5-haiku":  modelClaudeHaiku45,

	// Anthropic dot-version forms emitted by various wrappers.
	"claude-sonnet-4.6": modelClaudeSonnet46,
	"claude-sonnet-4.5": modelClaudeSonnet45,
	"claude-opus-4.7":   modelClaudeOpus47,
	"claude-opus-4.6":   modelClaudeOpus46,
	"claude-opus-4.5":   modelClaudeOpus45,

	// Auto routers — frontends that pick a model on the user's behalf.
	// Default-route to current Sonnet (codeburn's choice; refresh if upstream
	// re-points these).
	"cursor-auto":            modelClaudeSonnet45,
	"cursor-agent-auto":      modelClaudeSonnet45,
	"copilot-auto":           modelClaudeSonnet45,
	"copilot-openai-auto":    "gpt-5", // LOCAL_OVERRIDE: codeburn → gpt-5.3-codex (not in LiteLLM). Best-guess fallback; Copilot's actual OpenAI route is unverified and likely a different gen — pricing for this alias may be inaccurate (probably under-priced) until LiteLLM ships codex/route-specific entries.
	"copilot-anthropic-auto": modelClaudeSonnet45,
	"ibm-bob-auto":           modelClaudeSonnet45,
	"kiro-auto":              modelClaudeSonnet45,
	"cline-auto":             modelClaudeSonnet45,
	"openclaw-auto":          modelClaudeSonnet45,
	"qwen-auto":              modelClaudeSonnet45,

	// Cursor — dot-version tier-last names plus tier/reasoning suffixes
	// (-high, -low, -medium, -thinking, -high-thinking, -fast-mode) that
	// LiteLLM does not index. Sources: Cursor's public model docs and forum
	// posts quoting literal slugs.
	"claude-4-sonnet":                 "claude-sonnet-4-20250514", // LOCAL_OVERRIDE: codeburn → claude-sonnet-4; LiteLLM only ships the date-suffixed key.
	"claude-4-sonnet-1m":              "claude-sonnet-4-20250514", // LOCAL_OVERRIDE: codeburn → claude-sonnet-4; LiteLLM only ships the date-suffixed key.
	"claude-4-sonnet-thinking":        modelClaudeSonnet45,
	"claude-4.5-sonnet":               modelClaudeSonnet45,
	"claude-4.5-sonnet-thinking":      modelClaudeSonnet45,
	"claude-4.6-sonnet":               modelClaudeSonnet46,
	"claude-4.6-sonnet-high":          modelClaudeSonnet46,
	"claude-4.6-sonnet-low":           modelClaudeSonnet46,
	"claude-4.6-sonnet-thinking":      modelClaudeSonnet46,
	"claude-4.6-sonnet-high-thinking": modelClaudeSonnet46,
	"claude-4-opus":                   "claude-opus-4-20250514", // LOCAL_OVERRIDE: codeburn → claude-opus-4; LiteLLM only ships the date-suffixed key.
	"claude-4.5-opus":                 modelClaudeOpus45,
	"claude-4.5-opus-high":            modelClaudeOpus45,
	"claude-4.5-opus-low":             modelClaudeOpus45,
	"claude-4.5-opus-medium":          modelClaudeOpus45,
	"claude-4.5-opus-high-thinking":   modelClaudeOpus45,
	"claude-4.6-opus":                 modelClaudeOpus46,
	"claude-4.6-opus-fast-mode":       modelClaudeOpus46,
	"claude-4.6-opus-high":            modelClaudeOpus46,
	"claude-4.6-opus-low":             modelClaudeOpus46,
	"claude-4.6-opus-medium":          modelClaudeOpus46,
	"claude-4.6-opus-high-thinking":   modelClaudeOpus46,
	"claude-4.7-opus":                 modelClaudeOpus47,
	// Dash form (not dot) — separate Cursor codepath.
	"claude-opus-4-7-thinking-high": modelClaudeOpus47,
	"claude-4.5-haiku":              modelClaudeHaiku45,
	"claude-4.6-haiku":              modelClaudeHaiku45,

	// Cursor in-house Composer family — no LiteLLM entry; price as the
	// underlying Sonnet generation per Cursor's docs.
	"composer-1":   modelClaudeSonnet45,
	"composer-1.5": modelClaudeSonnet45,
	"composer-2":   modelClaudeSonnet46,

	// Cursor / OpenAI variants of GPT-5 not (yet) tracked in LiteLLM.
	// gpt-4.1 is omitted (self-alias in codeburn; no-op for us — refresh
	// skill will pull it back if codeburn's normalization changes).
	"gpt-5-fast":         "gpt-5",
	"gpt-5.2-low":        "gpt-5",
	"gpt-5.1-codex-high": "gpt-5.1", // LOCAL_OVERRIDE: codeburn → gpt-5.3-codex (not in LiteLLM). Version-aligned with the input slug, but the codex / high-reasoning tier is unpriced — this under-prices by whatever premium Cursor's codex tier carries over base 5.1.
	// OpenAI Codex frontend prefixes the model id; gpt-5.5 is in LiteLLM.
	"openai-codex:gpt-5.5": "gpt-5.5",

	// Moonshot Kimi family — codeburn maps three coding-focused aliases to
	// "kimi-k2-thinking", which LiteLLM ships only under the dotted
	// `moonshot.kimi-k2-thinking` key.
	"kimi-auto":       modelKimiK2Thinking, // LOCAL_OVERRIDE: codeburn → kimi-k2-thinking (not in LiteLLM).
	"kimi-code":       modelKimiK2Thinking, // LOCAL_OVERRIDE: codeburn → kimi-k2-thinking (not in LiteLLM).
	"kimi-for-coding": modelKimiK2Thinking, // LOCAL_OVERRIDE: codeburn → kimi-k2-thinking (not in LiteLLM).

	// Antigravity Gemini IDs resolve to preview-priced entries. The conversation
	// store also emits ids the transcript never shows: "gemini-pro-default" (the
	// unpinned default Pro) and "gemini-3-flash-a" (the short agent-flash form).
	// All gemini-3.x share the 1M context window, so the context bar resolves
	// regardless of which preview tier they price as.
	"gemini-3.1-pro":         modelGemini31ProPreview,
	"gemini-3-flash":         modelGemini3FlashPreview,
	"gemini-3.1-pro-high":    modelGemini31ProPreview,
	"gemini-3.1-pro-low":     modelGemini31ProPreview,
	"gemini-3-flash-agent":   modelGemini3FlashPreview,
	"gemini-3-flash-a":       modelGemini3FlashPreview,
	"gemini-pro-default":     modelGemini31ProPreview,
	"gemini-3-pro":           "gemini-3-pro-preview",
	"gemini-3.1-flash-image": "gemini-3.1-flash-image-preview",
	"gemini-3.1-flash-lite":  "gemini-3.1-flash-lite-preview",
	// Antigravity Gemini 3.5 Flash — dash-form reasoning suffixes plus the
	// human-readable display-name forms; price as the base flash entry.
	"gemini-3.5-flash-high":     modelGemini35Flash,
	"gemini-3.5-flash-medium":   modelGemini35Flash,
	"gemini-3.5-flash-low":      modelGemini35Flash,
	"Gemini 3.5 Flash (High)":   modelGemini35Flash,
	"Gemini 3.5 Flash (Medium)": modelGemini35Flash,
	"Gemini 3.5 Flash (Low)":    modelGemini35Flash,
	// Antigravity also routes OpenAI's open gpt-oss models; the reasoning-effort
	// suffix ("-medium") rides on the base id. Resolve to the LiteLLM key for the
	// 128k context window.
	"gpt-oss-120b-medium": "openai.gpt-oss-120b-1:0",

	// Warp — auto-router slugs plus the literal "GPT-5.3 Codex (… reasoning)"
	// display strings Warp emits. codeburn maps the codex tier to
	// "gpt-5.3-codex", which LiteLLM does not yet price — these resolve to a
	// zero-value capacity and log on miss until LiteLLM ships the key.
	"warp-auto-efficient":                  modelGPT53Codex,
	"warp-auto-powerful":                   modelClaudeOpus46,
	"GPT-5.3 Codex (low reasoning)":        modelGPT53Codex,
	"GPT-5.3 Codex (medium reasoning)":     modelGPT53Codex,
	"GPT-5.3 Codex (high reasoning)":       modelGPT53Codex,
	"GPT-5.3 Codex (extra high reasoning)": modelGPT53Codex,

	// Human-readable display-name forms emitted by some frontends.
	"Claude Sonnet 4.6": modelClaudeSonnet46,
	"Claude Sonnet 4.5": modelClaudeSonnet45,
	"Claude Haiku 4.5":  modelClaudeHaiku45,
	"Claude Opus 4.6":   modelClaudeOpus46,

	// Cursor dash-form reasoning suffixes (tier-last): -high/-low/-medium/
	// -high-fast and the opus -xhigh tier. Dot-form equivalents live above.
	"claude-4-6-sonnet-high":      modelClaudeSonnet46,
	"claude-4-6-sonnet-low":       modelClaudeSonnet46,
	"claude-4-6-sonnet-medium":    modelClaudeSonnet46,
	"claude-4-6-sonnet-high-fast": modelClaudeSonnet46,
	"claude-4-7-opus-xhigh":       modelClaudeOpus47,
	"claude-4-7-opus-xhigh-fast":  modelClaudeOpus47,

	// Zhipu GLM — codeburn maps "GLM-5.2" to "glm-5p1", which LiteLLM does not
	// ship (it carries zai.glm-5, with no context window populated yet). Kept
	// as codeburn's canonical; resolves to a zero-value capacity and logs the
	// mapping on miss until LiteLLM prices it.
	"GLM-5.2": "glm-5p1",
	// Hermes Agent stores the same id lowercased in its sessions table, so it
	// misses the capitalized alias above; map the lowercase spelling too.
	"glm-5.2": "glm-5p1",

	// xAI Grok — codeburn's "grok-build-0.1" canonical isn't in LiteLLM (no
	// grok-build pricing yet). Zero-value capacity + log on miss until the key
	// lands upstream.
	"grok-build": "grok-build-0.1",
}
