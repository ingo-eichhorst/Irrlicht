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
/// Usage (cost/token time series), Metrics (a growing set of derived
/// analytics — Yield's productive-vs-reverted plus DORA, #951), and Quota
/// (live per-provider subscription rate-limit forecast).
enum HistoryTab: String, CaseIterable, Identifiable {
    case usage, metrics, quota

    var id: String { rawValue }

    var label: String {
        switch self {
        case .usage: return "Usage"
        case .metrics: return "Metrics"
        case .quota: return "Quota"
        }
    }
}

/// Which analytic is shown inside the Metrics tab (#951) — a single-selection
/// inner picker, since both platforms render exactly one active
/// chart/section at a time. Yield is #373's existing productive-vs-reverted
/// aggregate; DORA is the first new section; more (AI-specific) metrics
/// arrive later.
enum MetricsSection: String, CaseIterable, Identifiable {
    case yield, dora

    var id: String { rawValue }

    var label: String {
        switch self {
        case .yield: return "Yield"
        case .dora: return "DORA"
        }
    }
}

struct HistoryView: View {
    let onClose: () -> Void

    // Top-level view: each tab is a self-contained concern with its own controls
    // (Usage = cost/token time series, Metrics = Yield + DORA sections, Quota =
    // live per-provider rate-limit forecast).
    @State private var tab: HistoryTab
    @State private var metricsSection: MetricsSection = .yield
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

    // DORA (#951) is inherently repo-scoped — needs exactly one project,
    // unlike cost/yield's implicit "all projects." knownProjects accumulates
    // the option list from any cost fetch grouped by project, so DORA's
    // picker doesn't need a separate project-discovery fetch.
    @State private var knownProjects: [String] = []
    @State private var doraProject: String?

    @State private var response: HistoryResponse?
    @State private var yieldResponse: HistoryYieldResponse?
    @State private var doraResponse: HistoryDoraResponse?
    @State private var loadFailed = false

    init(onClose: @escaping () -> Void, initialTab: HistoryTab = .usage) {
        self.onClose = onClose
        self._tab = State(initialValue: initialTab)
    }

    /// Re-runs the fetch via `.task(id:)` whenever the effective query changes.
    /// This is the macOS equivalent of the web's manual `historyFetchSeq`
    /// dedup — `.task(id:)` cancels the in-flight request when the key changes.
    private var queryKey: String {
        let dims = "\(tab.rawValue)-\(fetchChart.rawValue)-\(effectiveGroup.rawValue)-\(scope?.query ?? "")-\(doraProject ?? "")"
        if range == .custom {
            return "custom-\(appliedCustomStart ?? 0)-\(appliedCustomEnd ?? 0)-\(dims)"
        }
        return "\(range.rawValue)-\(dims)"
    }

