import Charts
import SwiftUI

// MARK: - Autonomy section (#1905)
//
// Two elements over the always-on span log, rendered together because they
// answer two halves of one question: element 1 is "is this getting better?"
// (p95/p50/p5 of autonomous run duration over time), element 2 is "who ran
// long, who kept stopping, and were the stops questions or errors?".
//
// Pure inputs (the two decoded responses), so a snapshot test can host this
// view directly with fixture data.

struct HistoryAutonomyContentView: View {
    let duration: HistoryAutonomyDurationResponse
    let spans: HistoryAutonomySpansResponse
    let range: HistoryAutonomyRange
    /// A BINDING, unlike `range`: the Span picker lives in this view now,
    /// in the strip's own section header. Range still belongs to the tab's
    /// control row because the chart is the first thing under it; Span sat up
    /// there too, one row of controls above two elements, with nothing saying
    /// which control moved which — and the two vocabularies overlap textually
    /// (`30d` is a Range value AND a Span value), so the row read as one zoom
    /// control with an odd set of steps.
    @Binding var spanWindow: HistoryAutonomySpanWindow

    /// #1659 — every date this view renders takes its zone as an INPUT rather
    /// than reading `NSTimeZone.default`.
    @Environment(\.formatTimeZone) private var formatTimeZone

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp4) {
            durationSection
            Divider()
            stripSection
            Divider()
            collectionProvenance
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: Element 1 — duration over time

    @ViewBuilder private var durationSection: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            HStack {
                Text("Autonomous run duration · \(range.label)")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Spacer()
                Text("log scale")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
            if duration.hasData {
                AutonomyDurationChart(data: duration, timeZone: formatTimeZone)
                    .frame(height: 190)
                AutonomyKeyView()
                summaryRow
                if thinCount > 0 { thinNote }
            } else {
                emptyDurationText
            }
        }
    }

    /// "p95 1h58m · p50 11m · p5 41s · longest 2h14m · shortest 22s · 312 runs"
    /// — the true extremes are figures here, deliberately not lines on the
    /// chart (see HistoryAutonomySummary).
    private var summaryRow: some View {
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

    private var thinCount: Int { duration.buckets.filter(\.thin).count }

    /// Thin buckets are MARKED, never hidden and never smoothed — and the
    /// marking is explained in words, because a fainter plane means nothing on
    /// its own. Three signals, since a distinction is easy to lose inside a
    /// smooth band: the plane itself, the edges bounding it, and the points.
    private var thinNote: some View {
        Text("\(thinCount) of \(duration.buckets.count) buckets hold fewer than \(duration.sampleFloor) runs "
             + "(fainter band, dashed edges, hollow points): there, p95 is that bucket's longest run and "
             + "p5 its shortest — not percentiles.")
            .font(.caption2)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// An empty chart with axes drawn is not acceptable (#1905): the empty
    /// state says, in words, that the feature collects from the day it ships.
    private var emptyDurationText: some View {
        Text(duration.totalRecorded == 0
             ? "No autonomous runs recorded yet. Irrlicht starts measuring them the first time a session runs after this update — an empty chart here means \"nothing recorded\", not \"nothing happened\"."
             : "No runs in this range. \(duration.totalRecorded) runs are on record outside it.")
            .font(.callout)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, minHeight: 150, alignment: .center)
            .multilineTextAlignment(.center)
    }

    // MARK: Element 2 — the run strip

    @ViewBuilder private var stripSection: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            // The strip's own header: its title and the picker that changes
            // it, together — the whole point of moving Span down here.
            HStack(spacing: IrrSpacing.sp2) {
                Text("Runs · last \(spanWindow.label)")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Spacer(minLength: IrrSpacing.sp2)
                Picker("Span", selection: $spanWindow) {
                    ForEach(HistoryAutonomySpanWindow.allCases) { Text($0.label).tag($0) }
                }
                .labelsHidden()
                .fixedSize()
                .pickerStyle(.menu)
                .controlSize(.small)
                .font(.caption)
            }
            if spans.hasData {
                // A header over the value column: the figure at the end of
                // each row was a bare duration with nothing saying what it
                // measured.
                stripHeader
                // Capped at the busiest few: unbounded, a back-filled 12mo
                // window draws 95 rows into a 380 pt popover and buries the
                // projects worth looking at (#1905 back-fill, QA-1).
                ForEach(spans.visibleProjects, id: \.self) { project in
                    AutonomyStripRow(project: project,
                                     spans: spans.spans(for: project),
                                     start: spans.start,
                                     end: spans.end)
                }
                if let overflow = spans.overflowLabel { stripOverflow(overflow) }
                // …and the window's bounds under it, so a mark can be placed
                // in time. Two labels, not a tick axis: at 12mo a full axis is
                // more furniture than the strip is worth, but "from when to
                // when" is the difference between a timeline and a texture.
                stripAxis
                stripLegend
                if spans.truncated { truncationNote }
            } else {
                emptyStripText
            }
        }
    }

    /// Column header for the per-row "longest" figure. Right-aligned by the
    /// same Spacer the rows use, so it sits over the values it names.
    private var stripHeader: some View {
        HStack(spacing: IrrSpacing.sp2) {
            Text("project")
            Spacer(minLength: IrrSpacing.sp2)
            Text("longest")
        }
        .font(.caption2)
        .foregroundColor(.secondary)
        .accessibilityHidden(true) // each row's own label already says "longest"
    }

    /// "+N more projects" — quieter than a row and clearly not one: it is a
    /// statement about the strip, not another line of it.
    private func stripOverflow(_ text: String) -> some View {
        Text(text)
            .font(.caption2)
            .foregroundColor(.secondary)
            .opacity(0.85)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// The strip's window bounds: where it starts on the left, `now` on the
    /// right. The start label coarsens with the window (a time of day at 8h, a
    /// month at 12mo) — see AutonomyFormat.axisBound.
    private var stripAxis: some View {
        HStack(spacing: IrrSpacing.sp2) {
            Text(AutonomyFormat.axisBound(Date(timeIntervalSince1970: TimeInterval(spans.start)),
                                          windowSeconds: spans.end - spans.start,
                                          timeZone: formatTimeZone))
            Spacer(minLength: IrrSpacing.sp2)
            Text("now")
        }
        .font(.caption2)
        .foregroundColor(.secondary)
    }

    /// The measured reasons, plus a neutral `unknown` entry when — and only
    /// when — this window actually holds a run whose end reason nothing can
    /// name (#1905 back-fill).
    private var stripLegend: some View {
        HStack(spacing: IrrSpacing.sp3) {
            ForEach(AutonomyLegend.entries(for: spans.spans)) { entry in
                HStack(spacing: IrrSpacing.sp1) {
                    RoundedRectangle(cornerRadius: 1)
                        .fill(AutonomyPalette.color(for: entry.reason))
                        .frame(width: 10, height: 8)
                    Text("\(entry.glyph) \(entry.label)")
                }
            }
            Spacer(minLength: 0)
        }
        .font(.caption2)
        .foregroundColor(.secondary)
    }

    private var truncationNote: some View {
        Text("This window holds more runs than one request returns; the strip shows the oldest part of it. "
             + "Pick a shorter span for a complete picture.")
            .font(.caption2)
            .foregroundColor(IrrColors.waiting)
            .fixedSize(horizontal: false, vertical: true)
    }

    private var emptyStripText: some View {
        Text(spans.totalRecorded == 0
             ? "No runs recorded yet — this strip fills in as sessions run."
             : "No runs in the last \(spanWindow.label). \(spans.totalRecorded) runs are on record over a longer period.")
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
    private var collectionProvenance: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            Text(AutonomyFormat.provenance(earliest: duration.earliestSpan,
                                           total: duration.totalRecorded,
                                           timeZone: formatTimeZone))
            if let note = AutonomyFormat.reconstructionNote(duration.provenanceOrNone,
                                                            inView: duration.summary.count,
                                                            timeZone: formatTimeZone) {
                Text(note)
            }
        }
        .font(.caption2)
        .foregroundColor(.secondary)
        .fixedSize(horizontal: false, vertical: true)
    }
}

