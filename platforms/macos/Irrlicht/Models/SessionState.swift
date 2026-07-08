import AppKit
import Foundation
import SwiftUI
import os

/// Verbose per-message/per-render debug logging, enabled with `IRRLICHT_DEBUG=1`.
/// A global initializes lazily exactly once, so the hot decode/render path — which
/// runs on every WebSocket push — reads a cached flag instead of re-checking the
/// environment per call (#690).
let irrlichtVerboseLogging = ProcessInfo.processInfo.environment["IRRLICHT_DEBUG"] == "1"

/// Branding for one inbound agent adapter, served by the daemon's
/// GET /api/v1/agents endpoint. Co-locating display name + icon SVGs with
/// the Go adapter (issue #260) lets a Go-only contributor add a new adapter
/// without touching Swift — `SessionState.adapterName` / `adapterIcon` look
/// the entry up dynamically.
struct AgentBranding: Decodable {
    let name: String
    let displayName: String
    let iconSVGLight: String
    let iconSVGDark: String
    /// Backchannel presets this agent supports (issue #754), e.g. ["compact"].
    /// Optional with a default so an older daemon that omits the field still
    /// decodes (and call sites that predate it still build).
    var presets: [String]? = nil

    /// Non-nil accessor for the supported preset ids.
    var supportedPresets: [String] { presets ?? [] }

    enum CodingKeys: String, CodingKey {
        case name
        case displayName = "display_name"
        case iconSVGLight = "icon_svg_light"
        case iconSVGDark = "icon_svg_dark"
        case presets
    }
}

/// File-scope holder for the adapter branding registry. Marked
/// `@MainActor` so the compiler enforces single-threaded access — writes
/// come from `SessionManager.hydrateAgents()` (already on main) and reads
/// come from `SessionState.adapterName` / `adapterIcon` (callers are all
/// SwiftUI views and other MainActor code, so we annotate those properties
/// MainActor too). This is the Swift-6 strict-concurrency-clean shape;
/// it costs nothing under Swift 5 mode and avoids a `nonisolated(unsafe)`
/// hatch later.
@MainActor
enum AgentRegistry {
    static var byName: [String: AgentBranding] = [:]
}

/// A single item in the Claude Code task list, derived from TaskCreate / TaskUpdate tool calls.
struct SessionTask: Codable, Hashable {
    let id: String
    let subject: String
    let description: String?
    let activeForm: String?
    let status: String  // "pending" | "in_progress" | "completed"

    enum CodingKeys: String, CodingKey {
        case id, subject, description, status
        case activeForm = "active_form"
    }

    var isCompleted: Bool { status == "completed" }
    var isInProgress: Bool { status == "in_progress" }

    var displayLabel: String {
        if isInProgress, let form = activeForm, !form.isEmpty {
            return form
        }
        return subject
    }
}

/// Subscription-quota window emitted by Anthropic / OpenAI subscription
/// providers. Two windows typically arrive together (5h primary, 7d
/// secondary). UsedPercent is the provider value as-is (may carry
/// floating-point noise like 14.000000000000002 — round at render time,
/// not at decode).
struct RateLimitWindowInfo: Codable, Hashable {
    let usedPercent: Double
    let windowMinutes: Int
    let resetsAt: Date

    enum CodingKeys: String, CodingKey {
        case usedPercent = "used_percent"
        case windowMinutes = "window_minutes"
        case resetsAt = "resets_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        usedPercent = try c.decode(Double.self, forKey: .usedPercent)
        windowMinutes = try c.decode(Int.self, forKey: .windowMinutes)
        let epoch = try c.decode(Double.self, forKey: .resetsAt)
        resetsAt = Date(timeIntervalSince1970: epoch)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(usedPercent, forKey: .usedPercent)
        try c.encode(windowMinutes, forKey: .windowMinutes)
        try c.encode(resetsAt.timeIntervalSince1970, forKey: .resetsAt)
    }

    /// Nominal window length, snapping the Codex server-rounding quirk
    /// (299→300, 10079→10080) to the canonical 5h / 7d values so callers can
    /// match on exact minutes.
    var canonicalWindowMinutes: Int {
        switch windowMinutes {
        case 299, 300: return 300
        case 10079, 10080: return 10080
        default: return windowMinutes
        }
    }
}

/// Prepaid credits balance, populated only when the user is on the API-key
/// / usage path. Subscription users see `credits == nil`.
struct CreditsInfo: Codable, Hashable {
    let hasCredits: Bool
    let unlimited: Bool?
    let balance: Double?

    enum CodingKeys: String, CodingKey {
        case hasCredits = "has_credits"
        case unlimited, balance
    }
}

/// One subscription-quota snapshot for a session. Mirrors
/// session.RateLimitSnapshot in the Go daemon (issue #309). Carried under
/// SessionMetrics.rateLimit; nil for sessions that don't surface a quota
/// (API-key Claude Code, Bedrock, Vertex).
// Agent-authored task-progress estimate (issue #558), parsed read-only from
// an in-band marker in the agent's transcript. A "round" is the agent's own
// unit (≈ a task phase).
struct TaskEstimateInfo: Codable, Hashable {
    let totalRounds: Int
    let completedRounds: Int
    let risk: String?
    let confidence: Double?
    let updatedAt: Date?    // when the marker was last observed (staleness degrade)
    let source: String?     // "marker" | "tasks" — attribution for the tooltip (#604)

    enum CodingKeys: String, CodingKey {
        case totalRounds = "total_rounds"
        case completedRounds = "completed_rounds"
        case risk
        case confidence
        case updatedAt = "updated_at"
        case source
    }

