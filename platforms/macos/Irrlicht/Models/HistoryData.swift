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
    let forecast: HistoryForecast?
    let tokenSplit: HistoryTokenSplit? // chart=tokens only
    let scope: String?                 // active drilldown filter "field:value"

    enum CodingKeys: String, CodingKey {
        case range, chart, group, start, end, total, series, forecast, scope
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

struct HistoryForecast: Codable {
    let projected: Double
    let basis: String
    let horizonBuckets: Int
    let series: [HistoryForecastPoint]

    enum CodingKeys: String, CodingKey {
        case projected, basis, series
        case horizonBuckets = "horizon_buckets"
    }
}

struct HistoryForecastPoint: Codable {
    let ts: Int64
    let value: Double
}

/// The History range selector. Mirrors the web dashboard's segmented control,
/// minus `this-month` (issue #755 scopes macOS Phase 1 to Day/Week/Month/Year
/// + Custom).
enum HistoryRange: String, CaseIterable, Identifiable {
    // Quota rate-limit windows first (they render a burn-rate projection, not
    // cost), then the cost calendar spans.
    case fiveHour, sevenDay, day, week, month, year, custom

    var id: String { rawValue }

    /// True for the subscription rate-limit windows (5h / 7d), which render a
    /// quota burn-rate projection to the cap instead of cost.
    var isQuota: Bool { self == .fiveHour || self == .sevenDay }

    /// Rate-limit window length in minutes (300 / 10080) for the quota spans.
    var windowMinutes: Int? {
        switch self {
        case .fiveHour: return 300
        case .sevenDay: return 10080
        default: return nil
        }
    }

    /// Side-panel label ("Total · Day"), matching the web `RANGE_LABELS`.
    var label: String {
        switch self {
        case .fiveHour: return "5h"
        case .sevenDay: return "7d"
        case .day: return "Day"
        case .week: return "Week"
        case .month: return "Month"
        case .year: return "Year"
        case .custom: return "Custom"
        }
    }

    /// Compact label for the 380pt-popover segmented control.
    var shortLabel: String {
        switch self {
        case .fiveHour: return "5h"
        case .sevenDay: return "7d"
        case .day: return "Day"
        case .week: return "Wk"
        case .month: return "Mo"
        case .year: return "Yr"
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

/// History chart type (#750). Mirrors the web Chart segmented control. cost and
/// the models/providers presets measure USD; tokens measures token counts.
enum HistoryChart: String, CaseIterable, Identifiable {
    case cost, tokens, models, providers

    var id: String { rawValue }

    var label: String {
        switch self {
        case .cost: return "Cost"
        case .tokens: return "Tokens"
        case .models: return "Models"
        case .providers: return "Providers"
        }
    }

    /// True for the USD metrics (everything but tokens) — they render a $ axis
    /// and a cost forecast.
    var isCost: Bool { self != .tokens }

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

    var id: String { rawValue }

    var label: String {
        switch self {
        case .project: return "Project"
        case .branch: return "Branch"
        case .provider: return "Provider"
        case .model: return "Model"
        case .session: return "Session"
        }
    }

    var shortLabel: String {
        switch self {
        case .project: return "Proj"
        case .branch: return "Branch"
        case .provider: return "Prov"
        case .model: return "Model"
        case .session: return "Sess"
        }
    }

    /// The next finer axis to drill into, or nil for a leaf — mirrors the web
    /// DRILL_NEXT map (project → branch → session; provider/model → session).
    var drillNext: HistoryGroup? {
        switch self {
        case .project: return .branch
        case .branch: return .session
        case .provider: return .model
        case .model: return .session
        case .session: return nil
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
