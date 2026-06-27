import AppKit
import Charts
import SwiftUI

// MARK: - History view (issue #755)
//
// Phase-1 cost-analytics parity with the web dashboard's History tab (#369 /
// #752). A full-panel swap inside the menu-bar popover (like SettingsView),
// reached from the footer. Consumes `GET /api/v1/history` only — no daemon
// changes. The web's side-by-side chart + side-panel is restacked vertically
// to fit the 380pt popover.

struct HistoryView: View {
    let onClose: () -> Void

    @State private var range: HistoryRange = .day
    @State private var forecastEnabled = true
    @State private var customStart = Calendar.current.date(byAdding: .day, value: -7, to: Date()) ?? Date()
    @State private var customEnd = Date()
    // Resolved [start, end) unix seconds for a custom range, set on Apply (and
    // when the range first switches to .custom).
    @State private var appliedCustomStart: Int64?
    @State private var appliedCustomEnd: Int64?

    @State private var response: HistoryResponse?
    @State private var loadFailed = false

    /// Re-runs the fetch via `.task(id:)` whenever the effective query changes.
    /// This is the macOS equivalent of the web's manual `historyFetchSeq`
    /// dedup — `.task(id:)` cancels the in-flight request when the key changes.
    private var queryKey: String {
        let fc = forecastEnabled ? "f1" : "f0"
        if range == .custom {
            return "custom-\(appliedCustomStart ?? 0)-\(appliedCustomEnd ?? 0)-\(fc)"
        }
        return "\(range.rawValue)-\(fc)"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
            controls
            Divider()
            content
        }
        .frame(width: SessionListView.panelWidth)
        .background(Color(NSColor.windowBackgroundColor))
        .task(id: queryKey) { await fetch() }
        .onChange(of: range) { newRange in
            if newRange == .custom { applyCustomRange() }
        }
    }

    // MARK: Header

    private var header: some View {
        ZStack {
            Text("History").font(.headline)
            HStack {
                Button(action: onClose) {
                    HStack(spacing: 2) {
                        Image(systemName: "chevron.left")
                        Text("Back")
                    }
                    .foregroundColor(.secondary)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                Spacer()
            }
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: Controls

    private var controls: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            Picker("", selection: $range) {
                ForEach(HistoryRange.allCases) { r in
                    Text(r.shortLabel).tag(r)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()

            if range == .custom {
                HStack(spacing: IrrSpacing.sp2) {
                    DatePicker("", selection: $customStart, displayedComponents: .date)
                        .labelsHidden()
                    Text("→").foregroundColor(.secondary)
                    DatePicker("", selection: $customEnd, displayedComponents: .date)
                        .labelsHidden()
                    Button("Apply") { applyCustomRange() }
                }
                .font(.caption)
            }

            Toggle("Forecast", isOn: $forecastEnabled)
                .toggleStyle(.checkbox)
                .font(.caption)
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: Content

    @ViewBuilder private var content: some View {
        if let r = response {
            HistoryContentView(
                data: r,
                range: range,
                forecastEnabled: forecastEnabled,
                onExportCSV: { save(ext: "csv", text: HistoryExport.csv(r)) },
                onExportJSON: { save(ext: "json", text: HistoryExport.json(r)) }
            )
        } else if loadFailed {
            Text("Couldn’t load history.")
                .font(.callout)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, minHeight: 220)
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, minHeight: 220)
        }
    }

    // MARK: Fetch

    private func applyCustomRange() {
        let cal = Calendar.current
        let s = Int64(cal.startOfDay(for: customStart).timeIntervalSince1970)
        // Include the end day, matching the web (+86400 on the end-date midnight).
        let e = Int64(cal.startOfDay(for: customEnd).timeIntervalSince1970) + 86_400
        appliedCustomStart = s
        appliedCustomEnd = max(s + 1, e)
    }

    private func fetch() async {
        loadFailed = false
        let cs = range == .custom ? appliedCustomStart : nil
        let ce = range == .custom ? appliedCustomEnd : nil
        var comps = URLComponents(string: "\(DaemonEndpoint.httpBase)/api/v1/history")
        comps?.queryItems = range.queryItems(forecast: forecastEnabled, customStart: cs, customEnd: ce)
        guard let url = comps?.url else { return }
        do {
            let (data, resp) = try await URLSession.shared.data(from: url)
            if Task.isCancelled { return }
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
                loadFailed = true
                return
            }
            let decoded = try JSONDecoder().decode(HistoryResponse.self, from: data)
            if Task.isCancelled { return }
            response = decoded
        } catch {
            if !Task.isCancelled { loadFailed = true }
        }
    }

    private func save(ext: String, text: String) {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "irrlicht-history-\(range.rawValue)-cost.\(ext)"
        panel.begin { resp in
            guard resp == .OK, let url = panel.url else { return }
            try? text.write(to: url, atomically: true, encoding: .utf8)
        }
    }
}

// MARK: - Pure content (chart + summary + export)
//
// No networking — renders entirely from an in-memory `HistoryResponse`, so
// snapshot tests can host it directly with fixture data.

struct HistoryContentView: View {
    let data: HistoryResponse
    let range: HistoryRange
    let forecastEnabled: Bool
    let onExportCSV: () -> Void
    let onExportJSON: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            chart
            Divider()
            summary
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    @ViewBuilder private var chart: some View {
        if data.hasData {
            HistoryCostChart(
                data: data,
                orderedProjects: orderedProjects,
                forecastEnabled: forecastEnabled
            )
            .frame(height: 200)
        } else {
            Text("no cost data in this range yet")
                .font(.callout)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, minHeight: 200)
        }
    }

    private var summary: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            Text("Total · \(range.label)")
                .font(.caption)
                .foregroundColor(.secondary)
            Text(HistoryFormat.dollar(data.total))
                .font(.title2)
                .fontWeight(.semibold)
                .monospacedDigit()

            if forecastEnabled, let fc = data.forecast {
                Text("▲ projected \(HistoryFormat.dollar(fc.projected)) (\(fc.basis))")
                    .font(.caption)
                    .foregroundColor(IrrColors.waiting)
            }

            if data.topContributors.isEmpty {
                Text("no spend in this range")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .padding(.top, IrrSpacing.sp1)
            } else {
                VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
                    ForEach(Array(data.topContributors.enumerated()), id: \.offset) { i, c in
                        HStack(spacing: IrrSpacing.sp2) {
                            Circle()
                                .fill(HistoryPalette.color(at: i))
                                .frame(width: 8, height: 8)
                            Text(c.label)
                                .font(.caption)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer(minLength: IrrSpacing.sp2)
                            Text(HistoryFormat.dollar(c.value))
                                .font(.caption)
                                .monospacedDigit()
                                .foregroundColor(.secondary)
                        }
                    }
                }
                .padding(.top, IrrSpacing.sp1)
            }

            HStack(spacing: IrrSpacing.sp2) {
                Button("Export CSV", action: onExportCSV)
                Button("Export JSON", action: onExportJSON)
                Spacer()
            }
            .font(.caption)
            .padding(.top, IrrSpacing.sp2)
        }
    }

    /// Project order: `top_contributors` first (so the panel dots match the
    /// chart colors), then any extra projects from the series — mirrors the web
    /// `paintHistoryChart`.
    private var orderedProjects: [String] {
        var seen = Set<String>()
        var order: [String] = []
        for c in data.topContributors where !seen.contains(c.label) {
            seen.insert(c.label)
            order.append(c.label)
        }
        for pt in data.series where !seen.contains(pt.project) {
            seen.insert(pt.project)
            order.append(pt.project)
        }
        return order
    }
}

