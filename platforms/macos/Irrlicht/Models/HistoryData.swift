import Foundation

// Codable mirror of the daemon's `GET /api/v1/history` response envelope
// (`historyResponse` in core/cmd/irrlichd/handlers.go). Phase 2 (#750) adds the
// tokens/models/providers chart types, the branch/provider/model/session group
// axes, and drilldown scoping. The types are Encodable as well as Decodable so
// the History view can round-trip the payload back out for the JSON export
// (web parity — the web exports the raw response object).

struct HistoryResponse: Codable {
    let range: String
    let chart: String
    let group: String
    let start: Int64
    let end: Int64
    let bucketSeconds: Int64
    let bucketStarts: [Int64]
    let total: Double
    let series: [HistoryPoint]
    let topContributors: [HistoryContributor]
    let tokenSplit: HistoryTokenSplit? // chart=tokens only
    let scope: String?                 // active drilldown filter "field:value"

    enum CodingKeys: String, CodingKey {
        case range, chart, group, start, end, total, series, scope
        case bucketSeconds = "bucket_seconds"
        case bucketStarts = "bucket_starts"
        case topContributors = "top_contributors"
        case tokenSplit = "token_split"
    }

    /// Mirrors the web `hasData` gate: a non-empty window with spend. Drives the
    /// "no cost data in this range yet" empty state.
    var hasData: Bool { total > 0 && !bucketStarts.isEmpty }
}

struct HistoryPoint: Codable {
    let ts: Int64
    // Carries the group key (project/branch/provider/model/session per ?group);
    // the json field stays "project" for Phase 1 wire compatibility.
    let project: String
    let value: Double
}

struct HistoryContributor: Codable {
    let label: String
    let value: Double
}

/// The window's aggregate token throughput by kind, present only for
/// chart=tokens. Drives the tokens side panel (in/out/cache).
struct HistoryTokenSplit: Codable {
    let input: Double
    let output: Double
    let cache: Double
}

// Codable mirror of the daemon's `chart=yield` response (#373). Yield is a
// per-project aggregate over completed sessions — productive vs reverted spend
// and the resulting ratio — not a time series, so it has its own envelope.
struct HistoryYieldResponse: Codable {
    let range: String
    let productiveCost: Double
    let revertedCost: Double
    let unknownCost: Double
    let totalCost: Double
    let yieldRatio: Double
    let projects: [HistoryYieldProject]

    enum CodingKeys: String, CodingKey {
        case range, projects
        case productiveCost = "productive_cost"
        case revertedCost = "reverted_cost"
        case unknownCost = "unknown_cost"
        case totalCost = "total_cost"
        case yieldRatio = "yield"
    }

    /// Anything to show: attributable spend, or unattributed (non-git) spend.
    var hasData: Bool { totalCost > 0 || unknownCost > 0 }
}

struct HistoryYieldProject: Codable, Identifiable {
    let project: String
    let productiveCost: Double
    let revertedCost: Double
    let unknownCost: Double
    let totalCost: Double
    let yieldRatio: Double
    let revertedCount: Int

    var id: String { project }

    enum CodingKeys: String, CodingKey {
        case project
        case productiveCost = "productive_cost"
        case revertedCost = "reverted_cost"
        case unknownCost = "unknown_cost"
        case totalCost = "total_cost"
        case yieldRatio = "yield"
        case revertedCount = "reverted_count"
    }
}

// Codable mirror of the daemon's `chart=dora` response (#951): DORA metrics
// for one project's git repo over the selected window — a period summary,
// not a time series, computed on request with no persistence.
struct HistoryDoraResponse: Codable {
    let range: String
    let project: String
    let start: Int64
    let end: Int64
    let available: Bool
    let message: String?
    let deploymentFrequency: DoraMetric
    let leadTime: DoraMetric
    let changeFailureRate: DoraMetric
    let mttr: DoraMetric

    enum CodingKeys: String, CodingKey {
        case range, project, start, end, available, message
        case deploymentFrequency = "deployment_frequency"
        case leadTime = "lead_time"
        case changeFailureRate = "change_failure_rate"
        case mttr
    }
}

/// One computed DORA statistic. `available` is false when there wasn't
/// enough data to compute `value` — `message` explains why, and `value`/
/// `unit` should not be rendered in that case.
struct DoraMetric: Codable {
    let value: Double
    let unit: String
    let sampleSize: Int
    let available: Bool
    let message: String?

