import Charts
import SwiftUI

// MARK: - Autonomy section (#1905)
//
// FIVE PER-PROJECT PANELS, one per project, stacked. Each carries a LINE — the
// longest run in each time bucket — and, under it, a low HISTOGRAM of how many
// runs were working at the same time in that bucket.
//
// HOW FIVE PANELS FIT A 380 pt POPOVER. The tab's content is already inside a
// ScrollView (HistoryView.content), so the stack scrolls rather than compresses:
// a 58 pt panel (14 pt header + 44 pt plot) times five is 290 pt, and the
// header, the "+N more" line, the axis, the key and the provenance paragraph
// follow it. Compressing instead would leave a histogram two points tall — a
// smear that cannot be read — and the paragraphs that explain the figures are
// the last thing that may be clipped.
//
// Pure inputs (the decoded response), so a snapshot test can host this view
// directly with fixture data.

struct HistoryAutonomyContentView: View {
    let data: HistoryAutonomyProjectsResponse
    let range: HistoryAutonomyRange

    /// #1659 — every date this view renders takes its zone as an INPUT rather
    /// than reading `NSTimeZone.default`.
    @Environment(\.formatTimeZone) private var formatTimeZone

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp4) {
            panelStack
            Divider()
            collectionProvenance
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: The stack

    @ViewBuilder private var panelStack: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            HStack {
                Text("Longest autonomous run · \(range.label)")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Spacer()
                Text("log scale, shared")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
            if data.hasData {
                ForEach(data.stackRows) { row in
                    switch row {
                    case let .panel(panel, index):
                        AutonomyPanelView(panel: panel,
                                          data: data,
                                          panelIndex: index,
                                          timeZone: formatTimeZone)
                    case let .more(label):
                        overflow(label)
                    }
                }
                stackAxis
                AutonomyKeyView()
                concurrencyCaveat
                summaryRow
            } else {
                emptyText
            }
        }
    }

    /// "+N more projects" — quieter than a panel and clearly not one: it is a
    /// statement about the stack, not another entry in it.
    private func overflow(_ text: String) -> some View {
        Text(text)
            .font(.caption2)
            .foregroundColor(.secondary)
            .opacity(0.85)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// The window's bounds, ONCE under the whole stack: the five panels share
    /// one x domain, so five copies would be five statements of one fact. The
    /// start label coarsens with the window — see AutonomyFormat.axisBound.
    private var stackAxis: some View {
        HStack(spacing: IrrSpacing.sp2) {
            Text(AutonomyFormat.axisBound(Date(timeIntervalSince1970: TimeInterval(data.start)),
                                          windowSeconds: data.end - data.start,
                                          timeZone: formatTimeZone))
            Spacer(minLength: IrrSpacing.sp2)
            Text("now")
        }
        .font(.caption2)
        .foregroundColor(.secondary)
        .padding(.leading, AutonomyPanelMetrics.labelGutter)
    }

    /// The sentence beside the `at once` figure, and it is not optional:
    /// without it the number is read as "N independent agents" and it is not
    /// that. A parent is held `working` while its subagents run.
    private var concurrencyCaveat: some View {
        Text(AutonomyFormat.concurrencyCaveat)
            .font(.caption2)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// "longest 11h39m in irrlicht · most at once 5 · 312 runs across 12 projects"
    private var summaryRow: some View {
        let s = data.summary
        var longest = "longest \(AutonomyFormat.duration(s.longest))"
        if !s.longestProject.isEmpty { longest += " in \(s.longestProject)" }
        if s.longestRunning { longest += " (still going)" }
        return Text("\(longest) · most at once \(s.peak) · \(s.runs) runs across \(s.projects) projects")
            .font(.caption)
            .monospacedDigit()
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// An empty stack with axes drawn is not acceptable (#1905): the empty
    /// state says, in words, that the feature collects from the day it ships.
    private var emptyText: some View {
        Text(data.totalRecorded == 0
             ? "No autonomous runs recorded yet. Irrlicht starts measuring them the first time a session runs after this update — an empty section here means \"nothing recorded\", not \"nothing happened\"."
             : "No runs in this range. \(data.totalRecorded) runs are on record outside it.")
            .font(.callout)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, minHeight: 150, alignment: .center)
            .multilineTextAlignment(.center)
    }

    // MARK: Provenance

    /// States when collection started, so an empty or short history is never
    /// read as "you did nothing" (#1905) — and, when any of the view was
    /// back-filled, says that too.
    private var collectionProvenance: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            // What these figures counted (#1905 subagents). Above the
            // provenance line because it qualifies every number in the section,
            // where provenance qualifies where they came from — and because its
            // `unknown` clause is why a panel's `at once` figure sometimes
            // carries no split.
            if let counting = AutonomyFormat.countingLine(data.kinds) {
                Text(counting)
            }
            // …and which of them are floors rather than measurements (#1905
            // recording). Same register, same reason: a lower bound rendered as
            // a measurement is a wrong number with nothing on screen saying it
            // is wrong. Silent when every run in view is finished and measured.
            if let measurement = AutonomyFormat.measurementLine(data.measurementOrNone) {
                Text(measurement)
            }
            Text(AutonomyFormat.provenance(earliest: data.earliestSpan,
                                           total: data.totalRecorded,
                                           timeZone: formatTimeZone))
            if let note = AutonomyFormat.reconstructionNote(data.provenanceOrNone,
                                                            inView: data.summary.runs,
                                                            timeZone: formatTimeZone) {
                Text(note)
            }
        }
        .font(.caption2)
        .foregroundColor(.secondary)
        .fixedSize(horizontal: false, vertical: true)
    }
}