    init(totalRounds: Int, completedRounds: Int, risk: String? = nil, confidence: Double? = nil, updatedAt: Date? = nil, source: String? = nil) {
        self.totalRounds = totalRounds
        self.completedRounds = completedRounds
        self.risk = risk
        self.confidence = confidence
        self.updatedAt = updatedAt
        self.source = source
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        totalRounds = try c.decodeIfPresent(Int.self, forKey: .totalRounds) ?? 0
        completedRounds = try c.decodeIfPresent(Int.self, forKey: .completedRounds) ?? 0
        risk = try c.decodeIfPresent(String.self, forKey: .risk)
        confidence = try c.decodeIfPresent(Double.self, forKey: .confidence)
        if let epoch = try c.decodeIfPresent(Double.self, forKey: .updatedAt), epoch > 0 {
            updatedAt = Date(timeIntervalSince1970: epoch)
        } else {
            updatedAt = nil
        }
        source = try c.decodeIfPresent(String.self, forKey: .source)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(totalRounds, forKey: .totalRounds)
        try c.encode(completedRounds, forKey: .completedRounds)
        try c.encodeIfPresent(risk, forKey: .risk)
        try c.encodeIfPresent(confidence, forKey: .confidence)
        try c.encodeIfPresent(updatedAt.map { $0.timeIntervalSince1970 }, forKey: .updatedAt)
        try c.encodeIfPresent(source, forKey: .source)
    }
}

struct RateLimitInfo: Codable, Hashable {
    let windows: [RateLimitWindowInfo]
    let planType: String?
    let credits: CreditsInfo?
    let reachedType: String?
    let sampledAt: Date

    enum CodingKeys: String, CodingKey {
        case windows
        case planType = "plan_type"
        case credits
        case reachedType = "reached_type"
        case sampledAt = "sampled_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        windows = try c.decodeIfPresent([RateLimitWindowInfo].self, forKey: .windows) ?? []
        planType = try c.decodeIfPresent(String.self, forKey: .planType)
        credits = try c.decodeIfPresent(CreditsInfo.self, forKey: .credits)
        reachedType = try c.decodeIfPresent(String.self, forKey: .reachedType)
        let epoch = try c.decode(Double.self, forKey: .sampledAt)
        sampledAt = Date(timeIntervalSince1970: epoch)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(windows, forKey: .windows)
        try c.encodeIfPresent(planType, forKey: .planType)
        try c.encodeIfPresent(credits, forKey: .credits)
        try c.encodeIfPresent(reachedType, forKey: .reachedType)
        try c.encode(sampledAt.timeIntervalSince1970, forKey: .sampledAt)
    }

    /// The window with the highest UsedPercent — the natural "next to hit
    /// the cap" pick for the header display. Returns nil when no windows
    /// carry a non-zero reading.
    var imminentWindow: RateLimitWindowInfo? {
        let nonZero = windows.filter { $0.usedPercent > 0 }
        return nonZero.max { $0.usedPercent < $1.usedPercent }
    }

    /// Returns a friendly tier label for tooltip / display, e.g. "Claude
    /// Max", "ChatGPT Plus", or nil when planType is empty/unset.
    var planTypeLabel: String? {
        guard let pt = planType, !pt.isEmpty else { return nil }
        switch pt {
        case "max": return "Claude Max"
        case "pro": return "Claude Pro"
        case "plus": return "ChatGPT Plus"
        case "team": return "Team"
        case "enterprise": return "Enterprise"
        default: return pt.capitalized
        }
    }

    /// Provider identity inferred from planType (when populated) or the
    /// adapter that produced this snapshot. The bucket is account-scoped
    /// at the subscription provider, not the CLI — multiple agents (Claude
    /// Code + Pi(anthropic) + OpenCode(anthropic-oauth)) share a single
    /// Anthropic subscription, so the chip should brand by provider.
    ///
    /// Returns "anthropic" / "openai" for known providers, or nil when
    /// the snapshot doesn't tell us enough (rare: usually planType or the
    /// adapter is enough). Callers fall back to the adapter icon when nil.
    func providerKey(adapter: String?) -> String? {
        switch planType {
        case "max", "pro": return "anthropic"
        case "plus": return "openai"
        default: break
        }
        switch adapter {
        case "claude-code": return "anthropic"
        case "codex": return "openai"
        default: return nil
        }
    }
}

/// Per-provider display preference, stored in @AppStorage under
/// `providerMode_<key>`. Issue #309's maintainer comment asked for an
/// explicit toggle so a user with multiple paths into the same provider
/// (e.g. Anthropic Pro **and** Bedrock API key) can pick which view the
/// chip should render. `auto` defers to snapshot detection — the
/// default and the right choice for the typical single-path user.
enum ProviderModePreference: String, CaseIterable, Identifiable {
    case auto         // infer from snapshot.planType / snapshot.credits
    case subscription // force the bars chip
    case usage        // force the spend chip

    var id: String { rawValue }

    var label: String {
        switch self {
        case .auto: return "Auto"
        case .subscription: return "Subscription"
        case .usage: return "Usage"
        }
    }

    /// AppStorage key for this provider. Stable across app launches.
    static func storageKey(providerKey: String) -> String {
        "providerMode_\(providerKey)"
    }

    /// Read the persisted preference for a provider. Falls back to
    /// `.auto` for unknown keys or unparseable values.
    static func current(for providerKey: String) -> ProviderModePreference {
        let raw = UserDefaults.standard.string(forKey: storageKey(providerKey: providerKey)) ?? ""
        return ProviderModePreference(rawValue: raw) ?? .auto
    }
}

/// Hardcoded provider-level icons, picked by RateLimitInfo.providerKey for
/// the quota chip. These are subscription-provider icons (Anthropic,
/// OpenAI) — distinct from the agent CLI icons in AgentRegistry, since
/// many CLIs can share one subscription.
///
/// Lives Swift-side rather than in the Go AgentRegistry because providers
/// don't have agent adapters in irrlicht; they are an orthogonal axis.
enum ProviderIconRegistry {
    /// Anthropic logomark — three angled strokes forming a stylized "A",
    /// matched to the mockup in issue #309. White-on-transparent so it
    /// inherits the foreground color when rendered as a template.
    static let anthropicSVG = """
    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24">
      <path d="M14.83 4.5h-3.49l5.83 15h3.5l-5.84-15zM6.49 4.5l-5.83 15h3.57l1.19-3.13h6.09l1.2 3.13h3.57l-5.83-15H6.49zm-.05 8.98l1.99-5.2 1.99 5.2H6.44z" fill="currentColor"/>
    </svg>
    """

