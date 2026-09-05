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

/// Which runs the section counts (#1905 subagents) — the section-wide control,
/// unlike Range and Span, which each drive one element.
///
/// `topLevel` is the default because a subagent's run is a NESTED INTERVAL
/// inside its parent's: the daemon deliberately holds a parent `working` while
/// its children run, so counting both counts one stretch of wall clock twice
/// and drags the headline median down with short nested runs.
enum HistoryAutonomyRunScope: String, CaseIterable, Identifiable {
    case topLevel = "top"
    case all

    var id: String { rawValue }

    var label: String {
        switch self {
        case .topLevel: return "Top-level"
        case .all: return "+ subagents"
        }
    }

    /// What to send as `?include_subagents=`. Only the affirmative case sends
    /// anything: the daemon's default is top-level runs, so the default view
    /// asks for nothing extra and an older daemon behaves the same way rather
    /// than silently including runs the panel says it excluded.
    var includeSubagentsParam: String? { self == .all ? "true" : nil }
}

/// What one Autonomy payload counted, and what it left out (#1905 subagents).
///
/// The three counts describe the WINDOW, not the returned rows: `subagent` is
/// how many subagent runs the window holds whether or not they were returned,
/// which is exactly the number the panel prints as "N excluded". The mode
/// travels with them so a panel drawn from a response that predates the last
/// toggle still labels that response correctly.
struct HistoryAutonomyKinds: Codable, Equatable {
    /// `"top_level"` or `"all"` — mirrors the daemon's autonomyModeName.
    let mode: String
    let topLevel: Int
    let subagent: Int
    /// Runs whose kind nothing established: rows written before the
    /// classification existed, and rows the back-fill rebuilt from a source
    /// carrying no parent information. ALWAYS counted in the figures, and
    /// always named in the panel — a view that folded them silently into
    /// either of the other two would report a number nobody could check.
    let unknown: Int

    enum CodingKeys: String, CodingKey {
        case mode, subagent, unknown
        case topLevel = "top_level"
    }

    var includesSubagents: Bool { mode == "all" }

    /// What a daemon that predates the field amounts to: nothing known, so
    /// nothing claimed. `AutonomyFormat.modeLine` returns nil for it rather
    /// than asserting a mode this payload never stated.
    static let unavailable = HistoryAutonomyKinds(mode: "", topLevel: 0, subagent: 0, unknown: 0)
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
        case reconstructed, boundaries
        case costDerived = "cost_derived"
        case liveSince = "live_since"
    }

    /// Spelled out rather than left to the synthesized memberwise init so
    /// `boundaries` can default: a provenance with no source handover is the
    /// normal case (every machine that was never back-filled), and every call
    /// site that predates boundaries means exactly that.
    init(reconstructed: Int, costDerived: Int, liveSince: Int64, boundaries: [HistoryAutonomyBoundary]? = []) {
        self.reconstructed = reconstructed
        self.costDerived = costDerived
        self.liveSince = liveSince
        self.boundaries = boundaries
    }

    /// Instants where the PROVENANCE of the data changes, oldest first. Empty
    /// when everything on record came from one source.
    let boundaries: [HistoryAutonomyBoundary]?

    /// What a daemon that predates the field, or a window measured end to end,
    /// amounts to. Both mean "say nothing".
    static let none = HistoryAutonomyProvenance(reconstructed: 0, costDerived: 0, liveSince: 0, boundaries: [])

    var isReconstructed: Bool { reconstructed > 0 }
    var boundaryList: [HistoryAutonomyBoundary] { boundaries ?? [] }
}

/// One instant where the data's provenance changes — a run drawn to the LEFT
/// of it came from `from`, one to the right from `to`.
///
/// It exists because the provenance PARAGRAPH cannot fix what the eye reads off
/// the CURVE (#1905 back-fill, QA-2). The cost log cannot see a run shorter
/// than its 60 s write interval; the event log records one-second runs. Across
/// that boundary the p5 line steps by two orders of magnitude, and a reader
/// takes a change of instrument for a change of behaviour. The marker puts the
/// explanation where the artefact is.
struct HistoryAutonomyBoundary: Codable, Equatable, Identifiable {
    let ts: Int64
    let from: String
    let to: String

    var id: Int64 { ts }

    var date: Date { Date(timeIntervalSince1970: TimeInterval(ts)) }

    /// What lies to the LEFT of this line, and at what resolution — the arrow
    /// is load-bearing, since it is what makes the caption describe the data
    /// before the marker rather than the marker itself.
    var label: String { "← " + Self.eraLabels[from, default: from.isEmpty ? "a different source" : from] }

    private static let eraLabels = [
        "cost": "cost log · 60s resolution",
        "log": "event log · rebuilt",
        "live": "measured",
    ]
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
    /// What this payload counted (#1905 subagents). Optional for the same
    /// reason as `provenance`, and absent means SAY NOTHING — never "it counted
    /// top-level runs", which is a claim a daemon that predates the field
    /// cannot make.
    let kinds: HistoryAutonomyKinds?

