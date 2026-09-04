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
    let spanWindow: HistoryAutonomySpanWindow

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
    /// marking is explained in words, because a hollow dot means nothing on
    /// its own.
    private var thinNote: some View {
        Text("\(thinCount) of \(duration.buckets.count) buckets hold fewer than \(duration.sampleFloor) runs "
             + "(hollow points, dashed): there, p95 is that bucket's longest run and p5 its shortest — "
             + "not percentiles.")
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
            Text("Runs · last \(spanWindow.label)")
                .font(.caption)
                .foregroundColor(.secondary)
            if spans.hasData {
                ForEach(spans.projects, id: \.self) { project in
                    AutonomyStripRow(project: project,
                                     spans: spans.spans(for: project),
                                     start: spans.start,
                                     end: spans.end)
                }
                stripLegend
                if spans.truncated { truncationNote }
            } else {
                emptyStripText
            }
        }
    }

    private var stripLegend: some View {
        HStack(spacing: IrrSpacing.sp3) {
            ForEach(AutonomyEndReason.allCases) { reason in
                HStack(spacing: IrrSpacing.sp1) {
                    RoundedRectangle(cornerRadius: 1)
                        .fill(AutonomyPalette.color(for: reason))
                        .frame(width: 10, height: 8)
                    Text("\(reason.glyph) \(reason.label)")
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
    /// read as "you did nothing" (#1905).
    private var collectionProvenance: some View {
        Text(AutonomyFormat.provenance(earliest: duration.earliestSpan,
                                       total: duration.totalRecorded,
                                       timeZone: formatTimeZone))
            .font(.caption2)
            .foregroundColor(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }
}

// MARK: - The percentile chart

private struct AutonomyDurationChart: View {
    let data: HistoryAutonomyDurationResponse
    let timeZone: TimeZone

    /// One drawn point. `series` names which of the three lines it belongs to.
    private struct Datum: Identifiable {
        let id: String
        let date: Date
        let series: String
        let value: Double
        let thin: Bool
    }

    /// Log scales cannot plot 0, and a span shorter than a second is not a
    /// run — clamp to one second so a degenerate value cannot blank the chart.
    private func plottable(_ v: Double) -> Double { Swift.max(1, v) }

    private var data3: [Datum] {
        var out: [Datum] = []
        for b in data.buckets {
            let date = Date(timeIntervalSince1970: TimeInterval(b.ts))
            out.append(Datum(id: "p95-\(b.ts)", date: date, series: "p95", value: plottable(b.p95), thin: b.thin))
            out.append(Datum(id: "p50-\(b.ts)", date: date, series: "p50", value: plottable(b.p50), thin: b.thin))
            out.append(Datum(id: "p5-\(b.ts)", date: date, series: "p5", value: plottable(b.p5), thin: b.thin))
        }
        return out
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
        let points = data3
        Chart {
            ForEach(points) { d in
                LineMark(
                    x: .value("Date", d.date),
                    y: .value("Duration", d.value),
                    series: .value("Series", d.series)
                )
                .foregroundStyle(by: .value("Series", d.series))
                .interpolationMethod(.monotone)
            }
            // Thin buckets are marked rather than hidden: a hollow point on
            // every line at that bucket, so a low-sample day is visibly
            // different from a well-sampled one without being dropped.
            ForEach(points.filter(\.thin)) { d in
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
        .chartForegroundStyleScale([
            "p95": IrrColors.ready,
            "p50": IrrColors.working,
            "p5": IrrColors.waiting,
        ])
        .chartYScale(domain: yDomain, type: .log)
        .chartYAxis {
            AxisMarks { value in
                AxisGridLine()
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text(AutonomyFormat.duration(v))
                    }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { value in
                AxisGridLine()
                AxisValueLabel {
                    if let d = value.as(Date.self) {
                        Text(AutonomyFormat.axisDate(d, timeZone: timeZone))
                    }
                }
            }
        }
        .chartLegend(position: .bottom, spacing: 4)
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

enum AutonomyPalette {
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

    /// X-axis tick label, in the caller's zone (#1659 — never the machine's).
    static func axisDate(_ date: Date, timeZone: TimeZone) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "MMM d"
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
}
