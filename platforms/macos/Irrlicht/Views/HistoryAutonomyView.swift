import Charts
import SwiftUI

// MARK: - Autonomy section (#1905)
//
// TWO ELEMENTS over one window, sharing one Range control:
//
//   THE AGGREGATE CHART — p95/p50/p5 of autonomous run duration across EVERY
//   project, with the plane between p95 and p5 filled as a band. "Is autonomy
//   getting better across the machine."
//   ONE PROJECT PANEL under it — a LINE of the longest run in each time bucket
//   and, beneath that, a HISTOGRAM of how many runs were working at the same
//   time. "What did THIS project do." A Picker in the panel's own header row
//   chooses which project.
//
// WHY BOTH. A maximum over one project is that project's story; a maximum
// charted across every project is a chart of whoever ran longest that day. A
// percentile over the whole population is a trend; a percentile over one
// project's four runs is not a percentile. Neither can stand in for the other.
//
// WHY ONE PANEL AND NOT FIVE. In a 380 pt popover five panels squeezed each plot
// to 32 pt and each histogram to 10 pt, and the reader could still only compare
// the five the daemon ranked highest. One panel gets 96 pt for its line and 40
// pt for its bars, and the Picker reaches EVERY project in the window.
//
// Pure inputs (the two decoded responses), so a snapshot test can host this view
// directly with fixture data.

struct HistoryAutonomyContentView: View {
    let duration: HistoryAutonomyDurationResponse
    let data: HistoryAutonomyProjectsResponse
    let range: HistoryAutonomyRange
    /// A BINDING, unlike `range`: the project Picker lives in the panel's own
    /// header row, beside the figures it changes. Range moves BOTH elements and
    /// belongs to the tab's control row; this moves one of them, and a control's
    /// place is what says which.
    ///
    /// nil is "no choice made yet" → rank 1. The NAME is held, not the rank, so
    /// the choice survives a Range change rather than silently swapping projects
    /// whenever a longer window reorders the ranking.
    @Binding var selectedProject: String?

