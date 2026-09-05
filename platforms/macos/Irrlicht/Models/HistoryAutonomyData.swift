import Foundation

// Codable mirrors of the daemon's Autonomy payload (#1905) —
// `chart=autonomy_projects` in core/cmd/irrlichd/history_autonomy.go — plus the
// Range picker's vocabulary and the pure layout rules the panels draw with.
//
// FIVE PER-PROJECT PANELS, one per project, stacked. Each carries a LINE (the
// longest run in each time bucket) and, under it, a HISTOGRAM (how many runs
// were working at the same time in that bucket, derived by the daemon from the
// spans by overlap).
//
// WHAT THIS REPLACED. Two payloads and two elements: a p5–p95 band with a p50
// line, and a per-project run strip coloured by end reason. Both are gone —
// with the strip's legend, glyphs and collapse ladder, the Span picker that
// governed its window, and the sample floor that marked buckets whose p95 was
// really their maximum. A maximum over one run IS that run, so the floor has
// nothing left to protect.

/// The section's Range picker. Two windows, and 30 days is the floor.
///
/// The raw values are WINDOW LENGTHS sent as `?window=`, not the
/// bucket-width-times-count that `HistoryGranularity`'s same-looking keys mean.
///
/// There is no second picker. `HistoryAutonomySpanWindow` governed the run
/// strip's own window and went with the strip: one window, one control, and no
/// pair of textually-overlapping vocabularies for a reader to keep apart.
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

/// There is NO run-scope control (#1905 recording). The section counts every
/// run, subagent runs included, because Irrlicht recorded them — so there is
/// nothing to pick, and no mode a reader has to remember before reading a
/// figure. What a run WAS still matters: the `kind` field is what splits a
/// concurrency peak into sessions and subagents.

/// What one Autonomy payload is made of, by run kind (#1905 subagents).
///
/// The three counts describe the window, and — nothing being dropped for its
/// kind — the returned rows with it. They are load-bearing twice over: "42 runs"
/// reads differently once you know how many of them happened inside another,
/// and `unknown` is what tells a reader why a panel's `at once` figure
/// sometimes carries no split.
struct HistoryAutonomyKinds: Codable, Equatable {
    let topLevel: Int
    let subagent: Int
    /// Runs whose kind nothing established: rows written before the
    /// classification existed, and rows the back-fill rebuilt from a source
    /// carrying no parent information. Counted like the rest, and always named
    /// in the panel — a view that folded them silently into either of the other
    /// two would report a number nobody could check.
    let unknown: Int

    enum CodingKeys: String, CodingKey {
        case subagent, unknown
        case topLevel = "top_level"
    }
}

/// How many of the runs in one payload have a duration that is a FLOOR rather
/// than a measurement (#1905 recording).
///
/// Two kinds, kept apart because they are two different limits and a reader who
/// merged them would misread the panels in opposite directions:
///
///   - `running` — the run has not ended, so its length is how long it has
///     lasted SO FAR. It COUNTS towards the longest run (the section reports a
///     maximum, and "already lasted 3h" is true) and is marked "still going".
///   - `lowerBoundStart` — the run has FINISHED, but Irrlicht met it already in
///     progress, so its start is where the watching began. Dropping those is
///     what left 5 of a day's 35 runs on the record.
struct HistoryAutonomyMeasurement: Codable, Equatable {
    let running: Int
    let lowerBoundStart: Int

    enum CodingKeys: String, CodingKey {
        case running
        case lowerBoundStart = "start_lower_bound"
    }

    /// What a daemon that predates the field amounts to: nothing marked.
    static let none = HistoryAutonomyMeasurement(running: 0, lowerBoundStart: 0)

    /// Whether anything in view is a floor — the single condition under which
    /// the panel has something extra to say.
    var any: Bool { running > 0 || lowerBoundStart > 0 }
}

/// How much of one window was RECONSTRUCTED rather than measured (#1905
/// back-fill).
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
/// the LINE (#1905 back-fill, QA-2). The cost log cannot see a run shorter than
/// its 60 s write interval; the event log records one-second runs. The redesign
/// makes the marker MORE necessary rather than less: five stacked panels share
/// one x axis, so all five step at the same instant, and five simultaneous steps
/// read as five findings when they are one change of instrument.
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

