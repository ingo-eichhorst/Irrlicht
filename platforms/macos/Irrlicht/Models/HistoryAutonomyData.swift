import Foundation

// Codable mirrors of the daemon's Autonomy payloads (#1905) —
// `chart=autonomy_duration` and `chart=autonomy_spans` in
// core/cmd/irrlichd/history_autonomy.go — plus the two pickers' vocabularies
// and the strip's pure collapse rule.
//
// An autonomy span is one unbroken stretch of `working` before the session
// left that state; the state it left FOR is the signal the section is about.

/// Element 1's Range picker. Two ranges, and 30 days is the floor: anything
/// shorter has too few spans per bucket for a percentile to mean anything.
///
/// The raw values are WINDOW LENGTHS sent as `?window=`, not the
/// bucket-width-times-count that `HistoryGranularity`'s same-looking keys mean.
enum HistoryAutonomyRange: String, CaseIterable, Identifiable {
    case days30 = "30d"
    case year = "1y"

    var id: String { rawValue }

    var label: String {
        switch self {
        case .days30: return "30 days"
        case .year: return "Year"
        }
    }
}

/// Element 2's Span picker — window lengths again, and deliberately a
/// different set from element 1's.
enum HistoryAutonomySpanWindow: String, CaseIterable, Identifiable {
    case hours8 = "8h"
    case hours24 = "24h"
    case days7 = "7d"
    case days30 = "30d"
    case months12 = "12mo"

    var id: String { rawValue }

    var label: String {
        switch self {
        case .hours8: return "8h"
        case .hours24: return "24h"
        case .days7: return "7d"
        case .days30: return "30d"
        case .months12: return "12mo"
        }
    }
}

/// How a span ended — the state the session left `working` for. Raw values
/// mirror `session.AutonomyEndReasons()` on the daemon.
///
/// One case per line, and one state per line in `priority` below: a single
/// line naming three of the four canonical states is exactly what
/// tools/state-vocabulary-lint.sh refuses, because such a list is complete
/// when written and silently stale one state later.
enum AutonomyEndReason: String, CaseIterable, Identifiable {
    case waiting
    case ready
    case error

    var id: String { rawValue }

    /// Glyph shown in the strip legend, matching the issue's sketch.
    var glyph: String {
        switch self {
        case .waiting: return "?"
        case .ready: return "✓"
        case .error: return "✗"
        }
    }

    var label: String {
        switch self {
        case .waiting: return "it asked"
        case .ready: return "turn finished"
        case .error: return "error"
        }
    }

    /// Rank on the strip's pixel-collapse ladder: when one device-pixel column
    /// holds several spans, the column paints the highest-ranked reason in it.
    ///
    /// Same order as the session-history strip's ladder (#1805) — one error in
    /// a column paints the whole column. `HistoryAutonomyTests` pins this
    /// against `SessionManager.historyPriorityForState`, which is the other
    /// hand-written copy of the same ladder.
    var priority: Int {
        switch self {
        case .error: return 3
        case .waiting: return 2
        case .ready: return 1
        }
    }
}

/// How much of one window was RECONSTRUCTED rather than measured (#1905
/// back-fill), carried by both Autonomy payloads.
///
/// `tools/autonomy-backfill` rebuilds pre-feature runs from logs a machine
/// already had, and marks every row it writes. The daemon never runs it — it
/// is a one-off the maintainer runs by hand — but it serves what it wrote, and
/// a reconstructed figure rendered as a measured one is precisely the "wrong
/// number with nothing on screen saying so" this section was built to avoid.
struct HistoryAutonomyProvenance: Codable, Equatable {
    /// Runs in THIS window that were reconstructed.
    let reconstructed: Int
    /// The subset of those whose end reason is unknown and cannot be
    /// recovered: their source records activity, never outcome.
    let costDerived: Int
    /// The earliest MEASURED span across the whole log — the instant before
    /// which everything on record is reconstructed. 0 when nothing has ever
    /// been measured live, which is a different claim from "since the epoch"
    /// and is why every reader tests it for zero before formatting a date.
    let liveSince: Int64

    enum CodingKeys: String, CodingKey {
        case reconstructed
        case costDerived = "cost_derived"
        case liveSince = "live_since"
    }