    /// #1659 — every date this view renders takes its zone as an INPUT rather
    /// than reading `NSTimeZone.default`.
    @Environment(\.formatTimeZone) private var formatTimeZone

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp4) {
            aggregateSection
            Divider()
            panelSection
            Divider()
            collectionProvenance
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: Element 1 — the aggregate percentile chart

    @ViewBuilder private var aggregateSection: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            HStack {
                Text("Autonomous run duration · \(range.label)")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Spacer()
                Text("all projects · linear scale")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
            if duration.hasData {
                AutonomyDurationChart(data: duration, timeZone: formatTimeZone)
                    .frame(height: 190)
                aggregateSummaryRow
                if duration.thinCount > 0 { thinNote }
            } else {
                emptyText
            }
        }
    }

    /// "p95 1h58m · p50 11m · p5 41s · longest 2h14m · shortest 22s · 312 runs"
    /// — the true extremes are figures here, deliberately not lines on the
    /// chart, and that matters MORE on a linear axis than it did on a log one.
    private var aggregateSummaryRow: some View {
        let s = duration.summary
        return Text(
            "p95 \(AutonomyFormat.duration(s.p95)) · p50 \(AutonomyFormat.duration(s.p50)) · "
            + "p5 \(AutonomyFormat.duration(s.p5)) · longest \(AutonomyFormat.duration(s.max)) · "
            + "shortest \(AutonomyFormat.duration(s.min)) · \(s.count) runs"
        )
        .font(.caption)
        .monospacedDigit()
        .foregroundColor(.secondary)
        .fixedSize(horizontal: false, vertical: true)
    }

    /// Thin buckets are MARKED, never hidden and never smoothed — and the
    /// marking is explained in words, because a fainter plane means nothing on
    /// its own.
    ///
    /// It has to say what p95 and p5 ARE in a thin bucket, not merely that the
    /// bucket is thin: with two samples they are the longest and the shortest
    /// run, and a reader who takes them for percentiles reads a two-run week as
    /// a spread. SAME WORDING AS THE WEB's `autonomyThinNote`.
    private var thinNote: some View {
        Text("\(duration.thinCount) of \(duration.buckets.count) "
             + "bucket\(duration.buckets.count == 1 ? "" : "s") "
             + "\(duration.thinCount == 1 ? "has" : "have") under \(duration.sampleFloor) "
             + "run\(duration.sampleFloor == 1 ? "" : "s") (drawn fainter): p95 and p5 there are just "
             + "the longest and shortest.")
            .font(.caption2)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    // MARK: Element 2 — one project's panel

    @ViewBuilder private var panelSection: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            if data.hasData {
                AutonomyPanelView(choice: data.choice(selected: selectedProject),
                                  data: data,
                                  selectedProject: $selectedProject,
                                  timeZone: formatTimeZone)
                if let label = data.overflowLabel { overflow(label) }
                stackAxis
                AutonomyKeyView()
                concurrencyCaveat
                summaryRow
            } else {
                emptyPanelText
            }
        }
    }

    /// "+N more projects" — what the daemon's payload cap left out, which is
    /// nothing on any machine seen so far. Quieter than the panel and clearly
    /// not one: it is a statement about the payload, not another entry in it.
    private func overflow(_ text: String) -> some View {
        Text(text)
            .font(.caption2)
            .foregroundColor(.secondary)
            .opacity(0.85)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// The window's bounds under the panel. The start label coarsens with the
    /// window — see AutonomyFormat.axisBound.
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

    /// An empty chart with axes drawn is not acceptable (#1905): the empty
    /// state says, in words, that the feature collects from the day it ships.
    private var emptyText: some View {
        Text(duration.totalRecorded == 0
             ? "No autonomous runs recorded yet. Irrlicht starts measuring them the first time a session runs after this update — an empty section here means \"nothing recorded\", not \"nothing happened\"."
             : "No runs in this range. \(duration.totalRecorded) runs are on record outside it.")
            .font(.callout)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, minHeight: 150, alignment: .center)
            .multilineTextAlignment(.center)
    }

    /// The panel's own empty state, for a window that holds no project at all.
    /// A project the reader SELECTED that this range does not hold is a
    /// different case and is answered inside the panel — see AutonomyPanelView.
    private var emptyPanelText: some View {
        Text(data.totalRecorded == 0
             ? "No project has an autonomous run yet — this panel fills in as sessions run."
             : "No runs in this range for any project.")
            .font(.callout)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, minHeight: 90, alignment: .center)
            .multilineTextAlignment(.center)
    }

    // MARK: Provenance

    /// States when collection started, so an empty or short history is never
    /// read as "you did nothing" (#1905) — and, when any of the view was
    /// back-filled, says that too.
    ///
    /// TWO LINES, one of them conditional. The run census (`countingLine`) and
    /// the three-sentence reconstruction paragraph were both deleted by #1905's
    /// prose cut: the first was reassurance rather than a caveat, and the
    /// second's count now hangs off the provenance line as four words.
    private var collectionProvenance: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            // Which of the figures are floors rather than measurements (#1905
            // recording): a lower bound rendered as a measurement is a wrong
            // number with nothing on screen saying it is wrong. Silent when
            // nothing is running, which is most of the time.
            if let measurement = AutonomyFormat.measurementLine(data.measurementOrNone) {
                Text(measurement)
            }
            Text(AutonomyFormat.provenance(earliest: data.earliestSpan,
                                           total: data.totalRecorded,
                                           reconstructed: data.provenanceOrNone.reconstructed,
                                           timeZone: formatTimeZone))
        }
        .font(.caption2)
        .foregroundColor(.secondary)
        .fixedSize(horizontal: false, vertical: true)
    }
}

// MARK: - Panel geometry