/// Where a source-boundary caption is written, once the stack has five panels.
///
/// THE RULE READS ONCE ACROSS THE STACK. The dashed rule is drawn through EVERY
/// panel — it has to be, or it annotates one project's line and leaves the four
/// below it stepping for no stated reason — but the caption is drawn on the TOP
/// panel only. Five stacked copies of "← cost log · 60s resolution" down one x
/// position are five competing captions where the reader needs one, and at 9 pt
/// in a 380 pt popover they would collide with four project names.
enum AutonomyBoundaryCaption {
    /// The panel index that carries the words.
    static let panelIndex = 0

    static func isShown(panelIndex: Int) -> Bool { panelIndex == Self.panelIndex }

    /// Which side of its rule the caption hangs off.
    ///
    /// The caption is a fixed string that can be most of the plot wide at 9 pt,
    /// and pinned to the rule's LEFT unconditionally it would be clamped to the
    /// chart's leading edge for any rule in the left third — leaving the caption
    /// detached from the rule with its arrow pointing off-chart at nothing,
    /// which reads as a rendering fault rather than as an annotation.
    ///
    /// Putting it on whichever side of the rule has more room cannot be clamped
    /// unless the caption is wider than HALF the plot, and the arrow keeps its
    /// meaning either way: it points across the rule, at the era to its left.
    enum Side: Equatable { case left, right }

    static func side(fraction: Double) -> Side { fraction >= 0.5 ? .left : .right }
}

/// One time bucket of one project's panel.
///
/// A BUCKET WITH NOTHING IN IT IS OMITTED by the daemon, never sent with zeros —
/// a day with no runs is a gap, not a run of length zero next to nobody working.
/// So a bucket present here has `longest > 0` or `peak > 0`.
struct HistoryAutonomyPanelBucket: Codable, Identifiable, Equatable {
    let ts: Int64

    /// The longest run that ENDED in this bucket, in seconds; 0 when no run
    /// ended here.
    ///
    /// The consequence worth stating: a run that merely passed THROUGH a bucket
    /// raises that bucket's concurrency without putting a point on its line, so
    /// a bucket can carry a bar and no line point. That is the honest pair of
    /// facts — something was working, nothing finished.
    let longest: Double

    /// Whether this bucket's longest run HAS NOT ENDED: its length is how long
    /// it has lasted so far, a floor rather than a measurement. It is still the
    /// longest, and it is marked so nobody reads a floor as final.
    let running: Bool

    /// The greatest number of runs working AT THE SAME INSTANT anywhere in this
    /// bucket. PEAK, not average: peak answers "how wide did I go", where an
    /// average over a day is dominated by the hours nothing ran.
    let peak: Int

    /// `peak` split at the instant it was reached. Meaningful only when
    /// `peakSplit` is true.
    let peakTop: Int
    let peakSub: Int

    /// Whether every run alive at the peak instant said which kind it was, so
    /// the split is a derivation rather than a guess. False is the normal case
    /// for rows written before the classification existed.
    let peakSplit: Bool

    var id: Int64 { ts }

    enum CodingKeys: String, CodingKey {
        case ts, longest, running, peak
        case peakTop = "peak_top"
        case peakSub = "peak_sub"
        case peakSplit = "peak_split"
    }

    init(ts: Int64, longest: Double, running: Bool = false,
         peak: Int = 0, peakTop: Int = 0, peakSub: Int = 0, peakSplit: Bool = false) {
        self.ts = ts
        self.longest = longest
        self.running = running
        self.peak = peak
        self.peakTop = peakTop
        self.peakSub = peakSub
        self.peakSplit = peakSplit
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ts = try c.decode(Int64.self, forKey: .ts)
        longest = try c.decode(Double.self, forKey: .longest)
        // The three marks are omitempty on the wire: absent means false/zero.
        running = try c.decodeIfPresent(Bool.self, forKey: .running) ?? false
        peak = try c.decodeIfPresent(Int.self, forKey: .peak) ?? 0
        peakTop = try c.decodeIfPresent(Int.self, forKey: .peakTop) ?? 0
        peakSub = try c.decodeIfPresent(Int.self, forKey: .peakSub) ?? 0
        peakSplit = try c.decodeIfPresent(Bool.self, forKey: .peakSplit) ?? false
    }