    /// What a daemon that predates the field, or a window measured end to end,
    /// amounts to. Both mean "say nothing".
    static let none = HistoryAutonomyProvenance(reconstructed: 0, costDerived: 0, liveSince: 0)

    var isReconstructed: Bool { reconstructed > 0 }
}

/// One entry of the run strip's legend.
///
/// `reason` is optional because the neutral entry stands for every run whose
/// end reason nothing can name — a cost-derived span carrying `unknown`, and
/// an old row written before the reason was recorded. Both draw the same
/// neutral column, so one entry has to cover both or one of them is a colour
/// with no key.
struct AutonomyLegendEntry: Identifiable, Equatable {
    let reason: AutonomyEndReason?
    let glyph: String
    let label: String

    var id: String { reason?.rawValue ?? "unknown" }
}

enum AutonomyLegend {
    static let unknown = AutonomyLegendEntry(reason: nil, glyph: "·", label: "end reason unknown")

    /// The legend for one window: the measured reasons, plus the neutral entry
    /// ONLY when the window actually holds a run with no nameable reason.
    ///
    /// Conditional on the data rather than always shown, because a legend
    /// explains the colours in front of the reader. A permanent fourth swatch
    /// for a colour the strip is not drawing invites the opposite mistake —
    /// reading the absence of neutral columns as an absence of runs.
    static func entries(for spans: [HistoryAutonomySpanRow]) -> [AutonomyLegendEntry] {
        var out = AutonomyEndReason.allCases.map {
            AutonomyLegendEntry(reason: $0, glyph: $0.glyph, label: $0.label)
        }
        if spans.contains(where: { $0.endReason == nil }) { out.append(unknown) }
        return out
    }
}

/// One bucket of element 1. The daemon OMITS empty buckets, so every bucket
/// present here has `count >= 1` — a day with no runs is a gap in the line,
/// never a point on the axis.
struct HistoryAutonomyBucket: Codable, Identifiable {
    let ts: Int64
    let p95: Double
    let p50: Double
    let p5: Double
    let min: Double
    let max: Double
    let count: Int
    /// True when the bucket holds fewer than `sampleFloor` spans, so its p95
    /// IS its max and its p5 IS its min. Rendered visibly differently (dashed
    /// + faded), never hidden and never smoothed.
    let thin: Bool

    var id: Int64 { ts }

    enum CodingKeys: String, CodingKey {
        case ts, p95, p50, p5, min, max, count, thin
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ts = try c.decode(Int64.self, forKey: .ts)
        p95 = try c.decode(Double.self, forKey: .p95)
        p50 = try c.decode(Double.self, forKey: .p50)
        p5 = try c.decode(Double.self, forKey: .p5)
        min = try c.decode(Double.self, forKey: .min)
        max = try c.decode(Double.self, forKey: .max)
        count = try c.decode(Int.self, forKey: .count)
        // `thin` is omitempty on the wire: absent means false.
        thin = try c.decodeIfPresent(Bool.self, forKey: .thin) ?? false
    }
}

/// The window-wide figure row under the chart. The true extremes stay FIGURES
/// and are deliberately not lines: one four-hour run left going overnight
/// would otherwise redraw the whole Y scale and flatten every other bucket.
struct HistoryAutonomySummary: Codable {
    let p95: Double
    let p50: Double
    let p5: Double
    let min: Double
    let max: Double
    let count: Int
}

struct HistoryAutonomyDurationResponse: Codable {
    let window: String
    let chart: String
    let start: Int64
    let end: Int64
    let bucketSeconds: Int64
    let bucketStarts: [Int64]
    let buckets: [HistoryAutonomyBucket]
    let summary: HistoryAutonomySummary
    let sampleFloor: Int
    /// Earliest span on record across the WHOLE log (0 = nothing ever
    /// recorded). What lets an empty chart say "collecting since <date>"
    /// instead of being read as "you did nothing".
    let earliestSpan: Int64
    let totalRecorded: Int
    /// Optional so a payload from a daemon that predates the field decodes
    /// rather than failing outright — `provenanceOrNone` collapses the two
    /// "say nothing" cases into one.
    let provenance: HistoryAutonomyProvenance?

