import Foundation

// Codable mirrors of the daemon's Autonomy payloads (#1905) —
// `chart=autonomy_duration` and `chart=autonomy_projects` in
// core/cmd/irrlichd/history_autonomy*.go — plus the Range picker's vocabulary
// and the pure layout rules both elements draw with.
//
// TWO ELEMENTS over one window:
//
//   THE AGGREGATE CHART — p95/p50/p5 of run duration across EVERY project, with
//   the plane between p95 and p5 filled as a band.
//   ONE PROJECT PANEL — a LINE (the longest run in each time bucket) and, under
//   it, a HISTOGRAM (how many runs were working at the same time in that
//   bucket, derived by the daemon from the spans by overlap). Exactly one is
//   drawn; a Picker in the panel's own header chooses which.
//
// WHAT IS STILL GONE. The per-project run strip, its legend, glyphs and
// collapse ladder, and the Span picker that governed its window. The section's
// subject is how LONG a run was, not how it ended.

/// The section's Range picker. Two windows, and 30 days is the floor: anything
/// shorter has too few spans per bucket for a percentile to mean anything.
///
/// The raw values are WINDOW LENGTHS sent as `?window=`, not the
/// bucket-width-times-count that `HistoryGranularity`'s same-looking keys mean.
///
/// ONE picker for BOTH elements. They are read together — one above the other,
/// over the same instants — so two windows would let the chart show a month
/// while the panel under it showed a year, with nothing on screen saying they
/// disagreed. `HistoryAutonomySpanWindow` was exactly that and went with the
/// strip.
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

/// Where a source-boundary caption is written, given the section's two elements.
///
/// THE RULE READS ONCE ACROSS THE SECTION. The dashed rule is drawn on BOTH
/// elements — it has to be, or it annotates the aggregate line and leaves the
/// panel below it stepping for no stated reason — but the caption is written by
/// the AGGREGATE chart only, which is the top one. Two stacked copies of
/// "← cost log · 60s resolution" down one x position are two competing captions
/// where the reader needs one, and at 9 pt in a 380 pt popover the lower one
/// collides with the panel's own header row.
enum AutonomyBoundaryCaption {
    /// The section's two drawn elements, in the order they appear.
    enum Element: String, CaseIterable { case aggregate, panel }

    /// The element that carries the words.
    static let captionedBy = Element.aggregate

    static func isShown(element: Element) -> Bool { element == Self.captionedBy }

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

/// Which project the single panel draws — the macOS twin of the web's
/// `autonomyPanelChoice`.
///
/// THREE OUTCOMES, and the third is the one that has to be written down rather
/// than fallen into:
///
///   - nothing selected → rank 1, the project with the most autonomous time in
///     the window. A freshly opened tab needs no stored name.
///   - the selection is in this range → that panel.
///   - the selection is NOT in this range → `isMissing`, and the selection is
///     KEPT. Silently falling back to rank 1 would answer a question the reader
///     did not ask, and would make a Range change look like a click they never
///     made; the panel says "no runs for <project> in this range" instead, and
///     the Picker still carries the name so one Range change back restores it.
struct AutonomyPanelChoice: Equatable {
    let panel: HistoryAutonomyPanel?
    let project: String

    var isMissing: Bool { panel == nil && !project.isEmpty }
}

/// One entry of the project Picker — the macOS twin of the web's
/// `autonomyProjectOptions`.
struct AutonomyProjectOption: Identifiable, Equatable {
    let project: String
    /// Whether this range holds no runs for it, in which case the Picker says
    /// so rather than offering a name that draws nothing with no explanation.
    let isMissing: Bool

    var id: String { project }
    var label: String { isMissing ? project + " — no runs in this range" : project }
}

struct HistoryAutonomyProjectsResponse: Codable {
    let window: String
    let chart: String
    let start: Int64
    let end: Int64
    let bucketSeconds: Int64
    let bucketStarts: [Int64]

    /// EVERY project with a run in the window, most autonomous time first, up
    /// to the daemon's safety cap.
    ///
    /// Every one of them, because exactly ONE is drawn and the rest are offered
    /// in a Picker: a payload carrying only the top few would leave the Picker
    /// unable to show a project the summary above it counts.
    ///
    /// The rank is GREATEST TOTAL AUTONOMOUS TIME in the window, never longest
    /// single run: one lucky overnight run would otherwise promote a project
    /// nobody has touched in a month. The daemon ranks them; each panel carries
    /// `totalSeconds` so the ranking can be checked.
    let panels: [HistoryAutonomyPanel]
    let panelLimit: Int
    /// How many projects the window holds beyond the cap, all of them with less
    /// autonomous time than every panel above. 0 on every machine seen so far.
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

    /// Which project the panel draws, given the reader's selection (nil = none
    /// made yet). See `AutonomyPanelChoice` for why an absent selection is kept
    /// rather than silently replaced.
    func choice(selected: String?) -> AutonomyPanelChoice {
        guard let selected, !selected.isEmpty else {
            return AutonomyPanelChoice(panel: panels.first, project: panels.first?.project ?? "")
        }
        if let found = panels.first(where: { $0.project == selected }) {
            return AutonomyPanelChoice(panel: found, project: selected)
        }
        return AutonomyPanelChoice(panel: nil, project: selected)
    }