// MARK: - The percentile chart

private struct AutonomyDurationChart: View {
    let data: HistoryAutonomyDurationResponse
    let timeZone: TimeZone

    /// One drawn point.
    ///
    /// `series` names which of the three lines it belongs to and is what the
    /// colour scale reads. `run` is a different key on purpose: it names the
    /// CONTIGUOUS STRETCH the point belongs to, and Swift Charts connects
    /// points that share it. Without that second key a line is drawn through
    /// every bucket the daemon sent — which silently bridges the omitted ones,
    /// exactly the interpolation `alignedBuckets` exists to refuse.
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

    /// Log scales cannot plot 0, and a span shorter than a second is not a
    /// run — clamp to one second so a degenerate value cannot blank the chart.
    private func plottable(_ v: Double) -> Double { Swift.max(1, v) }

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
                                     value: plottable(v),
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
                                     low: plottable(b.p5),
                                     high: plottable(b.p95),
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
                             low: plottable(b.p5), high: plottable(b.p95), thin: segment.thin)
        }
    }

    /// Y domain with a little headroom, floored so a single short run still
    /// draws inside a readable range.
    private var yDomain: ClosedRange<Double> {
        let values = data.buckets.flatMap { [plottable($0.p5), plottable($0.p95)] }
        let lo = Swift.max(1, (values.min() ?? 1) * 0.8)
        let hi = Swift.max(lo * 2, (values.max() ?? 60) * 1.25)
        return lo...hi
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
            // A LONG-DASHED 1.5 pt rule, deliberately unlike the axis
            // gridlines below (solid hairlines): before, both were thin dashes
            // in a muted colour and the one line on the chart that carries an
            // explanation was indistinguishable from furniture.
            ForEach(data.visibleBoundaries) { boundary in
                RuleMark(x: .value("Source change", boundary.date))
                    .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [6, 3]))
                    .foregroundStyle(Color.secondary.opacity(0.75))
                    .annotation(position: .top,
                                alignment: captionAlignment(for: boundary),
                                spacing: 2) {
                        Text(boundary.label)
                            .font(.system(size: 9))
                            .foregroundColor(.secondary)
                            .opacity(0.9)
                            .fixedSize()
                    }
            }
            ForEach(lines) { d in
                LineMark(
                    x: .value("Date", d.date),
                    y: .value("Duration", d.value),
                    series: .value("Run", d.run)
                )
                .foregroundStyle(by: .value("Series", d.series))
                .interpolationMethod(.monotone)
                .lineStyle(StrokeStyle(lineWidth: AutonomyPalette.lineWidth(AutonomyPalette.role(of: d.series)),
                                       dash: d.thin ? [3, 3] : []))
                .opacity(AutonomyPalette.opacity(AutonomyPalette.role(of: d.series), thin: d.thin))
            }
            // Thin buckets are marked rather than hidden: a hollow point on
            // every line at that bucket, so a low-sample day is visibly
            // different from a well-sampled one without being dropped. Inside
            // a smooth band this is the third signal, alongside the fainter
            // plane and the dashed edges.
            ForEach(lines.filter(\.thin)) { d in
                PointMark(
                    x: .value("Date", d.date),
                    y: .value("Duration", d.value)
                )
                .symbol(.circle)
                .symbolSize(28)
                .foregroundStyle(by: .value("Series", d.series))
                .opacity(0.45)
            }
        }
        // One table for the three lines, read by the scale AND by
        // AutonomyPalette.keyEntries — the web's twin of this is
        // AUTONOMY_SERIES, and both surfaces must name the lines the same way.
        .chartForegroundStyleScale(domain: AutonomyPalette.seriesOrder,
                                   range: AutonomyPalette.seriesRange)
        // …and the legend Swift Charts derives from that scale is hidden,
        // because with one hue it would be three identical swatches naming
        // three percentiles. AutonomyKeyView draws the two entries that
        // actually correspond to marks on the chart.
        .chartLegend(.hidden)
        .chartYScale(domain: yDomain, type: .log)
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

