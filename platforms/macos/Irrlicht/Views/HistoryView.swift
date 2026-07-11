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

/// Top-level History tabs. The three concerns are too different to share one
/// control bar, so each is its own tab with only the controls it needs:
/// Usage (cost/token time series), Yield (productive-vs-reverted), and Quota
/// (live per-provider subscription rate-limit forecast).
enum HistoryTab: String, CaseIterable, Identifiable {
    case usage, yield, quota

    var id: String { rawValue }

    var label: String {
        switch self {
        case .usage: return "Usage"
        case .yield: return "Yield"
        case .quota: return "Quota"
        }
    }
}

struct HistoryView: View {
    let onClose: () -> Void

    // Top-level view: each tab is a self-contained concern with its own controls
    // (Usage = cost/token time series, Yield = productive-vs-reverted, Quota =
    // live per-provider rate-limit forecast).
    @State private var tab: HistoryTab
    @State private var range: HistoryRange = .day
    @State private var chart: HistoryChart = .cost
    @State private var group: HistoryGroup = .project
    // Single-level drilldown filter (#750); nil = unscoped.
    @State private var scope: HistoryScope?
    @State private var customStart = Calendar.current.date(byAdding: .day, value: -7, to: Date()) ?? Date()
    @State private var customEnd = Date()
    // Resolved [start, end) unix seconds for a custom range, set on Apply (and
    // when the range first switches to .custom).
    @State private var appliedCustomStart: Int64?
    @State private var appliedCustomEnd: Int64?

    // Orthogonal cross-filters (#750): each narrows the others; the grouped
    // dimension's filter is hidden. knownProviders/knownProjects accumulate the
    // option lists seen across responses (token types are a fixed set).
    @State private var filterProvider: Set<String> = []
    @State private var filterTokenType: Set<HistoryTokenType> = []
    @State private var filterProject: Set<String> = []
    @State private var knownProviders: [String] = []
    @State private var knownProjects: [String] = []

    @State private var response: HistoryResponse?
    @State private var yieldResponse: HistoryYieldResponse?
    @State private var loadFailed = false

    init(onClose: @escaping () -> Void, initialTab: HistoryTab = .usage) {
        self.onClose = onClose
        self._tab = State(initialValue: initialTab)
    }

    /// The cross-filters keyed by dimension, for `queryItems` and the query key.
    private var filtersDict: [HistoryGroup: [String]] {
        [.provider: Array(filterProvider),
         .tokenType: filterTokenType.map(\.rawValue),
         .project: Array(filterProject)]
    }

    /// Re-runs the fetch via `.task(id:)` whenever the effective query changes.
    /// This is the macOS equivalent of the web's manual `historyFetchSeq`
    /// dedup — `.task(id:)` cancels the in-flight request when the key changes.
    private var queryKey: String {
        let flt = "\(filterProvider.sorted().joined(separator: ","))|\(filterTokenType.map(\.rawValue).sorted().joined(separator: ","))|\(filterProject.sorted().joined(separator: ","))"
        let dims = "\(tab.rawValue)-\(fetchChart.rawValue)-\(effectiveGroup.rawValue)-\(scope?.query ?? "")-\(flt)"
        if range == .custom {
            return "custom-\(appliedCustomStart ?? 0)-\(appliedCustomEnd ?? 0)-\(dims)"
        }
        return "\(range.rawValue)-\(dims)"
    }