    enum CodingKeys: String, CodingKey {
        case value, unit, available, message
        case sampleSize = "sample_size"
    }
}

/// The History range selector — the cost calendar spans shown in the dropdown.
/// The subscription rate-limit windows (5h / 7d) are no longer a range; quota is
/// surfaced as a live per-provider forecast strip instead (see HistoryView).
enum HistoryRange: String, CaseIterable, Identifiable {
    case day, week, month, year, custom

    var id: String { rawValue }

    /// Dropdown + side-panel label ("Total · Day"), matching the web `RANGE_LABELS`.
    var label: String {
        switch self {
        case .day: return "Day"
        case .week: return "Week"
        case .month: return "Month"
        case .year: return "Year"
        case .custom: return "Custom"
        }
    }

    /// Query items for `GET /api/v1/history`, mirroring the web `historyQuery()`:
    /// presets send `range`; `.custom` sends explicit `start`/`end` unix seconds.
    func queryItems(chart: HistoryChart, group: HistoryGroup, scope: HistoryScope?, forecast: Bool, customStart: Int64?, customEnd: Int64?) -> [URLQueryItem] {
        var items = [
            URLQueryItem(name: "chart", value: chart.rawValue),
            URLQueryItem(name: "group", value: group.rawValue),
            URLQueryItem(name: "forecast", value: forecast ? "true" : "false"),
        ]
        if let scope {
            items.append(URLQueryItem(name: "scope", value: scope.query))
        }
        if self == .custom, let customStart, let customEnd {
            items.append(URLQueryItem(name: "start", value: String(customStart)))
            items.append(URLQueryItem(name: "end", value: String(customEnd)))
        } else {
            items.append(URLQueryItem(name: "range", value: rawValue))
        }
        return items
    }
}

/// Token kind for the `token_type` group axis and its band labels in the
/// content-view legend (#750). Raw values match the daemon's
/// `outbound.TokenTypeKeys`.
enum HistoryTokenType: String, CaseIterable, Identifiable {
    case input, output
    case cacheRead = "cache_read"
    case cacheCreation = "cache_creation"

    var id: String { rawValue }

    var label: String {
        switch self {
        case .input: return "Input"
        case .output: return "Output"
        case .cacheRead: return "Cache read"
        case .cacheCreation: return "Cache create"
        }
    }
}

/// History chart type (#750). Mirrors the web Chart segmented control. cost and
/// the models/providers presets measure USD; tokens measures token counts;
/// co2 (issue #829) measures estimated CO2e grams.
enum HistoryChart: String, CaseIterable, Identifiable {
    case cost, tokens, co2, models, providers
    case yieldRatio = "yield" // #373 — per-project productive vs reverted spend
    case dora // #951 — per-project DORA metrics (deploy frequency, lead time, CFR, MTTR)
    case state // #1028 — per-project/per-state Activity Matrix grid

    var id: String { rawValue }

    var label: String {
        switch self {
        case .cost: return "Cost"
        case .tokens: return "Tokens"
        case .co2: return "CO2"
        case .models: return "Models"
        case .providers: return "Providers"
        case .yieldRatio: return "Yield"
        case .dora: return "DORA"
        case .state: return "Activity"
        }
    }

    /// True for the USD metrics (everything but tokens and co2) — they render a $ axis.
    var isCost: Bool { self != .tokens && self != .co2 && self != .state }

    /// True for the co2 metric — renders a CO2e axis (mg/g/kg), not $ or tokens.
    var isCO2: Bool { self == .co2 }

    /// models/providers are presets that pin the stacking axis to that
    /// dimension; cost/tokens leave the group axis to the user.
    var pinnedGroup: HistoryGroup? {
        switch self {
        case .models: return .model
        case .providers: return .provider
        default: return nil
        }
    }
}

/// History group axis (#750). Mirrors the web Group segmented control.
enum HistoryGroup: String, CaseIterable, Identifiable {
    case project, branch, provider, model, session
    case tokenType = "token_type" // tokens metric only; stacks by token kind

    var id: String { rawValue }

    /// Segmented-control label (the 380pt popover uses the compact form).
    var shortLabel: String {
        switch self {
        case .project: return "Proj"
        case .branch: return "Branch"
        case .provider: return "Prov"
        case .model: return "Model"
        case .session: return "Sess"
        case .tokenType: return "Type"
        }
    }