/// The panel's fixed geometry, in points. Named rather than spread across the
/// views so the section's total height is arithmetic a reader can do.
///
/// TALLER THAN THE FIVE-PANEL STACK COULD AFFORD, because only one panel is
/// drawn: the plot went 32 → 96 pt and the histogram 10 → 40 pt. At 10 pt a peak
/// of 1 and a peak of 15 differed by a few points of ink, which is half of why
/// "the number of agents running in parallel is not visible"; the other half is
/// that the gutter beside them carried no scale, which `concurrencyBars` now
/// labels.
enum AutonomyPanelMetrics {
    static let plotHeight: CGFloat = 96
    static let barsHeight: CGFloat = 40
    /// The clearance between the two charts, and it is load-bearing rather than
    /// cosmetic (#1905, QA-4). Both axes are labelled in the SAME right-hand
    /// gutter now, and neither knows about the other: the line axis's lowest
    /// value label sits at the foot of its plot and the histogram's peak label
    /// at the head of the bar band. Butted together at the 1 pt spacing the
    /// five-panel stack used — where the bar band carried no labels at all —
    /// the two overprint. The web build showed exactly that in QA before this
    /// gap existed; `tickFontSize` is what it has to clear.
    static let chartGap: CGFloat = 10
    /// The tick font both axes label in. One value, so the two gutters cannot
    /// end up at different sizes and read as two columns.
    static let tickFontSize: CGFloat = 9
    /// The width reserved for the duration labels. The panel's own x axis is
    /// indented by the same amount so its two bounds sit under the plot rather
    /// than under the labels — and the histogram's own axis uses the same
    /// gutter, so the two read as one column.
    static let labelGutter: CGFloat = 46
}

// MARK: - One project's panel

private struct AutonomyPanelView: View {
    let choice: AutonomyPanelChoice
    let data: HistoryAutonomyProjectsResponse
    @Binding var selectedProject: String?
    let timeZone: TimeZone