    /// The chart metric actually sent to the daemon: the Metrics tab forces
    /// whichever section is active (Yield's aggregate or DORA); otherwise the
    /// Usage tab's Cost/Tokens choice. (Quota does not fetch.)
    private var fetchChart: HistoryChart {
        guard tab == .metrics else { return chart }
        return metricsSection == .dora ? .dora : .yieldRatio
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
            PanelHeader(title: "History", onBack: onClose)
            Divider()
            tabSwitcher
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
        }
        .onChange(of: chart) { newChart in
            if newChart != .tokens && group == .tokenType { group = .project }
        }
        // Switching tabs drops any drilldown and keeps the Usage metric valid
        // (Cost/Tokens only — Yield/DORA live in the Metrics tab now).
        .onChange(of: tab) { newTab in
            scope = nil
            if newTab == .usage, chart == .yieldRatio || chart == .dora { chart = .cost }
        }
    }

    // MARK: Header

    /// Tab switcher row, below the shared `PanelHeader` (issue #940 — the
    /// header itself is now identical across every sub-panel; view-specific
    /// controls live in their own row underneath it).
    private var tabSwitcher: some View {
        HStack {
            Spacer(minLength: 0)
            Picker("", selection: $tab) {
                ForEach(HistoryTab.allCases) { Text($0.label).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            // App accent instead of system blue, matching Settings' Menu Bar
            // Icon control and every session-state color (issue #940).
            .tint(IrrColors.working)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    // MARK: Controls

    /// Only the controls that apply to the active tab — Quota needs none,
    /// Metrics needs a section picker + range (+ a project picker for DORA),
    /// Usage gets the full set (the tab selector itself lives in
    /// `tabSwitcher`, above).
    private var topControls: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
            switch tab {
            case .usage: usageControls
            case .metrics: metricsControls
            case .quota: EmptyView()
            }
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    /// Range picker for the Metrics tab's Yield/DORA sections (Usage builds
    /// its own Range/Chart/Group row where all three share the width evenly,
    /// so it doesn't reuse this).
    @ViewBuilder private var rangePicker: some View {
        Picker("Range", selection: $range) {
            ForEach(HistoryRange.allCases) { Text($0.label).tag($0) }
        }
        .labelsHidden()
        .fixedSize()
    }

    /// Usage tab: cost/token time series. Range, Chart, and Group share the
    /// row evenly, each a third of the width (issue #940 — the "Filters"
    /// cross-filter dropdown was dropped: narrowing to one project/branch/
    /// model already works via drilldown, and the extra dimension wasn't
    /// earning its row space) — the models/providers presets are gone too,
    /// they're just Cost grouped by model/provider, which the Group axis
    /// already offers.
    @ViewBuilder private var usageControls: some View {
        HStack(spacing: IrrSpacing.sp3) {
            Picker("Range", selection: $range) {
                ForEach(HistoryRange.allCases) { Text($0.label).tag($0) }
            }
            .labelsHidden()
            .frame(maxWidth: .infinity)
            Picker("Chart", selection: Binding(
                get: { chart },
                set: { chart = $0; scope = nil } // a new metric resets any drilldown
            )) {
                ForEach(visibleCharts) { Text($0.label).tag($0) }
            }
            .labelsHidden()
            .frame(maxWidth: .infinity)
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
            .frame(maxWidth: .infinity)
        }
        .pickerStyle(.menu)
        .controlSize(.small)
        .font(.caption)

        if range == .custom { customRangeRow }
    }

    /// Metrics tab (#951): a section picker (Yield | DORA, more later) plus
    /// the range picker Yield needs — and, only for the DORA section, a
    /// project picker, since DORA is inherently repo-scoped and needs
    /// exactly one project (unlike Yield's implicit "all projects").
    @ViewBuilder private var metricsControls: some View {
        HStack(spacing: IrrSpacing.sp3) {
            Picker("Section", selection: $metricsSection) {
                ForEach(MetricsSection.allCases) { Text($0.label).tag($0) }
            }
            .labelsHidden()
            .fixedSize()
            rangePicker
            if metricsSection == .dora { doraProjectPicker }
            Spacer(minLength: 0)
        }
        .pickerStyle(.menu)
        .controlSize(.small)
        .font(.caption)

        if range == .custom { customRangeRow }
    }

    /// DORA's project picker (#951), sourced from knownProjects — already
    /// populated from cost fetches grouped by project, so no separate
    /// project-discovery fetch is needed.
    @ViewBuilder private var doraProjectPicker: some View {
        Picker("Project", selection: Binding(
            get: { doraProject ?? "" },
            set: { doraProject = $0.isEmpty ? nil : $0 }
        )) {
            Text("Select a project…").tag("")
            ForEach(knownProjects, id: \.self) { Text($0).tag($0) }
        }
        .labelsHidden()
        .fixedSize()
    }

    /// Custom-range date pickers, shared by the Usage and Metrics tabs.
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
                case .metrics: metricsContent
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

    /// Metrics tab (#951): whichever section is active. DORA with no project
    /// selected is a distinct empty state, not a load failure or a spinner —
    /// nothing was fetched at all (queryKey still changes on project
    /// selection, so picking one triggers the fetch normally).
    @ViewBuilder private var metricsContent: some View {
        switch metricsSection {
        case .yield: yieldContent
        case .dora: doraContent
        }
    }

    /// Yield section: per-project productive-vs-reverted aggregate over the range.
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

    /// DORA section (#951): a project must be selected first — its picker
    /// lives in metricsControls above.
    @ViewBuilder private var doraContent: some View {
        if doraProject == nil {
            Text("select a project to see its DORA metrics")
                .font(.callout)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, minHeight: 220)
        } else if let d = doraResponse {
            HistoryDoraContentView(data: d)
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
        // DORA needs exactly one project — with none selected, there's
        // nothing to fetch at all (a distinct empty state, not a load
        // failure or a spinner; see doraContent).
        if fetchChart == .dora, doraProject == nil { return }
        loadFailed = false
        var comps = URLComponents(string: "\(DaemonEndpoint.httpBase)/api/v1/history")
        // queryItems ignores the custom bounds unless range == .custom.
        comps?.queryItems = range.queryItems(chart: fetchChart, group: effectiveGroup, scope: scope, forecast: false, customStart: appliedCustomStart, customEnd: appliedCustomEnd)
        if fetchChart == .dora, let doraProject {
            comps?.queryItems?.append(URLQueryItem(name: "project", value: doraProject))
        }
        guard let url = comps?.url else { return }
        do {
            let (data, resp) = try await URLSession.shared.data(from: url)
            if Task.isCancelled { return }
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
                loadFailed = true
                return
            }
            if fetchChart == .dora {
                let decoded = try JSONDecoder().decode(HistoryDoraResponse.self, from: data)
                if Task.isCancelled { return }
                doraResponse = decoded
            } else if fetchChart == .yieldRatio {
                let decoded = try JSONDecoder().decode(HistoryYieldResponse.self, from: data)
                if Task.isCancelled { return }
                yieldResponse = decoded
            } else {
                let decoded = try JSONDecoder().decode(HistoryResponse.self, from: data)
                if Task.isCancelled { return }
                response = decoded
                // Grows DORA's project picker vocabulary — no separate
                // project-discovery fetch needed (#951).
                if decoded.group == "project" {
                    knownProjects = mergeKnownProjects(knownProjects, decoded.topContributors)
                }
            }
        } catch {
            if !Task.isCancelled { loadFailed = true }
        }
    }

    /// Merges a response's contributor labels into an accumulated project
    /// list, dropping the synthetic "unknown" bucket and keeping it sorted
    /// (#951 — DORA's project picker; #750's broader multi-dimension
    /// version of this was removed along with the Filters dropdown).
    private func mergeKnownProjects(_ existing: [String], _ contribs: [HistoryContributor]) -> [String] {
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
// — including the Usage tab's Chart/Group menus — dismissing any open one.

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

// MARK: - DORA metrics (#951)

struct HistoryDoraContentView: View {
    let data: HistoryDoraResponse

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            Text("DORA · \(data.project)")
                .font(.caption)
                .foregroundColor(.secondary)
            if !data.available {
                Text(data.message ?? "not enough data to compute DORA metrics")
                    .font(.callout)
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 160)
            } else {
                VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
                    HistoryDoraMetricRow(label: "Deployment Frequency", metric: data.deploymentFrequency, format: HistoryFormat.doraPerWeek)
                    HistoryDoraMetricRow(label: "Lead Time for Changes", metric: data.leadTime, format: HistoryFormat.doraHours)
                    HistoryDoraMetricRow(label: "Change Failure Rate", metric: data.changeFailureRate, format: HistoryFormat.doraPercent)
                    HistoryDoraMetricRow(label: "Mean Time to Restore", metric: data.mttr, format: HistoryFormat.doraHours)
                }
            }
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }
}

private struct HistoryDoraMetricRow: View {
    let label: String
    let metric: DoraMetric
    let format: (Double) -> String

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            Text(label)
                .font(.caption)
                .foregroundColor(.secondary)
            if metric.available {
                HStack(alignment: .firstTextBaseline, spacing: IrrSpacing.sp2) {
                    Text(format(metric.value))
                        .font(.title3)
                        .fontWeight(.semibold)
                        .monospacedDigit()
                    if let message = metric.message {
                        Text(message)
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                }
            } else {
                Text(metric.message ?? "n/a")
                    .font(.callout)
                    .foregroundColor(.secondary)
            }
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

    /// One CO2-equivalent reference line, with `nearTop` flagging whether its
    /// label should flip below the line (mirrors the web's pixel-space
    /// `(y - padT) < 10` check — done here in data-space instead, since Swift
    /// Charts doesn't expose rendered plot geometry to the view pre-macOS 14).
    private struct CO2Line: Identifiable {
        let id: String
        let equivalent: CO2Equivalent
        let nearTop: Bool
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

    /// Tallest stacked column across all buckets, ×1.12 headroom, floored at
    /// 1 — the macOS twin of the web `historyMaxY` (no forecast term: the
    /// daemon's history response carries no forecast field for this chart).
    /// Only meaningful for the CO2 chart, but cheap enough to leave unguarded.
    private var co2MaxY: Double {
        var perDate: [Date: Double] = [:]
        for d in costData { perDate[d.date, default: 0] += d.value }
        let peak = perDate.values.max() ?? 0
        return (peak > 0 ? peak : 1) * 1.12
    }

    /// Red dotted reference lines for relatable everyday CO2e activities
    /// (issue #952) — empty for every chart type except CO2.
    private var co2Lines: [CO2Line] {
        guard chart.isCO2 else { return [] }
        let maxY = co2MaxY
        return CO2Equivalents.pick(maxY: maxY).map { eq in
            CO2Line(id: eq.id, equivalent: eq, nearTop: eq.grams > maxY * 0.9)
        }
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
            // Drawn after the AreaMarks, so painted on top of them — Swift
            // Charts, like SwiftUI's ZStack, layers later marks over earlier
            // ones with no explicit z-order API needed.
            ForEach(co2Lines) { line in
                RuleMark(y: .value("CO2 equivalent", line.equivalent.grams))
                    .foregroundStyle(IrrColors.pressureHigh.opacity(0.8))
                    .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [1, 4]))
                    .annotation(position: line.nearTop ? .bottom : .top, alignment: .leading) {
                        Text("≈ \(line.equivalent.label)")
                            .font(.caption2)
                            .foregroundColor(IrrColors.pressureHigh)
                            .lineLimit(1)
                    }
            }
        }
        .modifier(CO2YScale(isCO2: chart.isCO2, maxY: co2MaxY))
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

/// Applies an explicit Y domain only for the CO2 chart, leaving every other
/// chart type's automatic Swift Charts scale untouched — `.chartYScale`
/// takes a concrete, non-optional domain, so a ternary can't switch between
/// "a range" and "no scale" inline. Gating this to CO2 only matters: applying
/// it unconditionally would change tick-value selection for cost/tokens/
/// models/providers too and risk their existing snapshot tests.
private struct CO2YScale: ViewModifier {
    let isCO2: Bool
    let maxY: Double
    func body(content: Content) -> some View {
        if isCO2 {
            content.chartYScale(domain: 0...maxY)
        } else {
            content
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

    /// "X.X/week", for DORA Deployment Frequency (#951).
    static func doraPerWeek(_ v: Double) -> String { String(format: "%.1f/week", v) }

    /// "X%", for DORA Change Failure Rate (#951).
    static func doraPercent(_ v: Double) -> String { String(format: "%.0f%%", v) }

    /// Hours → "N hours" below a day, "X.X days" at or above — matching the
    /// daemon's own format_hours convention for Lead Time / MTTR (#951).
    static func doraHours(_ v: Double) -> String {
        if v >= 24 { return String(format: "%.1f days", v / 24) }
        return String(format: "%.0f hours", v)
    }

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