    /// OpenAI mark — a simplified knot/whirl glyph.
    static let openaiSVG = """
    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24">
      <path d="M22.28 9.821a5.985 5.985 0 0 0-.515-4.91 6.046 6.046 0 0 0-6.51-2.9A6.065 6.065 0 0 0 4.981 4.18a5.985 5.985 0 0 0-3.998 2.9 6.046 6.046 0 0 0 .743 7.097 5.98 5.98 0 0 0 .51 4.911 6.051 6.051 0 0 0 6.515 2.9A5.985 5.985 0 0 0 13.26 24a6.056 6.056 0 0 0 5.772-4.206 5.99 5.99 0 0 0 3.997-2.9 6.056 6.056 0 0 0-.747-7.073zM13.26 22.43a4.476 4.476 0 0 1-2.876-1.04l.142-.08 4.774-2.758a.795.795 0 0 0 .392-.681v-6.737l2.018 1.168a.071.071 0 0 1 .038.052v5.583a4.504 4.504 0 0 1-4.488 4.493zM3.6 18.304a4.47 4.47 0 0 1-.535-3.014l.142.085 4.778 2.758a.795.795 0 0 0 .787 0l5.832-3.367v2.332a.08.08 0 0 1-.033.062L9.74 19.95a4.5 4.5 0 0 1-6.14-1.646zM2.34 7.896a4.485 4.485 0 0 1 2.366-1.973V11.6a.766.766 0 0 0 .388.676l5.815 3.355-2.02 1.168a.077.077 0 0 1-.062 0l-4.83-2.79A4.504 4.504 0 0 1 2.34 7.872zm16.597 3.855l-5.833-3.387L15.119 7.2a.077.077 0 0 1 .062 0l4.83 2.79a4.5 4.5 0 0 1-.676 8.05v-5.678a.79.79 0 0 0-.398-.66zm2.01-3.023l-.142-.085-4.774-2.782a.776.776 0 0 0-.787 0L9.409 9.23V6.897a.066.066 0 0 1 .028-.061l4.83-2.787a4.5 4.5 0 0 1 6.68 4.66zm-12.64 4.135l-2.02-1.164a.08.08 0 0 1-.038-.057V6.075a4.504 4.504 0 0 1 7.375-3.453l-.142.08L8.704 5.46a.795.795 0 0 0-.392.681v6.737zm1.097-2.365l2.602-1.5 2.607 1.5v2.999l-2.597 1.5-2.607-1.5z" fill="currentColor"/>
    </svg>
    """

    /// Render the SVG for the given key into an NSImage sized to fit the
    /// chip icon slot. Returns nil for unknown keys; callers fall back to
    /// the adapter icon.
    @MainActor
    static func image(forKey key: String?) -> NSImage? {
        guard let key = key else { return nil }
        let svg: String
        switch key {
        case "anthropic": svg = anthropicSVG
        case "openai": svg = openaiSVG
        default: return nil
        }
        guard let data = svg.data(using: .utf8),
              let img = NSImage(data: data) else { return nil }
        img.isTemplate = true
        img.size = NSSize(width: 14, height: 14)
        return img
    }
}

// Performance metrics from transcript analysis
struct SessionMetrics: Codable {
    let elapsedSeconds: Int64       // elapsed time when metrics were computed (for ready sessions)
    let totalTokens: Int64          // total token count from transcript (0 if not available)
    let modelName: String           // model name extracted from transcript ("" if not available)
    let contextWindow: Int64?       // model context window size (nil/0 if unknown)
    let contextUtilization: Double  // context utilization percentage (0-100) (0 if not available)
    let pressureLevel: String       // pressure level: "safe", "caution", "warning", "critical" ("unknown" if not available)
    let contextWindowUnknown: Bool? // true when daemon has no LiteLLM pricing for the model — render tokens-only, no percentage
    let estimatedCostUSD: Double?   // estimated session cost in USD (nil if not available)
    let estimatedCO2Grams: Double?  // estimated CO2e footprint in grams — always a model, never a measurement (issue #829)
    let co2Tier: String?            // confidence tier behind estimatedCO2Grams: "provider_disclosed" or "fallback"
    let lastAssistantText: String?  // last assistant message text, truncated (~200 chars) — full text for the question tooltip
    let taskSummary: String?        // human-readable "what is this session about" (issue #738) — full text for the intent tooltip
    let intentHeadline: String?     // terse one-line version of taskSummary for the sidebar (issue #759)
    let questionHeadline: String?   // terse one-line version of the pending question for the sidebar (issue #759)
    let tasks: [SessionTask]?              // Claude Code task list (nil when TaskCreate never called)
    let rateLimit: RateLimitInfo?          // subscription-quota snapshot (nil for API-key / Bedrock / Vertex)
    let rateLimitForecastEta: Date?        // projected wall-clock time when the imminent window hits 100% (nil when unforecastable)
    let taskEstimate: TaskEstimateInfo?    // agent-authored task progress from the in-band marker (issue #558)
    let taskCompletionEta: Date?           // projected task completion (nil when no marker / no progress yet)
    let cacheBloat: Bool?                  // cache-creation regression detected for this session (issue #374)
    let cacheBloatTooltip: String?         // hover text naming the regressing version (empty when no attribution)
    let cacheBloatExplanation: String?     // longer plain-language hover text, composed daemon-side (issue #827)