    /// Whether this bucket puts a point on the line. A run of no length is not
    /// a run, so 0 is a GAP rather than a point on the floor.
    var hasLine: Bool { longest > 0 }

    /// Whether this bucket draws a bar. A zero-height bar on the axis reads as
    /// a measured zero, which is a different and false claim from "no runs".
    var hasBar: Bool { peak > 0 }
}

/// One project's panel: its two window-wide figures and its per-bucket series.
struct HistoryAutonomyPanel: Codable, Identifiable, Equatable {
    let project: String

    /// The longest run in the whole window, and whether it has ended.
    ///
    /// A STILL-RUNNING RUN COUNTS TOWARDS IT, unlike its old effect on a
    /// percentile: "the longest run already lasted 3h" is true and useful, where
    /// a p95 folding the same floor in would have claimed the top 5% of runs
    /// were shorter than they were.
    let longest: Double
    let longestRunning: Bool

    /// The sum of every run's length — the figure the panels are RANKED by. On
    /// the wire so the ranking can be checked against the panels rather than
    /// taken on trust.
    let totalSeconds: Double

    let runs: Int

    /// The most runs this project had working at once anywhere in the window,
    /// split the same way a bucket's is when the split is derivable.
    let peak: Int
    let peakTop: Int
    let peakSub: Int
    let peakSplit: Bool

    let buckets: [HistoryAutonomyPanelBucket]

    var id: String { project }

    enum CodingKeys: String, CodingKey {
        case project, longest, runs, peak, buckets
        case longestRunning = "longest_running"
        case totalSeconds = "total_seconds"
        case peakTop = "peak_top"
        case peakSub = "peak_sub"
        case peakSplit = "peak_split"
    }

    init(project: String, longest: Double, longestRunning: Bool = false, totalSeconds: Double = 0,
         runs: Int = 0, peak: Int = 0, peakTop: Int = 0, peakSub: Int = 0, peakSplit: Bool = false,
         buckets: [HistoryAutonomyPanelBucket] = []) {
        self.project = project
        self.longest = longest
        self.longestRunning = longestRunning
        self.totalSeconds = totalSeconds
        self.runs = runs
        self.peak = peak
        self.peakTop = peakTop
        self.peakSub = peakSub
        self.peakSplit = peakSplit
        self.buckets = buckets
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        project = try c.decode(String.self, forKey: .project)
        longest = try c.decode(Double.self, forKey: .longest)
        longestRunning = try c.decodeIfPresent(Bool.self, forKey: .longestRunning) ?? false
        totalSeconds = try c.decodeIfPresent(Double.self, forKey: .totalSeconds) ?? 0
        runs = try c.decodeIfPresent(Int.self, forKey: .runs) ?? 0
        peak = try c.decodeIfPresent(Int.self, forKey: .peak) ?? 0
        peakTop = try c.decodeIfPresent(Int.self, forKey: .peakTop) ?? 0
        peakSub = try c.decodeIfPresent(Int.self, forKey: .peakSub) ?? 0
        peakSplit = try c.decodeIfPresent(Bool.self, forKey: .peakSplit) ?? false
        buckets = try c.decodeIfPresent([HistoryAutonomyPanelBucket].self, forKey: .buckets) ?? []
    }

    /// The panel's figure line: its longest run and its widest moment, for the
    /// whole window. A still-running longest run is MARKED rather than dropped.
    var headline: String {
        var out = "longest " + AutonomyFormat.duration(longest)
        if longestRunning { out += " (still going)" }
        let concurrency = AutonomyFormat.concurrency(peak: peak, top: peakTop, sub: peakSub, splitKnown: peakSplit)
        return concurrency.isEmpty ? out : out + " · " + concurrency
    }