    /// The chart metric actually sent to the daemon: the Yield tab forces the
    /// yield aggregate; otherwise the Usage tab's Cost/Tokens choice. (Quota does
    /// not fetch.)
    private var fetchChart: HistoryChart { tab == .yield ? .yieldRatio : chart }

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
            // Quota has no controls (topControls renders EmptyView for it) —
            // skip the row entirely rather than showing a blank padded gap
            // between two dividers.
            if tab != .quota {
                topControls
                Divider()
            }
            content
        }
        .frame(width: SessionListView.panelWidth, height: panelHeight)
        .background(Color(NSColor.windowBackgroundColor))
        .task(id: queryKey) { await fetch() }
        .onChange(of: range) { newRange in
            if newRange == .custom { applyCustomRange() }
        }
        // token_type grouping and the tokens metric are coupled (the bands are a
        // token concept); keep them consistent however either is changed.
        .onChange(of: group) { newGroup in
            if newGroup == .tokenType && chart != .tokens { chart = .tokens }
            clearFilter(for: newGroup) // a dimension is never both axis and filter
        }
        .onChange(of: chart) { newChart in
            if newChart != .tokens && group == .tokenType { group = .project }
        }
        // Switching tabs drops any drilldown and keeps the Usage metric valid
        // (Cost/Tokens only — Yield lives in its own tab now).
        .onChange(of: tab) { newTab in
            scope = nil
            if newTab == .usage, chart == .yieldRatio { chart = .cost }
        }
    }

    /// Clears the filter on whichever dimension just became the stacking axis.
    private func clearFilter(for group: HistoryGroup) {
        switch group {
        case .provider: filterProvider = []
        case .tokenType: filterTokenType = []
        case .project: filterProject = []
        default: break
        }
    }

    // MARK: Header

    private var header: some View {
        // A leading Back button, a centered tab switcher, and an invisible
        // mirror of the Back button on the trailing side to balance the
        // centering — the two Spacers guarantee a minimum gap on both sides
        // of the switcher regardless of how wide Dynamic Type/accessibility
        // text sizing makes the Back button, so it can never overlap it.
        HStack {
            backButton
            Spacer(minLength: IrrSpacing.sp2)
            Picker("", selection: $tab) {
                ForEach(HistoryTab.allCases) { Text($0.label).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            Spacer(minLength: IrrSpacing.sp2)
            backButton.opacity(0).allowsHitTesting(false).accessibilityHidden(true)
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    private var backButton: some View {
        Button(action: onClose) {
            HStack(spacing: 2) {
                Image(systemName: "chevron.left")
                Text("Back")
            }
            .foregroundColor(.secondary)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    // MARK: Controls

    /// Only the controls that apply to the active tab — Quota needs none,
    /// Yield needs just a range, Usage gets the full set (the tab selector
    /// itself now lives in the header).
    private var topControls: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            switch tab {
            case .usage: usageControls
            case .yield: yieldControls
            case .quota: EmptyView()
            }
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    /// Range picker, shared by the Usage and Yield tabs. Labels are hidden —
    /// with the row down to Range/Chart/Group/Filters, the value alone
    /// ("Day", "Cost", "Proj") reads fine and keeps everything on one line.
    @ViewBuilder private var rangePicker: some View {
        Picker("Range", selection: $range) {
            ForEach(HistoryRange.allCases) { Text($0.label).tag($0) }
        }
        .labelsHidden()
        .fixedSize()
    }

    /// Usage tab: cost/token time series. Range, Chart, Group, and the
    /// cross-filters (as one "Filters" dropdown) all fit on a single row —
    /// the models/providers presets are gone, they're just Cost grouped by
    /// model/provider, which the Group axis already offers.
    @ViewBuilder private var usageControls: some View {
        HStack(spacing: IrrSpacing.sp3) {
            rangePicker
            Picker("Chart", selection: Binding(
                get: { chart },
                set: { chart = $0; scope = nil } // a new metric resets any drilldown
            )) {
                ForEach(visibleCharts) { Text($0.label).tag($0) }
            }
            .labelsHidden()
            .fixedSize()
            Picker("Group", selection: Binding(
                get: { effectiveGroup },
                set: { newGroup in
                    group = newGroup
                    if chart.pinnedGroup != nil { chart = .cost } // leave the presets
                    scope = nil
                }
            )) {
                ForEach(HistoryGroup.allCases) { Text($0.shortLabel).tag($0) }
            }
            .labelsHidden()
            .fixedSize()
            // Cross-filters (#750) as one dropdown (#750's three separate menus
            // didn't fit the row); the daemon query still drops the grouped
            // dimension and the token_type filter outside the tokens metric,
            // so those act as no-ops rather than vanishing from the UI.
            filtersMenu
            Spacer(minLength: 0)
        }
        .pickerStyle(.menu)
        .controlSize(.small)
        .font(.caption)

        if range == .custom { customRangeRow }
    }

    /// Yield tab: a per-project aggregate over the range — just the range picker.
    @ViewBuilder private var yieldControls: some View {
        HStack(spacing: IrrSpacing.sp3) {
            rangePicker
            Spacer(minLength: 0)
        }
        .pickerStyle(.menu)
        .controlSize(.small)
        .font(.caption)

        if range == .custom { customRangeRow }
    }

    /// Custom-range date pickers, shared by the Usage and Yield tabs.
    private var customRangeRow: some View {
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

    /// Metrics shown in the Chart dropdown — Cost, Tokens, and CO2 (issue #829;
    /// Yield is its own tab; the models/providers presets fold into the Group axis).
    private var visibleCharts: [HistoryChart] { [.cost, .tokens, .co2] }

    // MARK: Cross-filter menus

    private func filterLabel(_ name: String, count: Int) -> String {
        count > 0 ? "\(name) (\(count))" : name
    }

    private func setBinding(_ set: Binding<Set<String>>, _ value: String) -> Binding<Bool> {
        Binding(
            get: { set.wrappedValue.contains(value) },
            set: { on in if on { set.wrappedValue.insert(value) } else { set.wrappedValue.remove(value) } }
        )
    }

    private func tokenBinding(_ tt: HistoryTokenType) -> Binding<Bool> {
        Binding(
            get: { filterTokenType.contains(tt) },
            set: { on in if on { filterTokenType.insert(tt) } else { filterTokenType.remove(tt) } }
        )
    }

    /// Total selections across all three filter dimensions, shown as the
    /// "Filters" dropdown's badge count.
    private var activeFilterCount: Int {
        filterProvider.count + filterTokenType.count + filterProject.count
    }

    /// One dropdown holding all three cross-filters as submenus — Provider,
    /// Type, and Project no longer need a row slot each.
    @ViewBuilder private var filtersMenu: some View {
        Menu(filterLabel("Filters", count: activeFilterCount)) {
            Menu(filterLabel("Provider", count: filterProvider.count)) {
                if knownProviders.isEmpty {
                    Text("none seen yet")
                } else {
                    ForEach(knownProviders, id: \.self) { p in
                        Toggle(p, isOn: setBinding($filterProvider, p))
                    }
                }
            }
            Menu(filterLabel("Type", count: filterTokenType.count)) {
                ForEach(HistoryTokenType.allCases) { tt in
                    Toggle(tt.label, isOn: tokenBinding(tt))
                }
            }
            Menu(filterLabel("Project", count: filterProject.count)) {
                if knownProjects.isEmpty {
                    Text("none seen yet")
                } else {
                    ForEach(knownProjects, id: \.self) { p in
                        Toggle(p, isOn: setBinding($filterProject, p))
                    }
                }
            }
        }
        .fixedSize()
    }

    // MARK: Content

    /// Fixed total panel height, independent of the width. Keeps the popover
    /// from inheriting whatever height SessionListView happened to be at
    /// before switching to History, and from resizing between Usage/Yield/
    /// Quota — sized to comfortably fit the Usage tab's chart + legend.
    /// (Quota/Yield's shorter content leaves blank space below rather than
    /// shrinking the panel — a deliberate tradeoff for a constant height.)
    /// Grows by customRangeRowHeight while the custom date-range row is
    /// showing, so that row grows the popover instead of shrinking the
    /// chart/legend to make room for itself.
    private static let basePanelHeight: CGFloat = 560
    private static let customRangeRowHeight: CGFloat = 32
    private var panelHeight: CGFloat {
        Self.basePanelHeight + (range == .custom ? Self.customRangeRowHeight : 0)
    }

    @ViewBuilder private var content: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                switch tab {
                case .usage: usageContent
                case .yield: yieldContent
                case .quota: quotaContent
                }
            }
        }
        .frame(maxHeight: .infinity)
    }

    /// Usage tab: the stacked-area cost/token time series.
    @ViewBuilder private var usageContent: some View {
        if let r = response {
            HistoryContentView(
                data: r,
                range: range,
                chart: chart,
                group: effectiveGroup,
                scope: scope,
                onDrill: { field, value in drill(into: field, value: value) },
                onClearDrill: { clearDrill() },
                onExportCSV: { save(ext: "csv", text: HistoryExport.csv(r)) },
                onExportJSON: { save(ext: "json", text: HistoryExport.json(r)) }
            )
        } else if loadFailed {
            loadFailedText
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, minHeight: 220)
        }
    }

    /// Yield tab: per-project productive-vs-reverted aggregate over the range.
    @ViewBuilder private var yieldContent: some View {
        if let y = yieldResponse {
            HistoryYieldContentView(data: y, range: range)
        } else if loadFailed {
            loadFailedText
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, minHeight: 220)
        }
    }

    /// Quota tab: live per-provider rate-limit forecast. Isolated into its own
    /// view (see HistoryQuotaTabContent) rather than reading `sessionManager`
    /// directly here.
    private var quotaContent: some View {
        HistoryQuotaTabContent()
    }

    private var loadFailedText: some View {
        Text("Couldn’t load history.")
            .font(.callout)
            .foregroundColor(.secondary)
            .frame(maxWidth: .infinity, minHeight: 220)
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
        guard tab != .quota else { return }  // quota reads the live snapshot, no fetch
        loadFailed = false
        var comps = URLComponents(string: "\(DaemonEndpoint.httpBase)/api/v1/history")
        // queryItems ignores the custom bounds unless range == .custom.
        comps?.queryItems = range.queryItems(chart: fetchChart, group: effectiveGroup, scope: scope, filters: filtersDict, forecast: false, customStart: appliedCustomStart, customEnd: appliedCustomEnd)
        guard let url = comps?.url else { return }
        do {
            let (data, resp) = try await URLSession.shared.data(from: url)
            if Task.isCancelled { return }
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
                loadFailed = true
                return
            }
            if fetchChart == .yieldRatio {
                let decoded = try JSONDecoder().decode(HistoryYieldResponse.self, from: data)
                if Task.isCancelled { return }
                yieldResponse = decoded
            } else {
                let decoded = try JSONDecoder().decode(HistoryResponse.self, from: data)
                if Task.isCancelled { return }
                response = decoded
                // Grow the provider/project filter vocabularies from any
                // response grouped on that axis (token types are a fixed set).
                if decoded.group == "provider" {
                    knownProviders = mergeKnown(knownProviders, decoded.topContributors)
                } else if decoded.group == "project" {
                    knownProjects = mergeKnown(knownProjects, decoded.topContributors)
                }
            }
        } catch {
            if !Task.isCancelled { loadFailed = true }
        }
    }

    /// Merges a response's contributor labels into an accumulated option list,
    /// dropping the synthetic "unknown" bucket and keeping it sorted.
    private func mergeKnown(_ existing: [String], _ contribs: [HistoryContributor]) -> [String] {
        var set = Set(existing)
        for c in contribs where !c.label.isEmpty && c.label != "unknown" { set.insert(c.label) }
        return set.sorted()
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

// MARK: - Quota tab (live rate-limit snapshot → per-provider projection)
//
// A standalone view rather than a computed property on HistoryView: it's the
// only part of History that needs `sessionManager`, which publishes on every
// live session tick (as often as every ~100ms during active sessions). If
// HistoryView itself subscribed, that churn would re-evaluate the whole body
// — including the Usage tab's Filters menu — dismissing any open submenu.

private struct HistoryQuotaTabContent: View {
    @EnvironmentObject var sessionManager: SessionManager

    var body: some View {
        let providers = quotaForecasts
        if providers.isEmpty {
            Text("No subscription quota data yet.\nStart a Claude Pro/Max or ChatGPT session.")
                .font(.callout)
                .multilineTextAlignment(.center)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, minHeight: 220)
                .padding(.horizontal, IrrSpacing.sp4)
        } else {
            HistoryQuotaForecastView(providers: providers)
                .padding(.horizontal, IrrSpacing.sp4)
                .padding(.vertical, IrrSpacing.sp3)
        }
    }

    /// One projection view-model per active subscription provider, each carrying
    /// its 5h/7d windows. Reads the live session snapshots (not the history API),
    /// so it's independent of the selected range. Mirrors the per-provider dedup
    /// in SessionListView.quotaChipData: one bucket per provider, the freshest
    /// non-stale snapshot wins.
    private var quotaForecasts: [QuotaProviderVM] {
        let now = Date()
        struct Bucket { var info: RateLimitInfo; var eta: Date?; var adapter: String?; var stale: Bool }
        var byProvider: [String: Bucket] = [:]
        for s in sessionManager.sessions {
            guard let rl = s.metrics?.rateLimit, !rl.windows.isEmpty else { continue }
            let key = rl.providerKey(adapter: s.adapter) ?? "unknown:\(s.adapter ?? "")"
            let stale = rl.windows.contains { $0.resetsAt <= now }
            if let ex = byProvider[key] {
                // Prefer a fresh snapshot over a stale one regardless of
                // sampledAt; within the same freshness, the newest sample wins.
                let fresher = (ex.stale && !stale) || (ex.stale == stale && rl.sampledAt > ex.info.sampledAt)
                guard fresher else { continue }
            }
            byProvider[key] = Bucket(info: rl, eta: s.metrics?.rateLimitForecastEta, adapter: s.adapter, stale: stale)
        }
        return byProvider.map { key, b -> QuotaProviderVM in
            let windows = b.info.windows
                .filter { $0.canonicalWindowMinutes == 300 || $0.canonicalWindowMinutes == 10080 }
                .sorted { $0.canonicalWindowMinutes < $1.canonicalWindowMinutes }
                .map { w -> QuotaWindowVM in
                    let start = w.resetsAt.addingTimeInterval(-Double(w.windowMinutes) * 60)
                    return QuotaWindowVM(
                        label: HistoryFormat.quotaWindowLabel(w.canonicalWindowMinutes),
                        planLabel: b.info.planTypeLabel,
                        start: start,
                        end: w.resetsAt,
                        now: now,
                        usedPercent: w.usedPercent,
                        projectedCap: projectedCap(window: w, info: b.info, eta: b.eta, now: now, start: start),
                        isStale: w.resetsAt <= now
                    )
                }
            return QuotaProviderVM(
                id: key,
                iconKey: b.info.providerKey(adapter: b.adapter),
                planLabel: b.info.planTypeLabel,
                windows: windows
            )
        }
        .filter { !$0.windows.isEmpty }
        .sorted { $0.id < $1.id }
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
    // No-op defaults so previews/snapshot tests can host this view without
    // wiring drill-down interaction.
    var onDrill: (HistoryGroup, String) -> Void = { _, _ in
        // no-op default
    }
    var onClearDrill: () -> Void = {
        // no-op default
    }
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

            // The legend always reflects the stacking axis (the chart stacks by
            // group, for cost AND tokens alike) — only the Token-type grouping
            // gets friendly band labels. Cost vs Tokens is just the metric.
            if group == .tokenType {
                tokenBandRows
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

    /// Token-type grouping side panel: the stacked bands themselves, listed with
    /// friendly labels (the chart's per-kind breakdown, not the in/out/cache
    /// split).
    @ViewBuilder private var tokenBandRows: some View {
        if data.topContributors.isEmpty {
            Text("no token usage in this range")
                .font(.caption).foregroundColor(.secondary).padding(.top, IrrSpacing.sp1)
        } else {
            VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
                ForEach(Array(data.topContributors.enumerated()), id: \.offset) { i, c in
                    HStack(spacing: IrrSpacing.sp2) {
                        Circle().fill(HistoryPalette.color(at: i)).frame(width: 8, height: 8)
                        Text(HistoryTokenType(rawValue: c.label)?.label ?? c.label).font(.caption)
                        Spacer(minLength: IrrSpacing.sp2)
                        Text(HistoryFormat.tokens(c.value))
                            .font(.caption).monospacedDigit().foregroundColor(.secondary)
                    }
                }
            }
            .padding(.top, IrrSpacing.sp1)
        }
    }

    /// Cost/models/providers side panel: top contributors, tappable to drill
    /// into the next finer axis (except the synthetic "unknown" bucket and leaf
    /// axes).
    @ViewBuilder private var contributorRows: some View {
        if data.topContributors.isEmpty {
            Text(chart == .tokens ? "no token usage in this range" : "no spend in this range")
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
    var chart: HistoryChart = .cost

    private struct Datum: Identifiable {
        let id: String
        let date: Date
        let project: String
        let value: Double
    }

    /// Densify the sparse series into a **cumulative** value for every (bucket,
    /// project): each project's per-bucket deltas are summed forward over
    /// `bucketStarts` (the daemon omits zero buckets, so the running total keeps
    /// the curve flat across gaps). The stacked areas then climb to the grand
    /// total at the right edge — the macOS twin of the web `historyRunningSum`.
    private var costData: [Datum] {
        var byKey: [Int64: [String: Double]] = [:]
        for pt in data.series {
            byKey[pt.ts, default: [:]][pt.project, default: 0] += pt.value
        }
        let dates = data.bucketStarts.map { Date(timeIntervalSince1970: TimeInterval($0)) }
        var out: [Datum] = []
        out.reserveCapacity(data.bucketStarts.count * max(1, orderedProjects.count))
        for project in orderedProjects {
            var running = 0.0
            for (i, ts) in data.bucketStarts.enumerated() {
                running += byKey[ts]?[project] ?? 0
                out.append(Datum(id: "\(ts)|\(project)", date: dates[i], project: project, value: running))
            }
        }
        return out
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

/// A provider's live rate-limit forecast: brand identity + its window
/// projections. Pure value type so the forecast strip is snapshot-testable.
struct QuotaProviderVM: Identifiable {
    let id: String          // providerKey ("anthropic"/"openai") or "unknown:<adapter>"
    let iconKey: String?    // ProviderIconRegistry key (nil → no icon)
    let planLabel: String?  // "Claude Max", "ChatGPT Plus", …
    let windows: [QuotaWindowVM]

    /// Heading text: the plan label when known, else the provider brand.
    var displayName: String {
        if let planLabel { return planLabel }
        switch id {
        case "anthropic": return "Anthropic"
        case "openai": return "OpenAI"
        default: return "Subscription"
        }
    }
}

// MARK: - Multi-provider quota forecast strip (#755 redesign)
//
// Replaces the single-window quota *mode* with an always-visible strip: one
// labelled block per active subscription provider, each window rendered as a
// compact burn-rate projection chart. Pure inputs ([QuotaProviderVM]) so it
// hosts directly under a snapshot test.

struct HistoryQuotaForecastView: View {
    let providers: [QuotaProviderVM]

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            Text("Rate limits")
                .font(.caption)
                .foregroundColor(.secondary)
            ForEach(providers) { providerBlock($0) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private func providerBlock(_ p: QuotaProviderVM) -> some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            HStack(spacing: IrrSpacing.sp1) {
                if let icon = ProviderIconRegistry.image(forKey: p.iconKey) {
                    Image(nsImage: icon)
                        .resizable()
                        .renderingMode(.template)
                        .frame(width: 12, height: 12)
                        .foregroundColor(.primary)
                }
                Text(p.displayName)
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            HStack(alignment: .top, spacing: IrrSpacing.sp3) {
                ForEach(Array(p.windows.enumerated()), id: \.offset) { _, w in
                    windowCard(w)
                }
            }
        }
    }

    @ViewBuilder private func windowCard(_ w: QuotaWindowVM) -> some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            HStack(spacing: IrrSpacing.sp1) {
                Text(w.label)
                    .font(.caption2)
                    .foregroundColor(.secondary)
                Spacer(minLength: 0)
                Text("\(Int(w.usedPercent.rounded()))%")
                    .font(.caption2)
                    .monospacedDigit()
            }
            HistoryQuotaChart(window: w, compact: true)
                .frame(height: 84)
            if let cap = w.projectedCap {
                Text("▲ cap \(HistoryFormat.clock(cap))")
                    .font(.caption2)
                    .foregroundColor(IrrColors.waiting)
                    .lineLimit(1)
                    .truncationMode(.tail)
            } else {
                Text("on pace")
                    .font(.caption2)
                    .foregroundColor(IrrColors.ready)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .opacity(w.isStale ? 0.5 : 1)
    }
}

private struct HistoryQuotaChart: View {
    let window: QuotaWindowVM
    /// Strips the x-axis labels and the "cap" annotation for the small
    /// per-provider cards in the forecast strip.
    var compact: Bool = false

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
                    if !compact { Text("cap").font(.caption2).foregroundColor(.secondary) }
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
                .symbolSize(compact ? 24 : 60)
        }
        .chartYScale(domain: 0...110)
        .chartXScale(domain: window.start...window.end)
        .chartLegend(.hidden)
        .chartYAxis {
            AxisMarks(position: .leading, values: compact ? [0, 100] : [0, 50, 100]) { v in
                AxisGridLine()
                AxisValueLabel {
                    if let d = v.as(Double.self) { Text("\(Int(d))%") }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: compact ? 3 : 4)) { v in
                AxisGridLine()
                if !compact {
                    AxisValueLabel {
                        if let d = v.as(Date.self) {
                            Text(HistoryFormat.axisLabel(d, bucketSeconds: showTime ? 3_600 : 86_400))
                        }
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

    /// "5h" / "7d" from a canonical rate-limit window length in minutes.
    static func quotaWindowLabel(_ minutes: Int) -> String {
        switch minutes {
        case 300: return "5h"
        case 10080: return "7d"
        default:
            if minutes % 1440 == 0 { return "\(minutes / 1440)d" }
            if minutes % 60 == 0 { return "\(minutes / 60)h" }
            return "\(minutes)m"
        }
    }

    /// Compact token count (1.2M / 3.4k / 970), matching the web `histTokens`.
    static func tokens(_ v: Double) -> String {
        if v >= 1_000_000 { return String(format: "%.1fM", v / 1_000_000) }
        if v >= 1_000 { return String(format: "%.1fk", v / 1_000) }
        return String(Int(v.rounded()))
    }

    /// Compact estimated CO2e footprint (issue #829), matching the web
    /// `histCO2` — unit-adaptive and always renders a value (a chart axis
    /// needs "0mg" at an empty bucket, not SessionMetrics.formattedCO2's
    /// hide-on-zero blank).
    static func co2(_ v: Double) -> String {
        if v < 1 { return String(format: "%.0fmg", v * 1000) }
        if v < 1000 { return String(format: "%.1fg", v) }
        return String(format: "%.2fkg", v / 1000)
    }

    /// Dollars for the USD charts, token counts for the tokens chart, CO2e for
    /// the co2 chart — the macOS twin of the web `histValue`.
    static func value(_ v: Double, chart: HistoryChart) -> String {
        if chart.isCost { return dollar(v) }
        if chart.isCO2 { return co2(v) }
        return tokens(v)
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