    enum CodingKeys: String, CodingKey {
        case window, chart, start, end, buckets, summary, provenance, kinds
        case bucketSeconds = "bucket_seconds"
        case bucketStarts = "bucket_starts"
        case sampleFloor = "sample_floor"
        case earliestSpan = "earliest_span"
        case totalRecorded = "total_recorded"
    }

    var hasData: Bool { !buckets.isEmpty }
    var provenanceOrNone: HistoryAutonomyProvenance { provenance ?? .none }
    var kindsOrUnavailable: HistoryAutonomyKinds { kinds ?? .unavailable }

    /// The source boundaries that fall inside the DRAWN domain, so the chart
    /// marks only what the reader can actually see.
    ///
    /// STRICTLY inside: a boundary at the very first or very last bucket would
    /// draw a rule on the axis itself, marking nothing and reading as a chart
    /// border. A range that does not straddle a boundary gets none — which is
    /// every range on a machine that was never back-filled.
    var visibleBoundaries: [HistoryAutonomyBoundary] {
        guard let first = bucketStarts.first, let last = bucketStarts.last, last > first else { return [] }
        return provenanceOrNone.boundaryList.filter { $0.ts > first && $0.ts < last }
    }

    /// Where in the DRAWN domain a boundary sits, 0…1 — the macOS twin of the
    /// web's `autonomyVisibleBoundaries().fraction`. 0 for a boundary that is
    /// not in view, which `visibleBoundaries` has already excluded.
    func domainFraction(of boundary: HistoryAutonomyBoundary) -> Double {
        guard let first = bucketStarts.first, let last = bucketStarts.last, last > first else { return 0 }
        return Double(boundary.ts - first) / Double(last - first)
    }

    /// The buckets aligned to `bucket_starts`, with `nil` for every bucket the
    /// daemon OMITTED — the macOS twin of the web's `autonomyChartPoints`.
    ///
    /// The null is the honesty rule, not a convenience: an empty bucket is a
    /// GAP, and a day with no runs must not pull a line down to the axis or
    /// let a filled band close over it. Reading `buckets` directly — which is
    /// what this section did before the band existed — hands Swift Charts a
    /// dense list in which the gap is simply not there, and it connects
    /// straight across.
    var alignedBuckets: [HistoryAutonomyBucket?] {
        var byTS: [Int64: HistoryAutonomyBucket] = [:]
        for b in buckets { byTS[b.ts] = b }
        return bucketStarts.map { byTS[$0] }
    }
}

/// Where a source-boundary caption sits relative to the rule it describes.
///
/// The caption is a fixed string ("← cost log · 60s resolution") that can be
/// most of the plot wide at 9 pt, and it used to be pinned to the rule's LEFT
/// unconditionally. A rule in the left third of the chart therefore had less
/// room than the caption needed, SwiftUI clamped the annotation to the chart's
/// leading edge, and the caption ended up detached from the rule with its
/// arrow pointing off-chart at nothing — which is worse than no caption, since
/// it reads as a rendering fault rather than as an annotation.
///
/// Putting it on whichever side of the rule has more room cannot be clamped
/// unless the caption is wider than HALF the plot, and the arrow keeps its
/// meaning either way: it points across the rule, at the era to its left.
enum AutonomyBoundaryCaption {
    enum Side: Equatable { case left, right }

    static func side(fraction: Double) -> Side { fraction >= 0.5 ? .left : .right }
}

/// The p5–p95 band's geometry (#1905 redesign), as a pure function so both the
/// gap rule and the thin-bucket rule are testable without a chart — and so
/// this and the web's `autonomyBandSegments` fill the same shape.
///
/// TWO RULES, both of them honesty rules a smooth fill would otherwise erase:
///
///   - **A filled area wants to close across a gap.** The daemon omits empty
///     buckets; a polygon spanning one paints a plane over days that hold no
///     runs at all, a stronger false claim than the interpolated line #1905
///     already refuses. A segment never crosses a `nil`.
///   - **A thin bucket is not a percentile.** Under `sample_floor`, p95 is
///     that bucket's longest run and p5 its shortest. The stroke already
///     dashes across such a bucket; the fill splits at the same place so the
///     thin stretch can be painted in its own fainter plane.
enum AutonomyBandLayout {
    /// One fillable stretch: inclusive indices into the aligned point list,
    /// and whether it is thin. Adjacent segments SHARE their boundary index,
    /// so a thin→solid handover leaves no seam. `from == to` is an isolated
    /// bucket — it has no neighbour to make an area with, and the chart draws
    /// its spread as a whisker rather than leaving it the one bucket with no
    /// visible range at all.
    struct Segment: Equatable, Identifiable {
        let from: Int
        let to: Int
        let thin: Bool

        var id: String { "\(from)-\(to)-\(thin)" }
        var isIsolated: Bool { from == to }
    }