// MARK: - Panel geometry

/// The panel's fixed geometry, in points. Named rather than spread across the
/// views so the stack's total height is arithmetic a reader can do: five times
/// (`headerHeight` + `plotHeight` + `barsHeight`) plus the spacing between them.
enum AutonomyPanelMetrics {
    static let plotHeight: CGFloat = 32
    static let barsHeight: CGFloat = 10
    /// The width reserved for the shared axis's duration labels. The stack's
    /// own x axis is indented by the same amount so its two bounds sit under
    /// the plot rather than under the labels.
    static let labelGutter: CGFloat = 46
}

// MARK: - One project's panel

private struct AutonomyPanelView: View {
    let panel: HistoryAutonomyPanel
    let data: HistoryAutonomyProjectsResponse
    /// Which panel of the stack this is — the caption rule reads it, and
    /// nothing else does.
    let panelIndex: Int
    let timeZone: TimeZone

    private var points: [HistoryAutonomyPanelBucket?] { panel.alignedBuckets(data.bucketStarts) }

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: IrrSpacing.sp2) {
                Text(panel.project)
                    .font(.caption2)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: IrrSpacing.sp2)
                // The panel's OWN figures, so the exact longest never depends
                // on reading a shared axis, and the concurrency split is
                // legible rather than buried.
                Text(panel.headline)
                    .font(.caption2)
                    .monospacedDigit()
                    .foregroundColor(.secondary)
                    .lineLimit(1)
            }
            longestRunChart
            concurrencyBars
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(panel.project): \(panel.headline), \(panel.runs) runs")
    }

    /// The longest-run line, on the SHARED log domain.
    ///
    /// `run` is the series key on purpose: it names the CONTIGUOUS STRETCH a
    /// point belongs to, and Swift Charts connects points that share it. Without
    /// that key the line is drawn through every bucket the daemon sent — which
    /// silently bridges the omitted ones, exactly the interpolation
    /// `alignedBuckets` exists to refuse.
    @ViewBuilder private var longestRunChart: some View {
        let domain = data.sharedYDomain ?? 1...60
        Chart {
            // The source-change markers first, so they sit UNDER the line: the
            // marker explains the data, it is not part of it.
            //
            // A LONG-DASHED 1.5 pt rule, deliberately unlike the axis gridlines
            // (solid hairlines): before, both were thin dashes in a muted colour
            // and the one line carrying an explanation was indistinguishable
            // from furniture. It is drawn on EVERY panel; only the top one is
            // captioned (AutonomyBoundaryCaption).
            ForEach(data.visibleBoundaries) { boundary in
                RuleMark(x: .value("Source change", boundary.date))
                    .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [6, 3]))
                    .foregroundStyle(Color.secondary.opacity(0.75))
                    .annotation(position: .top,
                                alignment: captionAlignment(for: boundary),
                                spacing: 2) {
                        if AutonomyBoundaryCaption.isShown(panelIndex: panelIndex) {
                            Text(boundary.label)
                                .font(.system(size: 9))
                                .foregroundColor(.secondary)
                                .opacity(0.9)
                                .fixedSize()
                        }
                    }
            }
            ForEach(lineData()) { d in
                LineMark(
                    x: .value("Date", d.date),
                    y: .value("Longest", d.value),
                    series: .value("Run", d.run)
                )
                .foregroundStyle(AutonomyPalette.lineColor)
                .lineStyle(StrokeStyle(lineWidth: 1.6))
                .interpolationMethod(.monotone)
            }
            // An isolated bucket has no neighbour to draw a segment to, and
            // would otherwise be the one bucket that vanishes.
            ForEach(lineData().filter(\.isolated)) { d in
                PointMark(x: .value("Date", d.date), y: .value("Longest", d.value))
                    .symbolSize(14)
                    .foregroundStyle(AutonomyPalette.lineColor)
            }
            // A bucket whose longest run has not ended is hollow: its length is
            // a floor, and the header says "still going" beside it.
            ForEach(lineData().filter(\.running)) { d in
                PointMark(x: .value("Date", d.date), y: .value("Longest", d.value))
                    .symbol(.circle)
                    .symbolSize(30)
                    .foregroundStyle(AutonomyPalette.lineColor.opacity(0.5))
            }
        }
        .chartYScale(domain: domain, type: .log)
        .chartXScale(domain: xDomain)
        .chartYAxis {
            AxisMarks(values: .automatic(desiredCount: 3)) { value in
                AxisGridLine(stroke: StrokeStyle(lineWidth: 0.5))
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text(AutonomyFormat.duration(v)).font(.system(size: 9))
                    }
                }
            }
        }
        .chartXAxis(.hidden)
        .frame(height: AutonomyPanelMetrics.plotHeight)
    }

    /// The concurrency histogram: one low bar per bucket, scaled against the
    /// stack's SHARED peak so the five panels are comparable.
    ///
    /// A BUCKET WITH NO ONE WORKING DRAWS NOTHING — not a zero-height bar, and
    /// not a hairline on the baseline. A mark on the axis reads as a measured
    /// zero, which is a different and false claim from "no runs here".
    @ViewBuilder private var concurrencyBars: some View {
        let scale = data.sharedPeakScale
        Chart {
            ForEach(barData()) { d in
                BarMark(
                    x: .value("Date", d.date),
                    y: .value("At once", d.peak)
                )
                .foregroundStyle(AutonomyPalette.bars)
            }
        }
        .chartYScale(domain: 0...Double(Swift.max(1, scale)))
        .chartXScale(domain: xDomain)
        .chartXAxis(.hidden)
        .chartYAxis {
            // The gutter is kept — an empty label of the same width — so the
            // bars line up under the plot above rather than starting further
            // left, which would put the histogram out of register with the line
            // it belongs to.
            AxisMarks(values: [0]) { _ in AxisValueLabel { Text("     ").font(.system(size: 9)) } }
        }
        .frame(height: AutonomyPanelMetrics.barsHeight)
    }

    /// Both charts share one x domain, so the line, the bars and the boundary
    /// rule stay in register down the whole stack.
    private var xDomain: ClosedRange<Date> {
        let first = data.bucketStarts.first ?? data.start
        let last = data.bucketStarts.last ?? data.end
        let lo = Date(timeIntervalSince1970: TimeInterval(first))
        let hi = Date(timeIntervalSince1970: TimeInterval(Swift.max(last, first + 1)))
        return lo...hi
    }

    private struct Datum: Identifiable {
        let id: String
        let date: Date
        let run: String
        let value: Double
        let running: Bool
        let isolated: Bool
    }

    private struct BarDatum: Identifiable {
        let id: String
        let date: Date
        let peak: Int
    }

    private func date(at index: Int) -> Date {
        Date(timeIntervalSince1970: TimeInterval(data.bucketStarts[index]))
    }

    /// The line, split into one series per contiguous stretch so the stroke
    /// BREAKS at every gap instead of being drawn through it.
    private func lineData() -> [Datum] {
        var out: [Datum] = []
        for segment in AutonomyLineLayout.segments(points: points) {
            for i in segment.from...segment.to {
                guard let b = points[i], b.hasLine, i < data.bucketStarts.count else { continue }
                out.append(Datum(id: "\(panel.project)-\(b.ts)",
                                 date: date(at: i),
                                 run: "\(panel.project)#\(segment.id)",
                                 // A log scale cannot plot 0, and a span shorter
                                 // than a second is not a run.
                                 value: Swift.max(1, b.longest),
                                 running: b.running,
                                 isolated: segment.isIsolated))
            }
        }
        return out
    }

    private func barData() -> [BarDatum] {
        points.enumerated().compactMap { i, b in
            guard let b, b.hasBar, i < data.bucketStarts.count else { return nil }
            return BarDatum(id: "bar-\(panel.project)-\(b.ts)", date: date(at: i), peak: b.peak)
        }
    }

    /// Which side of its rule a boundary caption hangs off — see
    /// `AutonomyBoundaryCaption`. `.trailing` puts the caption's trailing edge
    /// on the rule (text extends left), `.leading` its leading edge (text
    /// extends right).
    private func captionAlignment(for boundary: HistoryAutonomyBoundary) -> Alignment {
        AutonomyBoundaryCaption.side(fraction: data.domainFraction(of: boundary)) == .left
            ? .trailing
            : .leading
    }
}