// MARK: - The chart's key

/// Two entries, because the chart draws two things: a line and a plane. Each
/// swatch takes the SHAPE of what it stands for — a pair of identically
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
        case .band:
            (entry.fill ?? Color.clear)
                .frame(width: 16, height: 9)
                .overlay(alignment: .top) { entry.color.frame(height: 1) }
                .overlay(alignment: .bottom) { entry.color.frame(height: 1) }
                .clipShape(RoundedRectangle(cornerRadius: 1))
        }
    }
}

// MARK: - One strip row

private struct AutonomyStripRow: View {
    let project: String
    let spans: [HistoryAutonomySpanRow]
    let start: Int64
    let end: Int64

    private static let rowHeight: CGFloat = 14

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: IrrSpacing.sp2) {
                Text(project)
                    .font(.caption2)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: IrrSpacing.sp2)
                Text(AutonomyFormat.duration(Double(longest)))
                    .font(.caption2)
                    .monospacedDigit()
                    .foregroundColor(.secondary)
            }
            Canvas { context, size in
                // One column per point of width. The collapse rule (minimum
                // one column per span, highest-priority reason wins a shared
                // column) lives in AutonomyStripLayout so it is testable
                // without a view — and so the web draws the same strip.
                let columns = Swift.max(1, Int(size.width.rounded(.down)))
                let cells = AutonomyStripLayout.collapse(spans: spans, start: start, end: end, columns: columns)
                let colWidth = size.width / CGFloat(columns)
                for (i, cell) in cells.enumerated() where cell.occupied {
                    let rect = CGRect(x: CGFloat(i) * colWidth, y: 0,
                                      width: Swift.max(colWidth, 1), height: size.height)
                    context.fill(Path(rect), with: .color(AutonomyPalette.color(for: cell.reason)))
                }
            }
            .frame(height: Self.rowHeight)
            .background(Color.secondary.opacity(0.08))
            .clipShape(RoundedRectangle(cornerRadius: 2))
            .accessibilityLabel(accessibilityText)
        }
    }

    private var longest: Int64 { spans.map(\.duration).max() ?? 0 }

    private var accessibilityText: String {
        "\(project): \(spans.count) runs, longest \(AutonomyFormat.duration(Double(longest)))"
    }
}