// MARK: - Stacked-bar cost chart (Swift Charts)

private struct HistoryCostChart: View {
    let data: HistoryResponse
    let orderedProjects: [String]
    let forecastEnabled: Bool

    private struct Datum: Identifiable {
        let id: String
        let date: Date
        let project: String
        let value: Double
    }

    /// Stacked bars draw straight from the sparse series — no zero-fill needed
    /// (unlike a continuous area), which also keeps the mark count low on
    /// fine-grained ranges. Per-bucket stacking order follows the
    /// `chartForegroundStyleScale` domain (`orderedProjects`).
    private var costData: [Datum] {
        data.series.map { pt in
            Datum(
                id: "\(pt.ts)|\(pt.project)",
                date: Date(timeIntervalSince1970: TimeInterval(pt.ts)),
                project: pt.project,
                value: pt.value
            )
        }
    }

    private var forecastData: [Datum] {
        guard forecastEnabled, let fc = data.forecast, !fc.series.isEmpty else { return [] }
        var pts: [Datum] = []
        // Anchor the dashed line at the last data bucket (at the forecast's own
        // first value) so it connects to the chart's right edge and renders even
        // for a single-bucket horizon — mirrors the web `moveTo(xAt(B-1), …)`.
        if let lastBucket = data.bucketStarts.last {
            pts.append(Datum(
                id: "fc-anchor",
                date: Date(timeIntervalSince1970: TimeInterval(lastBucket)),
                project: "forecast",
                value: fc.series[0].value
            ))
        }
        pts += fc.series.map { p in
            Datum(
                id: "fc-\(p.ts)",
                date: Date(timeIntervalSince1970: TimeInterval(p.ts)),
                project: "forecast",
                value: p.value
            )
        }
        return pts
    }