// MARK: - The stack's key

/// Two entries, because a panel draws two things: a line and a row of bars.
/// Each swatch takes the SHAPE of what it stands for — a pair of identically
/// coloured dots would be a key that says the same thing twice.
private struct AutonomyKeyView: View {
    var body: some View {
        HStack(spacing: IrrSpacing.sp3) {
            ForEach(AutonomyPalette.keyEntries) { entry in
                HStack(spacing: IrrSpacing.sp1) {
                    swatch(entry)
                    Text(entry.label)
                }
            }
            Spacer(minLength: 0)
        }
        .font(.caption2)
        .foregroundColor(.secondary)
    }

    @ViewBuilder private func swatch(_ entry: AutonomyKeyEntry) -> some View {
        switch entry.kind {
        case .line:
            Capsule()
                .fill(entry.color)
                .frame(width: 16, height: 2)
        case .bars:
            HStack(alignment: .bottom, spacing: 2) {
                entry.color.frame(width: 3, height: 4)
                entry.color.frame(width: 3, height: 9)
                entry.color.frame(width: 3, height: 6)
            }
            .frame(width: 16, height: 9, alignment: .bottom)
        }
    }
}

// MARK: - Palette + formatting

/// One entry of the stack's key. `kind` names the MARK it stands for, so a key
/// entry cannot be given a colour for something the panels do not draw.
struct AutonomyKeyEntry: Identifiable, Equatable {
    enum Kind: String { case line, bars }