    enum CodingKeys: String, CodingKey {
        case elapsedSeconds = "elapsed_seconds"
        case totalTokens = "total_tokens"
        case modelName = "model_name"
        case contextWindow = "context_window"
        case contextUtilization = "context_utilization_percentage"
        case pressureLevel = "pressure_level"
        case contextWindowUnknown = "context_window_unknown"
        case estimatedCostUSD = "estimated_cost_usd"
        case estimatedCO2Grams = "estimated_co2_grams"
        case co2Tier = "co2_tier"
        case lastAssistantText = "last_assistant_text"
        case taskSummary = "task_summary"
        case intentHeadline = "intent_headline"
        case questionHeadline = "question_headline"
        case tasks
        case rateLimit = "rate_limit"
        case rateLimitForecastEta = "rate_limit_forecast_eta"
        case taskEstimate = "task_estimate"
        case taskCompletionEta = "task_completion_eta"
        case cacheBloat = "cache_bloat"
        case cacheBloatTooltip = "cache_bloat_tooltip"
        case cacheBloatExplanation = "cache_bloat_explanation"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        elapsedSeconds = try c.decodeIfPresent(Int64.self, forKey: .elapsedSeconds) ?? 0
        totalTokens = try c.decodeIfPresent(Int64.self, forKey: .totalTokens) ?? 0
        modelName = try c.decodeIfPresent(String.self, forKey: .modelName) ?? ""
        contextWindow = try c.decodeIfPresent(Int64.self, forKey: .contextWindow)
        contextUtilization = try c.decodeIfPresent(Double.self, forKey: .contextUtilization) ?? 0
        pressureLevel = try c.decodeIfPresent(String.self, forKey: .pressureLevel) ?? "unknown"
        contextWindowUnknown = try c.decodeIfPresent(Bool.self, forKey: .contextWindowUnknown)
        estimatedCostUSD = try c.decodeIfPresent(Double.self, forKey: .estimatedCostUSD)
        estimatedCO2Grams = try c.decodeIfPresent(Double.self, forKey: .estimatedCO2Grams)
        co2Tier = try c.decodeIfPresent(String.self, forKey: .co2Tier)
        lastAssistantText = try c.decodeIfPresent(String.self, forKey: .lastAssistantText)
        taskSummary = try c.decodeIfPresent(String.self, forKey: .taskSummary)
        intentHeadline = try c.decodeIfPresent(String.self, forKey: .intentHeadline)
        questionHeadline = try c.decodeIfPresent(String.self, forKey: .questionHeadline)
        tasks = try c.decodeIfPresent([SessionTask].self, forKey: .tasks)
        rateLimit = try c.decodeIfPresent(RateLimitInfo.self, forKey: .rateLimit)
        if let epoch = try c.decodeIfPresent(Double.self, forKey: .rateLimitForecastEta) {
            rateLimitForecastEta = Date(timeIntervalSince1970: epoch)
        } else {
            rateLimitForecastEta = nil
        }
        // Lenient: a malformed task_estimate (e.g. an unexpected field type)
        // must degrade to "no estimate", never throw and nuke the whole
        // SessionMetrics decode (which would blank the row's elapsed/tokens/
        // cost/tasks too). issue #558.
        taskEstimate = (try? c.decodeIfPresent(TaskEstimateInfo.self, forKey: .taskEstimate)) ?? nil
        if let epoch = try c.decodeIfPresent(Double.self, forKey: .taskCompletionEta) {
            taskCompletionEta = Date(timeIntervalSince1970: epoch)
        } else {
            taskCompletionEta = nil
        }
        cacheBloat = try c.decodeIfPresent(Bool.self, forKey: .cacheBloat)
        cacheBloatTooltip = try c.decodeIfPresent(String.self, forKey: .cacheBloatTooltip)
        cacheBloatExplanation = try c.decodeIfPresent(String.self, forKey: .cacheBloatExplanation)
    }

    /// Explicit memberwise initializer for SwiftUI previews and tests that
    /// build SessionMetrics in-process. New fields default to nil so existing
    /// call sites compile without changes.
    init(
        elapsedSeconds: Int64,
        totalTokens: Int64,
        modelName: String,
        contextWindow: Int64?,
        contextUtilization: Double,
        pressureLevel: String,
        contextWindowUnknown: Bool?,
        estimatedCostUSD: Double?,
        estimatedCO2Grams: Double? = nil,
        co2Tier: String? = nil,
        lastAssistantText: String?,
        taskSummary: String? = nil,
        intentHeadline: String? = nil,
        questionHeadline: String? = nil,
        tasks: [SessionTask]?,
        rateLimit: RateLimitInfo? = nil,
        rateLimitForecastEta: Date? = nil,
        taskEstimate: TaskEstimateInfo? = nil,
        taskCompletionEta: Date? = nil,
        cacheBloat: Bool? = nil,
        cacheBloatTooltip: String? = nil,
        cacheBloatExplanation: String? = nil
    ) {
        self.elapsedSeconds = elapsedSeconds
        self.totalTokens = totalTokens
        self.modelName = modelName
        self.contextWindow = contextWindow
        self.contextUtilization = contextUtilization
        self.pressureLevel = pressureLevel
        self.contextWindowUnknown = contextWindowUnknown
        self.estimatedCostUSD = estimatedCostUSD
        self.estimatedCO2Grams = estimatedCO2Grams
        self.co2Tier = co2Tier
        self.lastAssistantText = lastAssistantText
        self.taskSummary = taskSummary
        self.intentHeadline = intentHeadline
        self.questionHeadline = questionHeadline
        self.tasks = tasks
        self.rateLimit = rateLimit
        self.rateLimitForecastEta = rateLimitForecastEta
        self.taskEstimate = taskEstimate
        self.taskCompletionEta = taskCompletionEta
        self.cacheBloat = cacheBloat
        self.cacheBloatTooltip = cacheBloatTooltip
        self.cacheBloatExplanation = cacheBloatExplanation
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(elapsedSeconds, forKey: .elapsedSeconds)
        try c.encode(totalTokens, forKey: .totalTokens)
        try c.encode(modelName, forKey: .modelName)
        try c.encodeIfPresent(contextWindow, forKey: .contextWindow)
        try c.encode(contextUtilization, forKey: .contextUtilization)
        try c.encode(pressureLevel, forKey: .pressureLevel)
        try c.encodeIfPresent(contextWindowUnknown, forKey: .contextWindowUnknown)
        try c.encodeIfPresent(estimatedCostUSD, forKey: .estimatedCostUSD)
        try c.encodeIfPresent(estimatedCO2Grams, forKey: .estimatedCO2Grams)
        try c.encodeIfPresent(co2Tier, forKey: .co2Tier)
        try c.encodeIfPresent(lastAssistantText, forKey: .lastAssistantText)
        try c.encodeIfPresent(taskSummary, forKey: .taskSummary)
        try c.encodeIfPresent(intentHeadline, forKey: .intentHeadline)
        try c.encodeIfPresent(questionHeadline, forKey: .questionHeadline)
        try c.encodeIfPresent(tasks, forKey: .tasks)
        try c.encodeIfPresent(rateLimit, forKey: .rateLimit)
        try c.encodeIfPresent(rateLimitForecastEta.map { $0.timeIntervalSince1970 }, forKey: .rateLimitForecastEta)
        try c.encodeIfPresent(taskEstimate, forKey: .taskEstimate)
        try c.encodeIfPresent(taskCompletionEta.map { $0.timeIntervalSince1970 }, forKey: .taskCompletionEta)
        try c.encodeIfPresent(cacheBloat, forKey: .cacheBloat)
        try c.encodeIfPresent(cacheBloatTooltip, forKey: .cacheBloatTooltip)
        try c.encodeIfPresent(cacheBloatExplanation, forKey: .cacheBloatExplanation)
    }
    