    /// The Picker's contents: every project the window holds, ranked, plus the
    /// selected one when this range does not hold it.
    ///
    /// The absent project goes LAST, which is the same rule the rest follow
    /// rather than an exception to it: the order is most autonomous time first,
    /// and a project with no runs in this range has none of it.
    func projectOptions(selected: String?) -> [AutonomyProjectOption] {
        var out = panels.map { AutonomyProjectOption(project: $0.project, isMissing: false) }
        if let selected, !selected.isEmpty, !panels.contains(where: { $0.project == selected }) {
            out.append(AutonomyProjectOption(project: selected, isMissing: true))
        }
        return out
    }

    /// What the daemon's safety cap left out, and WHY those are the ones missing
    /// — so the reader knows the payload took the tail rather than an arbitrary
    /// slice. `nil` when the cap left nothing out, which is every machine seen
    /// so far.
    var overflowLabel: String? {
        guard moreProjects > 0 else { return nil }
        return "+\(moreProjects) more project\(moreProjects == 1 ? "" : "s") past the payload cap, "
            + "each with less autonomous time"
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

}

extension HistoryAutonomyPanel {
    /// This panel's own LINEAR Y domain, `nil` when it has no length to plot.
    ///
    /// ITS OWN, because exactly one panel is drawn: there is nothing left to be
    /// comparable WITH, and a domain stretched to fit projects that are not on
    /// screen would flatten the one that is. (It was shared across five, for
    /// exactly the reason that no longer applies.)
    ///
    /// LINEAR, AND WHAT THAT COSTS. This is the maintainer's explicit call
    /// (#1905), recorded here so the next reader knows it was a decision and not
    /// an oversight. On a linear axis one 11-hour run sets the whole domain, and
    /// the typical 10-minute runs beneath it collapse into the bottom 1.5% of
    /// the plot — visually indistinguishable from the floor. A log axis kept
    /// them apart, at the cost of making a doubling look like a small step
    /// wherever the eye landed. The panel header states the exact longest as a
    /// NUMBER for exactly this reason, so the figure never depends on reading
    /// the axis.
    ///
    /// ZERO IS THE FLOOR, which the log axis could not offer: a linear axis
    /// whose origin is not zero exaggerates every difference above it, and there
    /// is no longer any "a log scale cannot plot 0" reason to lift it.
    var yDomain: ClosedRange<Double>? {
        guard let biggest = buckets.map(\.longest).filter({ $0 > 0 }).max() else { return nil }
        return 0...(biggest * 1.1)
    }

    /// The histogram's full-height value: this panel's own highest peak. 0 when
    /// nothing overlapped anywhere in it, in which case no bar is drawn at all
    /// rather than every bar being drawn full height.
    var peakScale: Int { buckets.map(\.peak).max() ?? 0 }
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

// MARK: - The aggregate percentile chart (chart=autonomy_duration)

/// One bucket of the aggregate chart. The daemon OMITS empty buckets, so every
/// bucket present here has `count >= 1` — a day with no runs is a gap in the
/// line, never a point on the axis.
struct HistoryAutonomyBucket: Codable, Identifiable, Equatable {
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