    let kind: Kind
    let label: String
    let color: Color

    var id: String { kind.rawValue }
}

enum AutonomyPalette {
    /// ONE HUE, TWO WEIGHTS. The longest-run line is `working` at full
    /// strength; the concurrency histogram under it is the same hue, quieter,
    /// because the bars are a second reading of the same activity and not a
    /// second subject. `barsAreTheLineHue` in the tests pins that.
    ///
    /// What went: a colour per percentile. The first round drew p95 green, p50
    /// purple and p5 orange — three equally loud curves and a legend that had to
    /// be decoded before the chart said anything.
    static var lineColor: Color { IrrColors.working }
    static var bars: Color { IrrColors.autonomyBar }

    /// The stack's key: exactly two entries, because a panel draws exactly two
    /// things.
    ///
    /// SAME TWO STRINGS AS THE WEB's side-panel key (AUTONOMY_KEY in
    /// platforms/web/historyTab.js), and pinned against it by `the two surfaces
    /// name the key the same way` — two clients must not explain one chart
    /// differently. That panel is 260 pt where this popover is 380, so the
    /// length that fits THERE is the length both carry.
    static var keyEntries: [AutonomyKeyEntry] {
        [
            AutonomyKeyEntry(kind: .line, label: "longest run in a bucket", color: lineColor),
            AutonomyKeyEntry(kind: .bars, label: "working at once (peak)", color: bars),
        ]
    }
}

enum AutonomyFormat {
    /// Compact run length: "41s", "11m", "1h58m", "2d3h".
    static func duration(_ seconds: Double) -> String {
        let s = Int(seconds.rounded())
        if s < 60 { return "\(s)s" }
        if s < 3600 {
            let m = s / 60
            let rem = s % 60
            return rem == 0 ? "\(m)m" : "\(m)m\(rem)s"
        }
        if s < 86400 {
            let h = s / 3600
            let m = (s % 3600) / 60
            return m == 0 ? "\(h)h" : "\(h)h\(m)m"
        }
        let d = s / 86400
        let h = (s % 86400) / 3600
        return h == 0 ? "\(d)d" : "\(d)d\(h)h"
    }