    /// The buckets aligned to the payload's `bucket_starts`, with `nil` for
    /// every bucket the daemon OMITTED — the macOS twin of the web's
    /// `autonomyPanelPoints`.
    ///
    /// The nil is the honesty rule, not a convenience. Reading `buckets`
    /// directly hands a dense list to whatever draws it, and the gap is simply
    /// not there any more.
    func alignedBuckets(_ bucketStarts: [Int64]) -> [HistoryAutonomyPanelBucket?] {
        var byTS: [Int64: HistoryAutonomyPanelBucket] = [:]
        for b in buckets { byTS[b.ts] = b }
        return bucketStarts.map { byTS[$0] }
    }
}

/// The window-wide figure row: the section's headline numbers across EVERY
/// project, not just the five drawn.
struct HistoryAutonomySummary: Codable, Equatable {
    let longest: Double
    let longestRunning: Bool
    let longestProject: String

    /// The highest per-project concurrency in the window — the widest any ONE
    /// project went, never a sum across projects, which would be a different
    /// and much larger number.
    let peak: Int
    let peakProject: String

    let runs: Int
    let projects: Int

    enum CodingKeys: String, CodingKey {
        case longest, peak, runs, projects
        case longestRunning = "longest_running"
        case longestProject = "longest_project"
        case peakProject = "peak_project"
    }

    init(longest: Double, longestRunning: Bool = false, longestProject: String = "",
         peak: Int = 0, peakProject: String = "", runs: Int = 0, projects: Int = 0) {
        self.longest = longest
        self.longestRunning = longestRunning
        self.longestProject = longestProject
        self.peak = peak
        self.peakProject = peakProject
        self.runs = runs
        self.projects = projects
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        longest = try c.decodeIfPresent(Double.self, forKey: .longest) ?? 0
        longestRunning = try c.decodeIfPresent(Bool.self, forKey: .longestRunning) ?? false
        longestProject = try c.decodeIfPresent(String.self, forKey: .longestProject) ?? ""
        peak = try c.decodeIfPresent(Int.self, forKey: .peak) ?? 0
        peakProject = try c.decodeIfPresent(String.self, forKey: .peakProject) ?? ""
        runs = try c.decodeIfPresent(Int.self, forKey: .runs) ?? 0
        projects = try c.decodeIfPresent(Int.self, forKey: .projects) ?? 0
    }
}

/// One entry of the panel stack, in draw order — the macOS twin of the web's
/// `autonomyStackRows`.
///
/// The client never re-decides how many panels there are: the daemon computes
/// five and says how many it left out, so the two surfaces cannot disagree the
/// way the run strip's twelve-rows-on-web against six-on-macOS did.
enum AutonomyStackRow: Identifiable, Equatable {
    case panel(HistoryAutonomyPanel, index: Int)
    case more(String)

    var id: String {
        switch self {
        case let .panel(p, _): return "panel-" + p.project
        case .more: return "more"
        }
    }
}

struct HistoryAutonomyProjectsResponse: Codable {
    let window: String
    let chart: String
    let start: Int64
    let end: Int64
    let bucketSeconds: Int64
    let bucketStarts: [Int64]

    /// The five most important projects, most autonomous time first.
    ///
    /// "Most important" is GREATEST TOTAL AUTONOMOUS TIME in the window, never
    /// longest single run: one lucky overnight run would otherwise promote a
    /// project nobody has touched in a month. The daemon ranks them; each panel
    /// carries `totalSeconds` so the ranking can be checked.
    let panels: [HistoryAutonomyPanel]
    let panelLimit: Int
    /// How many projects the window holds beyond the panels, all of them with
    /// less autonomous time than every panel above.
    let moreProjects: Int

    let summary: HistoryAutonomySummary

    /// Earliest span on record across the WHOLE log (0 = nothing ever
    /// recorded). What lets an empty section say "collecting since <date>"
    /// instead of being read as "you did nothing".
    let earliestSpan: Int64
    let totalRecorded: Int

    /// Optional so a payload from a daemon that predates the field decodes
    /// rather than failing outright — `provenanceOrNone` collapses the two
    /// "say nothing" cases into one.
    let provenance: HistoryAutonomyProvenance?
    /// What this payload is made of, by run kind. Optional for the same reason,
    /// and absent means SAY NOTHING — which is not the same claim as "there
    /// were none".
    let kinds: HistoryAutonomyKinds?
    /// How many of the runs in view are floors rather than measurements.
    let measurement: HistoryAutonomyMeasurement?

