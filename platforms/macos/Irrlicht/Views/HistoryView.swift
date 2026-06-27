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

    @EnvironmentObject var sessionManager: SessionManager

    @State private var range: HistoryRange = .fiveHour
    @State private var chart: HistoryChart = .cost
    @State private var group: HistoryGroup = .project
    // Single-level drilldown filter (#750); nil = unscoped.
    @State private var scope: HistoryScope?
    @State private var forecastEnabled = true
    @State private var customStart = Calendar.current.date(byAdding: .day, value: -7, to: Date()) ?? Date()
    @State private var customEnd = Date()
    // Resolved [start, end) unix seconds for a custom range, set on Apply (and
    // when the range first switches to .custom).
    @State private var appliedCustomStart: Int64?
    @State private var appliedCustomEnd: Int64?

    @State private var response: HistoryResponse?
    @State private var yieldResponse: HistoryYieldResponse?
    @State private var loadFailed = false

    /// Re-runs the fetch via `.task(id:)` whenever the effective query changes.
    /// This is the macOS equivalent of the web's manual `historyFetchSeq`
    /// dedup — `.task(id:)` cancels the in-flight request when the key changes.
    private var queryKey: String {
        let fc = forecastEnabled ? "f1" : "f0"
        let dims = "\(chart.rawValue)-\(effectiveGroup.rawValue)-\(scope?.query ?? "")"
        if range == .custom {
            return "custom-\(appliedCustomStart ?? 0)-\(appliedCustomEnd ?? 0)-\(fc)-\(dims)"
        }
        return "\(range.rawValue)-\(fc)-\(dims)"
    }

    /// The stacking axis actually sent to the daemon: pinned to model/provider
    /// for the models/providers presets, else the user's group choice.
    private var effectiveGroup: HistoryGroup { chart.pinnedGroup ?? group }

    /// Drill into one contributor: scope the view to it and re-group by the next
    /// finer axis, always cost-based (matching the web).
    private func drill(into field: HistoryGroup, value: String) {
        guard let next = field.drillNext else { return }
        scope = HistoryScope(field: field, value: value)
        group = next
        chart = .cost
    }

    /// Back out of the drilldown, returning to the axis we drilled from.
    private func clearDrill() {
        if let field = scope?.field { group = field }
        scope = nil
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
            // Two rows so neither is cramped at 380pt, and the quota/cost split
            // is explicit. Both Pickers bind to `range`; the row that doesn't
            // own the current selection simply shows nothing highlighted.
            HStack(spacing: IrrSpacing.sp2) {
                Text("Quota")
                    .font(.caption2).foregroundColor(.secondary)
                    .frame(width: 40, alignment: .leading)
                Picker("", selection: $range) {
                    Text("5h").tag(HistoryRange.fiveHour)
                    Text("7d").tag(HistoryRange.sevenDay)
                }
                .pickerStyle(.segmented)
                .labelsHidden()
            }
            HStack(spacing: IrrSpacing.sp2) {
                Text("Cost")
                    .font(.caption2).foregroundColor(.secondary)
                    .frame(width: 40, alignment: .leading)
                Picker("", selection: $range) {
                    Text("Day").tag(HistoryRange.day)
                    Text("Wk").tag(HistoryRange.week)
                    Text("Mo").tag(HistoryRange.month)
                    Text("Yr").tag(HistoryRange.year)
                    Text("Custom").tag(HistoryRange.custom)
                }
                .pickerStyle(.segmented)
                .labelsHidden()
            }

            // Chart-type selector — only for the cost calendar ranges (the
            // quota spans render a burn-rate projection, not a chart type). #373.
            if !range.isQuota {
                HStack(spacing: IrrSpacing.sp2) {
                    Text("Chart")
                        .font(.caption2).foregroundColor(.secondary)
                        .frame(width: 40, alignment: .leading)
                    Picker("", selection: $chart) {
                        Text("Cost").tag(HistoryChart.cost)
                        Text("Yield").tag(HistoryChart.yieldRatio)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
            }

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

            // Chart-type + group axis (#750). Cost spans only — the quota
            // windows render a burn-rate projection, not a grouped series.
            if !range.isQuota {
                HStack(spacing: IrrSpacing.sp2) {
                    Text("Chart")
                        .font(.caption2).foregroundColor(.secondary)
                        .frame(width: 40, alignment: .leading)
                    Picker("", selection: Binding(
                        get: { chart },
                        set: { chart = $0; scope = nil } // a new metric resets any drilldown
                    )) {
                        ForEach(HistoryChart.allCases) { Text($0.label).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                HStack(spacing: IrrSpacing.sp2) {
                    Text("Group")
                        .font(.caption2).foregroundColor(.secondary)
                        .frame(width: 40, alignment: .leading)
                    Picker("", selection: Binding(
                        get: { effectiveGroup },
                        set: { newGroup in
                            group = newGroup
                            // Choosing a group leaves the metric-preset charts.
                            if chart.pinnedGroup != nil { chart = .cost }
                            scope = nil
                        }
                    )) {
                        ForEach(HistoryGroup.allCases) { Text($0.shortLabel).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }

                Toggle("Forecast", isOn: $forecastEnabled)
                    .toggleStyle(.checkbox)
                    .font(.caption)
            }
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: Content

    @ViewBuilder private var content: some View {
        if range.isQuota {
            quotaContent
        } else if chart == .yieldRatio {
            yieldContent
        } else if let r = response {
            HistoryContentView(
                data: r,
                range: range,
                chart: chart,
                group: effectiveGroup,
                scope: scope,
                forecastEnabled: forecastEnabled,
                onDrill: { field, value in drill(into: field, value: value) },
                onClearDrill: { clearDrill() },
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

    @ViewBuilder private var yieldContent: some View {
        if let y = yieldResponse {
            HistoryYieldContentView(data: y, range: range)
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

    @ViewBuilder private var quotaContent: some View {
        if let win = quotaWindow {
            HistoryQuotaContentView(window: win)
        } else {
            Text("No subscription quota data yet.\nStart a Claude Pro/Max or ChatGPT session.")
                .font(.callout)
                .multilineTextAlignment(.center)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, minHeight: 220)
                .padding(.horizontal, IrrSpacing.sp4)
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
        guard !range.isQuota else { return }  // quota spans read the live snapshot, no fetch
        loadFailed = false
        var comps = URLComponents(string: "\(DaemonEndpoint.httpBase)/api/v1/history")
        // queryItems ignores the custom bounds unless range == .custom.
        comps?.queryItems = range.queryItems(chart: chart, group: effectiveGroup, scope: scope, forecast: forecastEnabled, customStart: appliedCustomStart, customEnd: appliedCustomEnd)
        guard let url = comps?.url else { return }
        do {
            let (data, resp) = try await URLSession.shared.data(from: url)
            if Task.isCancelled { return }
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
                loadFailed = true
                return
            }
            if chart == .yieldRatio {
                let decoded = try JSONDecoder().decode(HistoryYieldResponse.self, from: data)
                if Task.isCancelled { return }
                yieldResponse = decoded
            } else {
                let decoded = try JSONDecoder().decode(HistoryResponse.self, from: data)
                if Task.isCancelled { return }
                response = decoded
            }
        } catch {
            if !Task.isCancelled { loadFailed = true }
        }
    }

    // MARK: Quota window (live rate-limit snapshot → projection)

    /// The freshest subscription rate-limit snapshot's window matching the
    /// selected span (5h / 7d), resolved to a projection view-model. nil when no
    /// subscription session is active or that window isn't present.
    private var quotaWindow: QuotaWindowVM? {
        guard let target = range.windowMinutes else { return nil }
        let now = Date()
        // Pick the freshest snapshot carrying this window, preferring one whose
        // window hasn't already reset — a finished session's pre-rollover
        // snapshot can have a newer sampledAt yet show a stale window (the same
        // tiebreak SessionListView's chip buckets use).
        var best: (info: RateLimitInfo, eta: Date?, window: RateLimitWindowInfo)?
        for s in sessionManager.sessions {
            guard let rl = s.metrics?.rateLimit,
                  let w = rl.windows.first(where: { $0.canonicalWindowMinutes == target })
            else { continue }
            if let b = best {
                let bStale = b.window.resetsAt <= now
                let wStale = w.resetsAt <= now
                let fresher = (bStale && !wStale) || (bStale == wStale && rl.sampledAt > b.info.sampledAt)
                guard fresher else { continue }
            }
            best = (rl, s.metrics?.rateLimitForecastEta, w)
        }
        guard let best else { return nil }
        let w = best.window
        let start = w.resetsAt.addingTimeInterval(-Double(w.windowMinutes) * 60)
        return QuotaWindowVM(
            label: range.label,
            planLabel: best.info.planTypeLabel,
            start: start,
            end: w.resetsAt,
            now: now,
            usedPercent: w.usedPercent,
            projectedCap: projectedCap(window: w, info: best.info, eta: best.eta, now: now, start: start),
            isStale: w.resetsAt <= now
        )
    }

    /// Projected wall-clock time the window hits 100%. Prefers the daemon's
    /// forecast when it belongs to this (imminent) window — keeping it
    /// consistent with the provider view's chip — else extrapolates the average
    /// pace since the window opened. nil when usage is on track to stay under
    /// the cap before reset.
    private func projectedCap(window w: RateLimitWindowInfo, info: RateLimitInfo, eta: Date?, now: Date, start: Date) -> Date? {
        if let eta, let imm = info.imminentWindow, imm.canonicalWindowMinutes == w.canonicalWindowMinutes,
           eta > now, eta <= w.resetsAt {
            return eta
        }
        return QuotaWindowVM.averagePaceCap(now: now, start: start, resetsAt: w.resetsAt, usedPercent: w.usedPercent)
    }

    private func save(ext: String, text: String) {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "irrlicht-history-\(range.rawValue)-\(chart.rawValue).\(ext)"
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
    var chart: HistoryChart = .cost
    var group: HistoryGroup = .project
    var scope: HistoryScope?
    let forecastEnabled: Bool
    var onDrill: (HistoryGroup, String) -> Void = { _, _ in }
    var onClearDrill: () -> Void = {}
    let onExportCSV: () -> Void
    let onExportJSON: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            breadcrumb
            chartView
            Divider()
            summary
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    @ViewBuilder private var breadcrumb: some View {
        if let scope {
            HStack(spacing: IrrSpacing.sp1) {
                Button(action: onClearDrill) {
                    Text("All").foregroundColor(IrrColors.working)
                }
                .buttonStyle(.plain)
                Text("›").foregroundColor(.secondary)
                Text("\(scope.field.rawValue): \(scope.value)")
                    .lineLimit(1).truncationMode(.middle)
            }
            .font(.caption)
        }
    }

    @ViewBuilder private var chartView: some View {
        if data.hasData {
            HistoryCostChart(
                data: data,
                orderedProjects: orderedProjects,
                forecastEnabled: forecastEnabled,
                chart: chart
            )
            .frame(height: 200)
        } else {
            Text(chart == .tokens ? "no token usage in this range yet" : "no cost data in this range yet")
                .font(.callout)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, minHeight: 200)
        }
    }

    private var summary: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            Text("\(chart.label) · \(range.label)")
                .font(.caption)
                .foregroundColor(.secondary)
            Text(HistoryFormat.value(data.total, chart: chart))
                .font(.title2)
                .fontWeight(.semibold)
                .monospacedDigit()

            // Forecast is USD-only; the daemon omits it for the tokens chart.
            if forecastEnabled, chart.isCost, let fc = data.forecast {
                Text("▲ projected \(HistoryFormat.dollar(fc.projected)) (\(fc.basis))")
                    .font(.caption)
                    .foregroundColor(IrrColors.waiting)
            }

            if chart == .tokens {
                tokenSplitRows
            } else {
                contributorRows
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

    /// Tokens side panel: the input/output/cache split, not a contributor rank.
    @ViewBuilder private var tokenSplitRows: some View {
        if let split = data.tokenSplit, data.total > 0 {
            VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
                ForEach(Array([("Input", split.input), ("Output", split.output), ("Cache", split.cache)].enumerated()), id: \.offset) { i, row in
                    HStack(spacing: IrrSpacing.sp2) {
                        Circle().fill(HistoryPalette.color(at: i)).frame(width: 8, height: 8)
                        Text(row.0).font(.caption)
                        Spacer(minLength: IrrSpacing.sp2)
                        Text(HistoryFormat.tokens(row.1))
                            .font(.caption).monospacedDigit().foregroundColor(.secondary)
                    }
                }
            }
            .padding(.top, IrrSpacing.sp1)
        } else {
            Text("no token usage in this range")
                .font(.caption).foregroundColor(.secondary).padding(.top, IrrSpacing.sp1)
        }
    }

    /// Cost/models/providers side panel: top contributors, tappable to drill
    /// into the next finer axis (except the synthetic "unknown" bucket and leaf
    /// axes).
    @ViewBuilder private var contributorRows: some View {
        if data.topContributors.isEmpty {
            Text("no spend in this range")
                .font(.caption)
                .foregroundColor(.secondary)
                .padding(.top, IrrSpacing.sp1)
        } else {
            let drillable = group.drillNext != nil
            VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
                ForEach(Array(data.topContributors.enumerated()), id: \.offset) { i, c in
                    let canDrill = drillable && c.label != "unknown"
                    let row = HStack(spacing: IrrSpacing.sp2) {
                        Circle()
                            .fill(HistoryPalette.color(at: i))
                            .frame(width: 8, height: 8)
                        Text(c.label)
                            .font(.caption)
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer(minLength: IrrSpacing.sp2)
                        Text(HistoryFormat.value(c.value, chart: chart))
                            .font(.caption)
                            .monospacedDigit()
                            .foregroundColor(.secondary)
                    }
                    .contentShape(Rectangle())
                    .onTapGesture { if canDrill { onDrill(group, c.label) } }
                    if canDrill {
                        row.tooltip("Drill into \(c.label)")
                    } else {
                        row
                    }
                }
            }
            .padding(.top, IrrSpacing.sp1)
        }
    }

    /// Key order: `top_contributors` first (so the panel dots match the chart
    /// colors), then any extra keys from the series — mirrors the web
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

// MARK: - Yield content (#373)
//
// Per-project productive-vs-reverted spend and the headline ratio. Yield is an
// aggregate over completed sessions, not a time series, so it renders a summary
// plus per-project split bars rather than the stacked-area chart. Pure inputs
// (HistoryYieldResponse) so it hosts directly under a snapshot test.

struct HistoryYieldContentView: View {
    let data: HistoryYieldResponse
    let range: HistoryRange

    private var projectsWithSpend: [HistoryYieldProject] {
        data.projects.filter { $0.totalCost > 0 || $0.unknownCost > 0 }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            summary
            Divider()
            if projectsWithSpend.isEmpty {
                Text("no completed sessions in this range")
                    .font(.callout)
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 160)
            } else {
                VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
                    ForEach(projectsWithSpend) { p in
                        HistoryYieldRow(project: p)
                    }
                }
            }
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    private var summary: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            Text("Yield · \(range.label)")
                .font(.caption)
                .foregroundColor(.secondary)
            HStack(alignment: .firstTextBaseline, spacing: IrrSpacing.sp2) {
                Text(data.totalCost > 0 ? "\(Int((data.yieldRatio * 100).rounded()))%" : "—")
                    .font(.title2)
                    .fontWeight(.semibold)
                    .monospacedDigit()
                Text("\(HistoryFormat.dollar(data.productiveCost)) productive of \(HistoryFormat.dollar(data.totalCost))")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            if data.unknownCost > 0 {
                Text("\(HistoryFormat.dollar(data.unknownCost)) unattributed (non-git)")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
        }
    }
}

private struct HistoryYieldRow: View {
    let project: HistoryYieldProject

    private var productiveFraction: CGFloat {
        guard project.totalCost > 0 else { return 0 }
        return CGFloat(project.productiveCost / project.totalCost)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            HStack(spacing: IrrSpacing.sp2) {
                Text(project.project)
                    .font(.caption)
                    .lineLimit(1)
                    .truncationMode(.middle)
                if project.revertedCount > 0 {
                    Text("↩\(project.revertedCount)")
                        .font(.caption2)
                        .foregroundColor(IrrColors.pressureHigh)
                }
                Spacer(minLength: IrrSpacing.sp2)
                Text(project.totalCost > 0 ? "\(Int((project.yieldRatio * 100).rounded()))%" : "—")
                    .font(.caption)
                    .monospacedDigit()
                    .foregroundColor(.secondary)
            }
            // Productive (green) vs reverted (red) split bar. Unknown-only
            // projects (no attributable spend) show a neutral track.
            GeometryReader { geo in
                if project.totalCost > 0 {
                    HStack(spacing: 0) {
                        Rectangle()
                            .fill(IrrColors.ready)
                            .frame(width: geo.size.width * productiveFraction)
                        Rectangle()
                            .fill(IrrColors.pressureHigh)
                            .frame(width: geo.size.width * (1 - productiveFraction))
                    }
                } else {
                    Rectangle().fill(Color.secondary.opacity(0.2))
                }
            }
            .frame(height: 6)
            .clipShape(Capsule())
        }
    }
}

// MARK: - Stacked-area cost chart (Swift Charts)

private struct HistoryCostChart: View {
    let data: HistoryResponse
    let orderedProjects: [String]
    let forecastEnabled: Bool
    var chart: HistoryChart = .cost

    private struct Datum: Identifiable {
        let id: String
        let date: Date
        let project: String
        let value: Double
    }

    /// Densify the sparse series to a value for every (bucket, project) so the
    /// stacked areas stay continuous — the daemon omits zero buckets. Per-bucket
    /// stacking order follows the `chartForegroundStyleScale` domain.
    private var costData: [Datum] {
        var byKey: [Int64: [String: Double]] = [:]
        for pt in data.series {
            byKey[pt.ts, default: [:]][pt.project, default: 0] += pt.value
        }
        var out: [Datum] = []
        out.reserveCapacity(data.bucketStarts.count * max(1, orderedProjects.count))
        for ts in data.bucketStarts {
            let date = Date(timeIntervalSince1970: TimeInterval(ts))
            for project in orderedProjects {
                out.append(Datum(id: "\(ts)|\(project)", date: date, project: project, value: byKey[ts]?[project] ?? 0))
            }
        }
        return out
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
                AreaMark(
                    x: .value("Time", d.date),
                    y: .value("Cost", d.value)
                )
                .foregroundStyle(by: .value("Project", d.project))
                .interpolationMethod(.monotone)
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
                        Text(HistoryFormat.value(v, chart: chart))
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

// MARK: - Quota burn-rate projection (5h / 7d windows)
//
// For the rate-limit spans the chart isn't cost — it's the subscription
// "projected cap line" from the provider view, shown as its own time span:
// usage % over the exact window (start→end), with a 100% cap line and the
// forecast extrapolated to the projected-cap time. Pure inputs (QuotaWindowVM)
// so the chart is snapshot-testable.

/// Pure inputs for the quota projection chart.
struct QuotaWindowVM {
    let label: String          // "5h" / "7d"
    let planLabel: String?     // "Claude Max", etc.
    let start: Date
    let end: Date
    let now: Date
    let usedPercent: Double
    let projectedCap: Date?    // when usage hits 100% (nil = won't this window)
    let isStale: Bool

    var willHitCap: Bool { projectedCap != nil }

    /// Projected usage % at window end at the average pace (when the cap isn't
    /// reached) — the endpoint of the dashed projection line.
    var projectedEndPercent: Double {
        let elapsed = now.timeIntervalSince(start)
        guard usedPercent > 0, elapsed > 0 else { return usedPercent }
        let total = end.timeIntervalSince(start)
        return min(100, usedPercent * (total / elapsed))
    }

    /// Average-pace cap time: when usage hits 100% if it holds the mean rate
    /// since the window opened. nil when flat, already at the cap, or not
    /// projected to hit it before reset. Pure so it can be unit-tested.
    static func averagePaceCap(now: Date, start: Date, resetsAt: Date, usedPercent: Double) -> Date? {
        let elapsed = now.timeIntervalSince(start)
        guard usedPercent > 0, usedPercent < 100, elapsed > 0 else { return nil }
        let cap = start.addingTimeInterval(elapsed * (100.0 / usedPercent))
        return (cap > now && cap <= resetsAt) ? cap : nil
    }
}

struct HistoryQuotaContentView: View {
    let window: QuotaWindowVM

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            HistoryQuotaChart(window: window)
                .frame(height: 200)
            Divider()
            summary
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
        .opacity(window.isStale ? 0.5 : 1)
    }

    private var summary: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            HStack(spacing: IrrSpacing.sp2) {
                Text("\(window.label) · \(Int(window.usedPercent.rounded()))% used")
                    .font(.title3)
                    .fontWeight(.semibold)
                    .monospacedDigit()
                if let plan = window.planLabel {
                    Text(plan).font(.caption).foregroundColor(.secondary)
                }
            }
            if let cap = window.projectedCap {
                Text("▲ projected cap \(HistoryFormat.clock(cap))")
                    .font(.caption)
                    .foregroundColor(IrrColors.waiting)
            } else {
                Text("on pace — won’t hit the cap this window")
                    .font(.caption)
                    .foregroundColor(IrrColors.ready)
            }
            Text("Resets \(HistoryFormat.clock(window.end))")
                .font(.caption)
                .foregroundColor(.secondary)
            if window.isStale {
                Text("window reset — waiting for fresh data")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct HistoryQuotaChart: View {
    let window: QuotaWindowVM

    private struct Pt: Identifiable {
        let id: String
        let date: Date
        let percent: Double
    }

    // Even-pace reference: straight line from window open (0%) to reset (100%).
    private var pace: [Pt] {
        [Pt(id: "c0", date: window.start, percent: 0),
         Pt(id: "c1", date: window.end, percent: 100)]
    }
    // Clamp to the window so a stale snapshot (now past reset) keeps its marks
    // inside the chart's x-domain.
    private var clampedNow: Date { min(window.now, window.end) }
    // Average-pace trajectory up to the current reading (window open 0% → now).
    // We only hold the latest sample, so this is the mean rate, not a measured
    // history curve.
    private var actual: [Pt] {
        [Pt(id: "a0", date: window.start, percent: 0),
         Pt(id: "a1", date: clampedNow, percent: window.usedPercent)]
    }
    // Projection: now → projected cap (100%), or → window end at average pace.
    private var projected: [Pt] {
        let tail = window.projectedCap.map { Pt(id: "p1", date: $0, percent: 100) }
            ?? Pt(id: "p1", date: window.end, percent: window.projectedEndPercent)
        return [Pt(id: "p0", date: clampedNow, percent: window.usedPercent), tail]
    }
    private var lineColor: Color { window.willHitCap ? IrrColors.waiting : IrrColors.ready }
    private var showTime: Bool { window.end.timeIntervalSince(window.start) <= 86_400 }

    var body: some View {
        Chart {
            RuleMark(y: .value("Cap", 100))
                .foregroundStyle(IrrColors.pressureHigh.opacity(0.8))
                .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
                .annotation(position: .top, alignment: .trailing) {
                    Text("cap").font(.caption2).foregroundColor(.secondary)
                }
            ForEach(pace) { p in
                LineMark(x: .value("Time", p.date), y: .value("Used", p.percent), series: .value("s", "pace"))
                    .foregroundStyle(Color.secondary.opacity(0.35))
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [2, 3]))
            }
            ForEach(actual) { p in
                LineMark(x: .value("Time", p.date), y: .value("Used", p.percent), series: .value("s", "actual"))
                    .foregroundStyle(lineColor)
                    .lineStyle(StrokeStyle(lineWidth: 2))
            }
            ForEach(projected) { p in
                LineMark(x: .value("Time", p.date), y: .value("Used", p.percent), series: .value("s", "proj"))
                    .foregroundStyle(lineColor)
                    .lineStyle(StrokeStyle(lineWidth: 2, dash: [4, 3]))
            }
            PointMark(x: .value("Time", clampedNow), y: .value("Used", window.usedPercent))
                .foregroundStyle(lineColor)
                .symbolSize(60)
        }
        .chartYScale(domain: 0...110)
        .chartXScale(domain: window.start...window.end)
        .chartLegend(.hidden)
        .chartYAxis {
            AxisMarks(position: .leading, values: [0, 50, 100]) { v in
                AxisGridLine()
                AxisValueLabel {
                    if let d = v.as(Double.self) { Text("\(Int(d))%") }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { v in
                AxisGridLine()
                AxisValueLabel {
                    if let d = v.as(Date.self) {
                        Text(HistoryFormat.axisLabel(d, bucketSeconds: showTime ? 3_600 : 86_400))
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

    /// Compact token count (1.2M / 3.4k / 970), matching the web `histTokens`.
    static func tokens(_ v: Double) -> String {
        if v >= 1_000_000 { return String(format: "%.1fM", v / 1_000_000) }
        if v >= 1_000 { return String(format: "%.1fk", v / 1_000) }
        return String(Int(v.rounded()))
    }

    /// Dollars for the USD charts, token counts for the tokens chart — the
    /// macOS twin of the web `histValue`.
    static func value(_ v: Double, chart: HistoryChart) -> String {
        chart.isCost ? dollar(v) : tokens(v)
    }

    // Cached formatters — DateFormatter init is expensive and these fire per
    // axis tick. POSIX-pinned so snapshot tests stay stable; timezone tracks the
    // process default (daemon + app share one Mac).
    private static let hourMinute = posix("HH:mm")
    private static let monthDay = posix("M/d")
    private static let weekdayClock = posix("EEE h:mm a")

    private static func posix(_ format: String) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = format
        return f
    }

    /// X-axis tick label: `HH:mm` for sub-day buckets, `M/d` otherwise —
    /// matching the web `histAxisLabel`.
    static func axisLabel(_ date: Date, bucketSeconds: Int64) -> String {
        (bucketSeconds < 86_400 ? hourMinute : monthDay).string(from: date)
    }

    /// Weekday + 12-hour clock, e.g. "Sat 3:15 PM" — for projected-cap and reset.
    static func clock(_ date: Date) -> String {
        weekdayClock.string(from: date)
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