    /// One "at once" figure, with its split only when the daemon says the split
    /// is derivable.
    ///
    /// NO SPLIT IS SHOWN OVER DATA THAT CANNOT SUPPORT ONE. Most rows written
    /// before 18 Aug 2026 carry `kind: unknown` — they predate the
    /// classification, or the back-fill rebuilt them from a source with no
    /// parent information — and there is no way to recover which they were. The
    /// total alone is the honest answer there; "5 (5 + 0 sub)" would be an
    /// invented one.
    static func concurrency(peak: Int, top: Int, sub: Int, splitKnown: Bool) -> String {
        guard peak > 0 else { return "" }
        guard splitKnown else { return "\(peak) at once" }
        return "\(peak) at once (\(top) + \(sub) sub)"
    }

    /// The caveat beside the `at once` figure, and it is not optional: without
    /// it the number is read as "N independent agents" and it is not that.
    ///
    /// The daemon deliberately holds a PARENT session in `working` while its
    /// subagents run, so one agent with three subagents overlaps as four. Four
    /// things really were working — the figure is not wrong — but a reader who
    /// takes it for four independent agents has been misled by a true number,
    /// which is the exact failure this section keeps writing sentences to avoid.
    ///
    /// SAME WORDING AS THE WEB's AUTONOMY_CONCURRENCY_CAVEAT.
    static let concurrencyCaveat =
        "A parent is held working while its subagents run, so one agent with three subagents counts as "
        + "four at once. Four things really were working — but not four independent agents. That is what "
        + "the “N + M sub” split separates, and it is shown wherever every run at the peak said which it was."

    /// The stack's left bound, coarsening with the window the way the activity
    /// matrix's column headers do: a 30-day stack needs a date, a year-long one
    /// a month. In the caller's zone (#1659), never the machine's.
    static func axisBound(_ date: Date, windowSeconds: Int64, timeZone: TimeZone) -> String {
        if windowSeconds <= 36 * 3600 { return formatted(date, "HH:mm", timeZone) }
        if windowSeconds <= 60 * 86400 { return formatted(date, "MMM d", timeZone) }
        return formatted(date, "MMM yyyy", timeZone)
    }

    /// X-axis tick label, in the caller's zone (#1659 — never the machine's).
    static func axisDate(_ date: Date, timeZone: TimeZone) -> String {
        formatted(date, "MMM d", timeZone)
    }