    // Computed properties for UI display
    var formattedElapsedTime: String {
        let minutes = elapsedSeconds / 60
        let seconds = elapsedSeconds % 60
        
        if minutes >= 60 {
            let hours = minutes / 60
            let remainingMinutes = minutes % 60
            return String(format: "%dh %dm", hours, remainingMinutes)
        } else if minutes > 0 {
            return String(format: "%dm %ds", minutes, seconds)
        } else {
            return String(format: "%ds", seconds)
        }
    }
    
    var formattedTokenCount: String {
        if totalTokens == 0 { return "—" }
        
        if totalTokens < 1000 {
            return "\(totalTokens)"
        } else if totalTokens < 1000000 {
            return String(format: "%.1fK", Double(totalTokens) / 1000)
        } else {
            return String(format: "%.1fM", Double(totalTokens) / 1000000)
        }
    }
    
    var formattedTokenUsage: String {
        if totalTokens == 0 { return "—" }
        let used = formattedTokenCount
        if let cw = contextWindow, cw > 0 {
            let window: String
            if cw < 1000 {
                window = "\(cw)"
            } else if cw < 1000000 {
                window = String(format: "%.0fK", Double(cw) / 1000)
            } else {
                window = String(format: "%.0fM", Double(cw) / 1000000)
            }
            return "\(used) / \(window)"
        }
        return "\(used) / ?"
    }

    var formattedContextUtilization: String {
        if contextUtilization == 0 && pressureLevel == "unknown" { return "—" }
        return String(format: "%.1f%%", contextUtilization)
    }
    
    var contextPressureIcon: String {
        switch pressureLevel {
        case "safe":
            return "🟢"
        case "caution":
            return "🟡"
        case "warning":
            return "🔴"
        case "critical":
            return "⚠️"
        case "unknown", "":
            return "❓"
        default:
            return "❓"
        }
    }
    
    var contextPressureColor: Color {
        switch pressureLevel {
        case "safe":     return IrrColors.pressureLow
        case "caution":  return IrrColors.pressureMedium
        case "warning":  return IrrColors.pressureHigh
        case "critical": return IrrColors.pressureCritical
        default:         return IrrColors.cancelled
        }
    }
    
    // Check if context utilization data is available
    var hasContextData: Bool {
        return totalTokens > 0 && !modelName.isEmpty && pressureLevel != "unknown" && !pressureLevel.isEmpty
    }

    var formattedCost: String? {
        // Hide entirely when there's no session activity yet.
        guard totalTokens > 0 else { return nil }
        let cost = estimatedCostUSD ?? 0
        if cost > 0 {
            if cost < 0.01 { return "<$0.01" }
            if cost >= 100 { return String(format: "$%.0f", cost) }
            return String(format: "$%.2f", cost)
        }
        // Cost is zero with tokens flowing. We only render the explicit
        // "—" placeholder when the daemon has positively told us cost
        // can't be computed (model has no LiteLLM pricing entry, signaled
        // via contextWindowUnknown). For other adapters, returning nil
        // here keeps the historical "hide on transient zero-cost windows"
        // behavior — claudecode / codex / pi cost converges to a real
        // number within a turn, and we don't want a flicker through "—".
        if contextWindowUnknown == true { return "—" }
        return nil
    }

    // Estimated CO2e footprint, shown as an alternate view of the cost slot
    // (issue #829, click-to-cycle in SessionRowView). Unit-adaptive like the
    // web dashboard's formatCO2, but without the "CO2e" suffix — the row's
    // cost column is too narrow for it; co2TierTooltip carries that context.
    var formattedCO2: String? {
        guard totalTokens > 0 else { return nil }
        guard let grams = estimatedCO2Grams, grams > 0 else {
            if contextWindowUnknown == true { return "—" }
            return nil
        }
        if grams < 1 { return String(format: "%.0fmg", grams * 1000) }
        if grams < 1000 { return String(format: "%.1fg", grams) }
        return String(format: "%.1fkg", grams / 1000)
    }

    // co2TierTooltip explains the confidence behind the CO2 estimate — every
    // figure here is modeled from public disclosures, never measured (no
    // provider exposes per-request energy telemetry). Mirrors the web
    // dashboard's co2TierTitle.
    var co2TierTooltip: String {
        if co2Tier == "provider_disclosed" {
            return "Estimated CO2e, normalized from a provider-published energy/CO2 disclosure — not a live measurement."
        }
        return "Estimated CO2e — no public per-model figure exists for this model, so a cross-model fallback average is used. Not a live measurement."
    }

    // Real-time elapsed time for active sessions
    func formattedRealtimeElapsedTime(sessionFirstSeen: Date) -> String {
        let seconds = Int64(Date().timeIntervalSince(sessionFirstSeen))
        let minutes = seconds / 60
        let remainingSeconds = seconds % 60
        
        if minutes >= 60 {
            let hours = minutes / 60
            let remainingMinutes = minutes % 60
            return String(format: "%dh %dm", hours, remainingMinutes)
        } else if minutes > 0 {
            return String(format: "%dm %ds", minutes, remainingSeconds)
        } else {
            return String(format: "%ds", remainingSeconds)
        }
    }
}

/// Aggregate state of all child sessions for a parent session.
struct SubagentSummary: Codable {
    let total: Int
    let working: Int
    let waiting: Int
    let ready: Int
}

/// BackgroundAgent marks a session as a background agent spawned via the agent's
/// own orchestration (Claude Code Agent View). Such an agent keeps running
/// detached in the daemon pool after its window/terminal is closed, so it shows
/// up as a live session with no terminal the user can see (#744). Nil for normal
/// interactive sessions.
struct BackgroundAgent: Codable, Hashable {
    /// Claude's human-readable label for the background job
    /// (e.g. "Add guiding colors to quest cards"); may be nil/empty.
    let name: String?
    /// True when the agent has no controlling terminal — i.e. no window/tab
    /// owns it. Computed by the daemon from the captured launcher TTY.
    let detached: Bool?
}