    static func segments(points: [HistoryAutonomyBucket?]) -> [Segment] {
        var out: [Segment] = []
        var i = 0
        while i < points.count {
            guard points[i] != nil else { i += 1; continue }
            var last = i
            while last + 1 < points.count, points[last + 1] != nil { last += 1 }
            if last == i {
                out.append(Segment(from: i, to: i, thin: points[i]?.thin ?? false))
                i = last + 1
                continue
            }
            // Thinness belongs to the INTERVAL, not the bucket — matching the
            // stroke, which dashes a segment either of whose ends is thin.
            var start = i
            var thin = isThin(points, i) || isThin(points, i + 1)
            var j = i + 1
            while j < last {
                let next = isThin(points, j) || isThin(points, j + 1)
                if next != thin {
                    out.append(Segment(from: start, to: j, thin: thin))
                    start = j
                    thin = next
                }
                j += 1
            }
            out.append(Segment(from: start, to: last, thin: thin))
            i = last + 1
        }
        return out
    }

    private static func isThin(_ points: [HistoryAutonomyBucket?], _ i: Int) -> Bool {
        points[i]?.thin ?? false
    }
}

struct HistoryAutonomySpanRow: Codable, Identifiable {
    let start: Int64
    let end: Int64
    let project: String
    let session: String
    let reason: String?
    /// `"top"`, `"sub"` or `"unknown"` — the daemon resolves a blank row before
    /// it ships, so this is absent only from a payload that predates the field.
    let kind: String?
    /// The parent session id of a subagent run; absent otherwise.
    let parent: String?

    /// Spelled out rather than left to the synthesized memberwise init, so
    /// `kind` and `parent` can default — the same reason
    /// `HistoryAutonomyProvenance` spells its own out. A call site that says
    /// nothing about the kind means exactly what a row that says nothing means:
    /// nobody established it.
    init(start: Int64, end: Int64, project: String, session: String,
         reason: String?, kind: String? = nil, parent: String? = nil) {
        self.start = start
        self.end = end
        self.project = project
        self.session = session
        self.reason = reason
        self.kind = kind
        self.parent = parent
    }

    var id: String { "\(project)|\(session)|\(start)|\(end)" }

    var endReason: AutonomyEndReason? { reason.flatMap(AutonomyEndReason.init(rawValue:)) }
    var duration: Int64 { Swift.max(0, end - start) }
    /// Whether this run belonged to a subagent. FALSE for a row that never said
    /// — which is "nothing established it", not "it was top-level"; the strip
    /// draws such a row either way, and the panel's mode line is where the
    /// unknown count is stated.
    var isSubagentRun: Bool { kind == "sub" }
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
    let kinds: HistoryAutonomyKinds?

    enum CodingKeys: String, CodingKey {
        case window, chart, start, end, spans, projects, truncated, provenance, kinds
        case earliestSpan = "earliest_span"
        case totalRecorded = "total_recorded"
    }

    var hasData: Bool { !spans.isEmpty }
    var provenanceOrNone: HistoryAutonomyProvenance { provenance ?? .none }
    var kindsOrUnavailable: HistoryAutonomyKinds { kinds ?? .unavailable }

    func spans(for project: String) -> [HistoryAutonomySpanRow] {
        spans.filter { $0.project == project }
    }

    /// How many project rows the run strip draws.
    ///
    /// The strip had no cap at all, which was invisible until there was
    /// history to draw: a 12mo window over a back-filled log renders 95 rows
    /// (#1905 back-fill, QA-1), and this surface is a 380 pt popover. Six is
    /// what fits under a 190 pt chart and its summary without pushing the
    /// legend and the provenance line — the two things that explain the strip
    /// — off the bottom.
    ///
    /// Lower than the web's twelve ON PURPOSE, and that is not a disagreement:
    /// the daemon ranks `projects` by TOTAL AUTONOMOUS SECONDS, so each surface
    /// shows a PREFIX of one shared order. Neither invents an ordering, and one
    /// four-hour run still outranks forty three-second ones on both.
    static let maxStripRows = 6

    /// The rows actually drawn — the busiest `maxStripRows` projects.
    var visibleProjects: [String] { Array(projects.prefix(Self.maxStripRows)) }

    /// How many projects the cap left out. 0 when it did not bite.
    var hiddenProjectCount: Int { Swift.max(0, projects.count - Self.maxStripRows) }

    /// What the cap left out, named as a count rather than dropped in silence
    /// — an omission nothing mentions is indistinguishable from a project that
    /// never ran. It says WHY those rows are the missing ones, so the reader
    /// knows the cap took the tail rather than an arbitrary slice.
    var overflowLabel: String? {
        let hidden = hiddenProjectCount
        guard hidden > 0 else { return nil }
        return "+\(hidden) more project\(hidden == 1 ? "" : "s"), each with less autonomous time than the rows above"
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