    /// The next finer axis to drill into, or nil for a leaf — mirrors the web
    /// DRILL_NEXT map (project → branch → session; provider/model → session).
    /// token_type is a leaf (the bands aren't drillable).
    var drillNext: HistoryGroup? {
        switch self {
        case .project: return .branch
        case .branch: return .session
        case .provider: return .model
        case .model: return .session
        case .session: return nil
        case .tokenType: return nil
        }
    }
}

/// A single-level drilldown filter: show only rows whose `field` equals `value`.
struct HistoryScope: Equatable {
    let field: HistoryGroup
    let value: String

    /// The `scope=field:value` query-param form the daemon parses.
    var query: String { "\(field.rawValue):\(value)" }
}

// Codable mirror of the daemon's `chart=state` response (#981, #1028 — the
// Activity Matrix): a per-project, per-state agent-count grid, unlike every
// other chart's single-value-per-point series. `projects` is row order
// (server-sorted busiest-first); `byState` is keyed state -> project ->
// one count per bucket index, aligned to `bucketStarts`. Working/waiting
// counts are per-bucket peak concurrency; ready counts are a transition
// histogram (sessions that turned ready within the bucket), since ready has
// no duration to be "concurrent" in — not a bug, just a different meaning
// behind the same-shaped number, and worth remembering wherever this is
// rendered or described.
struct HistoryStateResponse: Codable {
    let range: String
    let chart: String
    let group: String
    let start: Int64
    let end: Int64
    let bucketSeconds: Int64
    let bucketStarts: [Int64]
    let projects: [String]
    let byState: [String: [String: [Double]]]
    let concurrency: HistoryStateConcurrency?
    let scope: String?

    enum CodingKeys: String, CodingKey {
        case range, chart, group, start, end, projects, scope, concurrency
        case bucketSeconds = "bucket_seconds"
        case bucketStarts = "bucket_starts"
        case byState = "by_state"
    }

    /// A nil/unresolved recordings dir yields an empty-but-valid payload
    /// (empty `projects`/`bucketStarts`) rather than an error — this mirrors
    /// the web `hasData` gate for that same empty case.
    var hasData: Bool { !projects.isEmpty && !bucketStarts.isEmpty }

    /// One cell's raw counts. A named triple (not a `[String: Double]`)
    /// so every call site gets `.total` for free instead of re-deriving it,
    /// and painting order — working bottom, waiting middle, ready top,
    /// mirroring the canonical state order in core/domain/session/session.go
    /// and the web's STATE_STACK_ORDER — is just field order, not a runtime
    /// loop over string keys.
    struct Counts {
        let working: Double
        let waiting: Double
        let ready: Double
        var total: Double { working + waiting + ready }
    }

    /// Raw counts for one project at one bucket index, defaulting any
    /// missing state/project/index to 0 — mirrors the web `stateCellCounts`.
    func counts(project: String, bucketIndex: Int) -> Counts {
        Counts(
            working: byState["working"]?[project]?[safe: bucketIndex] ?? 0,
            waiting: byState["waiting"]?[project]?[safe: bucketIndex] ?? 0,
            ready: byState["ready"]?[project]?[safe: bucketIndex] ?? 0
        )
    }
}

struct HistoryStateConcurrency: Codable {
    let peak: Double
    let average: Double
    let current: Double
}

/// Granularity step for the Activity Matrix (#981, #1028) — picks both the
/// server's bucket width and the matrix's visible column count at once (see
/// `historyGranularitySpecs` on the daemon side). Unlike every other chart,
/// this one sends `granularity=` instead of `range=`/`start=`/`end=`.
enum HistoryGranularity: String, CaseIterable, Identifiable {
    case min1 = "1m"
    case min10 = "10m"
    case min60 = "60m"
    case hr8 = "8h"
    case hr24 = "24h"
    case day7 = "7d"
    case mo1 = "1mo"
    case mo6 = "6mo"
    case yr1 = "1y"

    var id: String { rawValue }

    var label: String {
        switch self {
        case .min1: return "1 min"
        case .min10: return "10 min"
        case .min60: return "60 min"
        case .hr8: return "8 hr"
        case .hr24: return "24 hr"
        case .day7: return "7 day"
        case .mo1: return "1 mo"
        case .mo6: return "6 mo"
        case .yr1: return "1 yr"
        }
    }
}

extension Array {
    /// Bounds-safe index — the state matrix's `[Double]` arrays are always
    /// dense/bucket-aligned server-side, but defending against a short array
    /// here is cheap and keeps a malformed payload from crashing the view.
    subscript(safe index: Int) -> Element? {
        indices.contains(index) ? self[index] : nil
    }
}