    init(ts: Int64, p95: Double, p50: Double, p5: Double,
         min: Double, max: Double, count: Int, thin: Bool = false) {
        self.ts = ts
        self.p95 = p95
        self.p50 = p50
        self.p5 = p5
        self.min = min
        self.max = max
        self.count = count
        self.thin = thin
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

/// The window-wide figure row under the aggregate chart. The true extremes stay
/// FIGURES and are deliberately not lines: one four-hour run left going
/// overnight would otherwise redraw the whole Y scale and flatten every other
/// bucket — an effect the switch to a LINEAR axis makes stronger still, which is
/// why these figures matter more now rather than less.
struct HistoryAutonomySummaryStats: Codable, Equatable {
    let p95: Double
    let p50: Double
    let p5: Double
    let min: Double
    let max: Double
    let count: Int

    init(p95: Double = 0, p50: Double = 0, p5: Double = 0,
         min: Double = 0, max: Double = 0, count: Int = 0) {
        self.p95 = p95
        self.p50 = p50
        self.p5 = p5
        self.min = min
        self.max = max
        self.count = count
    }
}

struct HistoryAutonomyDurationResponse: Codable {
    let window: String
    let chart: String
    let start: Int64
    let end: Int64
    let bucketSeconds: Int64
    let bucketStarts: [Int64]
    let buckets: [HistoryAutonomyBucket]
    let summary: HistoryAutonomySummaryStats
    let sampleFloor: Int
    /// Earliest span on record across the WHOLE log (0 = nothing ever
    /// recorded). What lets an empty chart say "collecting since <date>"
    /// instead of being read as "you did nothing".
    let earliestSpan: Int64
    let totalRecorded: Int
    /// Optional so a payload from a daemon that predates the field decodes
    /// rather than failing outright.
    let provenance: HistoryAutonomyProvenance?
    let kinds: HistoryAutonomyKinds?
    let measurement: HistoryAutonomyMeasurement?

    enum CodingKeys: String, CodingKey {
        case window, chart, start, end, buckets, summary, provenance, kinds, measurement
        case bucketSeconds = "bucket_seconds"
        case bucketStarts = "bucket_starts"
        case sampleFloor = "sample_floor"
        case earliestSpan = "earliest_span"
        case totalRecorded = "total_recorded"
    }

    var hasData: Bool { !buckets.isEmpty }
    var provenanceOrNone: HistoryAutonomyProvenance { provenance ?? .none }
    var measurementOrNone: HistoryAutonomyMeasurement { measurement ?? .none }

    /// How many buckets in view are below the sample floor — the count the
    /// thin-bucket sentence states. A fainter plane means nothing on its own.
    var thinCount: Int { buckets.filter(\.thin).count }

    /// The source boundaries that fall inside the DRAWN domain, so the chart
    /// marks only what the reader can actually see. Same strictly-inside rule
    /// as the panel's, and over the same window, so the two elements mark the
    /// same instant.
    var visibleBoundaries: [HistoryAutonomyBoundary] {
        guard let first = bucketStarts.first, let last = bucketStarts.last, last > first else { return [] }
        return provenanceOrNone.boundaryList.filter { $0.ts > first && $0.ts < last }
    }

    /// Where in the DRAWN domain a boundary sits, 0…1.
    func domainFraction(of boundary: HistoryAutonomyBoundary) -> Double {
        guard let first = bucketStarts.first, let last = bucketStarts.last, last > first else { return 0 }
        return Double(boundary.ts - first) / Double(last - first)
    }

    /// The buckets aligned to `bucket_starts`, with `nil` for every bucket the
    /// daemon OMITTED — the macOS twin of the web's `autonomyChartPoints`.
    ///
    /// The nil is the honesty rule, not a convenience: an empty bucket is a
    /// GAP, and a day with no runs must not pull a line down to the axis or let
    /// a filled band close over it. Reading `buckets` directly hands Swift
    /// Charts a dense list in which the gap is simply not there, and it connects
    /// straight across.
    var alignedBuckets: [HistoryAutonomyBucket?] {
        var byTS: [Int64: HistoryAutonomyBucket] = [:]
        for b in buckets { byTS[b.ts] = b }
        return bucketStarts.map { byTS[$0] }
    }

    /// The chart's LINEAR Y domain: the highest p95 it DRAWS, plus a tenth of
    /// headroom, from zero.
    ///
    /// THE DOMAIN FITS WHAT IS DRAWN, and `max` is not drawn. The chart draws
    /// p95, p50, p5 and the plane between p95 and p5; a bucket's true maximum is
    /// a FIGURE in the summary row, and it is a figure precisely so it cannot
    /// redraw the axis — "one four-hour run left going overnight would otherwise
    /// redraw the whole Y scale and flatten every other bucket into the floor"
    /// (#1905). Folding it back into the domain re-created exactly that, and the
    /// first round of this restore shipped it.
    ///
    /// MEASURED on the reference machine's live span log (30-day window, 27 of
    /// 30 buckets with data, 2735 runs): highest p95 1h37m against a highest max
    /// of 11h39m — a 7.19x inflation that put the tallest p50 at 0.92% of the
    /// plot height and the median p50 at 0.46%, with 87.4% of the plot empty
    /// above the band. `testTheDomainFitsWhatIsDrawnNotAnUndrawnOutlier` pins it.
    ///
    /// Linear still costs what `HistoryAutonomyPanel.yDomain` says it costs — a
    /// long p95 flattens the short buckets under it — but that is a cost paid to
    /// a line the reader can SEE. Paying it to one the chart never draws buys
    /// nothing.
    var yDomain: ClosedRange<Double> {
        let drawn = buckets.map(\.p95).filter { $0 > 0 }
        let hi = Swift.max(1, drawn.max() ?? 60)
        return 0...(hi * 1.1)
    }
}

/// The p5–p95 band's geometry, as a pure function so both the gap rule and the
/// thin-bucket rule are testable without a chart — and so this and the web's
/// `autonomyBandSegments` fill the same shape.
///
/// TWO RULES, both of them honesty rules a smooth fill would otherwise erase:
///
///   - **A filled area wants to close across a gap.** The daemon omits empty
///     buckets; a polygon spanning one paints a plane over days that hold no
///     runs at all, a stronger false claim than the interpolated line #1905
///     already refuses. A segment never crosses a `nil`.
///   - **A thin bucket is not a percentile.** Under `sample_floor`, p95 is that
///     bucket's longest run and p5 its shortest. The stroke already dashes
///     across such a bucket; the fill splits at the same place so the thin
///     stretch can be painted in its own fainter plane.
enum AutonomyBandLayout {
    /// One fillable stretch: inclusive indices into the aligned point list, and
    /// whether it is thin. Adjacent segments SHARE their boundary index, so a
    /// thin→solid handover leaves no seam. `from == to` is an isolated bucket —
    /// it has no neighbour to make an area with, and the chart draws its spread
    /// as a whisker rather than leaving it the one bucket with no visible range.
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