/// Launcher identifies the terminal or IDE that spawned the session's agent.
/// Captured by the daemon when the PID is first assigned. All fields optional —
/// clients must fall back to the session CWD when nothing identifies the host.
struct Launcher: Codable, Hashable {
    let termProgram: String?
    let itermSessionID: String?
    let termSessionID: String?
    let tmuxPane: String?
    let tmuxSocket: String?
    let tty: String?
    let kittyListenOn: String?
    let kittyWindowID: String?
    let kittyPID: Int?
    /// CFBundleIdentifier of the host app the daemon resolved by process
    /// ancestry when no curated `termProgram` matched (e.g. `md.obsidian` for a
    /// terminal embedded in Obsidian). Lets `SessionLauncher` build a generic
    /// title-match activator for hosts that have no registry entry.
    let hostBundleID: String?

    enum CodingKeys: String, CodingKey {
        case termProgram    = "term_program"
        case itermSessionID = "iterm_session_id"
        case termSessionID  = "term_session_id"
        case tmuxPane       = "tmux_pane"
        case tmuxSocket     = "tmux_socket"
        case tty            = "tty"
        case kittyListenOn  = "kitty_listen_on"
        case kittyWindowID  = "kitty_window_id"
        case kittyPID       = "kitty_pid"
        case hostBundleID   = "host_bundle_id"
    }
}

struct SessionState: Identifiable, Codable {
    let id: String              // session_id
    let state: State            // working, waiting, ready
    let model: String           // claude-3.7-sonnet, etc.
    let cwd: String             // working directory
    let transcriptPath: String? // path to transcript.jsonl (optional for backwards compatibility)
    let gitBranch: String?      // git branch name (optional)
    let projectName: String?    // project folder name (optional)
    let firstSeen: Date         // when session was first created
    let updatedAt: Date         // last modified timestamp
    let eventCount: Int?        // number of events processed (optional)
    let lastEvent: String?      // last event type (optional)
    let metrics: SessionMetrics? // performance metrics from transcript analysis (optional)
    let pid: Int?               // Claude Code process PID (optional for backwards compatibility)
    let parentSessionId: String? // parent session ID for subagent sessions (optional)
    let subagents: SubagentSummary? // aggregate state of child sessions (optional)
    let adapter: String?        // source agent: "claude-code", "codex" (optional)
    let daemonVersion: String?  // irrlichd version that created this session (optional)
    var role: String?           // orchestrator role: "witness", "polecat", etc.
    var roleIcon: String?       // orchestrator role emoji
    var roleDescription: String? // orchestrator role description
    var workerName: String?     // orchestrator worker name
    var workerID: String?       // orchestrator worker/bead ID
    let children: [SessionState]? // nested child sessions from API (optional)
    let launcher: Launcher?     // terminal/IDE that spawned this session (optional)
    let controllable: Bool?     // daemon would accept input/interrupt now (backchannel, #724)
    let background: BackgroundAgent? // detached background agent marker (#744)
    let yieldState: String?     // yield verdict: "productive"/"reverted"/"unknown" (#373)

    // For duplicate handling (not stored in JSON, computed by SessionManager)
    var duplicateIndex: Int? = nil

    // Relay daemon that emitted this session (not stored in JSON; stamped by
    // SessionManager from the relay Push envelope `source`). Disambiguates two
    // daemons that share a session_id — see `rowID`. nil for local sessions.
    var daemonID: String? = nil