    enum CodingKeys: String, CodingKey {
        case window, chart, start, end, panels, summary, provenance, kinds, measurement
        case bucketSeconds = "bucket_seconds"
        case bucketStarts = "bucket_starts"
        case panelLimit = "panel_limit"
        case moreProjects = "more_projects"
        case earliestSpan = "earliest_span"
        case totalRecorded = "total_recorded"
    }

    var hasData: Bool { !panels.isEmpty }
    var provenanceOrNone: HistoryAutonomyProvenance { provenance ?? .none }
    var measurementOrNone: HistoryAutonomyMeasurement { measurement ?? .none }

    /// What the stack renders, in order: one entry per panel the daemon sent
    /// and, when the window holds more projects than those, a final entry
    /// naming the rest. An omission nothing mentions is indistinguishable from
    /// a project that never ran.
    var stackRows: [AutonomyStackRow] {
        var out: [AutonomyStackRow] = panels.enumerated().map { .panel($1, index: $0) }
        if let label = overflowLabel { out.append(.more(label)) }
        return out
    }

    /// What the five-panel view left out, and WHY those are the ones missing —
    /// so the reader knows the section took the tail rather than an arbitrary
    /// slice. `nil` when every project already has a panel.
    var overflowLabel: String? {
        guard moreProjects > 0 else { return nil }
        return "+\(moreProjects) more project\(moreProjects == 1 ? "" : "s"), each with less autonomous time"
    }

    /// The source boundaries that fall inside the DRAWN domain, so the panels
    /// mark only what the reader can actually see.
    ///
    /// STRICTLY inside: a boundary at the very first or very last bucket would
    /// draw a rule on the axis itself, marking nothing and reading as a panel
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

    /// The log Y domain SHARED by all five panels, `nil` when nothing in the
    /// window has a length to plot.
    ///
    /// Shared, so the projects are comparable — that is the point of picking
    /// five. Log, because a project whose best is two minutes would otherwise be
    /// a flat line under one whose best is eleven hours. Each panel states its
    /// own longest as a NUMBER in its header, so the exact figure never depends
    /// on reading the axis.
    var sharedYDomain: ClosedRange<Double>? {
        let values = panels.flatMap { $0.buckets.map(\.longest) }.filter { $0 > 0 }
        guard let smallest = values.min(), let biggest = values.max() else { return nil }
        // Floored at 1s: a log scale cannot plot 0, and a sub-second span is
        // not a run.
        let lo = Swift.max(1, smallest * 0.8)
        let hi = Swift.max(lo * 2, biggest * 1.25)
        return lo...hi
    }

    /// The histogram's SHARED full-height value: the highest peak any panel
    /// reaches. 0 when nothing overlapped anywhere, in which case no bar is
    /// drawn at all rather than every bar being drawn full height.
    var sharedPeakScale: Int {
        panels.flatMap { $0.buckets.map(\.peak) }.max() ?? 0
    }
}

/// The contiguous stretches a panel's line may be stroked over — as a pure
/// function so the gap rule is testable without a view, and so this and the
/// web's `autonomyLineSegments` break the line in the same places.
///
/// TWO KINDS OF GAP, and they are not the same gap. A bucket can be absent
/// entirely (nothing happened), or present with `longest == 0` — a run passed
/// THROUGH it without finishing in it, so something was working and nothing
/// finished. Both break the line; only the first also removes the bar. Drawing a
/// point at zero in the second case would claim a run of no length.
enum AutonomyLineLayout {
    struct Segment: Equatable, Identifiable {
        let from: Int
        let to: Int

        var id: String { "\(from)-\(to)" }
        var isIsolated: Bool { from == to }
    }

    static func segments(points: [HistoryAutonomyPanelBucket?]) -> [Segment] {
        var out: [Segment] = []
        var start: Int?
        for (i, point) in points.enumerated() {
            if point?.hasLine == true {
                if start == nil { start = i }
            } else if let from = start {
                out.append(Segment(from: from, to: i - 1))
                start = nil
            }
        }
        if let from = start { out.append(Segment(from: from, to: points.count - 1)) }
        return out
    }
}