    /// POSIX-locale formatter for one pattern in one zone. `locale` is assigned
    /// before `dateFormat` on purpose: `dateFormat` is interpreted against the
    /// formatter's current locale, so the reverse order silently re-interprets
    /// the pattern (the same note HistoryFormat.posix carries).
    private static func formatted(_ date: Date, _ pattern: String, _ timeZone: TimeZone) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = pattern
        return f.string(from: date)
    }

    /// The provenance line. Says when collection started — the sentence that
    /// keeps an empty view from reading as "you did nothing".
    static func provenance(earliest: Int64, total: Int, timeZone: TimeZone) -> String {
        guard earliest > 0 else {
            return "No autonomous runs recorded yet. Irrlicht began measuring them with this update; "
                + "the panels above fill in as sessions run."
        }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "MMM d, yyyy"
        let since = f.string(from: Date(timeIntervalSince1970: TimeInterval(earliest)))
        return "Collecting since \(since) · \(total) runs recorded."
    }

    /// States WHAT the figures counted (#1905 subagents, retargeted by #1905
    /// recording). `nil` only for a payload from a daemon that predates the
    /// census — absence there means "this response never said", which is not
    /// the same claim as "there were none".
    ///
    /// THERE IS NO MODE. Every run counts, subagent runs included, because
    /// Irrlicht recorded them — so no excluded count is reported and there is no
    /// control whose position a reader has to remember. What the sentence still
    /// does is describe the window's MAKEUP.
    ///
    /// THE UNKNOWN CLAUSE STAYS LOAD-BEARING, and now carries its consequence
    /// for the concurrency figure: a row written before Irrlicht told the two
    /// apart is counted like the rest, but a peak one of them was alive for
    /// cannot be split, and the sentence says so rather than leaving a missing
    /// split to read as a bug.
    static func countingLine(_ k: HistoryAutonomyKinds?) -> String? {
        guard let k else { return nil }
        var out = k.subagent > 0
            ? "Counting every run, including \(k.subagent) subagent run\(k.subagent == 1 ? "" : "s") "
                + "— each of which happened inside its parent's run."
            : "Counting every run, subagent runs included. This window holds none."
        if k.unknown > 0 {
            out += " \(k.unknown) run\(k.unknown == 1 ? " was" : "s were") recorded before Irrlicht told "
                + "top-level and subagent runs apart, so which they were is unknown — those are counted "
                + "either way, and a peak one of them was alive for is shown as a total with no split."
        }
        return out
    }

    /// Marks the runs in view whose duration is a FLOOR rather than a
    /// measurement (#1905 recording). `nil` when every run in view is finished
    /// and fully measured — the quiet case on a machine whose daemon has been
    /// up all day.
    ///
    /// Two kinds, two sentences, because they are two different limits and a
    /// reader who merged them would misread the panels in opposite directions:
    ///
    ///   - STILL RUNNING. The run has not ended, so its length is how long it
    ///     has lasted SO FAR. It COUNTS towards the longest — the section
    ///     reports a maximum, and "already lasted 3h" is true — and the panel
    ///     that shows it says "still going" rather than presenting a floor as
    ///     final.
    ///   - STARTED BEFORE IRRLICHT WAS WATCHING. The run has finished, but its
    ///     start is where the watching began. Dropping those is what left 5 of a
    ///     day's 35 runs on the record.
    static func measurementLine(_ m: HistoryAutonomyMeasurement) -> String? {
        guard m.any else { return nil }
        var parts: [String] = []
        if m.running > 0 {
            let one = m.running == 1
            parts.append("\(m.running) run\(one ? " is" : "s are") still going: "
                + "\(one ? "its length is" : "their lengths are") how long \(one ? "it has" : "they have") "
                + "lasted SO FAR. \(one ? "It counts" : "They count") towards the longest run, marked "
                + "\"still going\".")
        }
        if m.lowerBoundStart > 0 {
            let one = m.lowerBoundStart == 1
            parts.append("\(m.lowerBoundStart) run\(one ? "" : "s") already going when Irrlicht started "
                + "watching — \(one ? "its" : "their") start is when it started watching, not when the run "
                + "began, so \(one ? "that length is a minimum" : "those lengths are minimums").")
        }
        return parts.joined(separator: " ")
    }

    /// Marks a view that is showing back-filled history (#1905). `nil` when
    /// every run in view was measured as it happened — which is every install
    /// but the one `tools/autonomy-backfill` was run on, so a normal machine
    /// says nothing at all here.
    ///
    /// Three facts, in the register the empty state already uses, because each
    /// answers a question the reader would otherwise answer wrongly: HOW MANY
    /// of the runs in view are reconstructed, the date BEFORE WHICH everything
    /// is reconstructed, and whether any of it came from a source that cannot
    /// say how a run ended.
    static func reconstructionNote(_ p: HistoryAutonomyProvenance,
                                   inView: Int,
                                   timeZone: TimeZone) -> String? {
        guard p.isReconstructed else { return nil }
        let total = inView > 0 ? inView : p.reconstructed
        var out = "\(p.reconstructed) of \(total) runs in view were reconstructed from logs this Mac "
            + "already had, not measured as they happened. "
        if p.liveSince > 0 {
            let f = DateFormatter()
            f.locale = Locale(identifier: "en_US_POSIX")
            f.timeZone = timeZone
            f.dateFormat = "MMM d, yyyy"
            out += "Everything before \(f.string(from: Date(timeIntervalSince1970: TimeInterval(p.liveSince)))) "
                + "is reconstructed."
        } else {
            // liveSince == 0 is "nothing has ever been measured live", which is
            // a different claim from "measured since the epoch". Printing Jan 1
            // 1970 would be a fabricated date — the exact failure this whole
            // marking exists to prevent.
            out += "Nothing here was measured live — every run on record is reconstructed."
        }
        if p.costDerived > 0 {
            out += " \(p.costDerived) of them come from the cost log, which records when a session was "
                + "working and never why it stopped, so their end reason is unknown — not assumed."
        }
        return out
    }
}