// MARK: - Palette + formatting

/// One entry of the chart's key. `from` names the `AutonomyPalette.seriesOrder`
/// row the entry takes its colour from, so a key entry cannot be given a
/// colour the chart does not draw.
struct AutonomyKeyEntry: Identifiable, Equatable {
    enum Kind: String { case line, band }

    let kind: Kind
    let from: String
    let label: String
    let color: Color
    /// The plane's fill. `nil` for the line entry — a line has no area, and a
    /// swatch with a fill would claim one.
    let fill: Color?

    var id: String { kind.rawValue }
}

enum AutonomyPalette {
    /// The three drawn lines, in draw order, and their colours — ONE table,
    /// read by the chart's style scale AND by the two key entries below, so
    /// the key and the curves cannot disagree. Mirrors the web's
    /// AUTONOMY_SERIES, which carries the same property.
    ///
    /// ONE HUE, THREE WEIGHTS. The first round drew p95 green, p50 purple and
    /// p5 orange — three equally loud curves and a legend that had to be
    /// decoded before the chart said anything. It says one thing now: here is
    /// the typical run (the solid `line`), and here is the spread around it
    /// (the plane between the two quiet `edge`s). A hue per percentile made
    /// p95 and p5 read as two independent measurements rather than as the
    /// boundary of one range.
    static let seriesOrder = ["p95", "p50", "p5"]
    static let seriesColors: [String: Color] = [
        "p95": IrrColors.working,
        "p50": IrrColors.working,
        "p5": IrrColors.working,
    ]

    /// What each line is FOR. The colours no longer separate them, so the
    /// roles do — and the chart reads its stroke weights from here rather than
    /// from a literal beside each mark.
    enum Role { case line, edge }
    static let seriesRoles: [String: Role] = [
        "p95": .edge,
        "p50": .line,
        "p5": .edge,
    ]

    static func role(of key: String) -> Role { seriesRoles[key] ?? .line }

    /// The headline line's colour: the one every reader is meant to follow.
    static var lineColor: Color { seriesColors["p50"] ?? .gray }

    /// The band's own three weights — the plane, the fainter plane a thin
    /// stretch gets, and the weight its two edges are stroked at. A second
    /// TABLE, not a second palette: every one of them is the `working` hue at
    /// a lower alpha (`bandIsTheLineHue` in the tests pins that), and both the
    /// chart and the key read these same entries.
    static var band: Color { IrrColors.autonomyBand }
    static var bandThin: Color { IrrColors.autonomyBandThin }
    static var edge: Color { IrrColors.autonomyEdge }

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

    /// The chart's key: exactly two entries, because the chart draws exactly
    /// two things. Three percentile swatches in one hue would promise a
    /// distinction the chart stopped making — which is why the Swift Charts
    /// legend derived from `seriesOrder` is hidden and this is drawn instead.
    ///
    /// Named for a reader who has never seen a percentile — the register the
    /// section already used ("the typical run") — with the p-labels kept in
    /// front for a reader who has. Same two strings as the web's panel key.
    static var keyEntries: [AutonomyKeyEntry] {
        [
            AutonomyKeyEntry(kind: .line, from: "p50", label: "p50 · the typical run",
                             color: seriesColors["p50"] ?? .gray, fill: nil),
            AutonomyKeyEntry(kind: .band, from: "p95", label: "p5–p95 · where most runs land",
                             color: edge, fill: band),
        ]
    }

    /// The scale's range in `seriesOrder`. A key with no colour would be a
    /// line drawn in the fallback grey rather than silently undrawn, which is
    /// why this cannot crash on a bad key — but `seriesColorsCoverEveryLine`
    /// in the tests refuses one.
    static var seriesRange: [Color] { seriesOrder.map { seriesColors[$0] ?? .gray } }

    /// A column's fill. An unnamed reason is drawn neutral rather than
    /// skipped: the run happened, this build just cannot say how it ended.
    static func color(for reason: AutonomyEndReason?) -> Color {
        switch reason {
        case .some(.error): return IrrColors.error
        case .some(.waiting): return IrrColors.waiting
        case .some(.ready): return IrrColors.ready
        case .none: return Color.secondary.opacity(0.55)
        }
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

    /// The run strip's left bound, coarsening with the window the way the
    /// activity matrix's column headers do: an 8h strip needs a time of day, a
    /// 12mo strip needs a month. In the caller's zone (#1659), never the
    /// machine's.
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
                + "the charts above fill in as sessions run."
        }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "MMM d, yyyy"
        let since = f.string(from: Date(timeIntervalSince1970: TimeInterval(earliest)))
        return "Collecting since \(since) · \(total) runs recorded."
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
    /// say how a run ended — so an `unknown` column in the strip reads as a
    /// limit of the source rather than as a bug.
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