    private var points: [HistoryAutonomyPanelBucket?] {
        choice.panel?.alignedBuckets(data.bucketStarts) ?? []
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            header
            if let panel = choice.panel {
                longestRunChart(panel)
                // See AutonomyPanelMetrics.chartGap: the two axes share one
                // gutter, and butted together their innermost labels overprint.
                concurrencyBars(panel).padding(.top, AutonomyPanelMetrics.chartGap)
            } else {
                missingProjectNote
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(choice.panel.map { "\($0.project): \($0.headline), \($0.runs) runs" }
                            ?? AutonomyFormat.emptyProjectNote(choice.project))
    }

    /// The panel's own header row: the project Picker, and that project's two
    /// figures. The Picker sits HERE rather than in the tab's control row
    /// because it moves one element where Range moves both — a control's place
    /// is what says which.
    private var header: some View {
        HStack(spacing: IrrSpacing.sp2) {
            Picker("Project", selection: projectBinding) {
                ForEach(data.projectOptions(selected: selectedProject)) { option in
                    Text(option.label).tag(option.project)
                }
            }
            .labelsHidden()
            .pickerStyle(.menu)
            .controlSize(.small)
            .font(.caption2)
            .frame(maxWidth: 190, alignment: .leading)
            Spacer(minLength: IrrSpacing.sp2)
            // The panel's OWN figures, so the exact longest never depends on
            // reading the axis, and the concurrency split is legible rather
            // than buried.
            Text(choice.panel?.headline ?? "")
                .font(.caption2)
                .monospacedDigit()
                .foregroundColor(.secondary)
                .lineLimit(1)
        }
    }

    /// The Picker writes a NAME, never nil: once the reader has chosen, the
    /// choice is theirs to keep across a Range change.
    private var projectBinding: Binding<String> {
        Binding(get: { choice.project }, set: { selectedProject = $0 })
    }

    /// A SENTENCE, not an empty frame. An empty plot with axes on it is the
    /// same picture a failed request draws, and the reader cannot tell which
    /// they are looking at. Its height matches the plot's, so switching to a
    /// project this range does not hold does not make the section jump.
    private var missingProjectNote: some View {
        Text(AutonomyFormat.emptyProjectNote(choice.project))
            .font(.caption)
            .foregroundColor(.secondary)
            .frame(maxWidth: .infinity,
                   minHeight: AutonomyPanelMetrics.plotHeight + AutonomyPanelMetrics.barsHeight,
                   alignment: .center)
            .multilineTextAlignment(.center)
    }

    /// The longest-run line, on THIS PROJECT'S OWN linear domain.
    ///
    /// `run` is the series key on purpose: it names the CONTIGUOUS STRETCH a
    /// point belongs to, and Swift Charts connects points that share it. Without
    /// that key the line is drawn through every bucket the daemon sent — which
    /// silently bridges the omitted ones, exactly the interpolation
    /// `alignedBuckets` exists to refuse.
    @ViewBuilder private func longestRunChart(_ panel: HistoryAutonomyPanel) -> some View {
        let domain = panel.yDomain ?? 0...60
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
                        // Drawn on this element, captioned on the other: see
                        // AutonomyBoundaryCaption.
                        if AutonomyBoundaryCaption.isShown(element: .panel) {
                            Text(boundary.label)
                                .font(.system(size: 9))
                                .foregroundColor(.secondary)
                                .opacity(0.9)
                                .fixedSize()
                        }
                    }
            }
            ForEach(lineData(panel)) { d in
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
            ForEach(lineData(panel).filter(\.isolated)) { d in
                PointMark(x: .value("Date", d.date), y: .value("Longest", d.value))
                    .symbolSize(14)
                    .foregroundStyle(AutonomyPalette.lineColor)
            }
            // A bucket whose longest run has not ended is hollow: its length is
            // a floor, and the header says "still going" beside it.
            ForEach(lineData(panel).filter(\.running)) { d in
                PointMark(x: .value("Date", d.date), y: .value("Longest", d.value))
                    .symbol(.circle)
                    .symbolSize(30)
                    .foregroundStyle(AutonomyPalette.lineColor.opacity(0.5))
            }
        }
        // LINEAR, and what that costs is stated where the domain is chosen —
        // see HistoryAutonomyPanel.yDomain. This is the maintainer's explicit
        // call (#1905); there is no toggle and no log path kept behind a flag.
        .chartYScale(domain: domain)
        .chartXScale(domain: xDomain)
        .chartYAxis {
            AxisMarks(values: .automatic(desiredCount: 3)) { value in
                AxisGridLine(stroke: StrokeStyle(lineWidth: 0.5))
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text(AutonomyFormat.duration(v))
                            .font(.system(size: AutonomyPanelMetrics.tickFontSize))
                    }
                }
            }
        }
        .chartXAxis(.hidden)
        .frame(height: AutonomyPanelMetrics.plotHeight)
    }

    /// The concurrency histogram: one bar per bucket, scaled against THIS
    /// panel's own peak.
    ///
    /// A BUCKET WITH NO ONE WORKING DRAWS NOTHING — not a zero-height bar, and
    /// not a hairline on the baseline. A mark on the axis reads as a measured
    /// zero, which is a different and false claim from "no runs here".
    ///
    /// THE BAND'S OWN Y AXIS IS LABELLED (#1905). It used to be a deliberately
    /// BLANK gutter — `AxisMarks(values: [0])` with a spacer string — kept only
    /// so the bars stayed in register with the plot above. That is what "the
    /// number of agents running in parallel is not visible" was about: the bars
    /// carried the only figure on the panel with no scale of any kind. Two
    /// labels now, the band's full-height value and 0, in the same gutter and at
    /// the same 9 pt the line chart uses, so the two axes read as one column.
    /// Two is what the axis actually means — a bar's height is its peak as a
    /// fraction of the highest peak, so the top and the floor are the only two
    /// exact readings, and a ladder of intermediate ticks in a 40 pt band would
    /// be four numbers ten points apart.
    @ViewBuilder private func concurrencyBars(_ panel: HistoryAutonomyPanel) -> some View {
        let scale = panel.peakScale
        Chart {
            ForEach(barData(panel)) { d in
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
            AxisMarks(values: AutonomyBarAxis.values(peak: scale)) { value in
                AxisValueLabel {
                    if let v = value.as(Int.self) {
                        Text(AutonomyBarAxis.label(v))
                            .font(.system(size: AutonomyPanelMetrics.tickFontSize))
                    }
                }
            }
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
    private func lineData(_ panel: HistoryAutonomyPanel) -> [Datum] {
        var out: [Datum] = []
        for segment in AutonomyLineLayout.segments(points: points) {
            for i in segment.from...segment.to {
                guard let b = points[i], b.hasLine, i < data.bucketStarts.count else { continue }
                out.append(Datum(id: "\(panel.project)-\(b.ts)",
                                 date: date(at: i),
                                 run: "\(panel.project)#\(segment.id)",
                                 value: b.longest,
                                 running: b.running,
                                 isolated: segment.isIsolated))
            }
        }
        return out
    }

    private func barData(_ panel: HistoryAutonomyPanel) -> [BarDatum] {
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

// MARK: - The aggregate percentile chart

/// p95/p50/p5 across every project, with the plane between p95 and p5 filled.
///
/// Restored in #1905 after #1919 deleted it. What came back is what went: three
/// lines in ONE hue at three weights, the translucent band, the thin-bucket
/// marking, and the boundary rule with its caption. What did NOT come back is
/// the log scale — see the Y-scale comment below.
private struct AutonomyDurationChart: View {
    let data: HistoryAutonomyDurationResponse
    let timeZone: TimeZone

    /// One drawn point.
    ///
    /// `series` names which of the three lines it belongs to and is what the
    /// colour scale reads. `run` is a different key on purpose: it names the
    /// CONTIGUOUS STRETCH the point belongs to, and Swift Charts connects points
    /// that share it. Without that second key a line is drawn through every
    /// bucket the daemon sent — which silently bridges the omitted ones, exactly
    /// the interpolation `alignedBuckets` exists to refuse.
    private struct Datum: Identifiable {
        let id: String
        let date: Date
        let series: String
        let run: String
        let value: Double
        let thin: Bool
    }

    /// One stretch of the band: the plane between p5 and p95 over a run of
    /// consecutive buckets, or (when `isolated`) a single bucket's spread.
    private struct BandDatum: Identifiable {
        let id: String
        let date: Date
        let run: String
        let low: Double
        let high: Double
        let thin: Bool
    }

    private func date(at index: Int) -> Date {
        Date(timeIntervalSince1970: TimeInterval(data.bucketStarts[index]))
    }

    private var points: [HistoryAutonomyBucket?] { data.alignedBuckets }
    private var segments: [AutonomyBandLayout.Segment] {
        AutonomyBandLayout.segments(points: points)
    }

    /// The three lines, split into one series per contiguous stretch so the
    /// stroke BREAKS at every omitted bucket instead of being drawn through it.
    private func lineData(_ segments: [AutonomyBandLayout.Segment]) -> [Datum] {
        var out: [Datum] = []
        for segment in segments where !segment.isIsolated {
            for i in segment.from...segment.to {
                guard let b = points[i] else { continue }
                for key in AutonomyPalette.seriesOrder {
                    let v: Double
                    switch key {
                    case "p95": v = b.p95
                    case "p5": v = b.p5
                    default: v = b.p50
                    }
                    out.append(Datum(id: "\(key)-\(segment.id)-\(b.ts)",
                                     date: date(at: i),
                                     series: key,
                                     run: "\(key)#\(segment.id)",
                                     value: v,
                                     thin: segment.thin))
                }
            }
        }
        return out
    }

    private func bandData(_ segments: [AutonomyBandLayout.Segment]) -> [BandDatum] {
        var out: [BandDatum] = []
        for segment in segments where !segment.isIsolated {
            for i in segment.from...segment.to {
                guard let b = points[i] else { continue }
                out.append(BandDatum(id: "band-\(segment.id)-\(b.ts)",
                                     date: date(at: i),
                                     run: segment.id,
                                     low: b.p5,
                                     high: b.p95,
                                     thin: segment.thin))
            }
        }
        return out
    }

    /// Buckets with no neighbour to make an area with. Drawn as a whisker
    /// rather than dropped: a lone bucket is exactly where a reader most needs
    /// to see how wide the range was, and it would otherwise be the only one
    /// whose spread is invisible.
    private func whiskerData(_ segments: [AutonomyBandLayout.Segment]) -> [BandDatum] {
        segments.filter(\.isIsolated).compactMap { segment in
            guard let b = points[segment.from] else { return nil }
            return BandDatum(id: "whisker-\(segment.id)", date: date(at: segment.from), run: segment.id,
                             low: b.p5, high: b.p95, thin: segment.thin)
        }
    }

    var body: some View {
        let segments = self.segments
        let lines = lineData(segments)
        Chart {
            // FIRST in the builder, so the plane and the rules sit UNDER the
            // lines. The band is context; the p50 line is the headline, and it
            // is the last ink down so nothing crosses over it.
            ForEach(bandData(segments)) { d in
                AreaMark(
                    x: .value("Date", d.date),
                    yStart: .value("p5", d.low),
                    yEnd: .value("p95", d.high),
                    series: .value("Band", d.run)
                )
                .foregroundStyle(d.thin ? AutonomyPalette.bandThin : AutonomyPalette.band)
                .interpolationMethod(.monotone)
            }
            ForEach(whiskerData(segments)) { d in
                RuleMark(
                    x: .value("Date", d.date),
                    yStart: .value("p5", d.low),
                    yEnd: .value("p95", d.high)
                )
                .lineStyle(StrokeStyle(lineWidth: 1, dash: d.thin ? [3, 3] : []))
                .foregroundStyle(AutonomyPalette.edge)
            }
            // The source-change markers: the marker explains the data, it is
            // not part of it, so it sits under the curves too.
            //
            // A LONG-DASHED 1.5 pt rule, deliberately unlike the axis gridlines
            // below (solid hairlines): before, both were thin dashes in a muted
            // colour and the one line on the chart that carries an explanation
            // was indistinguishable from furniture.
            ForEach(data.visibleBoundaries) { boundary in
                RuleMark(x: .value("Source change", boundary.date))
                    .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [6, 3]))
                    .foregroundStyle(Color.secondary.opacity(0.75))
                    .annotation(position: .top,
                                alignment: captionAlignment(for: boundary),
                                spacing: 2) {
                        // This element writes the words; the panel below draws
                        // the same rule uncaptioned. See AutonomyBoundaryCaption.
                        if AutonomyBoundaryCaption.isShown(element: .aggregate) {
                            Text(boundary.label)
                                .font(.system(size: 9))
                                .foregroundColor(.secondary)
                                .opacity(0.9)
                                .fixedSize()
                        }
                    }
            }
            ForEach(lines) { d in
                LineMark(
                    x: .value("Date", d.date),
                    y: .value("Duration", d.value),
                    series: .value("Run", d.run)
                )
                .foregroundStyle(AutonomyPalette.lineColor)
                .interpolationMethod(.monotone)
                .lineStyle(StrokeStyle(lineWidth: AutonomyPalette.lineWidth(AutonomyPalette.role(of: d.series)),
                                       dash: d.thin ? [3, 3] : []))
                .opacity(AutonomyPalette.opacity(AutonomyPalette.role(of: d.series), thin: d.thin))
            }
            // Thin buckets are marked rather than hidden: a hollow point on
            // every line at that bucket, so a low-sample day is visibly
            // different from a well-sampled one without being dropped. Inside a
            // smooth band this is the third signal, alongside the fainter plane
            // and the dashed edges.
            ForEach(lines.filter(\.thin)) { d in
                PointMark(
                    x: .value("Date", d.date),
                    y: .value("Duration", d.value)
                )
                .symbol(.circle)
                .symbolSize(28)
                .foregroundStyle(AutonomyPalette.lineColor)
                .opacity(0.45)
            }
        }
        .chartLegend(.hidden)
        // LINEAR, and what that costs is stated where the domain is chosen —
        // see HistoryAutonomyDurationResponse.yDomain. This is the maintainer's
        // explicit call (#1905): no toggle, and no log path behind a flag.
        .chartYScale(domain: data.yDomain)
        .chartYAxis {
            AxisMarks { value in
                AxisGridLine(stroke: StrokeStyle(lineWidth: 0.5))
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text(AutonomyFormat.duration(v))
                    }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { value in
                // Solid hairlines. Dashed x gridlines read as several source
                // markers and made the real one impossible to find.
                AxisGridLine(stroke: StrokeStyle(lineWidth: 0.5))
                AxisValueLabel {
                    if let d = value.as(Date.self) {
                        Text(AutonomyFormat.axisDate(d, timeZone: timeZone))
                    }
                }
            }
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

/// Four entries, because the section draws four marks: the aggregate chart's
/// p50 line and p5–p95 plane, and the panel's longest-run line and concurrency
/// bars. Each swatch takes the SHAPE of what it stands for — four identically
/// coloured dots would be a key that says the same thing four times.
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
        case .p50, .line:
            Capsule()
                .fill(entry.color)
                .frame(width: 16, height: 2)
        case .band:
            (entry.fill ?? Color.clear)
                .frame(width: 16, height: 9)
                .overlay(alignment: .top) { entry.color.frame(height: 1) }
                .overlay(alignment: .bottom) { entry.color.frame(height: 1) }
                .clipShape(RoundedRectangle(cornerRadius: 1))
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
    enum Kind: String { case p50, band, line, bars }

    let kind: Kind
    let label: String
    let color: Color
    /// The band's plane. nil for every entry that is a stroke and has no area.
    let fill: Color?

    init(kind: Kind, label: String, color: Color, fill: Color? = nil) {
        self.kind = kind
        self.label = label
        self.color = color
        self.fill = fill
    }

    var id: String { kind.rawValue }
}

/// The concurrency histogram's own y axis (#1905), as a pure function so what
/// it draws is testable without a chart.
///
/// TWO VALUES: the band's full-height peak and 0. That is what the axis actually
/// means — a bar's height is its peak as a fraction of the highest peak, so the
/// top and the floor are the only two exact readings — and a ladder in a 40 pt
/// band would be four numbers ten points apart.
///
/// EMPTY when nothing overlapped anywhere in the panel: with no bar drawn, an
/// axis labelled 0 to 0 would be furniture claiming a measurement. This is the
/// twin of the web's `autonomyBarAxisLabels`.
enum AutonomyBarAxis {
    static func values(peak: Int) -> [Int] {
        guard peak > 0 else { return [] }
        return [0, peak]
    }

    static func label(_ v: Int) -> String { String(v) }
}

enum AutonomyPalette {
    /// ONE HUE, FOUR WEIGHTS. Both lines are `working` at full strength; the
    /// band, its edges and the concurrency histogram are the same hue, quieter,
    /// because they are further readings of the same activity and not further
    /// subjects. `marksAreOneHue` in the tests pins that.
    ///
    /// What never came back: a colour per percentile. The first round drew p95
    /// green, p50 purple and p5 orange — three equally loud curves and a legend
    /// that had to be decoded before the chart said anything.
    static var lineColor: Color { IrrColors.working }
    static var bars: Color { IrrColors.autonomyBar }
    static var band: Color { IrrColors.autonomyBand }
    static var bandThin: Color { IrrColors.autonomyBandThin }
    static var edge: Color { IrrColors.autonomyEdge }

    /// The three drawn percentile lines, in draw order, and what each is FOR.
    /// The colours no longer separate them, so the roles do — and the chart
    /// reads its stroke weights from here rather than from a literal beside each
    /// mark. Mirrors the web's AUTONOMY_SERIES.
    static let seriesOrder = ["p95", "p50", "p5"]
    enum Role { case line, edge }
    static let seriesRoles: [String: Role] = ["p95": .edge, "p50": .line, "p5": .edge]
    static func role(of key: String) -> Role { seriesRoles[key] ?? .line }

    /// Stroke weight per role. The line carries the chart; the edges are
    /// present enough to bound the plane and quiet enough not to compete.
    static func lineWidth(_ role: Role) -> CGFloat { role == .line ? 1.8 : 1 }
    static func opacity(_ role: Role, thin: Bool) -> Double {
        switch (role, thin) {
        case (.line, false): return 1
        case (.line, true): return 0.6
        case (.edge, false): return 0.5
        case (.edge, true): return 0.32
        }
    }

    /// The section's key: exactly four entries, because it draws exactly four
    /// marks, in the order they appear on screen.
    ///
    /// SAME FOUR STRINGS AS THE WEB's side-panel key (AUTONOMY_KEY in
    /// platforms/web/historyTab.js), and pinned against it by `the two surfaces
    /// name the key the same way` — two clients must not explain one chart
    /// differently. That panel is 260 pt where this popover is 380, so the
    /// length that fits THERE is the length both carry.
    static var keyEntries: [AutonomyKeyEntry] {
        [
            AutonomyKeyEntry(kind: .p50, label: "p50 · the typical run", color: lineColor),
            AutonomyKeyEntry(kind: .band, label: "p5–p95 · the usual spread", color: edge, fill: band),
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

    /// What the panel draws instead of an empty frame when the selected project
    /// has no runs in this range. Same wording as the web's
    /// `autonomyEmptyProjectNote` — two clients must not explain one state
    /// differently.
    static func emptyProjectNote(_ project: String) -> String {
        "no runs for \(project.isEmpty ? "this project" : project) in this range"
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
    static let concurrencyCaveat = "A parent counts as working while its subagents run."

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
    ///
    /// THE RECONSTRUCTED SUFFIX IS THE LAST SURVIVOR of the three-sentence
    /// reconstruction paragraph #1905's prose cut deleted.
    /// `tools/autonomy-backfill` rebuilds pre-feature runs from logs a machine
    /// already had, and a reconstructed figure rendered as a measured one is
    /// exactly the "wrong number with nothing on screen saying so" this section
    /// was built to prevent — so the count cannot go. It costs the reader it is
    /// written for nothing: a machine that was never back-filled reports 0 and
    /// the clause never appears. SAME WORDING AS THE WEB's
    /// `autonomyProvenanceLine`.
    static func provenance(earliest: Int64, total: Int, reconstructed: Int,
                           timeZone: TimeZone) -> String {
        guard earliest > 0 else {
            return "No autonomous runs recorded yet. Irrlicht began measuring them with this update; "
                + "the panels above fill in as sessions run."
        }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "MMM d, yyyy"
        let since = f.string(from: Date(timeIntervalSince1970: TimeInterval(earliest)))
        let backfill = reconstructed > 0 ? " · \(reconstructed) in view reconstructed" : ""
        return "Collecting since \(since) · \(total) run\(total == 1 ? "" : "s") recorded\(backfill)."
    }

    /// Marks the runs in view whose duration is a FLOOR rather than a
    /// measurement (#1905 recording). `nil` when nothing is running — the quiet
    /// case on a machine between sessions.
    ///
    /// A run that has not ended has no length yet, only how long it has lasted
    /// SO FAR. That floor still COUNTS towards the longest (the section reports
    /// a maximum, and "already lasted 3h" is true) and is deliberately not a
    /// sample for the aggregate chart's percentiles, where a floor shortens the
    /// longest runs hardest.
    ///
    /// THE SECOND SENTENCE WENT (#1905 prose cut). It named the runs already
    /// going when Irrlicht started watching, whose start is a lower bound
    /// rather than a beginning — a real limit, but a restart artefact a reader
    /// can do nothing with, and the longest sentence in the section.
    /// `measurement.start_lower_bound` stays on the wire; only the sentence
    /// went. SAME WORDING AS THE WEB's `autonomyMeasurementNote`.
    static func measurementLine(_ m: HistoryAutonomyMeasurement) -> String? {
        guard m.running > 0 else { return nil }
        let one = m.running == 1
        return "\(m.running) run\(one ? "" : "s") still going — \(one ? "length" : "lengths") so far."
    }
}