    /// Render/identity key (#537). Compound `(daemon, session_id)` for relay
    /// sessions with a known daemon, else the bare `id` — so local rows and the
    /// persisted session order are unchanged. Only ever compared/keyed, never
    /// split, so a "/" delimiter is safe.
    var rowID: String { daemonID.map { "\($0)/\(id)" } ?? id }

    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "SessionState")

    // Custom coding keys to match JSON from irrlichd
    enum CodingKeys: String, CodingKey {
        case id = "session_id"
        case state, model, cwd, pid
        case gitBranch = "git_branch"
        case projectName = "project_name"
        case transcriptPath = "transcript_path"
        case firstSeen = "first_seen"
        case updatedAt = "updated_at"
        case eventCount = "event_count"
        case lastEvent = "last_event"
        case metrics
        case parentSessionId = "parent_session_id"
        case subagents
        case adapter
        case daemonVersion = "daemon_version"
        case role
        case roleIcon = "icon"
        case roleDescription = "description"
        case workerName = "worker_name"
        case workerID = "worker_id"
        case children
        case launcher
        case controllable
        case background
        case yieldState = "yield_state"
    }
    
    // Custom decoder to handle multiple date formats and missing fields
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        
        id = try container.decode(String.self, forKey: .id)
        // Handle backwards compatibility: "finished" -> "ready"
        let stateString = try container.decode(String.self, forKey: .state)
        if stateString == "finished" {
            state = .ready
        } else {
            state = State(rawValue: stateString) ?? .ready
        }
        model = try container.decodeIfPresent(String.self, forKey: .model) ?? "unknown"
        cwd = try container.decodeIfPresent(String.self, forKey: .cwd) ?? ""
        transcriptPath = try container.decodeIfPresent(String.self, forKey: .transcriptPath)
        gitBranch = try container.decodeIfPresent(String.self, forKey: .gitBranch)
        projectName = try container.decodeIfPresent(String.self, forKey: .projectName)
        eventCount = try container.decodeIfPresent(Int.self, forKey: .eventCount)
        lastEvent = try container.decodeIfPresent(String.self, forKey: .lastEvent)
        metrics = try container.decodeIfPresent(SessionMetrics.self, forKey: .metrics)
        pid = try container.decodeIfPresent(Int.self, forKey: .pid)
        parentSessionId = try container.decodeIfPresent(String.self, forKey: .parentSessionId)
        subagents = try container.decodeIfPresent(SubagentSummary.self, forKey: .subagents)
        adapter = try container.decodeIfPresent(String.self, forKey: .adapter)
        daemonVersion = try container.decodeIfPresent(String.self, forKey: .daemonVersion)
        role = try container.decodeIfPresent(String.self, forKey: .role)
        roleIcon = try container.decodeIfPresent(String.self, forKey: .roleIcon)
        roleDescription = try container.decodeIfPresent(String.self, forKey: .roleDescription)
        workerName = try container.decodeIfPresent(String.self, forKey: .workerName)
        workerID = try container.decodeIfPresent(String.self, forKey: .workerID)
        children = try container.decodeIfPresent([SessionState].self, forKey: .children)
        launcher = try container.decodeIfPresent(Launcher.self, forKey: .launcher)
        controllable = try container.decodeIfPresent(Bool.self, forKey: .controllable)
        background = try container.decodeIfPresent(BackgroundAgent.self, forKey: .background)
        yieldState = try container.decodeIfPresent(String.self, forKey: .yieldState)

        // Handle firstSeen date (unix timestamp format)
        if let timestamp = try? container.decode(Double.self, forKey: .firstSeen) {
            firstSeen = Date(timeIntervalSince1970: timestamp)
        } else if let timestamp = try? container.decode(Int.self, forKey: .firstSeen) {
            firstSeen = Date(timeIntervalSince1970: Double(timestamp))
        } else {
            // Fallback to now if no valid date found
            firstSeen = Date()
        }
        
        // Handle multiple date formats
        if let dateString = try? container.decode(String.self, forKey: .updatedAt) {
            // ISO8601 string format
            let formatter = DateFormatter()
            formatter.dateFormat = "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'"
            formatter.timeZone = TimeZone(abbreviation: "UTC")
            
            if let date = formatter.date(from: dateString) {
                updatedAt = date
            } else {
                // Fallback to ISO8601 without milliseconds
                formatter.dateFormat = "yyyy-MM-dd'T'HH:mm:ss'Z'"
                updatedAt = formatter.date(from: dateString) ?? Date()
            }
        } else if let timestamp = try? container.decode(Double.self, forKey: .updatedAt) {
            // Unix timestamp format
            updatedAt = Date(timeIntervalSince1970: timestamp)
        } else if let timestamp = try? container.decode(Int.self, forKey: .updatedAt) {
            // Unix timestamp as integer
            updatedAt = Date(timeIntervalSince1970: Double(timestamp))
        } else {
            // Default to now if no valid date found
            updatedAt = Date()
        }
        
        // Log session data for debugging (after all properties are set). Gated:
        // this fires on every decode (one per WebSocket push), so leaving it
        // unconditional burns real CPU under many-agent load (#690).
        if irrlichtVerboseLogging {
            print("🔍 Decoded session: id=\(id), state=\(state.rawValue), topLevelModel=\(model), metricsModel=\(metrics?.modelName ?? "nil"), eventCount=\(eventCount ?? 0), lastEvent=\(lastEvent ?? "nil")")
        }
    }
    
    // Regular initializer for testing/preview purposes
    init(id: String, state: State, model: String, cwd: String, transcriptPath: String? = nil, gitBranch: String? = nil, projectName: String? = nil, firstSeen: Date, updatedAt: Date, eventCount: Int? = nil, lastEvent: String? = nil, metrics: SessionMetrics? = nil, pid: Int? = nil, parentSessionId: String? = nil, subagents: SubagentSummary? = nil, adapter: String? = nil, daemonVersion: String? = nil, role: String? = nil, roleIcon: String? = nil, roleDescription: String? = nil, workerName: String? = nil, workerID: String? = nil, children: [SessionState]? = nil, launcher: Launcher? = nil, controllable: Bool? = nil, background: BackgroundAgent? = nil, yieldState: String? = nil) {
        self.id = id
        self.state = state
        self.model = model
        self.cwd = cwd
        self.transcriptPath = transcriptPath
        self.gitBranch = gitBranch
        self.projectName = projectName
        self.firstSeen = firstSeen
        self.updatedAt = updatedAt
        self.eventCount = eventCount
        self.lastEvent = lastEvent
        self.metrics = metrics
        self.pid = pid
        self.parentSessionId = parentSessionId
        self.subagents = subagents
        self.adapter = adapter
        self.daemonVersion = daemonVersion
        self.role = role
        self.roleIcon = roleIcon
        self.roleDescription = roleDescription
        self.workerName = workerName
        self.workerID = workerID
        self.children = children
        self.launcher = launcher
        self.controllable = controllable
        self.background = background
        self.yieldState = yieldState
    }

    enum State: String, CaseIterable, Codable {
        case working, waiting, ready

        var glyph: String {
            switch self {
            case .working: return "hammer.fill"
            case .waiting: return "hourglass"
            case .ready: return "checkmark.circle.fill"
            }
        }

        var color: Color {
            switch self {
            case .working: return IrrColors.working
            case .waiting: return IrrColors.waiting
            case .ready:   return IrrColors.ready
            }
        }

        /// Hex without leading `#`, for inline SVG `fill="#..."` markup.
        var hexColor: String {
            switch self {
            case .working: return IrrSVG.working
            case .waiting: return IrrSVG.waiting
            case .ready:   return IrrSVG.ready
            }
        }

        /// Highest-priority state in a collection (waiting > working > ready).
        static func dominant<C: Collection>(in states: C) -> State where C.Element == State {
            if states.contains(.waiting) { return .waiting }
            if states.contains(.working) { return .working }
            return .ready
        }

        var emoji: String {
            switch self {
            case .working: return "🟣"   // purple circle
            case .waiting: return "🟠"   // orange circle
            case .ready: return "🟢"  // green circle
            }
        }

        var label: String {
            switch self {
            case .working: return "Working"
            case .waiting: return "Waiting for input"
            case .ready: return "Ready"
            }
        }
    }
    
    /// Return a copy preserving role/icon/description from an existing session
    /// when the incoming WS update doesn't carry them.
    func preservingRole(from existing: SessionState) -> SessionState {
        if role != nil { return self }
        var copy = self
        copy.role = existing.role
        copy.roleIcon = existing.roleIcon
        copy.roleDescription = existing.roleDescription
        copy.workerName = existing.workerName
        copy.workerID = existing.workerID
        return copy
    }

    /// Return a copy with a replacement children list. Used when the incremental
    /// WS patch path updates a single agent inside `apiGroups` — WS payloads
    /// don't carry `children`, so the patch has to reattach them.
    func withChildren(_ newChildren: [SessionState]?) -> SessionState {
        var copy = SessionState(
            id: id, state: state, model: model, cwd: cwd,
            transcriptPath: transcriptPath, gitBranch: gitBranch,
            projectName: projectName, firstSeen: firstSeen, updatedAt: updatedAt,
            eventCount: eventCount, lastEvent: lastEvent, metrics: metrics,
            pid: pid, parentSessionId: parentSessionId, subagents: subagents,
            adapter: adapter, daemonVersion: daemonVersion,
            role: role, roleIcon: roleIcon, roleDescription: roleDescription,
            workerName: workerName, workerID: workerID,
            children: newChildren, launcher: launcher,
            yieldState: yieldState
        )
        copy.duplicateIndex = duplicateIndex
        copy.daemonID = daemonID
        return copy
    }

    /// Return a copy with `state` replaced and `updatedAt` set to now. All
    /// other fields are preserved — including children, subagents, role, and
    /// adapter — which a field-by-field reconstruction would silently drop.
    func withState(_ newState: State) -> SessionState {
        var copy = SessionState(
            id: id, state: newState, model: model, cwd: cwd,
            transcriptPath: transcriptPath, gitBranch: gitBranch,
            projectName: projectName, firstSeen: firstSeen, updatedAt: Date(),
            eventCount: eventCount, lastEvent: lastEvent, metrics: metrics,
            pid: pid, parentSessionId: parentSessionId, subagents: subagents,
            adapter: adapter, daemonVersion: daemonVersion,
            role: role, roleIcon: roleIcon, roleDescription: roleDescription,
            workerName: workerName, workerID: workerID,
            children: children, launcher: launcher,
            yieldState: yieldState
        )
        copy.duplicateIndex = duplicateIndex
        copy.daemonID = daemonID
        return copy
    }

    var activeSubagentCount: Int {
        (subagents?.working ?? 0) + (subagents?.waiting ?? 0)
    }

    var shortId: String {
        String(id.suffix(6))  // Show last 6 chars of session ID
    }
    
    var friendlyName: String {
        // Create user-friendly name from project and branch
        let project = projectName ?? "unknown"
        let branch = gitBranch ?? "no-git"
        let baseName = "\(project)/\(branch)"
        
        // Add duplicate index if needed
        if let index = duplicateIndex, index > 1 {
            return "\(baseName) (\(index))"
        } else {
            return baseName
        }
    }
    
    var timeAgo: String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: updatedAt, relativeTo: Date())
    }
    
    var effectiveModel: String {
        // Prefer metrics.model_name if available, otherwise fall back to top-level model
        let effective = metrics?.modelName.isEmpty == false ? metrics!.modelName : model
        if irrlichtVerboseLogging {
            print("🎯 effectiveModel for \(shortId): using '\(effective)' (metrics='\(metrics?.modelName ?? "nil")', topLevel='\(model)')")
        }
        return effective
    }
    
    var displayName: String {
        "\(friendlyName) · \(state.rawValue) · \(effectiveModel) · \(timeAgo)"
    }
    
    var safeEventCount: Int {
        eventCount ?? 0
    }

    var shortModelName: String {
        var short = effectiveModel
        // Strip LiteLLM provider/routing prefix, e.g.
        // "openai/google/gemma-4-26b-a4b" → "gemma-4-26b-a4b"
        // "anthropic/claude-opus-4-7"     → "claude-opus-4-7"
        if let lastSlash = short.lastIndex(of: "/") {
            short = String(short[short.index(after: lastSlash)...])
        }
        short = short.replacingOccurrences(of: "claude-", with: "")
        // "sonnet-4-6" → "sonnet-4.6"
        if let range = short.range(of: #"-(\d+)$"#, options: .regularExpression) {
            short = short.replacingCharacters(in: range, with: "." + short[range].dropFirst())
        }
        return short
    }

    @MainActor
    var adapterName: String {
        let key = adapter ?? ""
        if let entry = AgentRegistry.byName[key], !entry.displayName.isEmpty {
            return entry.displayName
        }
        return key.isEmpty ? "Unknown" : key
    }

    /// SVG icon for the adapter, looked up from the registry the daemon
    /// publishes at GET /api/v1/agents. Falls back to a neutral generic icon
    /// when the registry has no entry for this adapter — e.g. before the
    /// first hydration completes, or when an adapter is rolled out by a
    /// daemon newer than this app build.
    @MainActor
    var adapterIcon: NSImage? {
        // NSApp can be nil before the app finishes launching (and is always
        // nil in unit-test contexts that don't bring up an NSApplication).
        // Default to the light variant in that case — it's the more common
        // ambient appearance and avoids an implicit-unwrap crash.
        let isDark = NSApp?.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
        let svg: String
        if let entry = AgentRegistry.byName[adapter ?? ""] {
            svg = isDark ? entry.iconSVGDark : entry.iconSVGLight
        } else {
            svg = SessionState.genericAdapterSVG
        }
        guard let data = svg.data(using: .utf8),
              let img = NSImage(data: data) else { return nil }
        img.isTemplate = false
        img.size = NSSize(width: 14, height: 14)
        return img
    }

    // Neutral placeholder shown when the adapter registry has no entry for
    // this session's adapter (pre-hydration, or unknown adapter from a newer
    // daemon). Question mark in a circle reads as "unknown" so users don't
    // confuse a missing-branding icon with a real adapter; the gray
    // (#9CA3AF) is deliberately distinct from the cancelled-state gray
    // (#8E8E93) used elsewhere in the dashboard so the two never collide.
    // Per-adapter SVGs now live in their Go packages — see
    // core/adapters/inbound/agents/<name>/config.go.
    private static let genericAdapterSVG = """
    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
      <circle cx="50" cy="50" r="44" fill="none" stroke="#9CA3AF" stroke-width="8"/>
      <path d="M38 38 Q38 26 50 26 Q62 26 62 38 Q62 46 54 50 Q50 52 50 60" fill="none" stroke="#9CA3AF" stroke-width="8" stroke-linecap="round" stroke-linejoin="round"/>
      <circle cx="50" cy="74" r="5" fill="#9CA3AF"/>
    </svg>
    """
}