    enum CodingKeys: String, CodingKey {
        case window, chart, start, end, buckets, summary, provenance
        case bucketSeconds = "bucket_seconds"
        case bucketStarts = "bucket_starts"
        case sampleFloor = "sample_floor"
        case earliestSpan = "earliest_span"
        case totalRecorded = "total_recorded"
    }

    var hasData: Bool { !buckets.isEmpty }
    var provenanceOrNone: HistoryAutonomyProvenance { provenance ?? .none }
}

struct HistoryAutonomySpanRow: Codable, Identifiable {
    let start: Int64
    let end: Int64
    let project: String
    let session: String
    let reason: String?

    var id: String { "\(project)|\(session)|\(start)|\(end)" }

    var endReason: AutonomyEndReason? { reason.flatMap(AutonomyEndReason.init(rawValue:)) }
    var duration: Int64 { Swift.max(0, end - start) }
}

struct HistoryAutonomySpansResponse: Codable {
    let window: String
    let chart: String
    let start: Int64
    let end: Int64
    let spans: [HistoryAutonomySpanRow]
    /// Strip row order, computed daemon-side (most autonomous seconds first)
    /// so both clients draw the same rows in the same order.
    let projects: [String]
    let earliestSpan: Int64
    let totalRecorded: Int
    let truncated: Bool
    let provenance: HistoryAutonomyProvenance?

    enum CodingKeys: String, CodingKey {
        case window, chart, start, end, spans, projects, truncated, provenance
        case earliestSpan = "earliest_span"
        case totalRecorded = "total_recorded"
    }

    var hasData: Bool { !spans.isEmpty }
    var provenanceOrNone: HistoryAutonomyProvenance { provenance ?? .none }

    func spans(for project: String) -> [HistoryAutonomySpanRow] {
        spans.filter { $0.project == project }
    }
}

/// The strip's pixel-collapse rule (#1905 design decision 4), as a pure
/// function so it can be tested without a view.
///
/// TWO HALVES, both load-bearing:
///
///   - **Every span draws at a minimum of one column.** At `12mo` a 40-second
///     run is far under one pixel wide; rounding it to nothing would erase
///     exactly the short runs the p5 line is about.
///   - **A column holding several spans takes the HIGHEST-priority end
///     reason** — error over waiting over ready, `AutonomyEndReason.priority`.
///     The same ladder the session-history strip uses: one error in a column
///     paints the whole column, because the thing worth seeing at a glance is
///     that something broke in there.
enum AutonomyStripLayout {
    /// One drawn column of the strip. `occupied` and `reason` are separate
    /// because a span whose reason this build cannot name STILL HAPPENED: it
    /// occupies its column (drawn neutral) rather than reading as idle.
    struct Column: Equatable {
        var occupied: Bool
        var reason: AutonomyEndReason?
    }

    /// Collapse `spans` into `columns` columns covering [start, end).
    static func collapse(spans: [HistoryAutonomySpanRow],
                         start: Int64,
                         end: Int64,
                         columns: Int) -> [Column] {
        guard columns > 0, end > start else { return [] }
        var out = [Column](repeating: Column(occupied: false, reason: nil), count: columns)
        let width = Double(end - start)
        for row in spans {
            var first = Int((Double(row.start - start) / width * Double(columns)).rounded(.down))
            var last = Int((Double(row.end - start) / width * Double(columns)).rounded(.down))
            // A span entirely outside the window contributes nothing; one that
            // straddles an edge is clipped to the visible part.
            if last < 0 || first > columns - 1 { continue }
            first = Swift.max(0, first)
            last = Swift.min(columns - 1, last)
            // The minimum-one-column rule: a sub-pixel run still draws.
            if last < first { last = first }
            let rank = row.endReason?.priority ?? 0
            for i in first...last {
                // -1 for an untouched column, so even an unnamed reason (rank
                // 0) claims it.
                let existingRank = out[i].occupied ? (out[i].reason?.priority ?? 0) : -1
                out[i].occupied = true
                if rank > existingRank { out[i].reason = row.endReason }
            }
        }
        return out
    }
}