    var body: some View {
        Chart {
            ForEach(costData) { d in
                BarMark(
                    x: .value("Time", d.date),
                    y: .value("Cost", d.value)
                )
                .foregroundStyle(by: .value("Project", d.project))
            }
            ForEach(forecastData) { d in
                LineMark(
                    x: .value("Time", d.date),
                    y: .value("Cost", d.value),
                    series: .value("Series", "forecast")
                )
                .foregroundStyle(IrrColors.waiting)
                .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [4, 3]))
            }
        }
        .chartForegroundStyleScale(
            domain: orderedProjects,
            range: orderedProjects.indices.map { HistoryPalette.color(at: $0) }
        )
        .chartLegend(.hidden)
        .chartYAxis {
            AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) { value in
                AxisGridLine()
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text(HistoryFormat.dollar(v))
                    }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 5)) { value in
                AxisGridLine()
                AxisValueLabel {
                    if let d = value.as(Date.self) {
                        Text(HistoryFormat.axisLabel(d, bucketSeconds: data.bucketSeconds))
                    }
                }
            }
        }
    }
}

// MARK: - Shared palette / formatting / export

enum HistoryPalette {
    /// Mirrors the web `HISTORY_COLORS` so the chart areas and side-panel dots
    /// share the same hues.
    static let colors: [Color] = [
        Color(hex: "#8B5CF6"), Color(hex: "#34C759"), Color(hex: "#FF9500"), Color(hex: "#0A84FF"), Color(hex: "#FF375F"),
        Color(hex: "#5E5CE6"), Color(hex: "#FFD60A"), Color(hex: "#30D158"), Color(hex: "#BF5AF2"), Color(hex: "#64D2FF"),
    ]
    static func color(at i: Int) -> Color { colors[i % colors.count] }
}

enum HistoryFormat {
    /// `$X.XX`, matching the web `histDollar`.
    static func dollar(_ v: Double) -> String { String(format: "$%.2f", v) }

    /// X-axis tick label: `HH:mm` for sub-day buckets, `M/d` otherwise —
    /// matching the web `histAxisLabel`. Uses the process timezone (the daemon
    /// and app are always on the same Mac); snapshot tests pin it for stability.
    static func axisLabel(_ date: Date, bucketSeconds: Int64) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = bucketSeconds < 86_400 ? "HH:mm" : "M/d"
        return f.string(from: date)
    }
}

enum HistoryExport {
    private static let iso: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    /// `bucket_start,project,value` rows over the sparse series — matching the
    /// web `exportHistoryCSV` (ISO-8601 UTC timestamps, 6-decimal values).
    static func csv(_ data: HistoryResponse) -> String {
        var lines = ["bucket_start,project,value"]
        for pt in data.series {
            let ts = iso.string(from: Date(timeIntervalSince1970: TimeInterval(pt.ts)))
            lines.append("\(ts),\(cell(pt.project)),\(String(format: "%.6f", pt.value))")
        }
        return lines.joined(separator: "\n") + "\n"
    }

    /// Pretty-printed JSON of the whole response — matching the web
    /// `exportHistoryJSON`, which stringifies the raw payload.
    static func json(_ data: HistoryResponse) -> String {
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        guard let out = try? enc.encode(data), let s = String(data: out, encoding: .utf8) else {
            return "{}"
        }
        return s
    }

    private static func cell(_ s: String) -> String {
        if s.contains("\"") || s.contains(",") || s.contains("\n") {
            return "\"" + s.replacingOccurrences(of: "\"", with: "\"\"") + "\""
        }
        return s
    }
}
